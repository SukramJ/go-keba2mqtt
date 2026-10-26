// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package config holds the daemon's runtime settings.
//
// Values flow: YAML file → env overrides (KEBA_* prefix) → defaults →
// validation. The result is a single typed [Config] the rest of the
// daemon reads from.
package config

import (
	"crypto/tls"
	"fmt"
	"time"

	"github.com/SukramJ/go-mqtt"
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
	// MQTTTLS dials tls:// instead of tcp:// (default port 8883).
	// Releases up to 0.2.0 called it MQTT_SSL; see [renamedKeys].
	MQTTTLS bool `yaml:"MQTT_TLS"`
	// MQTTTLSInsecure disables broker certificate verification. A
	// dangerous, explicit opt-in, never a default; prefer MQTTTLSCA. The
	// two are mutually exclusive.
	MQTTTLSInsecure bool `yaml:"MQTT_TLS_INSECURE"`
	// MQTTTLSCA is the path of a PEM CA bundle the broker certificate is
	// verified against instead of the system roots — for a broker whose
	// certificate a private CA issued. Empty uses the system roots.
	MQTTTLSCA string `yaml:"MQTT_TLS_CA"`

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

// renamedKeys were renamed after 0.2.0, old key → new key. A
// configuration that still sets one is REFUSED rather than ignored: an
// ignored MQTT_SSL: true would fall back to plain TCP — credentials in
// clear text — without anyone noticing.
var renamedKeys = [][2]string{
	{"MQTT_SSL", "MQTT_TLS"},
	{"MQTT_SSL_INSECURE", "MQTT_TLS_INSECURE"},
}

// IgnoredTLSKeys returns the TLS-only keys set while MQTT_TLS is off,
// where they have no effect. The daemon warns about each.
func (c *Config) IgnoredTLSKeys() []string {
	if c.MQTTTLS {
		return nil
	}
	var keys []string
	if c.MQTTTLSInsecure {
		keys = append(keys, "MQTT_TLS_INSECURE")
	}
	if c.MQTTTLSCA != "" {
		keys = append(keys, "MQTT_TLS_CA")
	}
	return keys
}

// MQTTTLSConfig builds the tls.Config for the broker connection, or nil
// when MQTT_TLS is off. The server name is MQTT_SERVER, the bare broker
// host (also the host part of [Config.MQTTBrokerURL]). An unusable
// MQTT_TLS_CA is an error naming the file, so the daemon stops at
// start-up instead of failing every handshake later.
func (c *Config) MQTTTLSConfig() (*tls.Config, error) {
	if !c.MQTTTLS {
		return nil, nil //nolint:nilnil // nil config = plain TCP, not an error
	}
	tlsConfig, err := mqtt.NewClientTLSConfigWithCA(c.MQTTServer, c.MQTTTLSCA, c.MQTTTLSInsecure)
	if err != nil {
		return nil, fmt.Errorf("config: MQTT_TLS_CA: %w", err)
	}
	return tlsConfig, nil
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
	if c.MQTTTLS {
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
