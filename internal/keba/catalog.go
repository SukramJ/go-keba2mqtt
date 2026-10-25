// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package keba models the KEBA KeContact P40 / P40 Pro Modbus-TCP
// interface (KEBA "Modbus TCP Programmers Guide" V1.03, document 135655,
// keba.com/emobility-downloads) and drives it over a paced go-modbus
// client. Section references (§2, §3.4.2, …) point into that guide.
//
// The catalog is Go code rather than a YAML file on purpose: the P40
// exposes a small, fixed set of registers, and almost every entry needs
// behaviour a data file cannot carry — a firmware-dependent scale, a
// read register paired with a different write register, a write value
// that is validated against two disjoint ranges.
//
// The vocabulary:
//
//   - a [Register] is one Modbus address. Readable registers are UINT32
//     (two words, FC03); writable ones are UINT16 (FC06). The two sets
//     are disjoint — the P40 has no register that is both.
//   - an [Item] is what this bridge publishes and Home Assistant shows:
//     one MQTT key in one [Group]. It reads from at most one register and
//     writes to at most one, which is how a "Charging current limit"
//     number shows register 1100 and sets register 5004.
package keba

// Group is a poll bucket and the second item level of every topic
// (`<name>/status/<serial>/<group>/<key>`).
type Group string

// Groups, in poll priority order.
const (
	// GroupNow is read every REFRESH_NOW: state, measurements, limits.
	GroupNow Group = "now"
	// GroupConfig is read every REFRESH_CONFIG: settings that change only
	// when someone changes them.
	GroupConfig Group = "config"
	// GroupStatic is read at start and every REFRESH_STATIC: identity.
	GroupStatic Group = "static"
)

// Groups lists every group in poll priority order.
var Groups = []Group{GroupNow, GroupConfig, GroupStatic}

// Kind is the value type an item publishes.
type Kind uint8

// Kinds.
const (
	// KindNumber publishes a float64 (or an int64 when Scale is 1 and no
	// Precision is set).
	KindNumber Kind = iota + 1
	// KindEnum publishes the English label of an [Option] as its token.
	KindEnum
	// KindBool publishes true/false.
	KindBool
	// KindText publishes a string.
	KindText
	// KindAction has no value: a write-only trigger (a button).
	KindAction
)

// Platform is the Home Assistant platform an item is announced as.
type Platform string

// Platforms this bridge emits.
const (
	PlatformSensor       Platform = "sensor"
	PlatformBinarySensor Platform = "binary_sensor"
	PlatformNumber       Platform = "number"
	PlatformSelect       Platform = "select"
	PlatformSwitch       Platform = "switch"
	PlatformButton       Platform = "button"
)

// Option is one value of an enum item. Read and Write are the device
// codes on either side; they differ for "phases", where register 1552
// reports 1/3 while register 5052 takes 0/1.
type Option struct {
	// Label is the stable English word published as `val` and accepted
	// on `set` (the wire token); it is also the English display label.
	Label string
	// LabelDE is the German display label.
	LabelDE string
	// Read is the code the read register reports, or -1 when this option
	// is never reported.
	Read int64
	// Write is the code written to the write register, or -1 when this
	// option cannot be written.
	Write int64
}

// Item is one published value and, optionally, one command.
type Item struct {
	Key    string
	Group  Group
	Name   string
	NameDE string

	// Read is the register the value comes from; nil for a write-only
	// item (published optimistically after a successful write, or not at
	// all for an action).
	Read *Register
	// Write is the register a `set` goes to; nil for a read-only item.
	Write *Register

	Kind     Kind
	Platform Platform

	// Unit is the published unit; Scale multiplies the raw register value
	// into it (0.001 for mA → A). Precision rounds the published number.
	Unit      string
	Scale     float64
	Precision int

	// WriteScale multiplies a `set` value into the raw register value
	// (1000 for A → mA). WriteRanges are the accepted raw values,
	// inclusive; a value outside every range is refused before it reaches
	// the device.
	WriteScale  float64
	WriteRanges [][2]float64
	// Min, Max and Step bound the Home Assistant number entity, in Unit.
	Min, Max, Step float64

	// ActionValue is the raw value an action writes.
	ActionValue uint16

	Options []Option

	// Home Assistant vocabulary.
	DeviceClass string
	StateClass  string
	// Category is "", "diagnostic" or "config".
	Category string
	Icon     string
	// Disabled ships the entity disabled by default.
	Disabled bool

	// EnergyRegister marks the two registers firmware below 1.2.1 reports
	// in Wh instead of 0.1 Wh (spec §5 "Known bugs").
	EnergyRegister bool
	// RFID marks the opt-in RFID item (RFID_ENABLE).
	RFID bool
	// Optimistic marks a write-only item whose state is the last value
	// written successfully.
	Optimistic bool
}

// Writable reports whether the item accepts `set`.
func (it *Item) Writable() bool { return it.Write != nil }

// LocalizedName returns the item's display name in lang.
func (it *Item) LocalizedName(lang string) string {
	if lang == "de" && it.NameDE != "" {
		return it.NameDE
	}
	return it.Name
}

// Register is one Modbus address of the P40.
type Register struct {
	Address uint16
	Name    string
}

// Read registers (spec chapter 3). All UINT32, read-only.
var (
	RegChargingState       = &Register{1000, "Charging state"}
	RegCableState          = &Register{1004, "Cable state"}
	RegErrorCode           = &Register{1006, "Error code"}
	RegCurrentL1           = &Register{1008, "Charging current phase 1"}
	RegCurrentL2           = &Register{1010, "Charging current phase 2"}
	RegCurrentL3           = &Register{1012, "Charging current phase 3"}
	RegSerialNumber        = &Register{1014, "Serial number"}
	RegProduct             = &Register{1016, "Product type and features"}
	RegFirmware            = &Register{1018, "Software package version"}
	RegActivePower         = &Register{1020, "Active power"}
	RegTotalEnergy         = &Register{1036, "Total energy"}
	RegVoltageL1           = &Register{1040, "Voltage phase 1"}
	RegVoltageL2           = &Register{1042, "Voltage phase 2"}
	RegVoltageL3           = &Register{1044, "Voltage phase 3"}
	RegPowerFactor         = &Register{1046, "Power factor"}
	RegMaxChargingCurrent  = &Register{1100, "Max charging current"}
	RegMaxSupportedCurrent = &Register{1110, "Max supported current"}
	RegFastCharging        = &Register{1200, "Fast charging status"}
	RegRFID                = &Register{1500, "RFID card"}
	RegChargedEnergy       = &Register{1502, "Charged energy"}
	RegPhaseSwitchSource   = &Register{1550, "Phase switching source"}
	RegPhaseSwitchState    = &Register{1552, "Phase switching state"}
	RegFailsafeCurrent     = &Register{1600, "Failsafe current setting"}
	RegFailsafeTimeout     = &Register{1602, "Failsafe timeout setting"}
	RegHWRevision          = &Register{1700, "Hardware revision device"}
	RegHWRevisionMS10      = &Register{1702, "Hardware revision KC-MS10"}
)

// Write registers (spec chapter 4). All UINT16, write-only.
var (
	RegSetChargingCurrent = &Register{5004, "Set charging current"}
	RegSetEnergy          = &Register{5010, "Set energy"}
	RegUnlockPlug         = &Register{5012, "Unlock plug"}
	RegEnableStation      = &Register{5014, "Enable/disable charging station"}
	RegSetFailsafeCurrent = &Register{5016, "Failsafe current"}
	RegSetFailsafeTimeout = &Register{5018, "Failsafe timeout"}
	RegSetPhaseSource     = &Register{5050, "Set phase switching source"}
	RegTriggerPhaseSwitch = &Register{5052, "Trigger phase switch"}
	RegFastChargingOn     = &Register{5200, "Activate fast charging"}
)

// Item keys referenced by name elsewhere (coordinator, evcc profile).
const (
	KeyChargingState       = "charging_state"
	KeyCableState          = "cable_state"
	KeyErrorCode           = "error_code"
	KeyCurrentL1           = "current_l1"
	KeyCurrentL2           = "current_l2"
	KeyCurrentL3           = "current_l3"
	KeyPower               = "power"
	KeyPowerFactor         = "power_factor"
	KeyVoltageL1           = "voltage_l1"
	KeyVoltageL2           = "voltage_l2"
	KeyVoltageL3           = "voltage_l3"
	KeyTotalEnergy         = "total_energy"
	KeySessionEnergy       = "session_energy"
	KeyChargingCurrent     = "charging_current"
	KeyMaxSupportedCurrent = "max_supported_current"
	KeyFastCharging        = "fast_charging"
	KeyActivateFastCharge  = "activate_fast_charging"
	KeyPhases              = "phases"
	KeyRFID                = "rfid_tag"
	KeyPhaseSwitchSource   = "phase_switch_source"
	KeyFailsafeCurrent     = "failsafe_current"
	KeyFailsafeTimeout     = "failsafe_timeout"
	KeyChargingEnabled     = "charging_enabled"
	KeyEnergyLimit         = "energy_limit"
	KeyUnlockPlug          = "unlock_plug"
	KeySerial              = "serial_number"
	KeyProductCode         = "product_code"
	KeyModel               = "model"
	KeyFirmware            = "firmware"
	KeyHWRevision          = "hw_revision"
	KeyHWRevisionMS10      = "hw_revision_ms10"
)

// Charging states (register 1000).
const (
	StateStartup   = 0
	StateNotReady  = 1
	StateReady     = 2
	StateCharging  = 3
	StateError     = 4
	StateInterrupt = 5
)

// Option labels referenced by name (the phase enum and the phase
// switching source evcc's phases1p3p needs).
const (
	OptionOnePhase   = "1 phase"
	OptionThreePhase = "3 phases"
	// OptionSourceModbus is the phase switching source that lets this
	// bridge trigger a phase switch (spec §4.6).
	OptionSourceModbus = "Modbus TCP"
)

// currentRanges are the raw mA values the P40 accepts for a charging or
// failsafe current: 0 (suspend) or 6000..32000.
var currentRanges = [][2]float64{{0, 0}, {6000, 32000}}

var catalog = []*Item{
	// --- now ----------------------------------------------------------
	{
		Key: KeyChargingState, Group: GroupNow, Read: RegChargingState,
		Name: "Charging state", NameDE: "Ladezustand",
		Kind: KindEnum, Platform: PlatformSensor, DeviceClass: "enum", Icon: "mdi:ev-station",
		Options: []Option{
			{Label: "Starting", LabelDE: "Startet", Read: StateStartup, Write: -1},
			{Label: "Not ready", LabelDE: "Nicht bereit", Read: StateNotReady, Write: -1},
			{Label: "Ready", LabelDE: "Bereit", Read: StateReady, Write: -1},
			{Label: "Charging", LabelDE: "Lädt", Read: StateCharging, Write: -1},
			{Label: "Error", LabelDE: "Fehler", Read: StateError, Write: -1},
			{Label: "Interrupted", LabelDE: "Unterbrochen", Read: StateInterrupt, Write: -1},
		},
	},
	{
		Key: KeyCableState, Group: GroupNow, Read: RegCableState,
		Name: "Cable state", NameDE: "Kabelstatus",
		Kind: KindEnum, Platform: PlatformSensor, DeviceClass: "enum", Icon: "mdi:ev-plug-type2",
		Options: []Option{
			{Label: "Unplugged", LabelDE: "Nicht gesteckt", Read: 0, Write: -1},
			{Label: "Plugged in station", LabelDE: "An Ladestation gesteckt", Read: 1, Write: -1},
			{Label: "Plugged in station, locked", LabelDE: "An Ladestation gesteckt, verriegelt", Read: 3, Write: -1},
			{Label: "Plugged in vehicle", LabelDE: "Am Fahrzeug gesteckt", Read: 5, Write: -1},
			{Label: "Plugged in vehicle, locked", LabelDE: "Am Fahrzeug gesteckt, verriegelt", Read: 7, Write: -1},
		},
	},
	{
		Key: KeyErrorCode, Group: GroupNow, Read: RegErrorCode,
		Name: "Error code", NameDE: "Fehlercode",
		Kind: KindNumber, Platform: PlatformSensor, Scale: 1, Category: "diagnostic", Icon: "mdi:alert-circle-outline",
	},
	{
		Key: KeyPower, Group: GroupNow, Read: RegActivePower,
		Name: "Power", NameDE: "Leistung",
		Kind: KindNumber, Platform: PlatformSensor, Unit: "W", Scale: 0.001, Precision: 1,
		DeviceClass: "power", StateClass: "measurement",
	},
	currentItem(KeyCurrentL1, RegCurrentL1, "Current L1", "Strom L1"),
	currentItem(KeyCurrentL2, RegCurrentL2, "Current L2", "Strom L2"),
	currentItem(KeyCurrentL3, RegCurrentL3, "Current L3", "Strom L3"),
	voltageItem(KeyVoltageL1, RegVoltageL1, "Voltage L1", "Spannung L1"),
	voltageItem(KeyVoltageL2, RegVoltageL2, "Voltage L2", "Spannung L2"),
	voltageItem(KeyVoltageL3, RegVoltageL3, "Voltage L3", "Spannung L3"),
	{
		Key: KeyPowerFactor, Group: GroupNow, Read: RegPowerFactor,
		Name: "Power factor", NameDE: "Leistungsfaktor",
		Kind: KindNumber, Platform: PlatformSensor, Unit: "%", Scale: 0.1, Precision: 1,
		DeviceClass: "power_factor", StateClass: "measurement", Disabled: true,
	},
	{
		Key: KeySessionEnergy, Group: GroupNow, Read: RegChargedEnergy,
		Name: "Session energy", NameDE: "Energie Ladevorgang",
		Kind: KindNumber, Platform: PlatformSensor, Unit: "kWh", Scale: 0.0001, Precision: 4,
		DeviceClass: "energy", StateClass: "total_increasing", EnergyRegister: true,
	},
	{
		Key: KeyTotalEnergy, Group: GroupNow, Read: RegTotalEnergy,
		Name: "Total energy", NameDE: "Gesamtenergie",
		Kind: KindNumber, Platform: PlatformSensor, Unit: "kWh", Scale: 0.0001, Precision: 4,
		DeviceClass: "energy", StateClass: "total_increasing", EnergyRegister: true,
	},
	{
		Key: KeyChargingCurrent, Group: GroupNow, Read: RegMaxChargingCurrent, Write: RegSetChargingCurrent,
		Name: "Charging current limit", NameDE: "Ladestromgrenze",
		Kind: KindNumber, Platform: PlatformNumber, Unit: "A", Scale: 0.001, Precision: 3,
		DeviceClass: "current", WriteScale: 1000, WriteRanges: currentRanges,
		Min: 0, Max: 32, Step: 0.1, Icon: "mdi:current-ac",
	},
	{
		Key: KeyMaxSupportedCurrent, Group: GroupNow, Read: RegMaxSupportedCurrent,
		Name: "Max supported current", NameDE: "Maximal möglicher Strom",
		Kind: KindNumber, Platform: PlatformSensor, Unit: "A", Scale: 0.001, Precision: 3,
		DeviceClass: "current", StateClass: "measurement", Category: "diagnostic",
	},
	{
		Key: KeyFastCharging, Group: GroupNow, Read: RegFastCharging,
		Name: "Fast charging", NameDE: "Schnellladen",
		Kind: KindBool, Platform: PlatformBinarySensor, Icon: "mdi:flash",
	},
	{
		Key: KeyPhases, Group: GroupNow, Read: RegPhaseSwitchState, Write: RegTriggerPhaseSwitch,
		Name: "Phases", NameDE: "Phasen",
		Kind: KindEnum, Platform: PlatformSelect, Icon: "mdi:sine-wave",
		Options: []Option{
			{Label: OptionOnePhase, LabelDE: "1 Phase", Read: 1, Write: 0},
			{Label: OptionThreePhase, LabelDE: "3 Phasen", Read: 3, Write: 1},
		},
	},
	{
		Key: KeyRFID, Group: GroupNow, Read: RegRFID,
		Name: "RFID tag", NameDE: "RFID-Karte",
		Kind: KindText, Platform: PlatformSensor, Icon: "mdi:card-account-details", RFID: true,
	},
	{
		Key: KeyActivateFastCharge, Group: GroupNow, Write: RegFastChargingOn,
		Name: "Activate fast charging", NameDE: "Schnellladen aktivieren",
		Kind: KindAction, Platform: PlatformButton, ActionValue: 1, Icon: "mdi:flash",
	},

	// --- config -------------------------------------------------------
	{
		Key: KeyChargingEnabled, Group: GroupConfig, Write: RegEnableStation,
		Name: "Charging station enabled", NameDE: "Ladestation aktiviert",
		Kind: KindBool, Platform: PlatformSwitch, Optimistic: true, Category: "config", Icon: "mdi:power",
	},
	{
		Key: KeyEnergyLimit, Group: GroupConfig, Write: RegSetEnergy,
		Name: "Session energy limit", NameDE: "Energielimit Ladevorgang",
		Kind: KindNumber, Platform: PlatformNumber, Unit: "kWh", Precision: 2,
		DeviceClass: "energy", WriteScale: 100, WriteRanges: [][2]float64{{0, 65535}},
		Min: 0, Max: 655.35, Step: 0.01, Optimistic: true, Icon: "mdi:battery-charging-high",
	},
	{
		Key: KeyUnlockPlug, Group: GroupConfig, Write: RegUnlockPlug,
		Name: "Unlock plug", NameDE: "Stecker entriegeln",
		Kind: KindAction, Platform: PlatformButton, ActionValue: 0, Icon: "mdi:lock-open-variant",
	},
	{
		Key: KeyPhaseSwitchSource, Group: GroupConfig, Read: RegPhaseSwitchSource, Write: RegSetPhaseSource,
		Name: "Phase switching source", NameDE: "Quelle Phasenumschaltung",
		Kind: KindEnum, Platform: PlatformSelect, Category: "config", Icon: "mdi:swap-horizontal",
		Options: []Option{
			{Label: "Disabled", LabelDE: "Deaktiviert", Read: 0, Write: 0},
			{Label: "OCPP / profiles", LabelDE: "OCPP / Profile", Read: 1, Write: 1},
			{Label: "REST API", LabelDE: "REST-API", Read: 2, Write: 2},
			{Label: OptionSourceModbus, LabelDE: "Modbus TCP", Read: 3, Write: 3},
		},
	},
	{
		Key: KeyFailsafeCurrent, Group: GroupConfig, Read: RegFailsafeCurrent, Write: RegSetFailsafeCurrent,
		Name: "Failsafe current", NameDE: "Failsafe-Strom",
		Kind: KindNumber, Platform: PlatformNumber, Unit: "A", Scale: 0.001, Precision: 3,
		DeviceClass: "current", WriteScale: 1000, WriteRanges: currentRanges,
		Min: 0, Max: 32, Step: 0.1, Category: "config",
	},
	{
		Key: KeyFailsafeTimeout, Group: GroupConfig, Read: RegFailsafeTimeout, Write: RegSetFailsafeTimeout,
		Name: "Failsafe timeout", NameDE: "Failsafe-Timeout",
		Kind: KindNumber, Platform: PlatformNumber, Unit: "s", Scale: 1,
		DeviceClass: "duration", WriteScale: 1, WriteRanges: [][2]float64{{0, 0}, {5, 600}},
		Min: 0, Max: 600, Step: 1, Category: "config",
	},

	// --- static -------------------------------------------------------
	{
		Key: KeySerial, Group: GroupStatic, Read: RegSerialNumber,
		Name: "Serial number", NameDE: "Seriennummer",
		Kind: KindText, Platform: PlatformSensor, Category: "diagnostic", Disabled: true,
	},
	{
		Key: KeyProductCode, Group: GroupStatic, Read: RegProduct,
		Name: "Product code", NameDE: "Produktcode",
		Kind: KindText, Platform: PlatformSensor, Category: "diagnostic", Disabled: true,
	},
	{
		Key: KeyModel, Group: GroupStatic, Read: RegProduct,
		Name: "Model", NameDE: "Modell",
		Kind: KindText, Platform: PlatformSensor, Category: "diagnostic",
	},
	{
		Key: KeyFirmware, Group: GroupStatic, Read: RegFirmware,
		Name: "Firmware", NameDE: "Firmware",
		Kind: KindText, Platform: PlatformSensor, Category: "diagnostic",
	},
	{
		Key: KeyHWRevision, Group: GroupStatic, Read: RegHWRevision,
		Name: "Hardware revision", NameDE: "Hardware-Revision",
		Kind: KindNumber, Platform: PlatformSensor, Scale: 1, Category: "diagnostic", Disabled: true,
	},
	{
		Key: KeyHWRevisionMS10, Group: GroupStatic, Read: RegHWRevisionMS10,
		Name: "Hardware revision KC-MS10", NameDE: "Hardware-Revision KC-MS10",
		Kind: KindNumber, Platform: PlatformSensor, Scale: 1, Category: "diagnostic", Disabled: true,
	},
}

func currentItem(key string, reg *Register, name, nameDE string) *Item {
	return &Item{
		Key: key, Group: GroupNow, Read: reg, Name: name, NameDE: nameDE,
		Kind: KindNumber, Platform: PlatformSensor, Unit: "A", Scale: 0.001, Precision: 3,
		DeviceClass: "current", StateClass: "measurement",
	}
}

func voltageItem(key string, reg *Register, name, nameDE string) *Item {
	return &Item{
		Key: key, Group: GroupNow, Read: reg, Name: name, NameDE: nameDE,
		Kind: KindNumber, Platform: PlatformSensor, Unit: "V", Scale: 1,
		DeviceClass: "voltage", StateClass: "measurement",
	}
}

// Catalog is the ordered item list.
type Catalog struct {
	Items []*Item
	byKey map[string]*Item
}

// NewCatalog returns the P40 catalog. withRFID includes the opt-in RFID
// item; without it register 1500 is never read.
func NewCatalog(withRFID bool) *Catalog {
	c := &Catalog{byKey: map[string]*Item{}}
	for _, it := range catalog {
		if it.RFID && !withRFID {
			continue
		}
		c.Items = append(c.Items, it)
		c.byKey[it.Key] = it
	}
	return c
}

// Item returns the item with key, or nil.
func (c *Catalog) Item(key string) *Item { return c.byKey[key] }

// ByGroup returns the items of g in catalog order.
func (c *Catalog) ByGroup(g Group) []*Item {
	var out []*Item
	for _, it := range c.Items {
		if it.Group == g {
			out = append(out, it)
		}
	}
	return out
}
