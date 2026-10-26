// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SukramJ/go-mqtt"
)

// writeCA writes a freshly generated self-signed CA certificate as PEM
// and returns its path.
func writeCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "keba2mqtt test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMQTTTLSDefaultsOff(t *testing.T) {
	t.Parallel()
	c, err := Load(strings.NewReader(minimumYAML), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.MQTTTLS || c.MQTTTLSInsecure || c.MQTTTLSCA != "" {
		t.Errorf("TLS keys must default off: tls=%v insecure=%v ca=%q", c.MQTTTLS, c.MQTTTLSInsecure, c.MQTTTLSCA)
	}
	tlsConfig, err := c.MQTTTLSConfig()
	if err != nil || tlsConfig != nil {
		t.Errorf("TLS off must give no tls.Config: %v, %v", tlsConfig, err)
	}
}

func TestMQTTTLSFromYAML(t *testing.T) {
	t.Parallel()
	ca := writeCA(t)
	c, err := Load(strings.NewReader(minimumYAML+"MQTT_TLS: true\nMQTT_TLS_CA: "+ca+"\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !c.MQTTTLS || c.MQTTTLSCA != ca || c.MQTTBrokerURL() != "tls://localhost" {
		t.Fatalf("tls=%v ca=%q url=%q", c.MQTTTLS, c.MQTTTLSCA, c.MQTTBrokerURL())
	}
	tlsConfig, err := c.MQTTTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	if tlsConfig.ServerName != "localhost" {
		t.Errorf("ServerName = %q, want the bare MQTT_SERVER host", tlsConfig.ServerName)
	}
	if tlsConfig.RootCAs == nil || tlsConfig.InsecureSkipVerify {
		t.Errorf("CA must become RootCAs with verification on: roots=%v insecure=%v",
			tlsConfig.RootCAs != nil, tlsConfig.InsecureSkipVerify)
	}
}

func TestMQTTTLSFromEnv(t *testing.T) {
	t.Parallel()
	env := fakeEnv{vars: map[string]string{
		"KEBA_MQTT_TLS":          "true",
		"KEBA_MQTT_TLS_INSECURE": "true",
	}}
	c, err := Load(strings.NewReader(minimumYAML), env)
	if err != nil {
		t.Fatal(err)
	}
	if !c.MQTTTLS || !c.MQTTTLSInsecure || c.MQTTPort != 8883 {
		t.Fatalf("env overrides failed: tls=%v insecure=%v port=%d", c.MQTTTLS, c.MQTTTLSInsecure, c.MQTTPort)
	}
	tlsConfig, err := c.MQTTTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !tlsConfig.InsecureSkipVerify || tlsConfig.ServerName != "localhost" {
		t.Errorf("insecure=%v serverName=%q", tlsConfig.InsecureSkipVerify, tlsConfig.ServerName)
	}

	// MQTT_TLS_CA is a string key: a path is taken verbatim.
	ca := writeCA(t)
	env = fakeEnv{vars: map[string]string{"KEBA_MQTT_TLS": "true", "KEBA_MQTT_TLS_CA": ca}}
	c, err = Load(strings.NewReader(minimumYAML), env)
	if err != nil {
		t.Fatal(err)
	}
	if c.MQTTTLSCA != ca {
		t.Errorf("KEBA_MQTT_TLS_CA = %q, want %q", c.MQTTTLSCA, ca)
	}
}

// An old key — file, any case, or env — must stop the daemon: silently
// ignoring MQTT_SSL: true would fall back to plain TCP.
func TestRenamedSSLKeysAreRefused(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		yaml string
		env  map[string]string
		want []string
	}{
		{"yaml MQTT_SSL", "MQTT_SSL: true\n", nil, []string{"MQTT_SSL was renamed to MQTT_TLS"}},
		{"yaml lower-case", "mqtt_ssl_insecure: false\n", nil, []string{"MQTT_SSL_INSECURE was renamed to MQTT_TLS_INSECURE"}},
		{"env both", "", map[string]string{"KEBA_MQTT_SSL": "true", "KEBA_MQTT_SSL_INSECURE": "true"}, []string{
			"MQTT_SSL was renamed to MQTT_TLS",
			"MQTT_SSL_INSECURE was renamed to MQTT_TLS_INSECURE",
		}},
		{"set to false still refused", "MQTT_SSL: false\n", nil, []string{"MQTT_SSL was renamed to MQTT_TLS"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Load(strings.NewReader(minimumYAML+tc.yaml), fakeEnv{vars: tc.env})
			if err == nil {
				t.Fatal("an old MQTT_SSL* key must refuse the configuration")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not contain %q", err, w)
				}
			}
		})
	}
}

func TestMQTTTLSCAWithInsecureIsInvalid(t *testing.T) {
	t.Parallel()
	_, err := Load(strings.NewReader(minimumYAML+"MQTT_TLS: true\nMQTT_TLS_INSECURE: true\nMQTT_TLS_CA: /ssl/ca.crt\n"), nil)
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want a ValidationError, got %v", err)
	}
	if !strings.Contains(err.Error(), "MQTT_TLS_CA and MQTT_TLS_INSECURE are mutually exclusive") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestMQTTTLSConfigNamesAnUnusableCA(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "nope.crt")
	c := &Config{MQTTServer: "broker", MQTTTLS: true, MQTTTLSCA: missing}
	_, err := c.MQTTTLSConfig()
	if err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("want an error naming %s, got %v", missing, err)
	}

	// The validation guard is the first line; the library's own refusal
	// is the second.
	c = &Config{MQTTServer: "broker", MQTTTLS: true, MQTTTLSCA: writeCA(t), MQTTTLSInsecure: true}
	if _, err := c.MQTTTLSConfig(); !errors.Is(err, mqtt.ErrInsecureWithCA) {
		t.Fatalf("want ErrInsecureWithCA, got %v", err)
	}
}

func TestIgnoredTLSKeys(t *testing.T) {
	t.Parallel()
	c := &Config{MQTTTLSInsecure: true, MQTTTLSCA: "/ssl/ca.crt"}
	if got := strings.Join(c.IgnoredTLSKeys(), ","); got != "MQTT_TLS_INSECURE,MQTT_TLS_CA" {
		t.Errorf("IgnoredTLSKeys = %q", got)
	}
	c.MQTTTLS = true
	if got := c.IgnoredTLSKeys(); got != nil {
		t.Errorf("with MQTT_TLS on nothing is ignored, got %v", got)
	}
}
