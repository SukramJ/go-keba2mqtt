// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SukramJ/go-hamqtt/discovery"
	"github.com/SukramJ/go-hamqtt/publisher"
	hagomqtt "github.com/SukramJ/go-hamqtt/publisher/gomqtt"
	"github.com/SukramJ/go-modbus"
	"github.com/SukramJ/go-modbus/modbustest"
	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-keba2mqtt/internal/config"
	"github.com/SukramJ/go-keba2mqtt/internal/hass"
	"github.com/SukramJ/go-keba2mqtt/internal/keba"
)

const testSerial = "18416854"

func init() {
	startupBackoff = 10 * time.Millisecond
	staticRetry = 10 * time.Millisecond
	watchdogTick = 20 * time.Millisecond
	writeRetryDelay = 10 * time.Millisecond
}

// --- a broker stub -----------------------------------------------------------

type stubMQTT struct {
	mu       sync.Mutex
	retained map[string][]byte
	log      []string
	handlers map[string]mqtt.MessageHandler
}

func newStubMQTT() *stubMQTT {
	return &stubMQTT{retained: map[string][]byte{}, handlers: map[string]mqtt.MessageHandler{}}
}

func (s *stubMQTT) Publish(_ context.Context, topic string, payload []byte, _ mqtt.QoS, retain bool, _ ...mqtt.PublishOption) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, topic)
	if retain {
		if len(payload) == 0 {
			delete(s.retained, topic)
		} else {
			s.retained[topic] = slices.Clone(payload)
		}
	}
	return nil
}

func (s *stubMQTT) Subscribe(_ context.Context, filter string, _ mqtt.QoS, h mqtt.MessageHandler, _ ...mqtt.SubscribeOption) (mqtt.SubscribeResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[filter] = h
	return mqtt.SubscribeResult{}, nil
}

func (s *stubMQTT) Unsubscribe(_ context.Context, filter string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.handlers, filter)
	return nil
}

func (s *stubMQTT) deliver(topic, payload string) {
	s.mu.Lock()
	var hs []mqtt.MessageHandler
	for f, h := range s.handlers {
		if publisher.MatchFilter(f, topic) {
			hs = append(hs, h)
		}
	}
	s.mu.Unlock()
	for _, h := range hs {
		h(&mqtt.Message{Topic: topic, Payload: []byte(payload)})
	}
}

// val returns the `val` of a retained status object, and whether it is there.
func (s *stubMQTT) val(topic string) (any, bool) {
	s.mu.Lock()
	b, ok := s.retained[topic]
	s.mu.Unlock()
	if !ok {
		return nil, false
	}
	var obj struct {
		Val any `json:"val"`
	}
	if json.Unmarshal(b, &obj) != nil {
		return string(b), true
	}
	return obj.Val, true
}

func (s *stubMQTT) raw(topic string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.retained[topic]
	return b, ok
}

// --- a simulated P40 -----------------------------------------------------------

// sim is a P40 whose write registers move their read counterparts, the
// way the real wallbox reports a new limit.
func sim(t *testing.T) (*modbustest.Bank, *modbus.Client) {
	t.Helper()
	bank := modbustest.NewBank().MaxRead(2)
	set := func(r *keba.Register, v uint32) { bank.SetUint32(r.Address, v) }
	for _, it := range keba.NewCatalog(true).Items {
		if it.Read != nil {
			set(it.Read, 0)
		}
	}
	set(keba.RegSerialNumber, 18416854)
	set(keba.RegProduct, 4212311)
	set(keba.RegFirmware, 10201)
	set(keba.RegHWRevision, 3)
	set(keba.RegChargingState, keba.StateCharging)
	set(keba.RegCableState, 7)
	set(keba.RegActivePower, 11_040_000) // 11.04 kW in mW
	set(keba.RegTotalEnergy, 1_234_567)  // 123.4567 kWh in 0.1 Wh
	set(keba.RegCurrentL1, 16000)
	set(keba.RegVoltageL1, 230)
	set(keba.RegMaxChargingCurrent, 16000)
	set(keba.RegPhaseSwitchState, 3)
	bank.OnWrite(func(a, v uint16) {
		switch a {
		case keba.RegSetChargingCurrent.Address:
			set(keba.RegMaxChargingCurrent, uint32(v))
		case keba.RegSetFailsafeCurrent.Address:
			set(keba.RegFailsafeCurrent, uint32(v))
		case keba.RegSetFailsafeTimeout.Address:
			set(keba.RegFailsafeTimeout, uint32(v))
		case keba.RegSetPhaseSource.Address:
			set(keba.RegPhaseSwitchSource, uint32(v))
		case keba.RegTriggerPhaseSwitch.Address:
			set(keba.RegPhaseSwitchState, map[uint16]uint32{0: 1, 1: 3}[v])
		}
	})
	srv := modbustest.NewServer(t, bank.Handle)
	cl := modbus.New(modbus.Config{Host: srv.Host(), Port: srv.Port(), UnitID: keba.DefaultUnitID, Timeout: time.Second})
	return bank, cl
}

type fixture struct {
	c      *Coordinator
	broker *stubMQTT
	bank   *modbustest.Bank
	cfg    *config.Config
}

func start(t *testing.T, extraYAML string) *fixture {
	t.Helper()
	if !strings.Contains(extraYAML, "REFRESH_NOW") {
		extraYAML += "REFRESH_NOW: 1\n"
	}
	cfg, err := config.Load(strings.NewReader(`
MODBUS_IP: 127.0.0.1
MQTT_SERVER: broker
HASS_ENABLE: true
HASS_BIRTH_GRACETIME: 0
`+extraYAML), nil)
	if err != nil {
		t.Fatal(err)
	}
	bank, cl := sim(t)
	broker := newStubMQTT()
	log := slog.New(slog.DiscardHandler)
	haCfg := HARuntimeConfig(cfg, log)
	newRT := func() *publisher.Runtime { return publisher.New(hagomqtt.Split(broker, broker), haCfg) }
	rt := newRT()
	c := New(Deps{
		Cfg:          cfg,
		Catalog:      keba.NewCatalog(cfg.RFIDEnable),
		Device:       keba.NewDevice(cl, keba.WithIntervals(time.Millisecond, 20*time.Millisecond)),
		MQTT:         broker,
		Logger:       log,
		NewHARuntime: newRT,
		StatePlane: publisher.StateFor(rt, publisher.StateConfig{
			QoS:            StateQoS,
			PulseQoS:       StateQoS,
			Encoding:       discovery.StatusObjectEncoding,
			CommandFilters: []string{hass.CommandFilter(cfg.MQTTTopic)},
			Logger:         log,
		}),
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	})
	f := &fixture{c: c, broker: broker, bank: bank, cfg: cfg}
	f.waitVal(t, f.topic("now", keba.KeyChargingState), "Charging")
	return f
}

func (f *fixture) topic(group, key string) string {
	return hass.StateTopic(f.cfg.MQTTTopic, testSerial, group, key)
}

func (f *fixture) set(group, key, payload string) {
	f.broker.deliver(hass.CommandTopic(f.cfg.MQTTTopic, testSerial, group, key), payload)
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (f *fixture) waitVal(t *testing.T, topic string, want any) {
	t.Helper()
	eventually(t, fmt.Sprintf("%s = %v", topic, want), func() bool {
		v, ok := f.broker.val(topic)
		return ok && fmt.Sprint(v) == fmt.Sprint(want)
	})
}

func (f *fixture) waitWrites(t *testing.T, want ...modbustest.Write) {
	t.Helper()
	eventually(t, fmt.Sprintf("writes %v", want), func() bool {
		return slices.Equal(f.bank.Writes(), want)
	})
}

// --- tests ---------------------------------------------------------------------

func TestBootPublishesIdentityValuesAndDiscovery(t *testing.T) {
	t.Parallel()
	f := start(t, "")
	f.waitVal(t, f.topic("static", keba.KeyFirmware), "1.2.1")
	f.waitVal(t, f.topic("static", keba.KeyModel), "KC-P40, 32 A, cable, 3-phase, legal meter, RFID, button")
	f.waitVal(t, f.topic("now", keba.KeyPower), 11040)
	f.waitVal(t, f.topic("now", keba.KeyTotalEnergy), 123.4567)
	f.waitVal(t, f.topic("now", keba.KeyChargingCurrent), 16)
	f.waitVal(t, f.topic("now", keba.KeyPhases), keba.OptionThreePhase)
	f.waitVal(t, hass.OnlineTopic("keba", testSerial), true)

	eventually(t, "discovery document", func() bool {
		_, ok := f.broker.raw("homeassistant/device/" + testSerial + "/config")
		return ok
	})
	doc, _ := f.broker.raw("homeassistant/device/" + testSerial + "/config")
	var bundle struct {
		Device     map[string]any            `json:"device"`
		Components map[string]map[string]any `json:"components"`
	}
	if err := json.Unmarshal(doc, &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.Device["model"] != "KeContact P40" || bundle.Device["sw_version"] != "1.2.1" {
		t.Errorf("device block = %v", bundle.Device)
	}
	if len(bundle.Components) != len(keba.NewCatalog(false).Items) {
		t.Errorf("%d components, want one per item", len(bundle.Components))
	}
	if _, ok := bundle.Components[keba.KeyRFID]; ok {
		t.Error("RFID entity announced without RFID_ENABLE")
	}
	// The evcc profile is off by default.
	if _, ok := f.broker.val(f.topic("evcc", "status")); ok {
		t.Error("evcc profile published without EVCC_ENABLE")
	}
}

func TestSetWritesOnceAndRefreshes(t *testing.T) {
	t.Parallel()
	f := start(t, "")
	f.set("now", keba.KeyChargingCurrent, "10")
	f.waitWrites(t, modbustest.Write{Address: 5004, Value: 10000})
	f.waitVal(t, f.topic("now", keba.KeyChargingCurrent), 10)

	// Unchanged value: no second write.
	f.set("now", keba.KeyChargingCurrent, `{"val": 10}`)
	time.Sleep(100 * time.Millisecond)
	f.waitWrites(t, modbustest.Write{Address: 5004, Value: 10000})

	// Out of range and wrong group: refused, never written.
	f.set("now", keba.KeyChargingCurrent, "5")
	f.set("config", keba.KeyChargingCurrent, "12")
	// German select label.
	f.set("config", keba.KeyPhaseSwitchSource, "Modbus TCP")
	f.waitWrites(t,
		modbustest.Write{Address: 5004, Value: 10000},
		modbustest.Write{Address: 5050, Value: 3})
	f.waitVal(t, f.topic("config", keba.KeyPhaseSwitchSource), keba.OptionSourceModbus)
}

func TestOptimisticAndActionItems(t *testing.T) {
	t.Parallel()
	f := start(t, "")
	f.set("config", keba.KeyEnergyLimit, "5.5")
	f.waitVal(t, f.topic("config", keba.KeyEnergyLimit), 5.5)
	f.set("config", keba.KeyChargingEnabled, "false")
	f.waitVal(t, f.topic("config", keba.KeyChargingEnabled), false)
	f.set("config", keba.KeyUnlockPlug, "PRESS")
	f.waitWrites(t,
		modbustest.Write{Address: 5010, Value: 550},
		modbustest.Write{Address: 5014, Value: 0},
		modbustest.Write{Address: 5012, Value: 0})
	// An action repeats; an optimistic value does not.
	f.set("config", keba.KeyChargingEnabled, "false")
	f.set("config", keba.KeyUnlockPlug, "PRESS")
	f.waitWrites(t,
		modbustest.Write{Address: 5010, Value: 550},
		modbustest.Write{Address: 5014, Value: 0},
		modbustest.Write{Address: 5012, Value: 0},
		modbustest.Write{Address: 5012, Value: 0})
}

func TestCommandsForAnotherSerialAreIgnored(t *testing.T) {
	t.Parallel()
	f := start(t, "")
	f.broker.deliver(hass.CommandTopic("keba", "999", "now", keba.KeyChargingCurrent), "10")
	time.Sleep(100 * time.Millisecond)
	if w := f.bank.Writes(); len(w) != 0 {
		t.Fatalf("wrote %v for another wallbox's command", w)
	}
}

func TestEVCCProfile(t *testing.T) {
	t.Parallel()
	f := start(t, "EVCC_ENABLE: true\n")
	f.waitVal(t, f.topic("evcc", "status"), "C")
	f.waitVal(t, f.topic("evcc", "enabled"), true)
	f.waitVal(t, f.topic("evcc", "power"), 11040)
	f.waitVal(t, f.topic("evcc", "energy"), 123.4567)
	f.waitVal(t, f.topic("evcc", "phases"), 3)
	f.waitVal(t, f.topic("evcc", "maxcurrent"), 16)

	// Disable, then change the current while disabled: only the disable
	// reaches the wallbox, the current is remembered.
	f.set("evcc", "enable", "false")
	f.waitWrites(t, modbustest.Write{Address: 5004, Value: 0})
	f.waitVal(t, f.topic("evcc", "enabled"), false)
	f.set("evcc", "maxcurrent", "10")
	time.Sleep(100 * time.Millisecond)
	f.waitWrites(t, modbustest.Write{Address: 5004, Value: 0})

	f.set("evcc", "enable", "true")
	f.waitWrites(t, modbustest.Write{Address: 5004, Value: 0}, modbustest.Write{Address: 5004, Value: 10000})
	f.set("evcc", "maxcurrentmillis", "12.5")
	f.waitWrites(t,
		modbustest.Write{Address: 5004, Value: 0},
		modbustest.Write{Address: 5004, Value: 10000},
		modbustest.Write{Address: 5004, Value: 12500})
	f.waitVal(t, f.topic("evcc", "maxcurrent"), 12.5)

	// evcc re-sends unchanged state every cycle: free.
	f.set("evcc", "enable", "true")
	f.set("evcc", "maxcurrent", "12.5")
	time.Sleep(100 * time.Millisecond)
	if n := len(f.bank.Writes()); n != 3 {
		t.Fatalf("%d writes after unchanged re-sends, want 3", n)
	}

	// Phase switching sets the source to Modbus TCP first.
	f.set("evcc", "phases1p3p", "1")
	f.waitWrites(t,
		modbustest.Write{Address: 5004, Value: 0},
		modbustest.Write{Address: 5004, Value: 10000},
		modbustest.Write{Address: 5004, Value: 12500},
		modbustest.Write{Address: 5050, Value: 3},
		modbustest.Write{Address: 5052, Value: 0})
	f.waitVal(t, f.topic("evcc", "phases"), 1)

	// Garbage is refused.
	f.set("evcc", "phases1p3p", "2")
	f.set("evcc", "maxcurrent", "40")
	time.Sleep(100 * time.Millisecond)
	if n := len(f.bank.Writes()); n != 5 {
		t.Fatalf("%d writes after invalid commands, want 5", n)
	}
}

func TestFailsafeKeepalive(t *testing.T) {
	t.Parallel()
	f := start(t, "")
	// Nothing commanded yet: the keepalive writes nothing.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = f.c.keepaliveEvery(ctx, 30*time.Millisecond) }()
	time.Sleep(100 * time.Millisecond)
	if w := f.bank.Writes(); len(w) != 0 {
		t.Fatalf("keepalive wrote %v before any command", w)
	}
	f.set("now", keba.KeyChargingCurrent, "8")
	// Not waitWrites: the keepalive may already have added its rewrite by
	// the time the command's write is seen, on a slow runner.
	eventually(t, "command and keepalive rewrite", func() bool { return len(f.bank.Writes()) >= 2 })
	for _, w := range f.bank.Writes() {
		if w != (modbustest.Write{Address: 5004, Value: 8000}) {
			t.Fatalf("keepalive wrote %v", w)
		}
	}
}

// Regression: evcc's first maxcurrent can arrive before the first poll
// has read register 1100. That must not be taken for "disabled" — it
// would write 0 and stop a charging car. The limit is read first.
func TestEVCCFirstCommandBeforeFirstPoll(t *testing.T) {
	t.Parallel()
	f := start(t, "EVCC_ENABLE: true\nREFRESH_NOW: 3600\n")
	f.c.mu.Lock()
	delete(f.c.raw, keba.RegMaxChargingCurrent.Address)
	f.c.mu.Unlock()
	f.set("evcc", "maxcurrent", "10")
	f.waitWrites(t, modbustest.Write{Address: 5004, Value: 10000})
}
