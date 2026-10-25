// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package hass renders this bridge's topic schema and its Home Assistant
// MQTT discovery document through github.com/SukramJ/go-hamqtt.
//
// The package owns no I/O. It answers two questions, each in one place:
//
//   - which topic carries an item's value and which takes its command
//     ([StateTopic], [CommandTopic], [CommandFilter], [ConnectedTopic],
//     [OnlineTopic]) — one function per topic, so the publisher, the
//     command router and the discovery document cannot disagree;
//   - what Home Assistant is told ([RenderBundle]): one retained device
//     document per wallbox, carrying every entity.
package hass

import (
	"fmt"
	"strings"

	hacatalog "github.com/SukramJ/go-ha-catalog"
	"github.com/SukramJ/go-hamqtt/discovery"
	hamodel "github.com/SukramJ/go-hamqtt/model"
	"github.com/SukramJ/go-hamqtt/publisher"
	"github.com/SukramJ/go-hamqtt/topic"

	"github.com/SukramJ/go-keba2mqtt/internal/keba"
)

// Identity constants. Home Assistant has no migration path for any of
// these strings, so they are compile-time constants, never configuration.
const (
	// OriginName and OriginURL identify this bridge in the `origin` block
	// Home Assistant requires on a device document.
	OriginName = "go-keba2mqtt"
	OriginURL  = "https://github.com/SukramJ/go-keba2mqtt"

	// IdentifierNamespace is the go-hamqtt context namespace. The device
	// identifier itself carries NO namespace: the device UID is the bare
	// serial, which is also the address level of every topic
	// (`<name>/status/<serial>/…`), so the device-level availability topic
	// the library derives from the UID is the one the coordinator writes.
	IdentifierNamespace = "keba"
	// uniqueIDPrefix starts every unique_id: "keba_<serial>_<key>". The
	// serial is always part of it, so several wallboxes (several
	// instances of this daemon) share one Home Assistant without
	// colliding.
	uniqueIDPrefix = "keba_"
	// defaultSlug seeds entity ids when no DEVICE_NAME is configured
	// ("sensor.keba_power").
	defaultSlug = "keba"

	manufacturer      = "KEBA"
	defaultDeviceName = "KEBA KeContact P40"
)

// Layout renders the mqtt-smarthome 2.0 topic grammar under the instance
// name (MQTT_TOPIC, "keba" by default):
//
//	<name>/status/<serial>/<group>/<key>   value, a {"val","ts","lc"} object
//	<name>/set/<serial>/<group>/<key>      command
//	<name>/status/<serial>/online          the wallbox's reachability
//	<name>/connected                       bridge: 0/1/2 — the Last Will writes 0
//
// The evcc profile is the group "evcc" in the same grammar.
type Layout struct {
	topic.SmartHome
}

var _ topic.SmartHomeLayout = Layout{}

// NewLayout returns the layout for an instance name. The name was
// validated by config.Validate with the same constructor; a name that
// fails anyway yields the zero layout, which renders no topic at all.
func NewLayout(name string) Layout {
	sh, err := topic.NewSmartHomeMultiLevel(name)
	if err != nil {
		return Layout{}
	}
	return Layout{SmartHome: sh}
}

func slot(serial, group, key string) hamodel.Slot {
	return hamodel.Slot{Address: serial, Path: []string{group, key}}
}

// StateTopic is "<name>/status/<serial>/<group>/<key>".
func StateTopic(root, serial, group, key string) string {
	return NewLayout(root).State(slot(serial, group, key))
}

// CommandTopic is "<name>/set/<serial>/<group>/<key>".
func CommandTopic(root, serial, group, key string) string {
	return NewLayout(root).Command(slot(serial, group, key))
}

// CommandFilter is the subscription for every command this daemon takes:
// "<name>/set/+/+/+" (serial, group, key) — device items and the evcc
// profile alike.
func CommandFilter(root string) string {
	return NewLayout(root).Name() + "/" + topic.FunctionSet + "/+/+/+"
}

// ConnectedTopic is "<name>/connected": 0 from the Last Will and on a
// graceful stop, 1 while the broker is reachable and the wallbox is not,
// 2 while both are.
func ConnectedTopic(root string) string { return NewLayout(root).Connected() }

// OnlineTopic is "<name>/status/<serial>/online", the wallbox's own
// reachability.
func OnlineTopic(root, serial string) string {
	return NewLayout(root).Availability(hamodel.Slot{Address: serial})
}

// PublishesAvailabilityTopic is the set of availability topics this
// daemon writes, for discovery.CheckBundleAvailability: an entity that
// names a source nobody writes is unavailable forever under
// `availability_mode: all`, with nothing in any log to say why.
func PublishesAvailabilityTopic(root, serial string) func(string) bool {
	connected := ConnectedTopic(root)
	online := OnlineTopic(root, serial)
	return func(t string) bool { return t == connected || t == online }
}

// Slug reduces a free-text device name to an entity-id-safe token:
// lower-case [a-z0-9_], runs of anything else collapsed to "_".
func Slug(s string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastUnderscore = false
		default:
			if b.Len() > 0 && !lastUnderscore {
				b.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}

// DeviceInfo is what the STATIC read learned about the wallbox.
type DeviceInfo struct {
	Serial      string
	Firmware    string
	ProductCode uint32
	HWRevision  string
	// Name is DEVICE_NAME; empty uses the generic name.
	Name string
}

// Entity is a go-hamqtt entity plus the platform-specific keys that have
// no typed home on discovery.Component.
type Entity struct {
	hamodel.Basic
	// PlatformFields carries payload_on/off, mode and payload_press.
	PlatformFields any
}

// BuildDiscovery implements discovery.Builder.
func (e *Entity) BuildDiscovery(_ discovery.Context, comp *discovery.Component) error {
	if e.PlatformFields != nil {
		comp.Fields = e.PlatformFields
	}
	return nil
}

// RenderContext pins this bridge's identity strings.
type RenderContext struct {
	discovery.StdContext
	Serial     string
	DeviceSlug string
}

var _ discovery.Context = RenderContext{}

// UniqueID implements discovery.Context: "keba_<serial>_<key>".
func (c RenderContext) UniqueID(_ *hamodel.Device, e hamodel.Entity) string {
	return uniqueIDPrefix + c.Serial + "_" + e.Key()
}

// ObjectID implements discovery.Context: the entity-id seed the library
// prefixes with "<platform>.", "<device slug>_<key>".
func (c RenderContext) ObjectID(_ *hamodel.Device, e hamodel.Entity) string {
	return c.DeviceSlug + "_" + e.Key()
}

// NewRenderContext builds the render context for one wallbox.
func NewRenderContext(root, lang string, info DeviceInfo) RenderContext {
	deviceSlug := Slug(info.Name)
	if deviceSlug == "" {
		deviceSlug = defaultSlug
	}
	return RenderContext{
		StdContext: discovery.StdContext{
			Layout:    NewLayout(root),
			Namespace: IdentifierNamespace,
			Lang:      lang,
			// mqtt-smarthome 2.0's status object: entities read
			// `value_json.val`.
			Enc: discovery.StatusObjectEncoding,
		},
		Serial:     info.Serial,
		DeviceSlug: deviceSlug,
	}
}

// NewDevice builds the Home Assistant device for one wallbox.
func NewDevice(info DeviceInfo) *hamodel.Device {
	name := strings.TrimSpace(info.Name)
	if name == "" {
		name = defaultDeviceName
	}
	modelID := ""
	if info.ProductCode != 0 {
		modelID = fmt.Sprintf("%d", info.ProductCode)
	}
	return &hamodel.Device{
		Identity: hamodel.Identity{
			IDs: []hamodel.Identifier{{Value: info.Serial}},
		},
		Name:         hamodel.L(name),
		Manufacturer: manufacturer,
		Model:        keba.ModelName(info.ProductCode),
		ModelID:      modelID,
		SerialNumber: info.Serial,
		SWVersion:    info.Firmware,
		HWVersion:    info.HWRevision,
	}
}

// NewEntities builds one entity per catalog item.
func NewEntities(cat *keba.Catalog, serial, lang string) []hamodel.Entity {
	out := make([]hamodel.Entity, 0, len(cat.Items))
	for _, it := range cat.Items {
		out = append(out, entityFor(it, serial, lang))
	}
	return out
}

// Origin is the `origin` block; sw is this bridge's build version.
func Origin(sw string) discovery.Origin {
	return discovery.Origin{Name: OriginName, SW: sw, URL: OriginURL}
}

// RenderBundle renders the wallbox's device document and checks that
// every availability source it names is one this daemon publishes.
func RenderBundle(root, lang string, info DeviceInfo, cat *keba.Catalog, origin discovery.Origin) (*discovery.Bundle, error) {
	ctx := NewRenderContext(root, lang, info)
	b, err := discovery.Render(ctx, NewDevice(info), NewEntities(cat, info.Serial, lang), origin)
	if err != nil {
		return nil, err
	}
	if err := discovery.CheckBundleAvailability(b, PublishesAvailabilityTopic(root, info.Serial)); err != nil {
		return nil, fmt.Errorf("hass: %w", err)
	}
	return b, nil
}

// BundleConfigTopic is where the device document is retained:
// "<prefix>/device/<node id>/config".
func BundleConfigTopic(prefix string, info DeviceInfo) string {
	return publisher.BundleConfigTopic(prefix, discovery.NodeID(NewDevice(info)))
}

// --- entities ------------------------------------------------------------

var (
	boolSensorFields = discovery.BinarySensorFields{PayloadOn: discovery.PayloadTrue, PayloadOff: discovery.PayloadFalse}
	boolSwitchFields = discovery.SwitchFields{PayloadOn: discovery.PayloadTrue, PayloadOff: discovery.PayloadFalse}
)

// bridgeAndDevice: every entity needs the daemon (`<name>/connected` at
// 2) AND the wallbox (`<name>/status/<serial>/online`).
func bridgeAndDevice() hamodel.Availability {
	return hamodel.Availability{
		Levels: []hamodel.AvailabilityLevel{hamodel.LevelBridge, hamodel.LevelDevice},
		Mode:   hamodel.AvailabilityAll,
	}
}

func localized(en, de string) hamodel.Localized {
	l := hamodel.Localized{Default: en}
	if de != "" {
		l.Lang = map[string]string{"de": de}
	}
	return l
}

// OptionsEnum turns an enum item's options into a go-hamqtt enum: the
// English token is the code on the wire, the German label its
// translation.
func OptionsEnum(it *keba.Item) *hamodel.Enum {
	e := &hamodel.Enum{Labels: map[string]hamodel.Localized{}}
	for _, o := range it.Options {
		e.Codes = append(e.Codes, o.Label)
		e.Labels[o.Label] = localized(o.Label, o.LabelDE)
	}
	return e
}

// EnumValueTemplate maps the wire token to the option Home Assistant
// lists in lang, and renders `None` (unknown) for anything else —
// [keba.UnknownToken] included. Without the explicit miss Home Assistant
// would log "Invalid option" for a select and keep showing the last
// valid option as if it were current.
func EnumValueTemplate(e *hamodel.Enum, lang string) string {
	var b strings.Builder
	b.WriteString(`{% set m = {`)
	for i, token := range e.Codes {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(discovery.JinjaQuote(token) + ": " + discovery.JinjaQuote(e.Label(token, lang)))
	}
	b.WriteString(`} %}{% if value_json is defined and value_json.val is string %}` +
		`{{ m.get(value_json.val, 'None') }}{% else %}None{% endif %}`)
	return b.String()
}

func entityFor(it *keba.Item, serial, lang string) *Entity {
	s := slot(serial, string(it.Group), it.Key)
	desc := hamodel.Description{
		Name:         localized(it.Name, it.NameDE),
		DeviceClass:  hamodel.DeviceClass(it.DeviceClass),
		StateClass:   hacatalog.StateClass(it.StateClass),
		Unit:         hamodel.Unit(it.Unit),
		Icon:         it.Icon,
		Category:     hacatalog.EntityCategory(it.Category),
		Enabled:      new(!it.Disabled),
		Availability: bridgeAndDevice(),
	}
	e := &Entity{Basic: hamodel.Basic{
		EntityKey:      it.Key,
		EntityPlatform: hacatalog.Platform(it.Platform),
	}}
	read := hamodel.Binding{Role: hamodel.RoleState, Slot: s, Mode: hamodel.Read}
	write := hamodel.Binding{Role: hamodel.RoleCommand, Slot: s, Mode: hamodel.Write}

	switch it.Platform {
	case keba.PlatformSensor:
		if it.Kind == keba.KindEnum {
			desc.Options = OptionsEnum(it)
			desc.ValueTemplate = EnumValueTemplate(desc.Options, lang)
		}
		e.Binds = []hamodel.Binding{read}
	case keba.PlatformBinarySensor:
		e.Binds = []hamodel.Binding{read}
		e.PlatformFields = boolSensorFields
	case keba.PlatformNumber:
		desc.StateClass = ""
		desc.Min, desc.Max, desc.Step = new(it.Min), new(it.Max), new(it.Step)
		e.Binds = []hamodel.Binding{read, write}
		e.PlatformFields = discovery.NumberFields{Mode: "box"}
	case keba.PlatformSelect:
		desc.DeviceClass = ""
		desc.Options = OptionsEnum(it)
		desc.ValueTemplate = EnumValueTemplate(desc.Options, lang)
		e.Binds = []hamodel.Binding{read, write}
	case keba.PlatformSwitch:
		e.Binds = []hamodel.Binding{read, write}
		e.PlatformFields = boolSwitchFields
	case keba.PlatformButton:
		e.Binds = []hamodel.Binding{write}
	}
	e.Description = desc
	return e
}
