package model

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"gopkg.in/yaml.v3"
)

// Every event in the shipped registry needs a human name in both places
// that show it: Telegram (eventLabels here) and the dashboard
// (EVENT_LABEL in js/core/format.js). A missing one shows the raw symbolic
// name to support staff, which is easy to miss in review.
func TestEveryRegistryEventHasLabels(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "registry.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var seed struct {
		Events map[string]any `yaml:"events"`
	}
	if err := yaml.Unmarshal(raw, &seed); err != nil {
		t.Fatal(err)
	}
	if len(seed.Events) == 0 {
		t.Fatal("registry.yaml has no events")
	}
	js, err := os.ReadFile(filepath.Join("..", "web", "static", "js", "core", "format.js"))
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)export const EVENT_LABEL = \{(.*?)\};`).FindSubmatch(js)
	if block == nil {
		t.Fatal("EVENT_LABEL not found in format.js")
	}
	for name := range seed.Events {
		if _, ok := eventLabels[name]; !ok {
			t.Errorf("%s: no label in labels.go (Telegram)", name)
		}
		if !regexp.MustCompile(`\b` + name + `:`).Match(block[1]) {
			t.Errorf("%s: no label in format.js EVENT_LABEL (dashboard)", name)
		}
	}
}
