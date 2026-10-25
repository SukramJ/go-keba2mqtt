// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package main

import (
	"bytes"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/SukramJ/go-modbus/modbustest"

	"github.com/SukramJ/go-keba2mqtt/internal/keba"
)

func p40(t *testing.T) (bank *modbustest.Bank, flags []string) {
	t.Helper()
	// No config file or KEBA_* variable may leak in from the host.
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("KEBA_MODBUS_READ_INTERVAL", "100")
	t.Setenv("KEBA_MODBUS_WRITE_INTERVAL", "1")

	bank = modbustest.NewBank().MaxRead(2)
	for _, it := range keba.NewCatalog(true).Items {
		if it.Read != nil {
			bank.SetUint32(it.Read.Address, 0)
		}
	}
	bank.SetUint32(keba.RegFirmware.Address, 10201).
		SetUint32(keba.RegChargingState.Address, keba.StateCharging).
		SetUint32(keba.RegCurrentL1.Address, 16000)
	srv := modbustest.NewServer(t, bank.Handle)
	return bank, []string{"--host", srv.Host(), "--port", strconv.Itoa(srv.Port())}
}

func TestListNeedsNoWallbox(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"list"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	for _, want := range []string{"charging_current", "5004", "1 phase | 3 phases", "rfid_tag"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("list misses %q", want)
		}
	}
}

func TestReadAndWrite(t *testing.T) {
	bank, flags := p40(t)
	var out, errOut bytes.Buffer
	if code := run(append(flags, "read", keba.KeyChargingState, keba.KeyCurrentL1), &out, &errOut); code != 0 {
		t.Fatalf("read exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "Charging") || !strings.Contains(out.String(), "16") {
		t.Errorf("read output %q", out.String())
	}

	out.Reset()
	if code := run(append(flags, "write", keba.KeyChargingCurrent, "12.5"), &out, &errOut); code != 0 {
		t.Fatalf("write exit %d: %s", code, errOut.String())
	}
	if code := run(append(flags, "write", keba.KeyPhases, "3 Phasen"), &out, &errOut); code != 0 {
		t.Fatalf("write exit %d: %s", code, errOut.String())
	}
	want := []modbustest.Write{{Address: 5004, Value: 12500}, {Address: 5052, Value: 1}}
	if got := bank.Writes(); !slices.Equal(got, want) {
		t.Fatalf("writes %v, want %v", got, want)
	}

	errOut.Reset()
	if code := run(append(flags, "write", keba.KeyChargingCurrent, "40"), &out, &errOut); code != 1 {
		t.Fatalf("out-of-range write exit %d", code)
	}
	if code := run(append(flags, "write", keba.KeyPower, "1"), &out, &errOut); code != 1 {
		t.Fatalf("read-only write exit %d", code)
	}
	if len(bank.Writes()) != 2 {
		t.Fatalf("refused writes reached the wallbox: %v", bank.Writes())
	}

	out.Reset()
	if code := run(append(flags, "raw", "1018"), &out, &errOut); code != 0 {
		t.Fatalf("raw exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "10201") {
		t.Errorf("raw output %q", out.String())
	}
}

func TestMissingAddress(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out, errOut bytes.Buffer
	if code := run([]string{"read"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "--host") {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}
	if code := run([]string{"bogus"}, &out, &errOut); code == 0 {
		t.Fatal("unknown command accepted")
	}
}
