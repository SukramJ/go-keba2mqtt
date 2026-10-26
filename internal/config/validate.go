// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/SukramJ/go-hamqtt/topic"
)

// ValidationError aggregates every problem [Validate] found, so the
// operator sees them all in one start attempt.
type ValidationError struct {
	Issues []string
}

// Error implements error.
func (e *ValidationError) Error() string {
	if len(e.Issues) == 1 {
		return "config: " + e.Issues[0]
	}
	return fmt.Sprintf("config: %d validation issue(s):\n  - %s",
		len(e.Issues), strings.Join(e.Issues, "\n  - "))
}

var allowedLanguages = map[string]bool{"en": true, "de": true}

// Validate checks the post-defaults config.
func Validate(c *Config) error {
	var issues []string
	add := func(format string, args ...any) {
		issues = append(issues, fmt.Sprintf(format, args...))
	}
	rangeCheck := func(name string, v, lo, hi int, unit string) {
		if v < lo || v > hi {
			add("%s must be %d..%d%s, got %d", name, lo, hi, unit, v)
		}
	}

	// --- Modbus ---
	if c.ModbusIP == "" {
		add("MODBUS_IP is required")
	}
	rangeCheck("MODBUS_PORT", c.ModbusPort, 1, 65535, "")
	// MODBUS_UNIT_ID needs no check: the field is a byte, and every value
	// is a legal Modbus-TCP unit id (the P40 accepts 0..255, default 255).
	rangeCheck("MODBUS_TIMEOUT", c.ModbusTimeout, 1, 600, " seconds")
	rangeCheck("MODBUS_READ_INTERVAL", c.ModbusReadInterval, 100, 10000, " ms")
	rangeCheck("MODBUS_WRITE_INTERVAL", c.ModbusWriteInterval, 1, 600, " seconds")

	// --- MQTT ---
	if c.MQTTServer == "" {
		add("MQTT_SERVER is required")
	}
	rangeCheck("MQTT_PORT", c.MQTTPort, 1, 65535, "")
	if _, err := topic.NewSmartHomeMultiLevel(c.MQTTTopic); err != nil {
		add("MQTT_TOPIC %q: %v", c.MQTTTopic, err)
	}
	if c.MQTTTLSCA != "" && c.MQTTTLSInsecure {
		// A CA to verify against and "do not verify" contradict each
		// other; refusing beats guessing which one was meant.
		add("MQTT_TLS_CA and MQTT_TLS_INSECURE are mutually exclusive: set MQTT_TLS_CA to verify the broker against your CA, or MQTT_TLS_INSECURE to skip verification, not both")
	}
	rangeCheck("MQTT_STATS_INTERVAL", c.MQTTStatsInterval, 0, 86400, " seconds")

	// --- HASS ---
	rangeCheck("HASS_BIRTH_GRACETIME", c.HASSBirthGracetime, 0, 600, " seconds")

	// --- Polling ---
	rangeCheck("REFRESH_NOW", c.RefreshNow, 1, 3600, " seconds")
	rangeCheck("REFRESH_CONFIG", c.RefreshConfig, 1, 86400, " seconds")
	rangeCheck("REFRESH_STATIC", c.RefreshStatic, 1, 86400, " seconds")

	// --- Features ---
	// The keepalive must stay inside the wallbox's failsafe timeout
	// (5..600 s) to be of any use, and a write needs MODBUS_WRITE_INTERVAL
	// between it and the next.
	if c.FailsafeKeepalive != 0 && (c.FailsafeKeepalive < c.ModbusWriteInterval || c.FailsafeKeepalive > 600) {
		add("FAILSAFE_KEEPALIVE must be 0 (off) or %d..600 seconds, got %d",
			c.ModbusWriteInterval, c.FailsafeKeepalive)
	}

	// --- Web UI ---
	if c.WebEnable {
		_, port, err := net.SplitHostPort(c.WebBind)
		if err != nil {
			add("WEB_BIND must be host:port, got %q: %v", c.WebBind, err)
		} else if p, perr := strconv.Atoi(port); perr != nil || p < 1 || p > 65535 {
			add("WEB_BIND port must be 1..65535, got %q", port)
		}
		if (c.WebUser == "") != (c.WebPassword == "") {
			add("WEB_USER and WEB_PASSWORD must both be set or both be empty")
		}
	}

	// --- Misc ---
	if !allowedLanguages[c.Language] {
		add("LANGUAGE must be one of [de en], got %q", c.Language)
	}

	if len(issues) > 0 {
		return &ValidationError{Issues: issues}
	}
	return nil
}
