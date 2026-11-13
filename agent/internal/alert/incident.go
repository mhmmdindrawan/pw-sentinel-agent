package alert

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"parkways-sentinel/agent/internal/config"
	"parkways-sentinel/agent/internal/model"
	"parkways-sentinel/agent/internal/status"
)

const (
	// An open incident's message is rewritten with the new count at most
	// this often, so a noisy gate doesn't turn into a stream of edits.
	incidentEditEvery   = 5 * time.Minute
	maxIncidentsPerTick = 50
	maxIncidentLines    = 6
)

// incidentAlerts sends one message per incident and route when it opens,
// edits it as the count grows, and edits it once more when it resolves.
// Bookkeeping lives in the incident row, so a restart continues editing
// the same Telegram message.
func (a *Alerter) incidentAlerts(ctx context.Context, fleet []model.FleetDevice, now time.Time) error {
	if now.Sub(a.started) < a.grace {
		return nil // every gate looks DOWN right after a restart
	}
	incidents, err := a.store.IncidentsToNotify(maxIncidentsPerTick)
	if err != nil {
		return err
	}
	if len(incidents) == 0 {
		return nil
	}
	names := map[string]string{}
	for _, d := range fleet {
		names[d.DeviceID] = d.Name
	}
	sentThisTick := map[string]int{}
	var msgID int64
	send := a.limited(func(r config.AlertRoute, msg string) error {
		id, err := a.sender.Send(ctx, Destination{ChatID: r.ChatID, ThreadID: r.ThreadID}, msg)
		msgID = id
		return err
	}, now, sentThisTick)
	var editID int64
	edit := a.limited(func(r config.AlertRoute, msg string) error {
		return a.sender.Edit(ctx, Destination{ChatID: r.ChatID, ThreadID: r.ThreadID}, editID, msg)
	}, now, sentThisTick)

	for _, in := range incidents {
		changed := false
		for _, r := range a.cfg.Routes {
			al, ok := in.Alerts[r.Name]
			if !ok {
				if !routeWants(r, alert{severity: in.Severity, location: in.Location}) {
					in.Alerts[r.Name] = model.Alert{Skipped: true}
					changed = true
					continue
				}
				text := a.incidentText(in, names[in.DeviceID])
				err := send(r, text)
				if err == nil {
					in.Alerts[r.Name] = model.Alert{ChatID: r.ChatID, ThreadID: r.ThreadID, MessageID: msgID, Count: in.Count,
						At: now.UTC().Format(time.RFC3339), Resolved: in.ResolvedAt != ""}
					changed = true
				}
				if err != nil && !errors.Is(err, errChatBusy) {
					log.Printf("alert: incident #%d to %s failed: %v", in.ID, r.Name, err)
				}
				if lerr := a.store.LogAlert(fmt.Sprintf("incident:%d", in.ID), r.Name, text, now, err); lerr != nil {
					return lerr
				}
				continue
			}
			if al.Skipped || al.Resolved {
				continue
			}
			last, _ := time.Parse(time.RFC3339, al.At)
			resolving := in.ResolvedAt != ""
			if !resolving && (in.Count == al.Count || now.Sub(last) < incidentEditEvery) {
				continue
			}
			editID = al.MessageID
			text := a.incidentText(in, names[in.DeviceID])
			if err := edit(r, text); err != nil {
				if !errors.Is(err, errChatBusy) {
					log.Printf("alert: incident #%d edit to %s failed: %v", in.ID, r.Name, err)
				}
				continue
			}
			al.Count, al.At, al.Resolved = in.Count, now.UTC().Format(time.RFC3339), resolving
			in.Alerts[r.Name] = al
			changed = true
		}
		done := in.ResolvedAt != ""
		for _, r := range a.cfg.Routes {
			if al, ok := in.Alerts[r.Name]; !ok || (!al.Skipped && !al.Resolved) {
				done = false
			}
		}
		if changed || done {
			if err := a.store.SaveIncidentAlerts(in.ID, in.Alerts, done); err != nil {
				return err
			}
		}
	}
	return nil
}

// incidentText is the whole message for an incident, open or resolved —
// the same function renders both so an edit replaces the text cleanly.
func (a *Alerter) incidentText(in model.Incident, name string) string {
	icon := "🟡"
	if status.SeverityRank(in.Severity) >= status.SeverityRank("HIGH") {
		icon = "🔴"
	}
	gate := in.Location + " " + in.Gate
	if name != "" {
		gate += " " + name
	}
	var b strings.Builder
	if in.ResolvedAt != "" {
		fmt.Fprintf(&b, "🟢 %s — SELESAI (insiden #%d)\n", gate, in.ID)
		fmt.Fprintf(&b, "Mulai %s · selesai %s · %s · %d event", a.clock(in.StartedAt), a.clock(in.ResolvedAt), duration(in.StartedAt, in.ResolvedAt), in.Count)
	} else {
		fmt.Fprintf(&b, "%s %s — INSIDEN #%d\n", icon, gate, in.ID)
		fmt.Fprintf(&b, "Mulai %s · %d event · terakhir %s", a.clock(in.StartedAt), in.Count, a.clock(in.LastAt))
	}
	type kv struct {
		name string
		n    int
	}
	var codes []kv
	for k, n := range in.Codes {
		codes = append(codes, kv{k, n})
	}
	sort.Slice(codes, func(i, j int) bool {
		if codes[i].n != codes[j].n {
			return codes[i].n > codes[j].n
		}
		return codes[i].name < codes[j].name
	})
	for i, c := range codes {
		if i == maxIncidentLines {
			fmt.Fprintf(&b, "\n• … dan %d jenis lain", len(codes)-i)
			break
		}
		fmt.Fprintf(&b, "\n• %s ×%d", model.EventLabel(c.name), c.n)
	}
	b.WriteString(a.link(in.DeviceID))
	return truncate(b.String(), maxMessageLen)
}

func duration(from, to string) string {
	f, err1 := time.Parse(time.RFC3339, from)
	t, err2 := time.Parse(time.RFC3339, to)
	if err1 != nil || err2 != nil {
		return "—"
	}
	d := t.Sub(f).Round(time.Minute)
	if d < time.Hour {
		return fmt.Sprintf("%d mnt", int(d.Minutes()))
	}
	return fmt.Sprintf("%d jam %d mnt", int(d.Hours()), int(d.Minutes())%60)
}
