// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type flakyStarter struct{ failures, calls int }

func (f *flakyStarter) Start(context.Context) error {
	f.calls++
	if f.calls <= f.failures {
		return errors.New("broker not up yet")
	}
	return nil
}

// A broker still booting is retried, not fatal.
func TestStartMQTTRetries(t *testing.T) {
	t.Parallel()
	s := &flakyStarter{failures: 3}
	if err := startMQTT(context.Background(), s, time.Millisecond, 2*time.Millisecond, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	if s.calls != 4 {
		t.Fatalf("%d attempts, want 4", s.calls)
	}
}

func TestStartMQTTStopsOnCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := startMQTT(ctx, &flakyStarter{failures: 100}, time.Hour, time.Hour, slog.New(slog.DiscardHandler))
	if !errors.Is(err, context.Canceled) || fatalErr(err) {
		t.Fatalf("err = %v; a cancelled start must not be fatal", err)
	}
}

func TestDeferredTransportBeforeWire(t *testing.T) {
	t.Parallel()
	var d deferredTransport
	if err := d.Publish(context.Background(), "t", nil, 0, false); !errors.Is(err, errTransportNotWired) {
		t.Fatalf("err = %v", err)
	}
}

// The healthcheck is a no-op (healthy) without a web UI, and unhealthy
// when the UI should answer but does not.
func TestHealthcheck(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	cfg := filepath.Join(dir, "c.yaml")
	write := func(s string) {
		if err := os.WriteFile(cfg, []byte("MODBUS_IP: 127.0.0.1\nMQTT_SERVER: localhost\n"+s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("WEB_ENABLE: false\n")
	if code := runHealthcheck(cfg); code != 0 {
		t.Fatalf("web off: exit %d", code)
	}
	write("WEB_ENABLE: true\nWEB_BIND: 127.0.0.1:1\n")
	if code := runHealthcheck(cfg); code != 1 {
		t.Fatalf("nothing listening: exit %d", code)
	}
}
