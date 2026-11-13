package config

import (
	"os"
	"path/filepath"
	"testing"
)

func load(t *testing.T, body string) *Config {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// An operator's old config.yaml without the new sections must get the
// protective defaults, not a disabled squelch.
func TestIngestAndIncidentDefaults(t *testing.T) {
	cfg := load(t, "server:\n  listen_addr: \"127.0.0.1:1\"\n")
	if cfg.Ingest.SquelchPerHour != 20 || cfg.Incident.GapMinutes != 10 || cfg.Incident.Disabled {
		t.Fatalf("defaults: %+v %+v", cfg.Ingest, cfg.Incident)
	}
	cfg = load(t, "ingest:\n  squelch_per_hour: -1\nincident:\n  gap_minutes: 5\n  disabled: true\n")
	if cfg.Ingest.SquelchPerHour != 0 || cfg.Incident.GapMinutes != 5 || !cfg.Incident.Disabled {
		t.Fatalf("explicit: %+v %+v", cfg.Ingest, cfg.Incident)
	}
}
