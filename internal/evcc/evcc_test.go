// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package evcc

import (
	"errors"
	"testing"

	"github.com/SukramJ/go-keba2mqtt/internal/keba"
)

func TestStatus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		state, cable uint32
		want         string
	}{
		{keba.StateReady, 0, StatusA},
		{keba.StateNotReady, 1, StatusA},
		{keba.StateReady, 3, StatusA},                // socket: plugged at station only
		{keba.StateReady, 5, StatusB},                // vehicle plugged, not locked
		{keba.StateNotReady, 7, StatusB},             // vehicle plugged, waiting for authorisation
		{keba.StateCharging, 7, StatusC},             // charging
		{keba.StateInterrupt, 7, StatusB},            // suspended: connected, not charging
		{keba.StateError, 7, StatusE},                // error wins over cable
		{keba.StateStartup, 0, StatusF},              // booting
		{keba.StateCharging, 3 /* cable */, StatusA}, // implausible, no vehicle reported
	}
	for _, tc := range cases {
		if got := Status(tc.state, tc.cable); got != tc.want {
			t.Errorf("Status(%d, %d) = %s, want %s", tc.state, tc.cable, got, tc.want)
		}
	}
}

func TestDeriveOnlyFromWhatWasRead(t *testing.T) {
	t.Parallel()
	got := Derive(map[string]any{keba.KeyPower: 11000.0, keba.KeyTotalEnergy: 123.4}, map[uint16]uint32{})
	if len(got) != 2 || got[KeyPower] != 11000.0 || got[KeyEnergy] != 123.4 {
		t.Fatalf("Derive = %v", got)
	}

	raw := map[uint16]uint32{
		keba.RegChargingState.Address:      keba.StateCharging,
		keba.RegCableState.Address:         7,
		keba.RegMaxChargingCurrent.Address: 0,
		keba.RegPhaseSwitchState.Address:   1,
	}
	got = Derive(map[string]any{}, raw)
	if got[KeyStatus] != StatusC || got[KeyEnabled] != false || got[KeyPhases] != int64(1) {
		t.Fatalf("Derive = %v", got)
	}
}

func TestParsers(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]bool{"true": true, "1": true, "ON": true, "false": false, "0": false, "off": false} {
		if got, err := ParseBool(in); err != nil || got != want {
			t.Errorf("ParseBool(%q) = %v/%v", in, got, err)
		}
	}
	if _, err := ParseBool("maybe"); !errors.Is(err, ErrCommand) {
		t.Error("ParseBool accepted garbage")
	}
	if f, err := ParseCurrent("16"); err != nil || f != 16 {
		t.Errorf("ParseCurrent(16) = %v/%v", f, err)
	}
	if f, err := ParseCurrent("6.5"); err != nil || f != 6.5 {
		t.Errorf("ParseCurrent(6.5) = %v/%v", f, err)
	}
	for _, bad := range []string{"-1", "x", "NaN"} {
		if _, err := ParseCurrent(bad); err == nil {
			t.Errorf("ParseCurrent(%q) accepted", bad)
		}
	}
	if n, err := ParsePhases("3"); err != nil || n != 3 {
		t.Errorf("ParsePhases(3) = %v/%v", n, err)
	}
	if _, err := ParsePhases("2"); err == nil {
		t.Error("ParsePhases(2) accepted")
	}
	if PhaseOption(1) != keba.OptionOnePhase || PhaseOption(3) != keba.OptionThreePhase {
		t.Error("PhaseOption")
	}
}
