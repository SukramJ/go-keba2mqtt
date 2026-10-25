// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package coordinator

import (
	"fmt"

	"github.com/SukramJ/go-keba2mqtt/internal/keba"
	"github.com/SukramJ/go-keba2mqtt/internal/state"
	"github.com/SukramJ/go-keba2mqtt/internal/version"
)

// This file implements web.Backend: the read-only views and the write
// entry point (Write, in write.go) the diagnostic web UI consumes.

// Snapshot returns the cached values, or an empty snapshot without a store.
func (c *Coordinator) Snapshot() state.Snapshot {
	if c.deps.Store == nil {
		return state.Snapshot{}
	}
	return c.deps.Store.Snapshot()
}

// Health summarises the daemon for /api/health.
func (c *Coordinator) Health() state.Health {
	now := c.deps.Now()
	cfg := c.deps.Cfg
	var serial, firmware, model string
	var groups map[string]state.GroupHealth
	if c.deps.Store != nil {
		serial, firmware, model = c.deps.Store.Static()
		groups = c.deps.Store.GroupHealth(now)
	}
	connected := c.deps.Device.IsConnected()
	status := "ok"
	switch {
	case serial == "":
		status = "starting"
	case !connected:
		status = "degraded"
	}
	return state.Health{
		Status:          status,
		Version:         version.Version,
		ModbusConnected: connected,
		ModbusAddr:      fmt.Sprintf("%s:%d", cfg.ModbusIP, cfg.ModbusPort),
		MQTTServer:      fmt.Sprintf("%s:%d", cfg.MQTTServer, cfg.MQTTPort),
		MQTTTopic:       cfg.MQTTTopic,
		HASSEnabled:     cfg.HASSEnable,
		Serial:          serial,
		Firmware:        firmware,
		Model:           model,
		Initialised:     serial != "",
		StartedAt:       c.startedAt,
		UptimeSeconds:   int64(now.Sub(c.startedAt).Seconds()),
		Groups:          groups,
	}
}

// Config returns the sanitised config view (no secrets).
func (c *Coordinator) Config() state.ConfigView {
	cfg := c.deps.Cfg
	return state.ConfigView{
		ModbusIP:            cfg.ModbusIP,
		ModbusPort:          cfg.ModbusPort,
		ModbusUnitID:        int(cfg.ModbusUnitID),
		ModbusTimeout:       cfg.ModbusTimeout,
		ModbusReadInterval:  cfg.ModbusReadInterval,
		ModbusWriteInterval: cfg.ModbusWriteInterval,
		MQTTServer:          cfg.MQTTServer,
		MQTTPort:            cfg.MQTTPort,
		MQTTTopic:           cfg.MQTTTopic,
		HASSEnable:          cfg.HASSEnable,
		HASSBaseTopic:       cfg.HASSBaseTopic,
		RefreshNow:          cfg.RefreshNow,
		RefreshConfig:       cfg.RefreshConfig,
		RefreshStatic:       cfg.RefreshStatic,
		RFIDEnable:          cfg.RFIDEnable,
		EVCCEnable:          cfg.EVCCEnable,
		FailsafeKeepalive:   cfg.FailsafeKeepalive,
		Debug:               cfg.Debug,
		Language:            cfg.Language,
	}
}

// Registers lists the catalog items for the UI's control panel.
func (c *Coordinator) Registers() []state.RegisterInfo {
	lang := c.deps.Cfg.Language
	out := make([]state.RegisterInfo, 0, len(c.deps.Catalog.Items))
	for _, it := range c.deps.Catalog.Items {
		info := state.RegisterInfo{
			Key:       it.Key,
			Name:      it.LocalizedName(lang),
			MQTT:      it.Key,
			Group:     string(it.Group),
			Unit:      it.Unit,
			Type:      kindName(it.Kind),
			Writable:  it.Writable(),
			Component: string(it.Platform),
		}
		if it.Read != nil {
			info.Key = fmt.Sprint(it.Read.Address)
		} else if it.Write != nil {
			info.Key = fmt.Sprint(it.Write.Address)
		}
		if len(it.Options) > 0 {
			info.ValueItems = make(map[int]string, len(it.Options))
			for i, o := range it.Options {
				label := o.Label
				if lang == "de" && o.LabelDE != "" {
					label = o.LabelDE
				}
				info.ValueItems[i] = label
			}
		}
		out = append(out, info)
	}
	return out
}

func kindName(k keba.Kind) string {
	switch k {
	case keba.KindNumber:
		return "number"
	case keba.KindEnum:
		return "enum"
	case keba.KindBool:
		return "bool"
	case keba.KindText:
		return "text"
	case keba.KindAction:
		return "action"
	}
	return ""
}

// Changes subscribes to value updates for the UI's event stream.
func (c *Coordinator) Changes() (events <-chan struct{}, cancel func()) {
	if c.deps.Store == nil {
		return make(chan struct{}), func() {}
	}
	return c.deps.Store.Subscribe()
}
