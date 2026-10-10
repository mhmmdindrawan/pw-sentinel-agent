// Package config loads the runtime settings for a Sentinel Agent instance
// from a local YAML file. Since one agent now serves many kiosks, identity
// (location/gate/pos) is no longer 1:1 with the agent process — that comes
// from the device directory (see internal/devices) instead.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Identity is a vestigial, optional instance-level label — it is no longer
// used to stamp events (see internal/devices for that), but is kept as a
// narrow escape hatch for future instance-level labeling (e.g. in logs).
type Identity struct {
	Location string `yaml:"location"`
	Gate     string `yaml:"gate"`
	POS      string `yaml:"pos"`
}

type Server struct {
	ListenAddr   string `yaml:"listen_addr"`
	RegistryPath string `yaml:"registry_path"`
	DevicesPath  string `yaml:"devices_path"`
	DBPath       string `yaml:"db_path"`
	UsersPath    string `yaml:"users_path"`
	// Signs dashboard session cookies. If empty, a random secret is
	// generated at startup (everyone is logged out on restart).
	SessionSecret string `yaml:"session_secret"`
	// Base URL of the dashboard as NOC reaches it, used for links in
	// alerts (e.g. "http://100.x.y.z:8787").
	PublicURL string `yaml:"public_url"`
	// Log every request, not just failed (>= 400) and slow ones.
	LogRequests bool `yaml:"log_requests"`
}

// Status sets how long a device may stay silent before it is shown as
// STALE or DOWN, and how far back errors count toward a gate's colour.
type Status struct {
	OnlineMinutes      int `yaml:"online_minutes"`
	StaleMinutes       int `yaml:"stale_minutes"`
	ErrorWindowMinutes int `yaml:"error_window_minutes"`
}

type Retention struct {
	Days int `yaml:"days"` // events older than this are deleted daily; 0 = default
}

type App struct {
	Version string `yaml:"version"`
}

// Monitor lets an outside service notice when the agent itself is down: the
// agent requests PingURL every interval while its database answers, and a
// dead-man's-switch service (healthchecks.io, Uptime Kuma push, ...) alerts
// when the pings stop. Alerts can't cover this — they come from the agent.
type Monitor struct {
	PingURL             string `yaml:"ping_url"`
	PingIntervalSeconds int    `yaml:"ping_interval_seconds"`
}

type Config struct {
	Identity  Identity  `yaml:"identity"`
	Server    Server    `yaml:"server"`
	App       App       `yaml:"app"`
	Status    Status    `yaml:"status"`
	Retention Retention `yaml:"retention"`
	Alert     Alert     `yaml:"alert"`
	Monitor   Monitor   `yaml:"monitor"`
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	if cfg.Server.ListenAddr == "" {
		cfg.Server.ListenAddr = "127.0.0.1:8787"
	}
	if cfg.Server.RegistryPath == "" {
		cfg.Server.RegistryPath = "registry.yaml"
	}
	if cfg.Server.DevicesPath == "" {
		cfg.Server.DevicesPath = "devices.yaml"
	}
	if cfg.Server.DBPath == "" {
		cfg.Server.DBPath = "./data/sentinel.db"
	}
	if cfg.Server.UsersPath == "" {
		cfg.Server.UsersPath = "users.yaml"
	}
	if cfg.Status.OnlineMinutes <= 0 {
		cfg.Status.OnlineMinutes = 3
	}
	if cfg.Status.StaleMinutes <= 0 {
		cfg.Status.StaleMinutes = 10
	}
	if cfg.Status.StaleMinutes < cfg.Status.OnlineMinutes {
		return nil, fmt.Errorf("config %s: status.stale_minutes (%d) must be >= status.online_minutes (%d)",
			path, cfg.Status.StaleMinutes, cfg.Status.OnlineMinutes)
	}
	if cfg.Status.ErrorWindowMinutes <= 0 {
		cfg.Status.ErrorWindowMinutes = 10
	}
	if cfg.Retention.Days <= 0 {
		cfg.Retention.Days = 90
	}
	if v := os.Getenv("SENTINEL_PING_URL"); v != "" {
		cfg.Monitor.PingURL = v
	}
	if cfg.Monitor.PingIntervalSeconds <= 0 {
		cfg.Monitor.PingIntervalSeconds = 60
	}
	cfg.Alert.applyDefaults()

	return &cfg, nil
}
