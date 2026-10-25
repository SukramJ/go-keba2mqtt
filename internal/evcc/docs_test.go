// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package evcc

import (
	"bytes"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/SukramJ/go-keba2mqtt/internal/hass"
)

// TestDocsSnippetMatchesTheProfile is the contract the shared profile
// asks for: every topic the evcc.yaml in docs/evcc.md names is one this
// bridge publishes (status) or routes (set), every getter reads `.val`,
// every setter writes a command topic, and every command is documented.
func TestDocsSnippetMatchesTheProfile(t *testing.T) {
	t.Parallel()
	md, err := os.ReadFile("../../docs/evcc.md")
	if err != nil {
		t.Fatal(err)
	}
	// A Windows checkout may carry CRLF line endings.
	md = bytes.ReplaceAll(md, []byte("\r\n"), []byte("\n"))
	blocks := regexp.MustCompile("(?s)```yaml\n(.*?)```").FindAllSubmatch(md, -1)
	if len(blocks) != 1 {
		t.Fatalf("docs/evcc.md has %d yaml blocks, want 1", len(blocks))
	}
	var doc struct {
		Chargers []map[string]any `yaml:"chargers"`
	}
	if err := yaml.Unmarshal(blocks[0][1], &doc); err != nil {
		t.Fatalf("snippet is not YAML: %v", err)
	}
	if len(doc.Chargers) != 1 || doc.Chargers[0]["type"] != "custom" {
		t.Fatalf("want one custom charger, got %v", doc.Chargers)
	}

	const serial = "18416854"
	status := map[string]bool{}
	for _, k := range StatusKeys() {
		status[hass.StateTopic("keba", serial, Group, k)] = true
	}
	set := map[string]bool{}
	for _, k := range Commands {
		set[hass.CommandTopic("keba", serial, Group, k)] = true
	}

	var setters []string
	check := func(field string, src map[string]any) {
		if src["source"] != "mqtt" {
			t.Errorf("%s: source %v, want mqtt", field, src["source"])
		}
		topic, _ := src["topic"].(string)
		switch {
		case status[topic]:
			if src["jq"] != ".val" {
				t.Errorf("%s reads %s without jq: .val", field, topic)
			}
		case set[topic]:
			if _, ok := src["jq"]; ok {
				t.Errorf("%s writes %s with a jq filter", field, topic)
			}
			if !strings.HasSuffix(topic, "/"+field) {
				t.Errorf("setter %s writes %s, want the command of the same name", field, topic)
			}
			setters = append(setters, field)
		default:
			t.Errorf("%s: %q is neither a published status nor a routed command topic", field, topic)
		}
	}
	for field, v := range doc.Chargers[0] {
		switch src := v.(type) {
		case map[string]any:
			check(field, src)
		case []any:
			for _, e := range src {
				if m, ok := e.(map[string]any); ok {
					check(field, m)
				}
			}
		}
	}
	slices.Sort(setters)
	want := slices.Clone(Commands)
	slices.Sort(want)
	if !slices.Equal(setters, want) {
		t.Errorf("snippet setters %v, want every command %v", setters, want)
	}
}
