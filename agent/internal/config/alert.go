package config

import (
	"os"
	"strings"
)

// Alert configures Telegram notifications. Alerting is off unless a bot
// token and at least one route are set.
type Alert struct {
	// Prefer the SENTINEL_TELEGRAM_BOT_TOKEN env var over this field so the
	// token stays out of config files.
	TelegramBotToken string `yaml:"telegram_bot_token"`
	IntervalSeconds  int    `yaml:"interval_seconds"`
	// Minimum gap between two alerts for the same device + error code.
	CooldownMinutes int `yaml:"cooldown_minutes"`
	// A device whose SDK reports more queued events than this is alerted.
	OutboxBacklogThreshold int `yaml:"outbox_backlog_threshold"`
	// IANA zone used for times in alert messages, e.g. "Asia/Jakarta".
	Timezone string       `yaml:"timezone"`
	Routes   []AlertRoute `yaml:"routes"`
}

// AlertRoute sends a filtered subset of alerts to one Telegram chat.
type AlertRoute struct {
	Name   string `yaml:"name"`
	ChatID string `yaml:"chat_id"`
	// Forum topic (message_thread_id) in a supergroup with Topics on; 0 or
	// unset posts to the group itself (its General topic).
	ThreadID    int    `yaml:"thread_id"`
	MinSeverity string `yaml:"min_severity"` // INFO, LOW, MEDIUM, HIGH, CRITICAL
	// Empty means every location.
	Locations []string `yaml:"locations"`
}

// Enabled reports whether alerts can actually be delivered.
func (a Alert) Enabled() bool {
	return a.TelegramBotToken != "" && len(a.Routes) > 0
}

func (a *Alert) applyDefaults() {
	if v := os.Getenv("SENTINEL_TELEGRAM_BOT_TOKEN"); v != "" {
		a.TelegramBotToken = v
	}
	if a.IntervalSeconds <= 0 {
		a.IntervalSeconds = 30
	}
	if a.CooldownMinutes <= 0 {
		a.CooldownMinutes = 15
	}
	if a.OutboxBacklogThreshold <= 0 {
		a.OutboxBacklogThreshold = 100
	}
	if a.Timezone == "" {
		a.Timezone = "Asia/Jakarta"
	}
	for i := range a.Routes {
		a.Routes[i].MinSeverity = strings.ToUpper(strings.TrimSpace(a.Routes[i].MinSeverity))
		if a.Routes[i].MinSeverity == "" {
			a.Routes[i].MinSeverity = "HIGH"
		}
		for j, loc := range a.Routes[i].Locations {
			a.Routes[i].Locations[j] = strings.ToUpper(strings.TrimSpace(loc))
		}
	}
}
