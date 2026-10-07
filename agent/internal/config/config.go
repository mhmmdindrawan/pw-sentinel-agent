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
}

type App struct {
	Version string `yaml:"version"`
}

type Config struct {
	Identity Identity `yaml:"identity"`
	Server   Server   `yaml:"server"`
	App      App      `yaml:"app"`
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

	return &cfg, nil
}
