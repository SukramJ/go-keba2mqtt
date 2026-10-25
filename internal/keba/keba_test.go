// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package keba

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/SukramJ/go-modbus"
	"github.com/SukramJ/go-modbus/modbustest"
)

// Spec examples, verbatim, through the catalog's own items.
func TestDecodeSpecExamples(t *testing.T) {
	t.Parallel()
	c := NewCatalog(true)
	cases := []struct {
		key  string
		raw  uint32
		fw   uint32
		want any
	}{
		{KeyCurrentL1, 645, 0, 0.645},
		{KeyCurrentL2, 1011, 0, 1.011},
		{KeyPower, 98661, 0, 98.7},
		{KeyTotalEnergy, 38101, 10201, 3.8101},
		{KeyPowerFactor, 928, 0, 92.8},
		{KeyVoltageL1, 230, 0, int64(230)},
		{KeySerial, 18416854, 0, "18416854"},
		{KeyFirmware, 10000, 0, "1.0.0"},
		{KeyFirmware, 10201, 0, "1.2.1"},
		{KeyHWRevision, 3, 0, int64(3)},
		{KeyChargingCurrent, 10000, 0, 10.0},
		{KeyMaxSupportedCurrent, 10000, 0, 10.0},
		{KeyRFID, 3570234960, 0, "D4CD7650"},
		{KeyRFID, 0, 0, ""},
		{KeySessionEnergy, 165, 10201, 0.0165},
		{KeyFailsafeCurrent, 6000, 0, 6.0},
		{KeyFailsafeTimeout, 11, 0, int64(11)},
		{KeyFastCharging, 1, 0, true},
		{KeyChargingState, 3, 0, "Charging"},
		{KeyCableState, 7, 0, "Plugged in vehicle, locked"},
		{KeyPhases, 1, 0, OptionOnePhase},
		{KeyPhaseSwitchSource, 3, 0, OptionSourceModbus},
		{KeyModel, 4212311, 0, "KC-P40, 32 A, cable, 3-phase, legal meter, RFID, button"},
		{KeyProductCode, 4212311, 0, "4212311"},
	}
	for _, tc := range cases {
		got, err := Decode(c.Item(tc.key), tc.raw, tc.fw)
		if err != nil {
			t.Errorf("%s(%d): %v", tc.key, tc.raw, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s(%d) = %#v, want %#v", tc.key, tc.raw, got, tc.want)
		}
	}
}

// Spec §5: firmware below 1.2.1 reports 1036 and 1502 in Wh.
func TestEnergyFirmwareBug(t *testing.T) {
	t.Parallel()
	c := NewCatalog(false)
	for _, key := range []string{KeyTotalEnergy, KeySessionEnergy} {
		old, _ := Decode(c.Item(key), 3810, 10200)
		fixed, _ := Decode(c.Item(key), 38100, 10201)
		if old != 3.81 || fixed != 3.81 {
			t.Errorf("%s: old firmware %v, fixed firmware %v, want 3.81 both", key, old, fixed)
		}
	}
}

func TestUnknownEnumCode(t *testing.T) {
	t.Parallel()
	got, err := Decode(NewCatalog(false).Item(KeyChargingState), 9, 0)
	if got != UnknownToken || !errors.Is(err, ErrUnknownCode) {
		t.Fatalf("got %v/%v, want Unknown + ErrUnknownCode", got, err)
	}
}

func TestEncodeNumberRanges(t *testing.T) {
	t.Parallel()
	c := NewCatalog(false)
	cur := c.Item(KeyChargingCurrent)
	for _, tc := range []struct {
		v    float64
		want uint16
		ok   bool
	}{
		{0, 0, true},
		{6, 6000, true},
		{16.5, 16500, true},
		{32, 32000, true},
		{5.9, 0, false},
		{32.1, 0, false},
		{-1, 0, false},
	} {
		got, err := EncodeNumber(cur, tc.v)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("current %v = %d/%v, want %d ok=%v", tc.v, got, err, tc.want, tc.ok)
		}
	}
	timeout := c.Item(KeyFailsafeTimeout)
	if _, err := EncodeNumber(timeout, 3); !errors.Is(err, ErrValue) {
		t.Errorf("timeout 3 s accepted (valid: 0 or 5..600)")
	}
	if v, err := EncodeNumber(c.Item(KeyEnergyLimit), 0.01); err != nil || v != 1 {
		t.Errorf("energy 0.01 kWh = %d/%v, want 1 (10 Wh)", v, err)
	}
}

func TestEncodeOption(t *testing.T) {
	t.Parallel()
	ph := NewCatalog(false).Item(KeyPhases)
	for in, want := range map[string]uint16{"1 phase": 0, "3 PHASES": 1, "1 Phase": 0, "3 Phasen": 1} {
		got, _, err := EncodeOption(ph, in)
		if err != nil || got != want {
			t.Errorf("%q = %d/%v, want %d", in, got, err, want)
		}
	}
	if _, _, err := EncodeOption(ph, "2 phases"); !errors.Is(err, ErrValue) {
		t.Errorf("unknown option accepted")
	}
}

func TestRFIDIsOptIn(t *testing.T) {
	t.Parallel()
	if NewCatalog(false).Item(KeyRFID) != nil {
		t.Fatal("RFID item present without opt-in")
	}
	if NewCatalog(true).Item(KeyRFID) == nil {
		t.Fatal("RFID item missing with opt-in")
	}
}

// Every write register is distinct from every read register, and every
// key is unique — the catalog's two invariants.
func TestCatalogInvariants(t *testing.T) {
	t.Parallel()
	c := NewCatalog(true)
	keys := map[string]bool{}
	for _, it := range c.Items {
		if keys[it.Key] {
			t.Errorf("duplicate key %s", it.Key)
		}
		keys[it.Key] = true
		if it.Read != nil && it.Read.Address >= 5000 {
			t.Errorf("%s reads write register %d", it.Key, it.Read.Address)
		}
		if it.Write != nil && it.Write.Address < 5000 {
			t.Errorf("%s writes read register %d", it.Key, it.Write.Address)
		}
		if it.Read == nil && it.Write == nil {
			t.Errorf("%s has no register", it.Key)
		}
		if it.Kind == KindEnum && it.Writable() {
			for _, o := range it.Options {
				if o.Write < 0 {
					t.Errorf("%s option %q not writable", it.Key, o.Label)
				}
			}
		}
	}
}

// p40 returns a simulated wallbox answering like the P40: UINT32 reads
// of at most two words, read registers refusing writes.
func p40(t *testing.T) (*modbustest.Bank, *modbus.Client) {
	t.Helper()
	bank := modbustest.NewBank().MaxRead(2)
	for _, it := range catalog {
		if it.Read != nil {
			bank.SetUint32(it.Read.Address, 0).ReadOnly(it.Read.Address, it.Read.Address+1)
		}
	}
	srv := modbustest.NewServer(t, bank.Handle)
	cl := modbus.New(modbus.Config{Host: srv.Host(), Port: srv.Port(), UnitID: DefaultUnitID, Timeout: time.Second})
	if err := cl.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	return bank, cl
}

func TestDeviceReadsEachRegisterOnce(t *testing.T) {
	t.Parallel()
	bank, cl := p40(t)
	bank.SetUint32(RegProduct.Address, 4212311).SetUint32(RegFirmware.Address, 10201).SetUint32(RegSerialNumber.Address, 18416854)
	d := NewDevice(cl, WithIntervals(time.Millisecond, time.Millisecond))
	c := NewCatalog(false)
	r := d.ReadItems(context.Background(), c.ByGroup(GroupStatic), 0)
	if err := r.Err(); err != nil {
		t.Fatal(err)
	}
	if r.Values[KeyModel] != "KC-P40, 32 A, cable, 3-phase, legal meter, RFID, button" || r.Values[KeyProductCode] != "4212311" {
		t.Fatalf("product decoded as %v / %v", r.Values[KeyModel], r.Values[KeyProductCode])
	}
	if r.Values[KeyFirmware] != "1.2.1" || r.Values[KeySerial] != "18416854" {
		t.Fatalf("values = %v", r.Values)
	}
	if len(r.Raw) != 5 {
		t.Fatalf("read %d registers, want 5 (1016 shared by two items)", len(r.Raw))
	}
}

func TestDeviceWriteAndRefusal(t *testing.T) {
	t.Parallel()
	bank, cl := p40(t)
	d := NewDevice(cl, WithIntervals(time.Millisecond, time.Millisecond))
	if err := d.WriteRegister(context.Background(), RegSetChargingCurrent, 16000); err != nil {
		t.Fatal(err)
	}
	if w := bank.Writes(); len(w) != 1 || w[0] != (modbustest.Write{Address: 5004, Value: 16000}) {
		t.Fatalf("writes = %v", w)
	}
	if err := d.WriteRegister(context.Background(), RegChargingState, 1); err == nil {
		t.Fatal("write to a read register accepted")
	}
}

// The gate spaces requests by the read interval and writes by the write
// interval, across callers.
func TestDevicePacing(t *testing.T) {
	t.Parallel()
	_, cl := p40(t)
	const readGap, writeGap = 40 * time.Millisecond, 150 * time.Millisecond
	d := NewDevice(cl, WithIntervals(readGap, writeGap))
	ctx := context.Background()

	start := time.Now()
	for range 3 {
		if _, err := d.ReadRegister(ctx, RegChargingState); err != nil {
			t.Fatal(err)
		}
	}
	if el := time.Since(start); el < 2*readGap {
		t.Fatalf("3 reads took %v, want >= %v", el, 2*readGap)
	}

	start = time.Now()
	for range 2 {
		if err := d.WriteRegister(ctx, RegSetChargingCurrent, 6000); err != nil {
			t.Fatal(err)
		}
	}
	if el := time.Since(start); el < writeGap {
		t.Fatalf("2 writes took %v, want >= %v", el, writeGap)
	}

	// A read issued while a write waits out its interval is not held up.
	writeDone := make(chan error, 1)
	go func() { writeDone <- d.WriteRegister(ctx, RegSetChargingCurrent, 7000) }()
	time.Sleep(readGap + 10*time.Millisecond)
	start = time.Now()
	if _, err := d.ReadRegister(ctx, RegChargingState); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el > writeGap/2 {
		t.Fatalf("read waited %v behind a pending write", el)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
}

func TestDeviceCancelledWait(t *testing.T) {
	t.Parallel()
	_, cl := p40(t)
	d := NewDevice(cl, WithIntervals(time.Millisecond, time.Hour))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := d.WriteRegister(ctx, RegSetChargingCurrent, 6000); err != nil {
		t.Fatal(err)
	}
	if err := d.WriteRegister(ctx, RegSetChargingCurrent, 6000); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline", err)
	}
}

// A write that is due is not starved by a reader polling back to back:
// it lands within its write interval plus a couple of read slots.
func TestDeviceWritePriority(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("timer granularity (~15.6 ms) is too coarse for the margins")
	}
	t.Parallel()
	_, cl := p40(t)
	const readGap, writeGap = 40 * time.Millisecond, 200 * time.Millisecond
	d := NewDevice(cl, WithIntervals(readGap, writeGap))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for ctx.Err() == nil {
			_, _ = d.ReadRegister(ctx, RegChargingState)
		}
	}()
	for i := range 5 {
		start := time.Now()
		if err := d.WriteRegister(ctx, RegSetChargingCurrent, 6000); err != nil {
			t.Fatal(err)
		}
		if el := time.Since(start); i > 0 && el > writeGap+3*readGap {
			t.Fatalf("write %d took %v under continuous reads, want <= %v", i, el, writeGap+3*readGap)
		}
	}
}
