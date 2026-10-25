// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package coordinator orchestrates the KEBA P40 → MQTT data flow.
//
// One coordinator owns the paced wallbox, the MQTT state plane, the
// command router and (optionally) the Home Assistant discovery plane.
// Run reads the wallbox's identity, publishes the discovery document,
// then runs the poll scheduler, the write worker, the Modbus watchdog
// and — when enabled — the discovery republisher and the failsafe
// keepalive until its context is cancelled.
//
// All I/O lives in the wired-in collaborators; the coordinator sequences
// calls and turns register values into MQTT status objects.
package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/SukramJ/go-hamqtt/discovery"
	"github.com/SukramJ/go-hamqtt/publisher"
	hagomqtt "github.com/SukramJ/go-hamqtt/publisher/gomqtt"
	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-keba2mqtt/internal/config"
	"github.com/SukramJ/go-keba2mqtt/internal/evcc"
	"github.com/SukramJ/go-keba2mqtt/internal/hass"
	"github.com/SukramJ/go-keba2mqtt/internal/keba"
	"github.com/SukramJ/go-keba2mqtt/internal/state"
	"github.com/SukramJ/go-keba2mqtt/internal/version"
)

// Device is the subset of [*keba.Device] the coordinator drives.
type Device interface {
	Connect(ctx context.Context) error
	Close() error
	IsConnected() bool
	ReadRegister(ctx context.Context, reg *keba.Register) (uint32, error)
	WriteRegister(ctx context.Context, reg *keba.Register, value uint16) error
	ReadItems(ctx context.Context, items []*keba.Item, firmware uint32) keba.Reading
}

// MQTTPublisher is the publish half of *mqtt.TCPClient.
type MQTTPublisher interface {
	Publish(ctx context.Context, topic string, payload []byte, qos mqtt.QoS, retain bool, opts ...mqtt.PublishOption) error
}

// MQTTSubscriber is the subscribe half of *mqtt.TCPClient.
type MQTTSubscriber interface {
	Subscribe(ctx context.Context, filter string, qos mqtt.QoS, handler mqtt.MessageHandler, opts ...mqtt.SubscribeOption) (mqtt.SubscribeResult, error)
	Unsubscribe(ctx context.Context, filter string) error
}

// Deps bundles the wired-in collaborators.
type Deps struct {
	Cfg     *config.Config
	Catalog *keba.Catalog
	Device  Device
	MQTT    interface {
		MQTTPublisher
		MQTTSubscriber
	}
	Logger *slog.Logger // nil → slog.Default()
	// Now is the clock; nil → time.Now.
	Now func() time.Time
	// Store, when non-nil, caches every published value for the web UI.
	Store *state.Store

	// NewHARuntime builds the Home Assistant plane for one broker
	// connection. Required. A factory, because the runtime remembers what
	// it published per CONNECTION and must not outlive one; and built at
	// the composition root, because its Will() is part of CONNECT.
	NewHARuntime func() *publisher.Runtime
	// StatePlane writes every retained status object. Required.
	StatePlane *publisher.StatePublisher
	// Instance publishes `<name>/info` and serves the maintenance
	// commands. Optional.
	Instance *publisher.Instance
	// BrokerMaxPacketSize reports the broker's advertised Maximum Packet
	// Size, so the discovery document's size is checked before it is
	// published. Optional.
	BrokerMaxPacketSize func() (uint32, bool)
}

// StateQoS is the delivery guarantee of every state publish (mqtt-smarthome
// §4: QoS 0). Stated, because publisher.QoS's zero value means "unset",
// which resolves to QoS 1.
const StateQoS = publisher.QoSAtMostOnce

// CommandQoS is the command subscription's QoS: a command dropped in
// transit is a button press that did nothing, with nothing to explain it.
const CommandQoS = publisher.QoSAtLeastOnce

// DiscoveryQoS is the QoS of the retained discovery document and of the
// bridge availability marker.
const DiscoveryQoS = publisher.QoSAtMostOnce

// HARuntimeConfig is the publisher.Config the Home Assistant plane runs
// on, in one place for the composition root and the tests.
func HARuntimeConfig(cfg *config.Config, logger *slog.Logger) publisher.Config {
	return publisher.Config{
		Prefix: cfg.HASSBaseTopic,
		// mqtt-smarthome 2.0: the Last Will writes 0 on `<name>/connected`.
		Layout: hass.NewLayout(cfg.MQTTTopic),
		QoS:    DiscoveryQoS,
		// A new bridge has no per-entity configs to supersede. Retract
		// only what the broker actually holds, so a fresh install sends
		// no retraction at all.
		SupersedeObserved: true,
		// A bad component costs that one entity, not the whole document.
		Contain: &discovery.ContainOptions{},
		Logger:  logger,
	}
}

// Coordinator is the wallbox → MQTT data-flow root.
type Coordinator struct {
	deps      Deps
	startedAt time.Time
	log       *slog.Logger

	// ident is the wallbox identity, set once by the first static read
	// and read from every goroutine.
	ident atomic.Pointer[identity]

	// mu guards the value caches and the command state below.
	mu sync.Mutex
	// values is the last decoded value per item key; raw the last raw
	// value per read register address.
	values map[string]any
	raw    map[uint16]uint32
	// written is the last raw value written per item key.
	written map[string]uint16
	// evccEnabled / evccCurrent are evcc's charger state: whether
	// charging is enabled, and the current (A) it should run at when it
	// is. Each is nil until evcc first says; see evccTarget.
	evccEnabled *bool
	evccCurrent *float64
	// currentCmd is the last raw value written to register 5004, for the
	// failsafe keepalive. Nil until something commands a current.
	currentCmd *uint16
	// unmappedSeen logs an unmapped enum code once, not every poll.
	unmappedSeen map[string]struct{}

	writes        *writeQueue
	writeWorkerUp atomic.Bool

	haRuntime     atomic.Pointer[publisher.Runtime]
	commands      *publisher.CommandRouter
	hassStatus    string
	bundle        *discovery.Bundle
	bundleTopic   string
	discoveryGen  atomic.Uint64
	discoverySent atomic.Bool

	// upstreamUp is the wallbox's reachability as last observed.
	upstreamUp atomic.Bool
}

// identity is what the first static read learned.
type identity struct {
	serial      string
	firmware    string
	firmwareRaw uint32
	productCode uint32
	model       string
	hwRevision  string
}

// New constructs a Coordinator. It does not touch the network.
func New(d Deps) *Coordinator {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.NewHARuntime == nil {
		panic("coordinator: Deps.NewHARuntime is required")
	}
	if d.StatePlane == nil {
		panic("coordinator: Deps.StatePlane is required")
	}
	rt := d.NewHARuntime()
	if rt == nil {
		panic("coordinator: Deps.NewHARuntime returned nil")
	}
	c := &Coordinator{
		deps:       d,
		startedAt:  d.Now(),
		log:        d.Logger,
		values:     map[string]any{},
		raw:        map[uint16]uint32{},
		written:    map[string]uint16{},
		writes:     newWriteQueue(32),
		hassStatus: publisher.BirthTopic(d.Cfg.HASSBaseTopic),
	}
	c.haRuntime.Store(rt)
	// The router subscribes and dispatches, never publishes, so it goes
	// straight to the client rather than through a publish breaker.
	c.commands = publisher.NewCommandRouter(hagomqtt.Split(d.MQTT, d.MQTT), publisher.CommandConfig{
		QoS: CommandQoS,
		// Every write goes through one paced device anyway.
		Workers: 1,
		// A retained command is the broker replaying a past one on
		// (re)subscribe, not a live request.
		DeliverRetained: false,
		Logger:          d.Logger,
	})
	return c
}

// Run executes the daemon loop until ctx is cancelled:
//
//  1. connect the wallbox (retried with backoff — at boot an unreachable
//     wallbox and one still booting look the same);
//  2. subscribe the Home Assistant birth topic and the command plane;
//  3. wait HASS_BIRTH_GRACETIME so a Home Assistant starting alongside
//     sees the discovery document;
//  4. read the static group for the serial every topic is keyed on;
//  5. publish the discovery document;
//  6. run the poll scheduler, the write worker, the watchdog, the
//     discovery republisher and the failsafe keepalive.
func (c *Coordinator) Run(ctx context.Context) error {
	cfg := c.deps.Cfg
	c.log.Info("coordinator.starting",
		slog.String("modbus", cfg.ModbusIP),
		slog.String("mqtt", cfg.MQTTServer),
		slog.Bool("hass", cfg.HASSEnable),
		slog.Bool("evcc", cfg.EVCCEnable))

	if err := c.retryWithBackoff(ctx, "coordinator.modbus_connect_retry", c.deps.Device.Connect); err != nil {
		return err
	}
	defer func() { _ = c.deps.Device.Close() }()
	c.updateUpstream(ctx)

	if err := c.installInbound(ctx); err != nil {
		return err
	}
	if cfg.HASSEnable {
		c.waitForHASSBirth(ctx)
	}
	if err := c.waitForStatic(ctx); err != nil {
		return err
	}
	if err := c.checkCommandDisjoint(); err != nil {
		return err
	}
	c.updateUpstream(ctx)

	if cfg.HASSEnable {
		c.buildBundle()
		c.publishDiscovery(ctx)
	}

	g, runCtx := errgroup.WithContext(ctx)
	g.Go(func() error { return c.pollLoop(runCtx) })
	g.Go(func() error { return c.writeWorker(runCtx) })
	g.Go(func() error { return c.modbusWatchdog(runCtx) })
	if cfg.HASSEnable {
		g.Go(func() error { return c.discoveryRepublisher(runCtx) })
	}
	if cfg.FailsafeKeepalive > 0 {
		g.Go(func() error { return c.keepalive(runCtx) })
	}
	err := g.Wait()

	// Stop the command plane before `connected` goes to 0: a command
	// accepted after this daemon announced itself gone would be executed
	// by nobody.
	stopCtx, stopCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	if serr := c.commands.Stop(stopCtx); serr != nil {
		c.log.Warn("coordinator.command_stop_failed", slog.String("err", serr.Error()))
	}
	stopCancel()

	if err != nil && ctx.Err() == nil {
		return err
	}
	c.log.Info("coordinator.stopped")
	return nil
}

// installInbound subscribes the Home Assistant birth topic and routes
// `<name>/set/+/+/+` (device items and the evcc profile) plus the
// maintenance commands through the library's command router.
func (c *Coordinator) installInbound(ctx context.Context) error {
	if c.deps.Cfg.HASSEnable {
		if err := c.retryWithBackoff(ctx, "coordinator.subscribe_retry", func(ctx context.Context) error {
			_, err := c.deps.MQTT.Subscribe(ctx, c.hassStatus, mqtt.QoS1, c.onBirth)
			return err
		}, slog.String("filter", c.hassStatus)); err != nil {
			return fmt.Errorf("coordinator: subscribe %s: %w", c.hassStatus, err)
		}
	}
	filter := hass.CommandFilter(c.deps.Cfg.MQTTTopic)
	if err := c.commands.HandleSet(filter, c.onSet); err != nil {
		return fmt.Errorf("coordinator: route %s: %w", filter, err)
	}
	if c.deps.Instance != nil {
		if err := c.deps.Instance.Register(c.commands); err != nil {
			return fmt.Errorf("coordinator: route maintenance: %w", err)
		}
	}
	if err := c.retryWithBackoff(ctx, "coordinator.subscribe_retry", c.commands.Start,
		slog.String("filter", filter)); err != nil {
		return fmt.Errorf("coordinator: subscribe %s: %w", filter, err)
	}
	return nil
}

// checkCommandDisjoint refuses a boot in which anything this daemon
// publishes would land inside its own command subscription and be
// echoed back as a command.
func (c *Coordinator) checkCommandDisjoint() error {
	root := c.deps.Cfg.MQTTTopic
	id := c.ident.Load()
	layout := hass.NewLayout(root)
	topics := make([]string, 0, 4+len(c.deps.Catalog.Items)+len(evcc.StatusKeys()))
	topics = append(topics, hass.ConnectedTopic(root), hass.OnlineTopic(root, id.serial), layout.Info(), layout.Maintenance("stats"))
	for _, it := range c.deps.Catalog.Items {
		topics = append(topics, hass.StateTopic(root, id.serial, string(it.Group), it.Key))
	}
	for _, k := range evcc.StatusKeys() {
		topics = append(topics, hass.StateTopic(root, id.serial, evcc.Group, k))
	}
	if err := c.commands.CheckDisjoint(topics...); err != nil {
		return fmt.Errorf("coordinator: command routes overlap what this daemon publishes: %w", err)
	}
	return nil
}

// startupBackoff / startupMaxBackoff bound retryWithBackoff. Vars so
// tests can shorten them.
var (
	startupBackoff    = time.Second
	startupMaxBackoff = 30 * time.Second
)

// retryWithBackoff runs fn until it succeeds or ctx is cancelled,
// backing off 1 s → 30 s and logging each failure under event.
func (c *Coordinator) retryWithBackoff(ctx context.Context, event string, fn func(context.Context) error, attrs ...slog.Attr) error {
	backoff := startupBackoff
	for {
		err := fn(ctx)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.log.LogAttrs(ctx, slog.LevelWarn, event, slices.Concat(attrs, []slog.Attr{
			slog.String("err", err.Error()),
			slog.Duration("retry_in", backoff),
		})...)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, startupMaxBackoff)
	}
}

func (c *Coordinator) waitForHASSBirth(ctx context.Context) {
	c.log.Info("coordinator.hass_birth_wait", slog.Duration("for", c.deps.Cfg.HASSBirthGracetimeDuration()))
	select {
	case <-ctx.Done():
	case <-time.After(c.deps.Cfg.HASSBirthGracetimeDuration()):
	}
}

// staticRetry is the wait between failed static reads. A var so tests
// can shorten it.
var staticRetry = 10 * time.Second

// waitForStatic blocks until the static group yields a serial number.
func (c *Coordinator) waitForStatic(ctx context.Context) error {
	for {
		err := c.initFromStatic(ctx)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.log.Warn("coordinator.static_init_retry", slog.String("err", err.Error()))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(staticRetry):
		}
	}
}

// initFromStatic does one static read and records the identity.
func (c *Coordinator) initFromStatic(ctx context.Context) error {
	r := c.deps.Device.ReadItems(ctx, c.deps.Catalog.ByGroup(keba.GroupStatic), 0)
	serialRaw, ok := r.Raw[keba.RegSerialNumber.Address]
	if !ok || serialRaw == 0 {
		if err := r.Err(); err != nil {
			return err
		}
		return errors.New("coordinator: static read returned no serial number")
	}
	id := &identity{
		serial:      fmt.Sprint(serialRaw),
		firmwareRaw: r.Raw[keba.RegFirmware.Address],
		productCode: r.Raw[keba.RegProduct.Address],
	}
	id.firmware, _ = r.Values[keba.KeyFirmware].(string)
	id.model, _ = r.Values[keba.KeyModel].(string)
	if hw, ok := r.Raw[keba.RegHWRevision.Address]; ok {
		id.hwRevision = fmt.Sprint(hw)
	}
	c.ident.Store(id)
	if c.deps.Store != nil {
		c.deps.Store.SetStatic(id.serial, id.firmware, id.model, c.deps.Now())
	}
	c.log.Info("coordinator.static_initialised",
		slog.String("serial", id.serial),
		slog.String("firmware", id.firmware),
		slog.String("model", id.model))
	if id.firmwareRaw != 0 && id.firmwareRaw < keba.FirmwareEnergyFixed {
		c.log.Info("coordinator.energy_scale_wh",
			slog.String("firmware", id.firmware),
			slog.String("note", "firmware below 1.2.1 reports energy in Wh; corrected"))
	}
	c.apply(ctx, keba.GroupStatic, r)
	return nil
}

// --- Home Assistant discovery --------------------------------------------

// ha returns the Home Assistant plane of the current broker connection.
func (c *Coordinator) ha() *publisher.Runtime { return c.haRuntime.Load() }

// deviceInfo is the identity as the hass package wants it.
func (c *Coordinator) deviceInfo() hass.DeviceInfo {
	id := c.ident.Load()
	return hass.DeviceInfo{
		Serial:      id.serial,
		Firmware:    id.firmware,
		ProductCode: id.productCode,
		HWRevision:  id.hwRevision,
		Name:        c.deps.Cfg.DeviceName,
	}
}

// buildBundle renders the device document once the serial is known. A
// document that does not render or contain is withheld whole and logged;
// the failure is deterministic, so the republisher does not retry it.
func (c *Coordinator) buildBundle() {
	b, err := hass.RenderBundle(c.deps.Cfg.MQTTTopic, c.deps.Cfg.Language, c.deviceInfo(), c.deps.Catalog, hass.Origin(version.Version))
	if err != nil {
		c.log.Error("coordinator.discovery_bundle_render", slog.String("err", err.Error()))
		c.discoverySent.Store(true)
		return
	}
	contained := discovery.Contain(b, discovery.ContainOptions{})
	if err := contained.Err(); err != nil {
		c.log.Error("coordinator.discovery_bundle_invalid", slog.String("err", err.Error()))
		c.discoverySent.Store(true)
		return
	}
	for _, key := range slices.Sorted(maps.Keys(contained.Withheld)) {
		c.log.Error("coordinator.discovery_component_withheld", slog.String("component", key))
	}
	c.bundle = b
	c.bundleTopic = hass.BundleConfigTopic(c.deps.Cfg.HASSBaseTopic, c.deviceInfo())
	c.log.Info("coordinator.discovery_bundle_built",
		slog.String("topic", c.bundleTopic),
		slog.Int("components", len(b.Components)))
}

// publishDiscovery writes the retained device document. A birth that
// arrives mid-publish leaves discoverySent false so the republisher
// sends it again.
func (c *Coordinator) publishDiscovery(ctx context.Context) {
	if c.bundle == nil {
		return
	}
	gen := c.discoveryGen.Load()
	if !c.bundleFitsBroker() {
		return
	}
	written, err := c.ha().PublishBundle(ctx, c.bundle)
	if err != nil {
		c.log.Warn("coordinator.discovery_publish", slog.String("topic", c.bundleTopic), slog.String("err", err.Error()))
		c.discoverySent.Store(false)
		return
	}
	c.discoverySent.Store(c.discoveryGen.Load() == gen)
	c.log.Info("coordinator.discovery_sent",
		slog.Int("entities", len(c.bundle.Components)),
		slog.Bool("written", written))
}

// bundleFitsBroker checks the document against the broker's advertised
// Maximum Packet Size before publishing it; an unknown limit is not a
// small one.
func (c *Coordinator) bundleFitsBroker() bool {
	if c.deps.BrokerMaxPacketSize == nil {
		return true
	}
	maxPacket, known := c.deps.BrokerMaxPacketSize()
	if !known || maxPacket == 0 {
		return true
	}
	payload, err := json.Marshal(c.bundle)
	if err != nil {
		c.log.Error("coordinator.discovery_bundle_marshal", slog.String("err", err.Error()))
		c.discoverySent.Store(true)
		return false
	}
	const publishOverhead = 64
	size := uint64(len(payload)) + uint64(len(c.bundleTopic)) + publishOverhead
	if size <= uint64(maxPacket) {
		return true
	}
	c.log.Error("coordinator.discovery_bundle_too_large",
		slog.String("topic", c.bundleTopic),
		slog.Uint64("bytes", size),
		slog.Uint64("broker_maximum", uint64(maxPacket)))
	c.discoverySent.Store(true)
	return false
}

// onBirth handles Home Assistant's birth message: re-send the document,
// because a Home Assistant that restarted against a broker without
// persistence has forgotten it.
func (c *Coordinator) onBirth(msg *mqtt.Message) {
	if msg.Topic != c.hassStatus || string(msg.Payload) != publisher.BirthPayload {
		return
	}
	c.log.Info("coordinator.hass_birth_seen")
	c.discoveryGen.Add(1)
	c.discoverySent.Store(false)
}

// discoveryRepublisher re-sends the document whenever discoverySent was
// cleared (a birth, a reconnect, a failed publish).
func (c *Coordinator) discoveryRepublisher(ctx context.Context) error {
	const tick = 5 * time.Second
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(tick):
		}
		if c.discoverySent.Load() {
			continue
		}
		c.publishDiscovery(ctx)
	}
}

// --- availability ----------------------------------------------------------

// PublishOnline (re)announces this daemon on a broker connection. Wired
// to the MQTT lifecycle's OnConnect hook, so it runs on every connect:
//
//   - a fresh Home Assistant plane (its memory is per connection) and
//     the discovery document re-sent;
//   - `<name>/connected` at 2 (wallbox reachable) or 1;
//   - `<name>/info`;
//   - the wallbox's `online` item, then every cached status object, so a
//     broker that lost its retained store is complete again.
func (c *Coordinator) PublishOnline(ctx context.Context) {
	fresh := c.deps.NewHARuntime()
	if fresh != nil {
		if old := c.haRuntime.Swap(fresh); old != nil {
			old.Close()
		}
	}
	c.discoveryGen.Add(1)
	c.discoverySent.Store(false)
	up := c.deps.Device.IsConnected()
	c.upstreamUp.Store(up)
	var err error
	if up {
		_, err = c.ha().SetConnected(ctx, discovery.ConnectedOperational)
	} else {
		err = c.ha().AnnounceOnline(ctx)
	}
	if err != nil {
		c.log.Warn("coordinator.online_failed", slog.String("err", err.Error()))
	}
	if c.deps.Instance != nil {
		if err := c.deps.Instance.AnnounceInfo(ctx); err != nil {
			c.log.Warn("coordinator.info_failed", slog.String("err", err.Error()))
		}
	}
	c.publishDeviceOnline(ctx, up)
	if _, err := c.deps.StatePlane.Republish(ctx); err != nil {
		c.log.Warn("coordinator.state_republish_failed", slog.String("err", err.Error()))
	}
}

// PublishOffline marks this daemon unavailable on a clean shutdown; the
// Last Will only fires on an ungraceful disconnect.
func (c *Coordinator) PublishOffline(ctx context.Context) {
	if err := c.ha().AnnounceOffline(ctx); err != nil {
		c.log.Warn("coordinator.offline_failed", slog.String("err", err.Error()))
	}
}

// updateUpstream reports the wallbox's reachability on `<name>/connected`
// (2/1) and on its `online` item. Both are no-ops when nothing changed.
func (c *Coordinator) updateUpstream(ctx context.Context) {
	up := c.deps.Device.IsConnected()
	c.upstreamUp.Store(up)
	level := discovery.ConnectedBroker
	if up {
		level = discovery.ConnectedOperational
	}
	if _, err := c.ha().SetConnected(ctx, level); err != nil {
		c.log.Warn("coordinator.connected_failed", slog.Int("level", level), slog.String("err", err.Error()))
	}
	c.publishDeviceOnline(ctx, up)
}

func (c *Coordinator) publishDeviceOnline(ctx context.Context, up bool) {
	id := c.ident.Load()
	if id == nil {
		return
	}
	t := hass.OnlineTopic(c.deps.Cfg.MQTTTopic, id.serial)
	if _, err := c.deps.StatePlane.PublishStatus(ctx, t, publisher.Observation{Value: up, At: c.deps.Now()}); err != nil {
		c.log.Warn("coordinator.publish_failed", slog.String("topic", t), slog.String("err", err.Error()))
	}
}

// watchdogTick is the Modbus watchdog period. A var so tests can shorten it.
var watchdogTick = 5 * time.Second

// modbusWatchdog reconnects the wallbox when the transport reports a
// closed socket (it poisons the connection on any I/O error).
func (c *Coordinator) modbusWatchdog(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(watchdogTick):
		}
		if c.deps.Device.IsConnected() {
			continue
		}
		c.updateUpstream(ctx)
		c.log.Warn("coordinator.modbus_reconnect")
		if err := c.deps.Device.Connect(ctx); err != nil {
			c.log.Warn("coordinator.modbus_reconnect_failed", slog.String("err", err.Error()))
		}
		c.updateUpstream(ctx)
	}
}
