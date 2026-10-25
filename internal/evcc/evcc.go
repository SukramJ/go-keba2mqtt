// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package evcc is this bridge's side of the shared evcc profile — the
// one MQTT surface go-keba2mqtt, go-mtec2mqtt and go-zendure2mqtt expose
// for evcc (https://evcc.io), so an evcc.yaml reads the same for each:
//
//	<name>/status/<serial>/evcc/<key>   {"val": …}  (evcc: `jq: .val`)
//	<name>/set/<serial>/evcc/<key>      plain value or {"val": …}
//
// A wallbox has a single role, so its keys sit directly under `evcc/`.
// Units and signs are evcc's: W, kWh, A, V; power is positive while
// charging.
//
// The package is pure: it derives the profile's status values from the
// wallbox's decoded items and parses its commands. The coordinator owns
// the I/O.
package evcc

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/SukramJ/go-keba2mqtt/internal/keba"
)

// Group is the item group of the profile.
const Group = "evcc"

// Status keys (published).
const (
	KeyStatus     = "status"
	KeyEnabled    = "enabled"
	KeyMaxCurrent = "maxcurrent"
	KeyPhases     = "phases"
	KeyPower      = "power"
	KeyEnergy     = "energy"
	KeyCurrentL1  = "current_l1"
	KeyCurrentL2  = "current_l2"
	KeyCurrentL3  = "current_l3"
	KeyVoltageL1  = "voltage_l1"
	KeyVoltageL2  = "voltage_l2"
	KeyVoltageL3  = "voltage_l3"
	KeyIdentify   = "identify"
)

// Command keys (routed).
const (
	CmdEnable           = "enable"
	CmdMaxCurrent       = "maxcurrent"
	CmdMaxCurrentMillis = "maxcurrentmillis"
	CmdPhases1p3p       = "phases1p3p"
)

// Commands lists every command key.
var Commands = []string{CmdEnable, CmdMaxCurrent, CmdMaxCurrentMillis, CmdPhases1p3p}

// IEC 61851 vehicle states as evcc names them.
const (
	StatusA = "A" // no vehicle
	StatusB = "B" // vehicle connected, not charging
	StatusC = "C" // charging
	StatusE = "E" // error
	StatusF = "F" // charging station unavailable
)

// passthrough maps profile keys to the wallbox items they mirror
// unchanged (same unit, same sign).
var passthrough = map[string]string{
	KeyPower:      keba.KeyPower,
	KeyEnergy:     keba.KeyTotalEnergy,
	KeyCurrentL1:  keba.KeyCurrentL1,
	KeyCurrentL2:  keba.KeyCurrentL2,
	KeyCurrentL3:  keba.KeyCurrentL3,
	KeyVoltageL1:  keba.KeyVoltageL1,
	KeyVoltageL2:  keba.KeyVoltageL2,
	KeyVoltageL3:  keba.KeyVoltageL3,
	KeyMaxCurrent: keba.KeyChargingCurrent,
	KeyIdentify:   keba.KeyRFID,
}

// StatusKeys lists every status key the profile can publish, for the
// self-echo guard and the documentation contract test.
func StatusKeys() []string {
	return []string{
		KeyStatus, KeyEnabled, KeyMaxCurrent, KeyPhases, KeyPower, KeyEnergy,
		KeyCurrentL1, KeyCurrentL2, KeyCurrentL3,
		KeyVoltageL1, KeyVoltageL2, KeyVoltageL3, KeyIdentify,
	}
}

// Derive computes the profile values for the wallbox values present in
// values (decoded items keyed by item key) and raw (register address →
// raw value). Only keys whose inputs are present are returned, so a
// partial read never publishes a guess.
func Derive(values map[string]any, raw map[uint16]uint32) map[string]any {
	out := map[string]any{}
	for k, src := range passthrough {
		if v, ok := values[src]; ok {
			out[k] = v
		}
	}
	state, okState := raw[keba.RegChargingState.Address]
	cable, okCable := raw[keba.RegCableState.Address]
	if okState && okCable {
		out[KeyStatus] = Status(state, cable)
	}
	if v, ok := raw[keba.RegMaxChargingCurrent.Address]; ok {
		out[KeyEnabled] = v > 0
	}
	if v, ok := raw[keba.RegPhaseSwitchState.Address]; ok && (v == 1 || v == 3) {
		out[KeyPhases] = int64(v)
	}
	return out
}

// Status maps the P40's charging state (register 1000) and cable state
// (register 1004) onto evcc's A–F.
//
// The cable state's bit 2 (values 5 and 7, and 7 on cable variants) is
// "connected to the vehicle"; everything below it is "no vehicle".
func Status(chargingState, cableState uint32) string {
	switch chargingState {
	case keba.StateError:
		return StatusE
	case keba.StateStartup:
		return StatusF
	}
	if cableState&4 == 0 {
		return StatusA
	}
	if chargingState == keba.StateCharging {
		return StatusC
	}
	return StatusB
}

// ErrCommand is returned for a command payload the profile cannot read.
var ErrCommand = errors.New("evcc: invalid command")

// ParseBool reads evcc's enable payload: true/false, 1/0, on/off.
func ParseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "on", "yes":
		return true, nil
	case "false", "0", "off", "no":
		return false, nil
	}
	return false, fmt.Errorf("%w: %q is not a boolean", ErrCommand, s)
}

// ParseCurrent reads a current in A (maxcurrent sends an integer,
// maxcurrentmillis a float).
func ParseCurrent(s string) (float64, error) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return 0, fmt.Errorf("%w: %q is not a current", ErrCommand, s)
	}
	return f, nil
}

// ParsePhases reads phases1p3p: 1 or 3.
func ParsePhases(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || (n != 1 && n != 3) {
		return 0, fmt.Errorf("%w: phases %q must be 1 or 3", ErrCommand, s)
	}
	return n, nil
}

// PhaseOption returns the wallbox's phases option for n (1 or 3).
func PhaseOption(n int) string {
	if n == 1 {
		return keba.OptionOnePhase
	}
	return keba.OptionThreePhase
}
