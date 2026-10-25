// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package keba

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Spec §2 "Available functions": the P40 answers one register per
// request, at most two words, and expects at least 0.5 s between reads
// and 5 s between writes.
const (
	DefaultReadInterval  = 500 * time.Millisecond
	DefaultWriteInterval = 5 * time.Second
	// DefaultUnitID is the P40's factory unit id (spec §2).
	DefaultUnitID = 255
)

// Transport is the subset of *modbus.Client (github.com/SukramJ/go-modbus)
// the device drives.
type Transport interface {
	Connect(ctx context.Context) error
	Close() error
	IsConnected() bool
	ReadHoldingRegisters(ctx context.Context, address, count uint16) ([]uint16, error)
	WriteSingleRegister(ctx context.Context, address, value uint16) error
}

// Device is a KEBA P40 behind a paced Modbus-TCP transport.
//
// Every request goes through one gate that enforces the spec's request
// intervals for every caller at once — the poll loop, the write worker
// and the web UI share one wallbox, and pacing them separately would
// let two of them together exceed what the device tolerates. A write
// waiting out its 5 s interval does not hold the gate, so reads continue
// in the meantime.
type Device struct {
	tr            Transport
	readInterval  time.Duration
	writeInterval time.Duration
	now           func() time.Time

	mu        sync.Mutex // serialises requests and guards the fields below
	lastReq   time.Time
	lastWrite time.Time
	// writesWaiting counts writes parked in do. Reads yield to a write
	// whose write interval has passed; see do.
	writesWaiting int
}

// DeviceOption configures a [Device].
type DeviceOption func(*Device)

// WithIntervals overrides the minimum spacing between requests and
// between writes. Zero keeps the default.
func WithIntervals(read, write time.Duration) DeviceOption {
	return func(d *Device) {
		if read > 0 {
			d.readInterval = read
		}
		if write > 0 {
			d.writeInterval = write
		}
	}
}

// WithClock injects the clock (tests).
func WithClock(now func() time.Time) DeviceOption {
	return func(d *Device) { d.now = now }
}

// NewDevice wraps tr.
func NewDevice(tr Transport, opts ...DeviceOption) *Device {
	d := &Device{
		tr:            tr,
		readInterval:  DefaultReadInterval,
		writeInterval: DefaultWriteInterval,
		now:           time.Now,
	}
	for _, o := range opts {
		o(d)
	}
	return d
}

// Connect opens the transport.
func (d *Device) Connect(ctx context.Context) error { return d.tr.Connect(ctx) }

// Close closes the transport.
func (d *Device) Close() error { return d.tr.Close() }

// IsConnected reports whether the transport holds a connection. It never
// blocks behind a request.
func (d *Device) IsConnected() bool { return d.tr.IsConnected() }

// ReadRegister reads one UINT32 register (two words, high word first).
func (d *Device) ReadRegister(ctx context.Context, reg *Register) (uint32, error) {
	var regs []uint16
	err := d.do(ctx, false, func(ctx context.Context) error {
		var err error
		regs, err = d.tr.ReadHoldingRegisters(ctx, reg.Address, 2)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("read %d (%s): %w", reg.Address, reg.Name, err)
	}
	return uint32(regs[0])<<16 | uint32(regs[1]), nil
}

// WriteRegister writes one UINT16 register.
func (d *Device) WriteRegister(ctx context.Context, reg *Register, value uint16) error {
	err := d.do(ctx, true, func(ctx context.Context) error {
		return d.tr.WriteSingleRegister(ctx, reg.Address, value)
	})
	if err != nil {
		return fmt.Errorf("write %d (%s) = %d: %w", reg.Address, reg.Name, value, err)
	}
	return nil
}

// do runs one request once the intervals allow it.
func (d *Device) do(ctx context.Context, write bool, fn func(context.Context) error) error {
	if write {
		d.mu.Lock()
		d.writesWaiting++
		d.mu.Unlock()
		defer func() {
			d.mu.Lock()
			d.writesWaiting--
			d.mu.Unlock()
		}()
	}
	for {
		d.mu.Lock()
		now := d.now()
		wait := d.lastReq.Add(d.readInterval).Sub(now)
		switch {
		case write:
			wait = max(wait, d.lastWrite.Add(d.writeInterval).Sub(now))
		case d.writesWaiting > 0 && !now.Before(d.lastWrite.Add(d.writeInterval)):
			// A write is due. Without this a poll loop that reads back to
			// back wins the race for every 0.5 s slot as often as not, and
			// a command that should reach the wallbox after 5 s took 10 or
			// 20 — measured against a simulated P40. The read waits one
			// slot; the write takes the next.
			wait = max(wait, d.readInterval)
		}
		if wait <= 0 {
			err := fn(ctx)
			end := d.now()
			d.lastReq = end
			if write {
				d.lastWrite = end
			}
			d.mu.Unlock()
			return err
		}
		d.mu.Unlock()
		// Wait without the gate so other requests are not held up by a
		// write that is still inside its interval.
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Reading is the outcome of reading one group.
type Reading struct {
	// Values maps item key to decoded value.
	Values map[string]any
	// Raw maps register address to raw value, for items that need it
	// (evcc derivations, the web UI).
	Raw map[uint16]uint32
	// Errs collects per-register failures; Values holds what succeeded.
	Errs []error
}

// Err joins the per-register errors.
func (r Reading) Err() error { return errors.Join(r.Errs...) }

// ReadItems reads every readable item in items, each register once, and
// decodes it. firmware selects the energy scale (0 = not yet known).
// A failing register does not stop the others; it is recorded in Errs.
// Cancellation stops the read and is reported once.
func (d *Device) ReadItems(ctx context.Context, items []*Item, firmware uint32) Reading {
	out := Reading{Values: map[string]any{}, Raw: map[uint16]uint32{}}
	failed := map[uint16]bool{}
	for _, it := range items {
		if it.Read == nil {
			continue
		}
		addr := it.Read.Address
		if failed[addr] {
			continue
		}
		raw, ok := out.Raw[addr]
		if !ok {
			v, err := d.ReadRegister(ctx, it.Read)
			if err != nil {
				if ctx.Err() != nil {
					out.Errs = append(out.Errs, ctx.Err())
					return out
				}
				failed[addr] = true
				out.Errs = append(out.Errs, err)
				continue
			}
			raw = v
			out.Raw[addr] = raw
		}
		val, err := Decode(it, raw, firmware)
		if err != nil && !errors.Is(err, ErrUnknownCode) {
			out.Errs = append(out.Errs, err)
			continue
		}
		if err != nil {
			out.Errs = append(out.Errs, err)
		}
		out.Values[it.Key] = val
	}
	return out
}
