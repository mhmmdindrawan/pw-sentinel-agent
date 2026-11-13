// Package config loads the runtime settings for a Sentinel Agent instance
// from a local YAML file. Since one agent now serves many kiosks, identity
// (location/gate/pos) is no longer 1:1 with the agent process — that comes
// from the device directory (see internal/devices) instead.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

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
	// MaxDBMB caps the database file. When the daily purge leaves it
	// larger, the oldest events are dropped regardless of Days, so a
	// noisy kiosk can never fill the site server's disk. 0 = 500.
	MaxDBMB int `yaml:"max_db_mb"`
	// DailyResetAt ("02:00") wipes the day's monitoring data at that local
	// time every day: events, incidents and status history from before
	// that moment go, then the file is compacted. Registered kiosks,
	// users and learned error codes stay. For sites that only need the
	// current operating day. Empty = off (only Days applies).
	DailyResetAt string `yaml:"daily_reset_at"`
	// Timezone for DailyResetAt, e.g. "Asia/Makassar"; empty = the
	// server's own clock, which is what the site's operators read.
	Timezone string `yaml:"timezone"`
}

// ResetClock parses DailyResetAt into hour and minute.
func (r Retention) ResetClock() (hour, minute int, ok bool) {
	if r.DailyResetAt == "" {
		return 0, 0, false
	}
	t, err := time.Parse("15:04", r.DailyResetAt)
	if err != nil {
		return 0, 0, false
	}
	return t.Hour(), t.Minute(), true
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

// Site names the location this agent serves when it runs on that location's
// own server (one agent per site, many gates). With EnrollKey set, a kiosk
// that knows the key can register itself (POST /v1/enroll) and receives its
// own device token; every enrolled device is attributed to Code. Leave the
// key empty to keep enrollment off and manage devices.yaml by hand.
type Site struct {
	Code      string `yaml:"code"`
	Name      string `yaml:"name"`
	EnrollKey string `yaml:"enroll_key"` // or SENTINEL_SITE_KEY in the environment
}

// EnrollEnabled reports whether kiosks may self-register.
func (s Site) EnrollEnabled() bool { return s.Code != "" && s.EnrollKey != "" }

// Ingest guards the agent against a kiosk that reports the same thing in a
// loop (a dead reader logged every poll cycle): after SquelchPerHour
// events with the same name from one device within an hour, the rest of
// that hour is stored as one summary event. 0 means the default (20); -1
// disables.
type Ingest struct {
	SquelchPerHour int `yaml:"squelch_per_hour"`
}

// Incident groups a gate's consecutive MEDIUM+ events (and a DOWN status)
// into one incident while they keep coming; it is resolved after
// GapMinutes of quiet with the gate online. Telegram gets one message per
// incident, edited as it grows and when it ends, instead of one per event.
type Incident struct {
	Disabled   bool `yaml:"disabled"`
	GapMinutes int  `yaml:"gap_minutes"`
}

type Config struct {
	Identity  Identity  `yaml:"identity"`
	Ingest    Ingest    `yaml:"ingest"`
	Incident  Incident  `yaml:"incident"`
	Site      Site      `yaml:"site"`
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
	if v := os.Getenv("SENTINEL_SITE_KEY"); v != "" {
		cfg.Site.EnrollKey = v
	}
	cfg.Site.Code = strings.ToUpper(strings.TrimSpace(cfg.Site.Code))
	cfg.Site.Name = strings.TrimSpace(cfg.Site.Name)
	cfg.Site.EnrollKey = strings.TrimSpace(cfg.Site.EnrollKey)
	if cfg.Site.EnrollKey != "" {
		if cfg.Site.Code == "" {
			return nil, fmt.Errorf("config %s: site.enroll_key is set but site.code is empty", path)
		}
		if len(cfg.Site.EnrollKey) < 16 {
			return nil, fmt.Errorf("config %s: site.enroll_key must be at least 16 characters (generate one with: openssl rand -hex 16)", path)
		}
	}
	if cfg.Retention.MaxDBMB <= 0 {
		cfg.Retention.MaxDBMB = 500
	}
	// Unset (0) takes the default; -1 disables squelching.
	cfg.Retention.DailyResetAt = strings.TrimSpace(cfg.Retention.DailyResetAt)
	if cfg.Retention.DailyResetAt != "" {
		if _, _, ok := cfg.Retention.ResetClock(); !ok {
			return nil, fmt.Errorf("config %s: retention.daily_reset_at %q must look like \"02:00\"", path, cfg.Retention.DailyResetAt)
		}
		if _, err := time.LoadLocation(cfg.Retention.Timezone); err != nil {
			return nil, fmt.Errorf("config %s: retention.timezone %q: %w", path, cfg.Retention.Timezone, err)
		}
	}
	if cfg.Ingest.SquelchPerHour == 0 {
		cfg.Ingest.SquelchPerHour = 20
	}
	if cfg.Ingest.SquelchPerHour < 0 {
		cfg.Ingest.SquelchPerHour = 0
	}
	if cfg.Incident.GapMinutes <= 0 {
		cfg.Incident.GapMinutes = 10
	}
	if cfg.Monitor.PingIntervalSeconds <= 0 {
		cfg.Monitor.PingIntervalSeconds = 60
	}
	cfg.Alert.applyDefaults()

	return &cfg, nil
}
