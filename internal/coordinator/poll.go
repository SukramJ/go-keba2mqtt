// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package coordinator

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"time"

	"github.com/SukramJ/go-hamqtt/publisher"

	"github.com/SukramJ/go-keba2mqtt/internal/evcc"
	"github.com/SukramJ/go-keba2mqtt/internal/hass"
	"github.com/SukramJ/go-keba2mqtt/internal/keba"
)

// pollLoop is the one scheduler for every group.
//
// One loop rather than one goroutine per group, because the groups share
// a single paced wallbox: three loops would only queue behind the same
// gate and make the order in which they get through it an accident. Here
// a due `now` group always goes first, and a group is due again
// REFRESH_<GROUP> after its previous read STARTED, so a slow read does
// not push the schedule back.
func (c *Coordinator) pollLoop(ctx context.Context) error {
	cfg := c.deps.Cfg
	every := map[keba.Group]time.Duration{
		keba.GroupNow:    cfg.RefreshNowDuration(),
		keba.GroupConfig: cfg.RefreshConfigDuration(),
		keba.GroupStatic: cfg.RefreshStaticDuration(),
	}
	now := c.deps.Now()
	// The static group was just read by Run.
	last := map[keba.Group]time.Time{keba.GroupStatic: now}
	c.log.Info("coordinator.poll_start",
		slog.Duration("now", every[keba.GroupNow]),
		slog.Duration("config", every[keba.GroupConfig]),
		slog.Duration("static", every[keba.GroupStatic]))
	for {
		for _, g := range keba.Groups {
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			if t, ok := last[g]; ok && c.deps.Now().Sub(t) < every[g] {
				continue
			}
			last[g] = c.deps.Now()
			c.pollGroup(ctx, g)
		}
		next := time.Duration(1<<63 - 1)
		for _, g := range keba.Groups {
			next = min(next, last[g].Add(every[g]).Sub(c.deps.Now()))
		}
		if next <= 0 {
			continue
		}
		t := time.NewTimer(next)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
	}
}

// pollGroup reads one group and publishes what it got.
func (c *Coordinator) pollGroup(ctx context.Context, g keba.Group) {
	log := c.log.With(slog.String("group", string(g)))
	if !c.deps.Device.IsConnected() {
		log.Debug("coordinator.poll_skipped_disconnected")
		return
	}
	r := c.deps.Device.ReadItems(ctx, c.deps.Catalog.ByGroup(g), c.firmwareRaw())
	c.logReadErrors(log, r)
	c.updateUpstream(ctx)
	c.apply(ctx, g, r)
}

func (c *Coordinator) firmwareRaw() uint32 {
	if id := c.ident.Load(); id != nil {
		return id.firmwareRaw
	}
	return 0
}

// logReadErrors logs a group's failures: unmapped enum codes once per
// (item, code), everything else at warn.
func (c *Coordinator) logReadErrors(log *slog.Logger, r keba.Reading) {
	for _, err := range r.Errs {
		if errors.Is(err, context.Canceled) {
			continue
		}
		if errors.Is(err, keba.ErrUnknownCode) {
			c.mu.Lock()
			if c.unmappedSeen == nil {
				c.unmappedSeen = map[string]struct{}{}
			}
			_, seen := c.unmappedSeen[err.Error()]
			if !seen && len(c.unmappedSeen) < 256 {
				c.unmappedSeen[err.Error()] = struct{}{}
			}
			c.mu.Unlock()
			if !seen {
				log.Warn("coordinator.unmapped_value", slog.String("err", err.Error()))
			}
			continue
		}
		log.Warn("coordinator.read_failed", slog.String("err", err.Error()))
	}
}

// apply caches a reading and publishes every value in it, then the evcc
// profile values that depend on it.
func (c *Coordinator) apply(ctx context.Context, g keba.Group, r keba.Reading) {
	if len(r.Values) == 0 {
		return
	}
	c.mu.Lock()
	maps.Copy(c.values, r.Values)
	maps.Copy(c.raw, r.Raw)
	c.mu.Unlock()
	c.publishValues(ctx, string(g), r.Values)
	if c.deps.Cfg.EVCCEnable {
		c.publishEVCC(ctx)
	}
}

// publishValues publishes a set of values of one group and mirrors them
// into the web UI store.
func (c *Coordinator) publishValues(ctx context.Context, group string, values map[string]any) {
	id := c.ident.Load()
	if id == nil {
		return
	}
	now := c.deps.Now()
	for key, val := range values {
		t := hass.StateTopic(c.deps.Cfg.MQTTTopic, id.serial, group, key)
		if _, err := c.deps.StatePlane.PublishStatus(ctx, t, publisher.Observation{Value: val, At: now}); err != nil {
			c.log.Warn("coordinator.publish_failed", slog.String("topic", t), slog.String("err", err.Error()))
		}
	}
	if c.deps.Store != nil {
		c.deps.Store.UpdateGroup(group, values, now)
	}
}

// publishEVCC derives and publishes the evcc profile from everything
// read so far. The state plane deduplicates on the value, so an
// unchanged derivation costs nothing on the wire.
func (c *Coordinator) publishEVCC(ctx context.Context) {
	c.mu.Lock()
	derived := evcc.Derive(maps.Clone(c.values), maps.Clone(c.raw))
	c.mu.Unlock()
	if len(derived) > 0 {
		c.publishValues(ctx, evcc.Group, derived)
	}
}

// refresh re-reads the registers behind items right after a write, so
// Home Assistant and evcc see the effect within a second rather than at
// the next poll.
func (c *Coordinator) refresh(ctx context.Context, items ...*keba.Item) {
	byGroup := map[keba.Group][]*keba.Item{}
	for _, it := range items {
		if it.Read == nil {
			continue
		}
		// Every item sharing the register, so e.g. both product items move.
		for _, other := range c.deps.Catalog.Items {
			if other.Read != nil && other.Read.Address == it.Read.Address {
				byGroup[other.Group] = append(byGroup[other.Group], other)
			}
		}
	}
	for g, its := range byGroup {
		r := c.deps.Device.ReadItems(ctx, its, c.firmwareRaw())
		c.logReadErrors(c.log.With(slog.String("group", string(g))), r)
		c.apply(ctx, g, r)
	}
}
