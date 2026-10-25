// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package hass

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SukramJ/go-hamqtt/discovery"

	"github.com/SukramJ/go-keba2mqtt/internal/keba"
)

var testInfo = DeviceInfo{Serial: "18416854", Firmware: "1.2.1", ProductCode: 4212311, HWRevision: "3"}

func render(t *testing.T, lang string, info DeviceInfo, cat *keba.Catalog) map[string]map[string]any {
	t.Helper()
	b, err := RenderBundle("keba", lang, info, cat, Origin("test"))
	if err != nil {
		t.Fatal(err)
	}
	if err := discovery.Contain(b, discovery.ContainOptions{}).Err(); err != nil {
		t.Fatalf("document would be refused: %v", err)
	}
	bs, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components map[string]map[string]any `json:"components"`
	}
	if err := json.Unmarshal(bs, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Components
}

func TestTopics(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		StateTopic("keba", "1", "now", "power"):                     "keba/status/1/now/power",
		CommandTopic("keba", "1", "evcc", "enable"):                 "keba/set/1/evcc/enable",
		CommandFilter("keba"):                                       "keba/set/+/+/+",
		ConnectedTopic("keba"):                                      "keba/connected",
		OnlineTopic("keba", "1"):                                    "keba/status/1/online",
		BundleConfigTopic("homeassistant", DeviceInfo{Serial: "1"}): "homeassistant/device/1/config",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

// Every component's topics are the ones the coordinator publishes to and
// routes, and every identity string carries the serial.
func TestDocumentTopicsAndIdentity(t *testing.T) {
	t.Parallel()
	cat := keba.NewCatalog(true)
	comps := render(t, "en", testInfo, cat)
	if len(comps) != len(cat.Items) {
		t.Fatalf("%d components for %d items", len(comps), len(cat.Items))
	}
	for _, it := range cat.Items {
		c, ok := comps[it.Key]
		if !ok {
			t.Errorf("no component for %s", it.Key)
			continue
		}
		if c["unique_id"] != "keba_18416854_"+it.Key {
			t.Errorf("%s unique_id %v", it.Key, c["unique_id"])
		}
		if c["default_entity_id"] != string(it.Platform)+".keba_"+it.Key {
			t.Errorf("%s default_entity_id %v", it.Key, c["default_entity_id"])
		}
		if it.Read != nil || it.Optimistic {
			if c["state_topic"] != StateTopic("keba", "18416854", string(it.Group), it.Key) {
				t.Errorf("%s state_topic %v", it.Key, c["state_topic"])
			}
		}
		if it.Writable() {
			if c["command_topic"] != CommandTopic("keba", "18416854", string(it.Group), it.Key) {
				t.Errorf("%s command_topic %v", it.Key, c["command_topic"])
			}
		} else if _, has := c["command_topic"]; has {
			t.Errorf("read-only %s has a command topic", it.Key)
		}
	}
}

func TestLocalisationAndDeviceName(t *testing.T) {
	t.Parallel()
	info := testInfo
	info.Name = "Garage Wallbox"
	comps := render(t, "de", info, keba.NewCatalog(false))
	c := comps[keba.KeyChargingCurrent]
	if c["name"] != "Ladestromgrenze" {
		t.Errorf("German name %v", c["name"])
	}
	if c["default_entity_id"] != "number.garage_wallbox_charging_current" {
		t.Errorf("entity id seed %v", c["default_entity_id"])
	}
	if c["unique_id"] != "keba_18416854_charging_current" {
		t.Errorf("unique_id moved with the device name: %v", c["unique_id"])
	}
	sel := comps[keba.KeyPhases]
	opts, _ := sel["options"].([]any)
	if len(opts) != 2 || opts[0] != "1 Phase" {
		t.Errorf("German options %v", opts)
	}
	if !strings.Contains(sel["value_template"].(string), `'1 phase': '1 Phase'`) {
		t.Errorf("token→label mapping missing: %v", sel["value_template"])
	}
}

func TestNumberBoundsAndCategories(t *testing.T) {
	t.Parallel()
	comps := render(t, "en", testInfo, keba.NewCatalog(false))
	cur := comps[keba.KeyChargingCurrent]
	if cur["min"] != 0.0 || cur["max"] != 32.0 || cur["step"] != 0.1 || cur["unit_of_measurement"] != "A" {
		t.Errorf("charging current bounds %v %v %v %v", cur["min"], cur["max"], cur["step"], cur["unit_of_measurement"])
	}
	if comps[keba.KeyFirmware]["entity_category"] != "diagnostic" {
		t.Errorf("firmware category %v", comps[keba.KeyFirmware]["entity_category"])
	}
	if comps[keba.KeyFailsafeTimeout]["entity_category"] != "config" {
		t.Errorf("failsafe timeout category %v", comps[keba.KeyFailsafeTimeout]["entity_category"])
	}
	if comps[keba.KeyPowerFactor]["enabled_by_default"] != false {
		t.Error("power factor should ship disabled")
	}
}

func TestSlug(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"Garage Wallbox": "garage_wallbox", "  Ü-Box 2 ": "box_2", "!!": ""} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}
