// Package alert pushes what NOC must act on to Telegram: a gate going DOWN
// (and recovering), new HIGH/CRITICAL events, and kiosks whose SDK is
// sitting on a backlog of undelivered events.
//
// It runs as one goroutine that wakes every interval, so it needs no hooks
// in the ingest path. Progress (last processed event id, last seen device
// statuses) lives in the agent database, so a restart neither re-sends old
// alerts nor floods NOC with every gate briefly looking DOWN.
package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"parkways-sentinel/agent/internal/config"
	"parkways-sentinel/agent/internal/model"
	"parkways-sentinel/agent/internal/status"
)

// Sender delivers one plain-text message to one chat.
type Sender interface {
	Send(ctx context.Context, chatID, text string) error
}

// Store is the slice of the agent database the alerter uses.
type Store interface {
	QueryEvents(f model.EventFilter) ([]model.StoredEvent, error)
	MaxEventID() (int64, error)
	GetKV(key string) (string, error)
	SetKV(key, value string) error
	LastAlertSent(key, route string) (time.Time, bool, error)
	LogAlert(key, route, message string, at time.Time, sendErr error) error
}

// FleetFunc returns the current per-device view (api.Server.Fleet).
type FleetFunc func(now time.Time) ([]model.FleetDevice, error)

const (
	kvLastEvent = "alert:last_event_id"
	kvStatuses  = "alert:statuses"
	// Cap per tick so a burst (or a long-offline agent catching up) can't
	// stall the loop; the rest is picked up next tick.
	maxEventsPerTick = 500
	// Telegram's message limit is 4096 characters.
	maxMessageLen = 3900
	// Lines per message before the rest is summarised as "+N more".
	maxLinesPerMessage = 12
	// Undelivered messages kept per route for retry.
	maxPendingPerRoute = 20
)

type Alerter struct {
	cfg       config.Alert
	publicURL string
	fleet     FleetFunc
	store     Store
	sender    Sender
	loc       *time.Location
	// No status alerts until this long after start: right after an agent
	// restart every kiosk looks DOWN until its next heartbeat arrives.
	grace   time.Duration
	started time.Time
	pending map[string][]string // route name -> messages that failed to send
}

func New(cfg config.Alert, publicURL string, grace time.Duration, fleet FleetFunc, store Store, sender Sender, now time.Time) (*Alerter, error) {
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return nil, fmt.Errorf("alert timezone %q: %w", cfg.Timezone, err)
	}
	for _, r := range cfg.Routes {
		if r.ChatID == "" {
			return nil, fmt.Errorf("alert route %q has no chat_id", r.Name)
		}
		if status.SeverityRank(r.MinSeverity) == 0 {
			return nil, fmt.Errorf("alert route %q: invalid min_severity %q", r.Name, r.MinSeverity)
		}
	}
	return &Alerter{
		cfg: cfg, publicURL: strings.TrimRight(publicURL, "/"), fleet: fleet, store: store, sender: sender,
		loc: loc, grace: grace, started: now, pending: map[string][]string{},
	}, nil
}

// Run ticks until ctx is cancelled.
func (a *Alerter) Run(ctx context.Context) {
	t := time.NewTicker(time.Duration(a.cfg.IntervalSeconds) * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if err := a.Tick(ctx, now); err != nil {
				log.Printf("alert: %v", err)
			}
		}
	}
}

// alert is one thing worth telling someone about.
type alert struct {
	key      string // cooldown/audit key
	severity string // for route filtering
	location string
	text     string
	cooldown bool // false: always send (status changes are rare and paired)
}

// Tick runs one evaluation round and sends what it found.
func (a *Alerter) Tick(ctx context.Context, now time.Time) error {
	var alerts []alert

	fleet, err := a.fleet(now)
	if err != nil {
		return fmt.Errorf("fleet: %w", err)
	}
	st, err := a.statusAlerts(fleet, now)
	if err != nil {
		return err
	}
	alerts = append(alerts, st...)
	alerts = append(alerts, a.backlogAlerts(fleet)...)

	ev, err := a.eventAlerts(fleet)
	if err != nil {
		return err
	}
	alerts = append(alerts, ev...)

	return a.deliver(ctx, alerts, now)
}

func (a *Alerter) statusAlerts(fleet []model.FleetDevice, now time.Time) ([]alert, error) {
	prev := map[string]string{}
	raw, err := a.store.GetKV(kvStatuses)
	if err != nil {
		return nil, err
	}
	first := raw == ""
	if !first {
		if err := json.Unmarshal([]byte(raw), &prev); err != nil {
			first = true // corrupt bookkeeping: start over quietly
		}
	}
	if now.Sub(a.started) < a.grace {
		// Keep the statuses from before the restart; compare after grace.
		if first {
			return nil, a.saveStatuses(fleet)
		}
		return nil, nil
	}

	var out []alert
	for _, d := range fleet {
		was, cur := prev[d.DeviceID], d.Status
		if first || was == cur {
			continue
		}
		switch {
		case cur == string(status.Down) && (was == string(status.Online) || was == string(status.Stale)):
			out = append(out, alert{
				key: "status:" + d.DeviceID, severity: "HIGH", location: d.Location,
				text: fmt.Sprintf("🔴 %s %s — MATI\nTidak ada kabar sejak %s.\n%s%s",
					d.Location, d.Gate, a.clock(d.LastSeen), d.DeviceID, a.link(d.DeviceID)),
			})
		case cur == string(status.Online) && was == string(status.Down):
			out = append(out, alert{
				key: "status:" + d.DeviceID, severity: "HIGH", location: d.Location,
				text: fmt.Sprintf("🟢 %s %s — kembali online.\n%s%s", d.Location, d.Gate, d.DeviceID, a.link(d.DeviceID)),
			})
		}
	}
	return out, a.saveStatuses(fleet)
}

func (a *Alerter) saveStatuses(fleet []model.FleetDevice) error {
	cur := map[string]string{}
	for _, d := range fleet {
		cur[d.DeviceID] = d.Status
	}
	b, _ := json.Marshal(cur)
	return a.store.SetKV(kvStatuses, string(b))
}

func (a *Alerter) backlogAlerts(fleet []model.FleetDevice) []alert {
	var out []alert
	for _, d := range fleet {
		if d.OutboxPending < a.cfg.OutboxBacklogThreshold {
			continue
		}
		out = append(out, alert{
			key: "outbox:" + d.DeviceID, severity: "MEDIUM", location: d.Location, cooldown: true,
			text: fmt.Sprintf("🟡 %s %s — %d event tertahan di kiosk (belum terkirim ke Sentinel).%s",
				d.Location, d.Gate, d.OutboxPending, a.link(d.DeviceID)),
		})
	}
	return out
}

func (a *Alerter) eventAlerts(fleet []model.FleetDevice) ([]alert, error) {
	raw, err := a.store.GetKV(kvLastEvent)
	if err != nil {
		return nil, err
	}
	if raw == "" {
		// First run: start from now, don't page NOC with history.
		maxID, err := a.store.MaxEventID()
		if err != nil {
			return nil, err
		}
		return nil, a.store.SetKV(kvLastEvent, strconv.FormatInt(maxID, 10))
	}
	last, _ := strconv.ParseInt(raw, 10, 64)

	evs, err := a.store.QueryEvents(model.EventFilter{AfterID: last, Severity: a.minSeverity(), Limit: maxEventsPerTick})
	if err != nil {
		return nil, err
	}
	if len(evs) == 0 {
		// Nothing alert-worthy, but skip past lower-severity events so the
		// next query doesn't rescan them.
		maxID, err := a.store.MaxEventID()
		if err != nil || maxID <= last {
			return nil, err
		}
		return nil, a.store.SetKV(kvLastEvent, strconv.FormatInt(maxID, 10))
	}

	var out []alert
	for _, e := range evs {
		out = append(out, alert{
			key: "event:" + e.DeviceID + ":" + e.ErrorCode, severity: e.Severity, location: e.Location, cooldown: true,
			text: a.eventText(e),
		})
	}
	return out, a.store.SetKV(kvLastEvent, strconv.FormatInt(evs[len(evs)-1].ID, 10))
}

func (a *Alerter) eventText(e model.StoredEvent) string {
	icon := "⚠️"
	if e.Severity == "CRITICAL" {
		icon = "🚨"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s %s — %s (%s)\n%s · %s", icon, e.Location, e.Gate, e.Event, e.Severity, e.ErrorCode, a.clock(e.ReceivedAt))
	if n, ok := e.Context["coalesced_count"].(float64); ok && n > 0 {
		fmt.Fprintf(&b, " · berulang %d×", int(n)+1)
	}
	if e.Message != "" {
		b.WriteString("\n" + truncate(e.Message, 200))
	}
	if e.Location == "UNMAPPED" {
		b.WriteString("\n(device_id tidak cocok dengan token — cek konfigurasi kiosk)")
	} else {
		b.WriteString(a.link(e.DeviceID))
	}
	return b.String()
}

// minSeverity is the lowest severity any route wants, so one query serves
// every route.
func (a *Alerter) minSeverity() string {
	minSev := "CRITICAL"
	for _, r := range a.cfg.Routes {
		if status.SeverityRank(r.MinSeverity) < status.SeverityRank(minSev) {
			minSev = r.MinSeverity
		}
	}
	return minSev
}

func (a *Alerter) deliver(ctx context.Context, alerts []alert, now time.Time) error {
	cooldown := time.Duration(a.cfg.CooldownMinutes) * time.Minute
	for _, r := range a.cfg.Routes {
		var lines, keys []string
		seen := map[string]bool{}
		suppressed := 0
		for _, al := range alerts {
			if !routeWants(r, al) {
				continue
			}
			if al.cooldown {
				if seen[al.key] {
					suppressed++ // same device + code again within this tick
					continue
				}
				at, ok, err := a.store.LastAlertSent(al.key, r.Name)
				if err != nil {
					return err
				}
				if ok && now.Sub(at) < cooldown {
					suppressed++
					continue
				}
			}
			seen[al.key] = true
			lines = append(lines, al.text)
			keys = append(keys, al.key)
		}

		// Retry messages that failed on earlier ticks first, oldest first.
		var failed []string
		for _, msg := range a.pending[r.Name] {
			if err := a.sender.Send(ctx, r.ChatID, msg); err != nil {
				log.Printf("alert: resend to %s failed: %v", r.Name, err)
				failed = append(failed, msg)
			}
		}
		if len(lines) > 0 {
			msg := compose(lines, suppressed)
			err := a.sender.Send(ctx, r.ChatID, msg)
			if err != nil {
				log.Printf("alert: send to %s failed: %v", r.Name, err)
				failed = append(failed, msg)
			}
			// One log row per alert key, so cooldowns and the audit trail
			// work per device + code even though the message is combined.
			for _, k := range keys {
				if lerr := a.store.LogAlert(k, r.Name, msg, now, err); lerr != nil {
					return lerr
				}
			}
		}
		if len(failed) > maxPendingPerRoute {
			failed = failed[len(failed)-maxPendingPerRoute:]
		}
		a.pending[r.Name] = failed
	}
	return nil
}

func routeWants(r config.AlertRoute, al alert) bool {
	if status.SeverityRank(al.severity) < status.SeverityRank(r.MinSeverity) {
		return false
	}
	if len(r.Locations) == 0 {
		return true
	}
	for _, l := range r.Locations {
		if l == al.location {
			return true
		}
	}
	return false
}

// compose joins one tick's alerts for a route into a single message, so a
// burst arrives as one notification rather than dozens.
func compose(lines []string, suppressed int) string {
	extra := 0
	if len(lines) > maxLinesPerMessage {
		extra = len(lines) - maxLinesPerMessage
		lines = lines[:maxLinesPerMessage]
	}
	msg := strings.Join(lines, "\n\n")
	if extra > 0 {
		msg += fmt.Sprintf("\n\n… dan %d alert lain. Buka dashboard untuk detail.", extra)
	}
	if suppressed > 0 {
		msg += fmt.Sprintf("\n(%d alert berulang ditahan agar tidak spam)", suppressed)
	}
	return truncate(msg, maxMessageLen)
}

func (a *Alerter) link(deviceID string) string {
	if a.publicURL == "" {
		return ""
	}
	return "\n" + a.publicURL + "/#/device/" + deviceID
}

// clock renders an agent timestamp in the configured zone, e.g. "14:05 WIB".
func (a *Alerter) clock(iso string) string {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return "—"
	}
	t = t.In(a.loc)
	if y, m, d := t.Date(); time.Now().In(a.loc).Format("2006-01-02") != fmt.Sprintf("%04d-%02d-%02d", y, m, d) {
		return t.Format("02/01 15:04 MST")
	}
	return t.Format("15:04 MST")
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// SortedRouteNames is used by `sentinel-agent alert test`.
func SortedRouteNames(cfg config.Alert) []string {
	var names []string
	for _, r := range cfg.Routes {
		names = append(names, r.Name)
	}
	sort.Strings(names)
	return names
}
