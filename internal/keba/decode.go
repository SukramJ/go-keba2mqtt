// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package keba

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// FirmwareEnergyFixed is the first software version whose registers
// 1036 and 1502 report 0.1 Wh. Below it they report Wh (spec §5).
const FirmwareEnergyFixed = 10201

// Decode errors.
var (
	// ErrUnknownCode is returned for an enum register value the catalog
	// does not map. The decoded value is still usable: it is "Unknown".
	ErrUnknownCode = errors.New("keba: unmapped enum code")
)

// UnknownToken is published for an enum code the catalog does not map.
const UnknownToken = "Unknown"

// Decode turns one raw register value into the item's published value.
// firmware is the raw software version (register 1018) or 0 when not yet
// known; it selects the energy scale.
//
// An unmapped enum code decodes to [UnknownToken] together with
// [ErrUnknownCode], so the caller can publish the token and log the code.
func Decode(it *Item, raw, firmware uint32) (any, error) {
	switch it.Key {
	case KeySerial:
		return strconv.FormatUint(uint64(raw), 10), nil
	case KeyProductCode:
		return strconv.FormatUint(uint64(raw), 10), nil
	case KeyModel:
		return ProductDescription(raw), nil
	case KeyFirmware:
		return FormatFirmware(raw), nil
	case KeyRFID:
		return FormatRFID(raw), nil
	}
	switch it.Kind {
	case KindEnum:
		for _, o := range it.Options {
			if o.Read == int64(raw) {
				return o.Label, nil
			}
		}
		return UnknownToken, fmt.Errorf("%w: %s=%d", ErrUnknownCode, it.Key, raw)
	case KindBool:
		return raw != 0, nil
	case KindText:
		return strconv.FormatUint(uint64(raw), 10), nil
	case KindNumber:
		scale := it.Scale
		if it.EnergyRegister && firmware != 0 && firmware < FirmwareEnergyFixed {
			scale *= 10 // Wh, not 0.1 Wh
		}
		if scale == 0 {
			scale = 1
		}
		if scale == 1 && it.Precision == 0 {
			return int64(raw), nil
		}
		return Round(float64(raw)*scale, it.Precision), nil
	default:
		return nil, fmt.Errorf("keba: item %s has no readable value", it.Key)
	}
}

// Round rounds v to digits decimal places.
func Round(v float64, digits int) float64 {
	p := math.Pow10(digits)
	return math.Round(v*p) / p
}

// FormatFirmware renders register 1018 ("two digits per part, no dots")
// as a dotted version: 10201 → "1.2.1".
func FormatFirmware(raw uint32) string {
	return fmt.Sprintf("%d.%d.%d", raw/10000, (raw/100)%100, raw%100)
}

// FormatRFID renders register 1500 as the hexadecimal UID prefix the
// card reader saw (spec §3.6.1: 3570234960 → "D4CD7650"). Zero means no
// card was used and renders empty.
func FormatRFID(raw uint32) string {
	if raw == 0 {
		return ""
	}
	return fmt.Sprintf("%08X", raw)
}

// Product is register 1016 decoded (spec §3.4.2): one decimal digit per
// feature, family first.
type Product struct {
	Family    int
	Current   int // device current in A, 0 when unknown
	Connector string
	Phases    string
	Metering  string
	RFID      bool
	Button    bool
	Valid     bool
}

// DecodeProduct splits register 1016 into its feature digits. A value
// that is not seven digits long yields Valid=false.
func DecodeProduct(raw uint32) Product {
	s := strconv.FormatUint(uint64(raw), 10)
	if len(s) != 7 {
		return Product{}
	}
	d := func(i int) int { return int(s[i] - '0') }
	p := Product{Family: d(0), Valid: true}
	switch d(1) {
	case 1:
		p.Current = 16
	case 2:
		p.Current = 32
	}
	p.Connector = lookup(d(2), map[int]string{1: "cable", 2: "socket"})
	p.Phases = lookup(d(3), map[int]string{1: "1-phase", 2: "3-phase", 3: "3/1-phase switching", 4: "rotation"})
	p.Metering = lookup(d(4), map[int]string{0: "no meter", 1: "energy meter", 2: "MID meter", 3: "legal meter"})
	p.RFID = d(5) == 1
	p.Button = d(6) == 1
	return p
}

func lookup(code int, m map[int]string) string {
	if s, ok := m[code]; ok {
		return s
	}
	return "unknown (" + strconv.Itoa(code) + ")"
}

// ModelName is the Home Assistant device model for a product code.
func ModelName(raw uint32) string {
	if p := DecodeProduct(raw); p.Valid && p.Family == 4 {
		return "KeContact P40"
	}
	return "KeContact"
}

// ProductDescription renders register 1016 as a readable feature list,
// e.g. "KC-P40, 32 A, cable, 3-phase, legal meter, RFID, button".
func ProductDescription(raw uint32) string {
	p := DecodeProduct(raw)
	if !p.Valid {
		return "unknown (" + strconv.FormatUint(uint64(raw), 10) + ")"
	}
	parts := []string{fmt.Sprintf("KC-P%d0", p.Family)}
	if p.Current > 0 {
		parts = append(parts, fmt.Sprintf("%d A", p.Current))
	}
	parts = append(parts, p.Connector, p.Phases, p.Metering)
	if p.RFID {
		parts = append(parts, "RFID")
	}
	if p.Button {
		parts = append(parts, "button")
	}
	return strings.Join(parts, ", ")
}

// Write-path errors.
var (
	// ErrUnknownItem is returned for a key the catalog does not define.
	ErrUnknownItem = errors.New("keba: unknown item")
	// ErrNotWritable is returned for a read-only item.
	ErrNotWritable = errors.New("keba: item is read-only")
	// ErrValue is returned for a value the item cannot take.
	ErrValue = errors.New("keba: invalid value")
)

// EncodeNumber converts a number in the item's unit into the raw
// register value and checks it against the item's accepted ranges.
func EncodeNumber(it *Item, v float64) (uint16, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("%w: %s=%v", ErrValue, it.Key, v)
	}
	scale := it.WriteScale
	if scale == 0 {
		scale = 1
	}
	raw := math.Round(v * scale)
	for _, r := range it.WriteRanges {
		if raw >= r[0] && raw <= r[1] {
			if raw < 0 || raw > math.MaxUint16 {
				break
			}
			return uint16(raw), nil
		}
	}
	return 0, fmt.Errorf("%w: %s=%v outside %s", ErrValue, it.Key, v, describeRanges(it))
}

func describeRanges(it *Item) string {
	scale := it.WriteScale
	if scale == 0 {
		scale = 1
	}
	parts := make([]string, 0, len(it.WriteRanges))
	for _, r := range it.WriteRanges {
		lo := strconv.FormatFloat(r[0]/scale, 'f', -1, 64)
		hi := strconv.FormatFloat(r[1]/scale, 'f', -1, 64)
		if lo == hi {
			parts = append(parts, lo)
		} else {
			parts = append(parts, lo+".."+hi)
		}
	}
	return strings.Join(parts, " or ") + " " + it.Unit
}

// EncodeOption returns the raw write code and the wire label of the enum
// option whose English or German label is s (case-insensitive).
func EncodeOption(it *Item, s string) (code uint16, label string, err error) {
	s = strings.TrimSpace(s)
	for _, o := range it.Options {
		if strings.EqualFold(o.Label, s) || strings.EqualFold(o.LabelDE, s) {
			if o.Write < 0 || o.Write > math.MaxUint16 {
				return 0, "", fmt.Errorf("%w: %s=%q cannot be written", ErrValue, it.Key, s)
			}
			return uint16(o.Write), o.Label, nil
		}
	}
	return 0, "", fmt.Errorf("%w: %s=%q is not one of %s", ErrValue, it.Key, s, strings.Join(Labels(it), ", "))
}

// Labels returns an enum item's tokens in catalog order.
func Labels(it *Item) []string {
	out := make([]string, len(it.Options))
	for i, o := range it.Options {
		out[i] = o.Label
	}
	return out
}
