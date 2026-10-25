// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package coordinator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SukramJ/go-hamqtt/publisher"

	"github.com/SukramJ/go-keba2mqtt/internal/evcc"
	"github.com/SukramJ/go-keba2mqtt/internal/keba"
)

// Errors surfaced to synchronous (web UI) writers.
var (
	// ErrWriteQueueFull means the command was not accepted.
	ErrWriteQueueFull = errors.New("coordinator: write queue full")
	// ErrWriteSuperseded means a newer command for the same target
	// replaced this one before it ran — the newer one is what runs.
	ErrWriteSuperseded = errors.New("coordinator: write superseded by a newer command")
	// ErrNotReady means the wallbox identity is not known yet.
	ErrNotReady = errors.New("coordinator: not initialised yet")
)

// writeJob is one command for the wallbox.
type writeJob struct {
	// target coalesces jobs: a newer job for the same target replaces a
	// queued older one. It is the write register for device items, and
	// shared by every command that ends on that register (evcc's enable
	// and maxcurrent both end on 5004).
	target string
	// desc is the log label ("charging_current=16").
	desc string
	run  func(ctx context.Context) error
	// topic/payload are the `set` request as it arrived, for the warn line
	// a failed request gets (mqtt-smarthome §3.3). Empty for the web UI.
	topic, payload string
	// reply, when non-nil (capacity 1), receives the outcome once.
	reply chan error
}

func (j *writeJob) done(err error) {
	if j.reply == nil {
		return
	}
	select {
	case j.reply <- err:
	default:
	}
}

// writeQueue is a bounded queue with one slot per target.
//
// Coalescing matters twice here. A dragged Home Assistant slider emits a
// burst of commands and only the last one is worth the 5 s the P40 makes
// every write wait; and evcc re-sends its charger commands every cycle,
// which without coalescing would let the queue grow faster than a
// 5-second write interval can drain it.
type writeQueue struct {
	mu     sync.Mutex
	order  []string
	jobs   map[string]*writeJob
	limit  int
	signal chan struct{}
}

func newWriteQueue(limit int) *writeQueue {
	return &writeQueue{jobs: map[string]*writeJob{}, limit: limit, signal: make(chan struct{}, 1)}
}

// push queues j, replacing a queued job for the same target in place.
// A full queue drops its oldest job to make room.
func (q *writeQueue) push(j *writeJob) (replaced, dropped *writeJob) {
	q.mu.Lock()
	if old, ok := q.jobs[j.target]; ok {
		q.jobs[j.target] = j
		replaced = old
	} else {
		if len(q.order) >= q.limit {
			oldest := q.order[0]
			q.order = q.order[1:]
			dropped = q.jobs[oldest]
			delete(q.jobs, oldest)
		}
		q.order = append(q.order, j.target)
		q.jobs[j.target] = j
	}
	q.mu.Unlock()
	select {
	case q.signal <- struct{}{}:
	default:
	}
	return replaced, dropped
}

func (q *writeQueue) pop() *writeJob {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.order) == 0 {
		return nil
	}
	t := q.order[0]
	q.order = q.order[1:]
	j := q.jobs[t]
	delete(q.jobs, t)
	return j
}

func (q *writeQueue) has(target string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, ok := q.jobs[target]
	return ok
}

// enqueue queues a job, answering any job it replaced or dropped.
func (c *Coordinator) enqueue(j *writeJob) {
	replaced, dropped := c.writes.push(j)
	if replaced != nil {
		c.log.Debug("coordinator.write_coalesced", slog.String("replaced", replaced.desc), slog.String("by", j.desc))
		replaced.done(ErrWriteSuperseded)
	}
	if dropped != nil {
		c.log.Warn("coordinator.write_queue_full_dropped_oldest", slog.String("dropped", dropped.desc))
		dropped.done(ErrWriteQueueFull)
	}
}

// writeWorker drains the queue, one job at a time — the device paces
// them anyway.
func (c *Coordinator) writeWorker(ctx context.Context) error {
	c.writeWorkerUp.Store(true)
	defer c.writeWorkerUp.Store(false)
	for {
		j := c.writes.pop()
		if j == nil {
			select {
			case <-ctx.Done():
				return nil
			case <-c.writes.signal:
			}
			continue
		}
		err := c.runRetrying(ctx, j)
		j.done(err)
		if err != nil {
			attrs := []any{slog.String("command", j.desc), slog.String("err", err.Error())}
			if j.topic != "" {
				attrs = append(attrs, slog.String("topic", j.topic), slog.String("payload", j.payload))
			}
			c.log.Warn("coordinator.write_failed", attrs...)
			continue
		}
		c.log.Info("coordinator.write_ok", slog.String("command", j.desc))
	}
}

// writeRetries / writeRetryDelay bound the retry of a transient failure
// (a write landing in the watchdog's reconnect window). Vars for tests.
var (
	writeRetries    = 3
	writeRetryDelay = time.Second
)

func (c *Coordinator) runRetrying(ctx context.Context, j *writeJob) error {
	var err error
	for attempt := 1; attempt <= writeRetries; attempt++ {
		err = j.run(ctx)
		if err == nil || !isTransient(err) || attempt == writeRetries {
			return err
		}
		c.log.Warn("coordinator.write_retry", slog.String("command", j.desc),
			slog.Int("attempt", attempt), slog.String("err", err.Error()))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(writeRetryDelay):
		}
	}
	return err
}

func isTransient(err error) bool {
	return !errors.Is(err, keba.ErrValue) &&
		!errors.Is(err, keba.ErrNotWritable) &&
		!errors.Is(err, keba.ErrUnknownItem) &&
		!errors.Is(err, evcc.ErrCommand) &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, context.DeadlineExceeded)
}

// --- inbound `set` ----------------------------------------------------------

// onSet handles one routed `<name>/set/<serial>/<group>/<key>` publish.
// A command for another serial belongs to a sibling instance on the same
// broker and is ignored.
func (c *Coordinator) onSet(_ context.Context, cmd publisher.Command, v publisher.SetValue) {
	id := c.ident.Load()
	if id == nil || len(cmd.Wildcards) != 3 || cmd.Wildcards[0] != id.serial {
		return
	}
	group, key := cmd.Wildcards[1], cmd.Wildcards[2]
	if v.Structured() {
		c.log.Warn("coordinator.set_rejected", slog.String("topic", cmd.Topic),
			slog.String("payload", string(cmd.Payload)), slog.String("err", "structured payload, this item takes a plain value"))
		return
	}
	var (
		j   *writeJob
		err error
	)
	if group == evcc.Group {
		j, err = c.evccJob(key, v.Text)
	} else {
		it := c.deps.Catalog.Item(key)
		if it == nil || string(it.Group) != group {
			err = fmt.Errorf("%w: %s/%s", keba.ErrUnknownItem, group, key)
		} else {
			j, err = c.itemJob(it, v.Text, false)
		}
	}
	if err != nil {
		c.log.Warn("coordinator.set_rejected", slog.String("topic", cmd.Topic),
			slog.String("payload", string(cmd.Payload)), slog.String("err", err.Error()))
		return
	}
	j.topic, j.payload = cmd.Topic, string(cmd.Payload)
	c.enqueue(j)
}

// Write is the web UI's synchronous write: it waits for the outcome.
func (c *Coordinator) Write(ctx context.Context, key, value string) error {
	it := c.deps.Catalog.Item(key)
	if it == nil {
		return fmt.Errorf("%w: %s", keba.ErrUnknownItem, key)
	}
	j, err := c.itemJob(it, value, false)
	if err != nil {
		return err
	}
	if !c.writeWorkerUp.Load() {
		return j.run(ctx)
	}
	j.reply = make(chan error, 1)
	c.enqueue(j)
	select {
	case err := <-j.reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// itemJob parses a command for a device item into a job. force skips
// the idempotency check (the failsafe keepalive must reach the device).
func (c *Coordinator) itemJob(it *keba.Item, text string, force bool) (*writeJob, error) {
	if c.ident.Load() == nil {
		return nil, ErrNotReady
	}
	if !it.Writable() {
		return nil, fmt.Errorf("%w: %s", keba.ErrNotWritable, it.Key)
	}
	text = strings.TrimSpace(text)
	var (
		raw   uint16
		state any // the value published for an optimistic item
		err   error
	)
	switch it.Kind {
	case keba.KindNumber:
		f, perr := strconv.ParseFloat(text, 64)
		if perr != nil {
			return nil, fmt.Errorf("%w: %s=%q is not a number", keba.ErrValue, it.Key, text)
		}
		raw, err = keba.EncodeNumber(it, f)
		state = keba.Round(f, it.Precision)
	case keba.KindEnum:
		var token string
		raw, token, err = keba.EncodeOption(it, text)
		state = token
	case keba.KindBool:
		var on bool
		on, err = evcc.ParseBool(text)
		if on {
			raw = 1
		}
		state = on
		if err != nil {
			err = fmt.Errorf("%w: %s: %w", keba.ErrValue, it.Key, err)
		}
	case keba.KindAction:
		raw = it.ActionValue
	default:
		return nil, fmt.Errorf("%w: %s", keba.ErrNotWritable, it.Key)
	}
	if err != nil {
		return nil, err
	}
	return &writeJob{
		target: strconv.Itoa(int(it.Write.Address)),
		desc:   fmt.Sprintf("%s=%s", it.Key, text),
		run: func(ctx context.Context) error {
			return c.writeItem(ctx, it, raw, state, force)
		},
	}, nil
}

// writeItem writes one raw value, unless the wallbox already reports it,
// then refreshes what the write changed.
func (c *Coordinator) writeItem(ctx context.Context, it *keba.Item, raw uint16, state any, force bool) error {
	if !force && c.alreadyAt(it, raw) {
		if it.Write == keba.RegSetChargingCurrent {
			// Still the commanded current, for the keepalive.
			c.mu.Lock()
			c.currentCmd = &raw
			c.mu.Unlock()
		}
		c.log.Debug("coordinator.write_skipped_unchanged", slog.String("item", it.Key), slog.Int("raw", int(raw)))
		return nil
	}
	if err := c.deps.Device.WriteRegister(ctx, it.Write, raw); err != nil {
		return err
	}
	c.mu.Lock()
	c.written[it.Key] = raw
	if it.Write == keba.RegSetChargingCurrent {
		c.currentCmd = &raw
	}
	c.mu.Unlock()
	if it.Optimistic && state != nil {
		c.mu.Lock()
		c.values[it.Key] = state
		c.mu.Unlock()
		c.publishValues(ctx, string(it.Group), map[string]any{it.Key: state})
	}
	c.refresh(ctx, it)
	return nil
}

// alreadyAt reports whether the wallbox already holds raw for it: its
// read register reports the matching value, or — for a write-only item
// — this process wrote exactly raw last. Actions are never skipped.
//
// It is what makes repeated commands free. evcc re-sends its charger
// state every cycle, and each write the P40 takes costs 5 s of the write
// interval and wakes its relays' logic for nothing.
func (c *Coordinator) alreadyAt(it *keba.Item, raw uint16) bool {
	if it.Kind == keba.KindAction {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if it.Read == nil {
		last, ok := c.written[it.Key]
		return ok && last == raw
	}
	cur, ok := c.raw[it.Read.Address]
	if !ok {
		return false
	}
	if it.Kind == keba.KindEnum {
		for _, o := range it.Options {
			if o.Write == int64(raw) {
				return o.Read == int64(cur)
			}
		}
		return false
	}
	return cur == uint32(raw)
}

// --- evcc ----------------------------------------------------------------

// evccJob turns an evcc profile command into a job.
//
// evcc drives a charger with two independent commands, enable and
// maxcurrent, and expects maxcurrent to be remembered while charging is
// disabled. The P40 has one register for both (5004: 0 suspends), so the
// two are folded into one desired state here and one job on 5004
// computes the register value from it when it runs — whichever command
// came last, the queued job writes the combination of both.
func (c *Coordinator) evccJob(key, text string) (*writeJob, error) {
	if c.ident.Load() == nil {
		return nil, ErrNotReady
	}
	cur := c.deps.Catalog.Item(keba.KeyChargingCurrent)
	switch key {
	case evcc.CmdEnable:
		on, err := evcc.ParseBool(text)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.evccEnabled = &on
		c.mu.Unlock()
	case evcc.CmdMaxCurrent, evcc.CmdMaxCurrentMillis:
		f, err := evcc.ParseCurrent(text)
		if err != nil {
			return nil, err
		}
		if _, err := keba.EncodeNumber(cur, f); err != nil || f == 0 {
			return nil, fmt.Errorf("%w: maxcurrent %v A outside 6..32 A", evcc.ErrCommand, f)
		}
		c.mu.Lock()
		c.evccCurrent = &f
		c.mu.Unlock()
	case evcc.CmdPhases1p3p:
		n, err := evcc.ParsePhases(text)
		if err != nil {
			return nil, err
		}
		return c.phasesJob(n), nil
	default:
		return nil, fmt.Errorf("%w: unknown evcc command %q", evcc.ErrCommand, key)
	}
	return &writeJob{
		target: strconv.Itoa(int(cur.Write.Address)),
		desc:   fmt.Sprintf("evcc %s=%s", key, text),
		run: func(ctx context.Context) error {
			on, amps, err := c.evccTarget(ctx, cur)
			if err != nil {
				return err
			}
			if !on {
				amps = 0
			}
			raw, err := keba.EncodeNumber(cur, amps)
			if err != nil {
				return err
			}
			return c.writeItem(ctx, cur, raw, nil, false)
		},
	}, nil
}

// errCurrentUnknown means the wallbox's current limit could not be read,
// so an evcc command that leaves enable or the current open cannot be
// completed safely. Transient: the job is retried.
var errCurrentUnknown = errors.New("coordinator: charging current limit not known yet")

// evccTarget resolves evcc's charger state at the moment the job runs.
// What evcc has said wins; what it has not said yet is taken from the
// wallbox — enabled when a current is set, at that current (or the 6 A
// minimum). Register 1100 is read first when no poll has reported it:
// guessing "disabled" from a missing value would stop a charging car the
// moment evcc sends its first maxcurrent after a restart.
func (c *Coordinator) evccTarget(ctx context.Context, cur *keba.Item) (on bool, amps float64, err error) {
	c.mu.Lock()
	_, known := c.raw[keba.RegMaxChargingCurrent.Address]
	needWallbox := c.evccEnabled == nil || c.evccCurrent == nil
	c.mu.Unlock()
	if needWallbox && !known {
		c.refresh(ctx, cur)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	limit, known := c.raw[keba.RegMaxChargingCurrent.Address]
	if needWallbox && !known {
		return false, 0, errCurrentUnknown
	}
	if c.evccEnabled != nil {
		on = *c.evccEnabled
	} else {
		on = limit > 0
	}
	switch {
	case c.evccCurrent != nil:
		amps = *c.evccCurrent
	case float64(limit)/1000 >= 6:
		amps = float64(limit) / 1000
	default:
		amps = 6
	}
	return on, amps, nil
}

// phasesJob switches between 1- and 3-phase charging. The P40 only takes
// the trigger (5052) from Modbus when the phase switching source (5050)
// is Modbus TCP, so the source is set first when it is not.
func (c *Coordinator) phasesJob(n int) *writeJob {
	phases := c.deps.Catalog.Item(keba.KeyPhases)
	source := c.deps.Catalog.Item(keba.KeyPhaseSwitchSource)
	return &writeJob{
		target: strconv.Itoa(int(phases.Write.Address)),
		desc:   fmt.Sprintf("evcc %s=%d", evcc.CmdPhases1p3p, n),
		run: func(ctx context.Context) error {
			want, _, err := keba.EncodeOption(phases, evcc.PhaseOption(n))
			if err != nil {
				return err
			}
			if c.alreadyAt(phases, want) {
				return nil
			}
			modbus, _, err := keba.EncodeOption(source, keba.OptionSourceModbus)
			if err != nil {
				return err
			}
			if err := c.writeItem(ctx, source, modbus, nil, false); err != nil {
				return fmt.Errorf("set phase switching source to Modbus TCP: %w", err)
			}
			return c.writeItem(ctx, phases, want, nil, false)
		},
	}
}

// keepalive re-writes the last commanded charging current every
// FAILSAFE_KEEPALIVE, so the wallbox's EMS failsafe engages only when
// the commands really stop. It writes nothing until something has
// commanded a current, and never adds a write while one for the same
// register is already queued.
func (c *Coordinator) keepalive(ctx context.Context) error {
	return c.keepaliveEvery(ctx, c.deps.Cfg.FailsafeKeepaliveDuration())
}

func (c *Coordinator) keepaliveEvery(ctx context.Context, every time.Duration) error {
	cur := c.deps.Catalog.Item(keba.KeyChargingCurrent)
	target := strconv.Itoa(int(cur.Write.Address))
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		c.mu.Lock()
		cmd := c.currentCmd
		c.mu.Unlock()
		if cmd == nil || !c.deps.Device.IsConnected() || c.writes.has(target) {
			continue
		}
		raw := *cmd
		c.enqueue(&writeJob{
			target: target,
			desc:   fmt.Sprintf("keepalive %s=%d mA", cur.Key, raw),
			run: func(ctx context.Context) error {
				return c.writeItem(ctx, cur, raw, nil, true)
			},
		})
	}
}
