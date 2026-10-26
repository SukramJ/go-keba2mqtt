// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package config

import "strings"

// Defaults applied when the YAML (and the environment) omit a field.
const (
	DefaultModbusPort          = 502
	DefaultModbusUnitID        = 255
	DefaultModbusTimeout       = 5
	DefaultModbusReadInterval  = 500 // ms, spec §2
	DefaultModbusWriteInterval = 5   // s, spec §2

	DefaultMQTTPort = 1883
	// DefaultMQTTTopic is the instance name when MQTT_TOPIC is unset.
	DefaultMQTTTopic         = "keba"
	DefaultMQTTMaintenance   = true
	DefaultMQTTStatsInterval = 60

	DefaultHASSBaseTopic      = "homeassistant"
	DefaultHASSBirthGracetime = 15

	// The `now` group is ~17 registers at one per 0.5 s, so a full read
	// takes ~9 s; 10 s keeps the loop busy without falling behind.
	DefaultRefreshNow    = 10
	DefaultRefreshConfig = 60
	DefaultRefreshStatic = 3600

	DefaultWebBind  = "127.0.0.1:8080"
	DefaultLanguage = "en"
)

// rawHasKey reports whether raw contains key, compared case-
// insensitively: yaml.v3 binds struct fields case-insensitively, so a
// presence check must too, or an explicit lower-case `refresh_now: 0`
// would be mistaken for an absent key and overwritten by the default.
func rawHasKey(raw map[string]any, key string) bool {
	for k := range raw {
		if strings.EqualFold(k, key) {
			return true
		}
	}
	return false
}

// applyDefaults fills in the documented defaults. For fields whose zero
// value is itself legal (or must be refused by Validate rather than
// silently replaced), key presence in raw decides — not the zero value.
func applyDefaults(c *Config, raw map[string]any) {
	if !rawHasKey(raw, "MODBUS_PORT") {
		c.ModbusPort = DefaultModbusPort
	}
	if !rawHasKey(raw, "MODBUS_UNIT_ID") {
		c.ModbusUnitID = DefaultModbusUnitID
	}
	if !rawHasKey(raw, "MODBUS_TIMEOUT") {
		c.ModbusTimeout = DefaultModbusTimeout
	}
	if !rawHasKey(raw, "MODBUS_READ_INTERVAL") {
		c.ModbusReadInterval = DefaultModbusReadInterval
	}
	if !rawHasKey(raw, "MODBUS_WRITE_INTERVAL") {
		c.ModbusWriteInterval = DefaultModbusWriteInterval
	}
	if !rawHasKey(raw, "MQTT_PORT") {
		c.MQTTPort = DefaultMQTTPort
		if c.MQTTTLS {
			c.MQTTPort = mqttDefaultTLSPort
		}
	}
	if c.MQTTTopic == "" {
		c.MQTTTopic = DefaultMQTTTopic
	}
	if !rawHasKey(raw, "MQTT_MAINTENANCE") {
		c.MQTTMaintenance = DefaultMQTTMaintenance
	}
	if !rawHasKey(raw, "MQTT_STATS_INTERVAL") {
		c.MQTTStatsInterval = DefaultMQTTStatsInterval
	}
	c.HASSBaseTopic = NormalizeHASSBaseTopic(c.HASSBaseTopic)
	if !rawHasKey(raw, "HASS_BIRTH_GRACETIME") {
		c.HASSBirthGracetime = DefaultHASSBirthGracetime
	}
	if !rawHasKey(raw, "REFRESH_NOW") {
		c.RefreshNow = DefaultRefreshNow
	}
	if !rawHasKey(raw, "REFRESH_CONFIG") {
		c.RefreshConfig = DefaultRefreshConfig
	}
	if !rawHasKey(raw, "REFRESH_STATIC") {
		c.RefreshStatic = DefaultRefreshStatic
	}
	if c.WebBind == "" {
		c.WebBind = DefaultWebBind
	}
	if c.Language == "" {
		c.Language = DefaultLanguage
	}
}

// NormalizeHASSBaseTopic trims a trailing slash off the Home Assistant
// discovery prefix and applies [DefaultHASSBaseTopic] to an empty value.
//
// The prefix is a PREFIX: every consumer appends "/<something>", and a
// value written "homeassistant/" would otherwise subscribe the birth on
// "homeassistant//status" — an empty topic level is legal and distinct,
// so Home Assistant's birth would never be seen.
func NormalizeHASSBaseTopic(s string) string {
	s = strings.TrimRight(s, "/")
	if s == "" {
		return DefaultHASSBaseTopic
	}
	return s
}
