package api

import (
	"context"
	"log"
	"time"

	"parkways-sentinel/agent/internal/model"
)

// TrackStatus records every device whose liveness differs from the last
// recorded one. Right after the agent starts every kiosk looks DOWN until
// it heartbeats again, so for one stale period only ONLINE is recorded
// (the alerter applies the same grace).
func (s *Server) TrackStatus(now time.Time, startedAt time.Time) error {
	fleet, err := s.Fleet(now)
	if err != nil {
		return err
	}
	last, err := s.db.LatestStatuses()
	if err != nil {
		return err
	}
	th, _ := s.thresholds()
	inGrace := now.Sub(startedAt) < th.Stale
	at := now.Format(time.RFC3339)
	down := map[string]bool{}
	for _, d := range fleet {
		if d.Status == "DOWN" {
			down[d.DeviceID] = true
		}
		if d.Status == "NEVER" || last[d.DeviceID].Status == d.Status {
			continue
		}
		if inGrace && d.Status != "ONLINE" {
			continue
		}
		if err := s.db.RecordStatus(d.DeviceID, d.Status, at); err != nil {
			return err
		}
		log.Printf("status: %s %s -> %s", d.DeviceID, orNone(last[d.DeviceID].Status), d.Status)
		if d.Status == "DOWN" && !s.cfg.Incident.Disabled {
			if in, opened, err := s.db.AttachToIncident(d.DeviceID, d.Location, d.Gate, model.StatusDownEvent, "HIGH", now); err != nil {
				log.Printf("warning: incident for %s: %v", d.DeviceID, err)
			} else if opened {
				log.Printf("incident #%d opened: %s %s is DOWN", in.ID, d.Location, d.Gate)
			}
		}
	}
	if s.cfg.Incident.Disabled || inGrace {
		return nil
	}
	gap := time.Duration(s.cfg.Incident.GapMinutes) * time.Minute
	resolved, err := s.db.ResolveQuietIncidents(now, gap, down)
	if err != nil {
		return err
	}
	for _, in := range resolved {
		log.Printf("incident #%d resolved: %s %s quiet for %s (%d event(s))", in.ID, in.Location, in.Gate, gap, in.Count)
	}
	return nil
}

// RunStatusTracker calls TrackStatus every `every` until ctx ends.
func (s *Server) RunStatusTracker(ctx context.Context, every time.Duration) {
	started := s.now()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := s.TrackStatus(s.now(), started); err != nil {
			log.Printf("status tracker: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
