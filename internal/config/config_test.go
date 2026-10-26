// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package config

import (
	"errors"
	"os"
	"strings"
	"testing"
)

type fakeEnv struct{ vars map[string]string }

func (f fakeEnv) LookupEnv(k string) (string, bool) {
	v, ok := f.vars[k]
	return v, ok
}

func (f fakeEnv) Environ() []string {
	out := make([]string, 0, len(f.vars))
	for k, v := range f.vars {
		out = append(out, k+"="+v)
	}
	return out
}

const minimumYAML = "MODBUS_IP: 192.168.1.50\nMQTT_SERVER: localhost\n"

func TestDefaultsFollowTheSpec(t *testing.T) {
	t.Parallel()
	c, err := Load(strings.NewReader(minimumYAML), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.ModbusPort != 502 || c.ModbusUnitID != 255 {
		t.Errorf("modbus defaults %d/%d, want 502/255 (spec §2)", c.ModbusPort, c.ModbusUnitID)
	}
	if c.ModbusReadIntervalDuration().Milliseconds() != 500 || c.ModbusWriteIntervalDuration().Seconds() != 5 {
		t.Errorf("intervals %v/%v, want 500ms/5s (spec §2)", c.ModbusReadIntervalDuration(), c.ModbusWriteIntervalDuration())
	}
	if c.MQTTTopic != "keba" || c.MQTTPort != 1883 || c.MQTTBrokerURL() != "tcp://localhost" {
		t.Errorf("mqtt defaults %q/%d/%q", c.MQTTTopic, c.MQTTPort, c.MQTTBrokerURL())
	}
	if c.RFIDEnable || c.EVCCEnable || c.FailsafeKeepalive != 0 || c.HASSEnable || c.WebEnable {
		t.Error("an opt-in feature is on by default")
	}
	if !c.MQTTMaintenance || c.MQTTStatsInterval != 60 || c.HASSBaseTopic != "homeassistant" {
		t.Errorf("maintenance/hass defaults: %v/%d/%q", c.MQTTMaintenance, c.MQTTStatsInterval, c.HASSBaseTopic)
	}
}

func TestTLSDefaultPort(t *testing.T) {
	t.Parallel()
	c, err := Load(strings.NewReader(minimumYAML+"MQTT_TLS: true\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.MQTTPort != 8883 || c.MQTTBrokerURL() != "tls://localhost" {
		t.Errorf("TLS: port %d url %q", c.MQTTPort, c.MQTTBrokerURL())
	}
	c, err = Load(strings.NewReader(minimumYAML+"MQTT_TLS: true\nMQTT_PORT: 8884\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.MQTTBrokerURL() != "tls://localhost:8884" {
		t.Errorf("TLS custom port url %q", c.MQTTBrokerURL())
	}
}

func TestEnvOverrides(t *testing.T) {
	t.Parallel()
	env := fakeEnv{vars: map[string]string{
		"KEBA_MODBUS_IP":          "10.0.0.5",
		"KEBA_MODBUS_UNIT_ID":     " 1",
		"KEBA_EVCC_ENABLE":        "true",
		"KEBA_MQTT_PASSWORD":      "007",
		"KEBA_MQTT_TOPIC":         "0055",
		"KEBA_FAILSAFE_KEEPALIVE": "30",
		"OTHER_VAR":               "ignored",
	}}
	c, err := Load(strings.NewReader(minimumYAML), env)
	if err != nil {
		t.Fatal(err)
	}
	if c.ModbusIP != "10.0.0.5" || c.ModbusUnitID != 1 || !c.EVCCEnable || c.FailsafeKeepalive != 30 {
		t.Errorf("overrides not applied: %+v", c)
	}
	if c.MQTTPassword != "007" || c.MQTTTopic != "0055" {
		t.Errorf("string fields coerced: %q %q", c.MQTTPassword, c.MQTTTopic)
	}
}

func TestExplicitZeroIsValidatedNotDefaulted(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"REFRESH_NOW", "refresh_now", "MODBUS_TIMEOUT", "MODBUS_READ_INTERVAL"} {
		_, err := Load(strings.NewReader(minimumYAML+key+": 0\n"), nil)
		if _, ok := errors.AsType[*ValidationError](err); !ok {
			t.Errorf("%s: 0 → %v, want a validation error", key, err)
		}
	}
	c, err := Load(strings.NewReader(minimumYAML+"MQTT_STATS_INTERVAL: 0\nHASS_BIRTH_GRACETIME: 0\n"), nil)
	if err != nil || c.MQTTStatsInterval != 0 || c.HASSBirthGracetime != 0 {
		t.Errorf("explicit legal zeros: %v / %+v", err, c)
	}
}

func TestValidationAggregates(t *testing.T) {
	t.Parallel()
	_, err := Load(strings.NewReader("MODBUS_PORT: 0\nLANGUAGE: fr\nMQTT_TOPIC: \"a/#\"\nFAILSAFE_KEEPALIVE: 2\n"), nil)
	v, ok := errors.AsType[*ValidationError](err)
	if !ok {
		t.Fatalf("got %v", err)
	}
	for _, want := range []string{"MODBUS_IP", "MQTT_SERVER", "MODBUS_PORT", "LANGUAGE", "MQTT_TOPIC", "FAILSAFE_KEEPALIVE"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %s in %v", want, v.Issues)
		}
	}
}

func TestWebValidation(t *testing.T) {
	t.Parallel()
	if _, err := Load(strings.NewReader(minimumYAML+"WEB_ENABLE: true\nWEB_USER: a\n"), nil); err == nil {
		t.Error("half basic-auth accepted")
	}
	if _, err := Load(strings.NewReader(minimumYAML+"WEB_ENABLE: false\nWEB_BIND: garbage\n"), nil); err != nil {
		t.Errorf("disabled web validated: %v", err)
	}
	if _, err := Load(strings.NewReader(minimumYAML+"WEB_ENABLE: true\nWEB_BIND: 0.0.0.0:99999\n"), nil); err == nil {
		t.Error("bad port accepted")
	}
}

func TestHASSBaseTopicNormalised(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"homeassistant/": "homeassistant", "ha//": "ha", "": "homeassistant", "/": "homeassistant"} {
		if got := NormalizeHASSBaseTopic(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}

func TestTemplateLoads(t *testing.T) {
	t.Parallel()
	f, err := os.Open("../../config-template.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	c, err := Load(f, nil)
	if err != nil {
		t.Fatalf("config-template.yaml does not load: %v", err)
	}
	if c.ModbusUnitID != 255 || c.MQTTTopic != DefaultMQTTTopic {
		t.Errorf("template drifted: unit %d topic %q", c.ModbusUnitID, c.MQTTTopic)
	}
}

func TestLocate(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", dir)
	if _, ok := Locate(fakeEnv{}); ok {
		t.Fatal("found a config in an empty directory")
	}
	if err := os.WriteFile(ConfigFile, []byte(minimumYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, ok := Locate(fakeEnv{}); !ok || !strings.HasSuffix(p, ConfigFile) {
		t.Fatalf("Locate = %q/%v", p, ok)
	}
}
