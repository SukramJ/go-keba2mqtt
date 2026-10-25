// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package config holds the daemon's runtime settings.
//
// Values flow: YAML file → env overrides (KEBA_* prefix) → defaults →
// validation. The result is a single typed [Config] the rest of the
// daemon reads from.
package config

import (
	"fmt"
	"time"
)

// Daemon-wide constants.
const (
	EnvPrefix  = "KEBA_"
	AppDirName = "keba2mqtt"
	ConfigFile = "config.yaml"
)

// Config is the validated daemon configuration. Fields are flat to
// match the YAML keys 1:1 — grouping into sub-structs would force a
// custom unmarshaller for what is otherwise a trivial yaml.v3 decode.
type Config struct {
	// --- Modbus (the wallbox) ---
	ModbusIP     string `yaml:"MODBUS_IP"`
	ModbusPort   int    `yaml:"MODBUS_PORT"`
	ModbusUnitID byte   `yaml:"MODBUS_UNIT_ID"`
	// ModbusTimeout bounds one request/response round-trip, in seconds.
	ModbusTimeout int `yaml:"MODBUS_TIMEOUT"`
	// ModbusReadInterval is the minimum spacing between two requests, in
	// milliseconds. The P40 asks for 500 (spec §2).
	ModbusReadInterval int `yaml:"MODBUS_READ_INTERVAL"`
	// ModbusWriteInterval is the minimum spacing between two writes, in
	// seconds. The P40 asks for 5 (spec §2).
	ModbusWriteInterval int `yaml:"MODBUS_WRITE_INTERVAL"`

	// --- MQTT ---
	MQTTServer   string `yaml:"MQTT_SERVER"`
	MQTTPort     int    `yaml:"MQTT_PORT"`
	MQTTLogin    string `yaml:"MQTT_LOGIN"`
	MQTTPassword string `yaml:"MQTT_PASSWORD"`
	// MQTTTopic is the mqtt-smarthome instance name, the first level of
	// every topic (`<name>/status/…`, `<name>/connected`, …). It is the
	// only thing that keeps two instances on one broker apart.
	MQTTTopic string `yaml:"MQTT_TOPIC"`
	// MQTTMaintenance enables `<name>/maintenance/set/loglevel`,
	// `…/set/restart` and `<name>/maintenance/stats`. Anyone who may
	// publish on the broker can then use them.
	MQTTMaintenance bool `yaml:"MQTT_MAINTENANCE"`
	// MQTTStatsInterval is the period of `<name>/maintenance/stats` in
	// seconds; 0 switches it off.
	MQTTStatsInterval int `yaml:"MQTT_STATS_INTERVAL"`
	// MQTTSSL dials tls:// instead of tcp://.
	MQTTSSL bool `yaml:"MQTT_SSL"`
	// MQTTSSLInsecure disables broker certificate verification. A
	// dangerous opt-in for self-signed certificates only.
	MQTTSSLInsecure bool `yaml:"MQTT_SSL_INSECURE"`

	// --- Home Assistant ---
	HASSEnable         bool   `yaml:"HASS_ENABLE"`
	HASSBaseTopic      string `yaml:"HASS_BASE_TOPIC"`
	HASSBirthGracetime int    `yaml:"HASS_BIRTH_GRACETIME"` // seconds
	// DeviceName is the Home Assistant device name and the entity_id seed
	// ("garage" → sensor.garage_power). Empty uses "KEBA KeContact P40"
	// and the seed "keba".
	DeviceName string `yaml:"DEVICE_NAME"`

	// --- Polling (seconds) ---
	RefreshNow    int `yaml:"REFRESH_NOW"`
	RefreshConfig int `yaml:"REFRESH_CONFIG"`
	RefreshStatic int `yaml:"REFRESH_STATIC"`

	// --- Features ---
	// RFIDEnable reads register 1500 (the UID of the RFID card that
	// authorised the session). Personal data, and the wallbox must have
	// RFID reading over Modbus enabled separately, so it is opt-in.
	RFIDEnable bool `yaml:"RFID_ENABLE"`
	// EVCCEnable publishes the shared evcc profile under
	// `<name>/status/<serial>/evcc/…` and routes its commands.
	EVCCEnable bool `yaml:"EVCC_ENABLE"`
	// FailsafeKeepalive re-writes the last commanded charging current
	// every N seconds (0 = off), so the wallbox's EMS failsafe only
	// engages when this bridge — or whatever commands it — really stops.
	FailsafeKeepalive int `yaml:"FAILSAFE_KEEPALIVE"`

	// --- Web UI ---
	WebEnable   bool   `yaml:"WEB_ENABLE"`
	WebBind     string `yaml:"WEB_BIND"`
	WebUser     string `yaml:"WEB_USER"`
	WebPassword string `yaml:"WEB_PASSWORD"`

	// --- Misc ---
	// Language selects the Home Assistant display names and the web UI
	// language: "en" or "de". Entity ids and topics never change with it.
	Language string `yaml:"LANGUAGE"`
	Debug    bool   `yaml:"DEBUG"`
}

const (
	mqttDefaultPlainPort = 1883
	mqttDefaultTLSPort   = 8883
)

// MQTTBrokerURL derives the broker URL go-mqtt dials. The port is only
// appended when it differs from the scheme's default.
func (c *Config) MQTTBrokerURL() string {
	scheme := "tcp"
	defaultPort := mqttDefaultPlainPort
	if c.MQTTSSL {
		scheme = "tls"
		defaultPort = mqttDefaultTLSPort
	}
	if c.MQTTPort == defaultPort {
		return fmt.Sprintf("%s://%s", scheme, c.MQTTServer)
	}
	return fmt.Sprintf("%s://%s:%d", scheme, c.MQTTServer, c.MQTTPort)
}

// ModbusTimeoutDuration returns ModbusTimeout as a duration.
func (c *Config) ModbusTimeoutDuration() time.Duration {
	return time.Duration(c.ModbusTimeout) * time.Second
}

// ModbusReadIntervalDuration returns ModbusReadInterval as a duration.
func (c *Config) ModbusReadIntervalDuration() time.Duration {
	return time.Duration(c.ModbusReadInterval) * time.Millisecond
}

// ModbusWriteIntervalDuration returns ModbusWriteInterval as a duration.
func (c *Config) ModbusWriteIntervalDuration() time.Duration {
	return time.Duration(c.ModbusWriteInterval) * time.Second
}

// HASSBirthGracetimeDuration returns HASSBirthGracetime as a duration.
func (c *Config) HASSBirthGracetimeDuration() time.Duration {
	return time.Duration(c.HASSBirthGracetime) * time.Second
}

// RefreshNowDuration returns RefreshNow as a duration.
func (c *Config) RefreshNowDuration() time.Duration {
	return time.Duration(c.RefreshNow) * time.Second
}

// RefreshConfigDuration returns RefreshConfig as a duration.
func (c *Config) RefreshConfigDuration() time.Duration {
	return time.Duration(c.RefreshConfig) * time.Second
}

// RefreshStaticDuration returns RefreshStatic as a duration.
func (c *Config) RefreshStaticDuration() time.Duration {
	return time.Duration(c.RefreshStatic) * time.Second
}

// FailsafeKeepaliveDuration returns FailsafeKeepalive as a duration.
func (c *Config) FailsafeKeepaliveDuration() time.Duration {
	return time.Duration(c.FailsafeKeepalive) * time.Second
}
