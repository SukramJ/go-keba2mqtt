// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SukramJ/go-bridgeui"
	gomodbus "github.com/SukramJ/go-modbus"

	"github.com/SukramJ/go-keba2mqtt/internal/coordinator"
	"github.com/SukramJ/go-keba2mqtt/internal/keba"
	"github.com/SukramJ/go-keba2mqtt/internal/state"
)

// fakeBackend is a deterministic [Backend].
type fakeBackend struct {
	writeErr         error
	lastKey, lastVal string
}

var t0 = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func (f *fakeBackend) Snapshot() state.Snapshot {
	return state.Snapshot{
		Serial: "18416854", Firmware: "1.2.1", Model: "KC-P40-…",
		Groups: map[string]state.GroupView{
			"now":    {UpdatedAt: t0, Values: map[string]any{"charging_state": "Charging", "power": 11000.0}},
			"config": {UpdatedAt: t0.Add(-time.Minute), Values: map[string]any{"charging_current": 16.0}},
		},
	}
}

func (f *fakeBackend) Health() state.Health {
	return state.Health{
		Status: "ok", Version: "0.2.0", ModbusConnected: true, ModbusAddr: "10.0.0.7:502",
		MQTTServer: "broker:1883", MQTTTopic: "keba", Serial: "18416854", Firmware: "1.2.1", StartedAt: t0,
	}
}

func (f *fakeBackend) Config() state.ConfigView {
	return state.ConfigView{ModbusIP: "10.0.0.7", ModbusPort: 502, ModbusUnitID: 255, MQTTServer: "broker", MQTTPort: 1883, FailsafeKeepalive: 30}
}

func (f *fakeBackend) Registers() []state.RegisterInfo {
	return []state.RegisterInfo{
		{Key: "1000", Name: "Charging state", MQTT: "charging_state", Group: "now", ValueItems: map[int]string{0: "Starting", 3: "Charging"}},
		{Key: "1020", Name: "Power", MQTT: "power", Group: "now", Unit: "W"},
		{Key: "1100", Name: "Charging current", MQTT: "charging_current", Group: "config", Unit: "A", Writable: true, Component: "number"},
		{Key: "5052", Name: "Phases", MQTT: "phases", Group: "config", Writable: true, Component: "select", ValueItems: map[int]string{1: "3 phases", 0: "1 phase"}},
		{Key: "5012", Name: "Unlock plug", MQTT: "unlock_plug", Group: "config", Writable: true, Component: "button"},
	}
}

func (f *fakeBackend) Write(_ context.Context, key, value string) error {
	f.lastKey, f.lastVal = key, value
	return f.writeErr
}

func (f *fakeBackend) Changes() (events <-chan struct{}, cancel func()) {
	return make(chan struct{}), func() {}
}

func TestSnapshotMapping(t *testing.T) {
	snap := (&adapter{b: &fakeBackend{}}).Snapshot()
	if len(snap.Devices) != 1 {
		t.Fatalf("devices = %d", len(snap.Devices))
	}
	d := snap.Devices[0]
	if d.ID != "18416854" || d.Title != "KEBA KC-P40-…" || d.Subtitle != "SN 18416854  ·  FW 1.2.1" || !d.UpdatedAt.Equal(t0) {
		t.Errorf("device = %+v", d)
	}
	byKey := map[string]bridgeui.Entry{}
	for _, e := range d.Entries {
		byKey[e.Key] = e
	}
	if e := byKey["power"]; e.Name != "Power" || e.Unit != "W" || e.Group != "now" {
		t.Errorf("power = %+v", e)
	}
	if e := byKey["charging_state"]; e.Tone != "warn" {
		t.Errorf("state tone = %q", e.Tone)
	}
}

// A button writes PRESS, a value map is a select in option order, the rest
// numbers.
func TestItems(t *testing.T) {
	items := (&adapter{b: &fakeBackend{}}).Items()
	if len(items) != 3 {
		t.Fatalf("items = %+v", items)
	}
	if items[0].Component != "number" || items[0].Unit != "A" {
		t.Errorf("current = %+v", items[0])
	}
	if p := items[1]; p.Component != "select" || p.Options[0].Value != "1 phase" || p.Options[1].Label != "3 phases" {
		t.Errorf("phases = %+v", p)
	}
	if b := items[2]; b.Component != "button" || b.Payload != "PRESS" || b.Options != nil {
		t.Errorf("button = %+v", b)
	}
}

func TestWriteErrorClasses(t *testing.T) {
	for _, c := range []struct {
		err  error
		want error
	}{
		{keba.ErrUnknownItem, bridgeui.ErrNotWritable},
		{keba.ErrNotWritable, bridgeui.ErrNotWritable},
		{keba.ErrValue, bridgeui.ErrInvalidValue},
		{gomodbus.ErrNotConnected, bridgeui.ErrNotReady},
		{coordinator.ErrNotReady, bridgeui.ErrNotReady},
	} {
		err := (&adapter{b: &fakeBackend{writeErr: c.err}}).Write(t.Context(), "", "phases", "x")
		if !errors.Is(err, c.want) || !errors.Is(err, c.err) {
			t.Errorf("%v → %v, want %v", c.err, err, c.want)
		}
	}
}

func TestConfigTiles(t *testing.T) {
	tiles := map[string]bridgeui.Tile{}
	for _, tl := range (&adapter{b: &fakeBackend{}}).Config() {
		tiles[tl.Label] = tl
	}
	if m := tiles["cfg.modbus"]; m.Value != "10.0.0.7:502" || m.Sub != "Unit 255" {
		t.Errorf("modbus = %+v", m)
	}
	if k := tiles["cfg.keepalive"]; k.Value != "30 s" {
		t.Errorf("keepalive = %+v", k)
	}
	if r := tiles["cfg.rfid"]; r.Text != "val.off" {
		t.Errorf("rfid = %+v", r)
	}
}

// Every tile label and section title is in both bundles, so none shows as
// a raw key; the overrides of go-bridgeui's built-ins are kept.
func TestLabelsTranslated(t *testing.T) {
	tr, err := bridgeui.LoadTranslations(i18nFS, "i18n")
	if err != nil {
		t.Fatal(err)
	}
	a := &adapter{b: &fakeBackend{}}
	var keys []string
	for _, tl := range append(a.Health().Tiles, a.Config()...) {
		keys = append(keys, tl.Label)
	}
	for _, s := range layout.Sections {
		keys = append(keys, s.Title, s.Nav)
		for _, g := range s.Groups {
			keys = append(keys, "group."+g)
		}
	}
	for _, g := range layout.SystemGroups {
		keys = append(keys, "group."+g)
	}
	keys = append(keys, "status.degraded", "control.hint", "btn.press")
	for _, lang := range []string{"en", "de"} {
		for _, k := range keys {
			if _, ok := tr[lang][k]; !ok {
				t.Errorf("%s.json lacks %q", lang, k)
			}
		}
	}
}

// End to end through the go-bridgeui server: a button press, the bundle.
func TestServer(t *testing.T) {
	fb := &fakeBackend{}
	ts := httptest.NewServer(New(Config{Language: "de"}, fb).Handler())
	defer ts.Close()

	res, err := http.Post(ts.URL+"/api/write", "application/json", strings.NewReader(`{"key":"unlock_plug","value":"PRESS"}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || fb.lastKey != "unlock_plug" || fb.lastVal != "PRESS" {
		t.Errorf("press: %d %q=%q", res.StatusCode, fb.lastKey, fb.lastVal)
	}

	res, err = http.Get(ts.URL + "/i18n/de.json")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var de map[string]string
	if err := json.NewDecoder(res.Body).Decode(&de); err != nil {
		t.Fatal(err)
	}
	if de["status.degraded"] != "Modbus getrennt" || de["section.evcc"] == "" {
		t.Errorf("bundle: %v", de)
	}
}
