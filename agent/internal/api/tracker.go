package api

import (
	"context"
	"log"
	"time"
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
	for _, d := range fleet {
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
