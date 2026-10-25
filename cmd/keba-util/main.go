// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Command keba-util talks to a KEBA KeContact P40 over Modbus TCP
// directly, without MQTT: list the catalog, read items or raw registers,
// write an item. It honours the same request intervals as the daemon,
// but does not coordinate with a running daemon — stop it first, the P40
// is meant to have one Modbus client.
//
//	keba-util [flags] list
//	keba-util [flags] read [key|group ...]      (default: every item)
//	keba-util [flags] raw <address> [address ...]
//	keba-util [flags] write <key> <value>
//
// The wallbox address comes from --host/--port/--unit, falling back to
// the daemon's config (--config, the standard search order, KEBA_* env).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/SukramJ/go-modbus"

	"github.com/SukramJ/go-keba2mqtt/internal/config"
	"github.com/SukramJ/go-keba2mqtt/internal/keba"
	"github.com/SukramJ/go-keba2mqtt/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// target is where and how to reach the wallbox.
type target struct {
	host    string
	port    int
	unit    int
	rfid    bool
	lang    string
	read    time.Duration
	write   time.Duration
	timeout time.Duration
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("keba-util", flag.ContinueOnError)
	fs.SetOutput(stderr)
	host := fs.String("host", "", "wallbox IP or hostname (default: MODBUS_IP from the config)")
	port := fs.Int("port", 0, "Modbus TCP port (default: config or 502)")
	unit := fs.Int("unit", -1, "Modbus unit id (default: config or 255)")
	cfgPath := fs.String("config", "", "explicit config.yaml path")
	rfid := fs.Bool("rfid", false, "include the RFID item (register 1500)")
	lang := fs.String("lang", "en", "display language for names (en|de)")
	showVersion := fs.Bool("version", false, "print build info and exit")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: keba-util [flags] list | read [key|group ...] | raw <address ...> | write <key> <value>\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		_, _ = fmt.Fprintln(stdout, version.String())
		return 0
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fs.Usage()
		return 2
	}

	cat := keba.NewCatalog(true)
	if rest[0] == "list" {
		list(stdout, cat, *lang)
		return 0
	}

	t := target{
		host: *host, port: *port, unit: *unit, rfid: *rfid, lang: *lang,
		read: keba.DefaultReadInterval, write: keba.DefaultWriteInterval, timeout: 5 * time.Second,
	}
	if err := t.fill(*cfgPath); err != nil {
		_, _ = fmt.Fprintln(stderr, "keba-util:", err)
		return 1
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	client := modbus.New(modbus.Config{Host: t.host, Port: t.port, UnitID: byte(t.unit), Timeout: t.timeout}) //nolint:gosec // unit validated to 0..255 in fill
	dev := keba.NewDevice(client, keba.WithIntervals(t.read, t.write))
	if err := dev.Connect(ctx); err != nil {
		_, _ = fmt.Fprintln(stderr, "keba-util:", err)
		return 1
	}
	defer func() { _ = dev.Close() }()

	var err error
	switch rest[0] {
	case "read":
		err = read(ctx, stdout, dev, keba.NewCatalog(t.rfid), rest[1:], t.lang)
	case "raw":
		err = raw(ctx, stdout, dev, rest[1:])
	case "write":
		err = write(ctx, stdout, dev, cat, rest[1:])
	default:
		fs.Usage()
		return 2
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "keba-util:", err)
		return 1
	}
	return 0
}

// fill completes the target from the daemon's config where the flags
// left it open. A missing or incomplete config is fine when --host is set.
func (t *target) fill(cfgPath string) error {
	var cfg *config.Config
	if cfgPath != "" {
		c, err := config.LoadFile(cfgPath, config.OSEnv{})
		if err != nil {
			return err
		}
		cfg = c
	} else if p, ok := config.Locate(config.OSEnv{}); ok {
		if c, err := config.LoadFile(p, config.OSEnv{}); err == nil {
			cfg = c
		}
	} else if c, err := config.Load(strings.NewReader(""), config.OSEnv{}); err == nil {
		cfg = c
	}
	if cfg != nil {
		if t.host == "" {
			t.host = cfg.ModbusIP
		}
		if t.port == 0 {
			t.port = cfg.ModbusPort
		}
		if t.unit < 0 {
			t.unit = int(cfg.ModbusUnitID)
		}
		t.read = cfg.ModbusReadIntervalDuration()
		t.write = cfg.ModbusWriteIntervalDuration()
		t.timeout = cfg.ModbusTimeoutDuration()
		t.rfid = t.rfid || cfg.RFIDEnable
	}
	if t.host == "" {
		return errors.New("no wallbox address: pass --host or configure MODBUS_IP")
	}
	if t.port == 0 {
		t.port = 502
	}
	if t.unit < 0 {
		t.unit = keba.DefaultUnitID
	}
	if t.unit > 255 {
		return fmt.Errorf("--unit %d out of range 0..255", t.unit)
	}
	return nil
}

func list(w io.Writer, cat *keba.Catalog, lang string) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "KEY\tGROUP\tREAD\tWRITE\tUNIT\tPLATFORM\tNAME")
	for _, it := range cat.Items {
		r, wr := "-", "-"
		if it.Read != nil {
			r = strconv.Itoa(int(it.Read.Address))
		}
		if it.Write != nil {
			wr = strconv.Itoa(int(it.Write.Address))
		}
		name := it.LocalizedName(lang)
		if len(it.Options) > 0 {
			name += " [" + strings.Join(keba.Labels(it), " | ") + "]"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", it.Key, it.Group, r, wr, it.Unit, it.Platform, name)
	}
	_ = tw.Flush()
}

// selectItems resolves keys and group names; none selects everything.
func selectItems(cat *keba.Catalog, names []string) ([]*keba.Item, error) {
	if len(names) == 0 {
		return cat.Items, nil
	}
	var out []*keba.Item
	for _, n := range names {
		if it := cat.Item(n); it != nil {
			out = append(out, it)
			continue
		}
		group := cat.ByGroup(keba.Group(n))
		if len(group) == 0 {
			return nil, fmt.Errorf("%w: %q (see `keba-util list`)", keba.ErrUnknownItem, n)
		}
		out = append(out, group...)
	}
	return out, nil
}

func read(ctx context.Context, w io.Writer, dev *keba.Device, cat *keba.Catalog, names []string, lang string) error {
	items, err := selectItems(cat, names)
	if err != nil {
		return err
	}
	// The firmware decides the energy scale, so read it first.
	var fw uint32
	if v, err := dev.ReadRegister(ctx, keba.RegFirmware); err == nil {
		fw = v
	}
	r := dev.ReadItems(ctx, items, fw)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, it := range items {
		v, ok := r.Values[it.Key]
		if !ok {
			continue
		}
		_, _ = fmt.Fprintf(tw, "%s\t%v\t%s\t%s\n", it.Key, v, it.Unit, it.LocalizedName(lang))
	}
	_ = tw.Flush()
	return r.Err()
}

func raw(ctx context.Context, w io.Writer, dev *keba.Device, args []string) error {
	if len(args) == 0 {
		return errors.New("raw: need at least one register address")
	}
	for _, a := range args {
		n, err := strconv.ParseUint(a, 10, 16)
		if err != nil {
			return fmt.Errorf("raw: %q is not a register address", a)
		}
		reg := &keba.Register{Address: uint16(n), Name: "raw"}
		v, err := dev.ReadRegister(ctx, reg)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(w, "%d\t%d\t0x%08X\n", n, v, v)
	}
	return nil
}

func write(ctx context.Context, w io.Writer, dev *keba.Device, cat *keba.Catalog, args []string) error {
	if len(args) != 2 {
		return errors.New("write: need <key> <value>")
	}
	it := cat.Item(args[0])
	if it == nil {
		return fmt.Errorf("%w: %q", keba.ErrUnknownItem, args[0])
	}
	if !it.Writable() {
		return fmt.Errorf("%w: %s", keba.ErrNotWritable, it.Key)
	}
	var (
		value uint16
		err   error
	)
	switch it.Kind {
	case keba.KindNumber:
		f, perr := strconv.ParseFloat(args[1], 64)
		if perr != nil {
			return fmt.Errorf("%w: %q is not a number", keba.ErrValue, args[1])
		}
		value, err = keba.EncodeNumber(it, f)
	case keba.KindEnum:
		value, _, err = keba.EncodeOption(it, args[1])
	case keba.KindBool:
		switch strings.ToLower(args[1]) {
		case "1", "true", "on":
			value = 1
		case "0", "false", "off":
		default:
			err = fmt.Errorf("%w: %q is not on/off", keba.ErrValue, args[1])
		}
	case keba.KindAction:
		value = it.ActionValue
	default:
		err = fmt.Errorf("%w: %s", keba.ErrNotWritable, it.Key)
	}
	if err != nil {
		return err
	}
	if err := dev.WriteRegister(ctx, it.Write, value); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "%s: wrote %d to register %d\n", it.Key, value, it.Write.Address)
	return nil
}
