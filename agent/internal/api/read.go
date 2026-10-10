package api

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"parkways-sentinel/agent/internal/model"
	"parkways-sentinel/agent/internal/status"
)

// thresholds falls back to the config defaults when a test builds the
// server from a zero Config.
func (s *Server) thresholds() (status.Thresholds, time.Duration) {
	minutes := func(v, def int) time.Duration {
		if v <= 0 {
			v = def
		}
		return time.Duration(v) * time.Minute
	}
	return status.Thresholds{
		Online: minutes(s.cfg.Status.OnlineMinutes, 3),
		Stale:  minutes(s.cfg.Status.StaleMinutes, 10),
	}, minutes(s.cfg.Status.ErrorWindowMinutes, 10)
}

// Fleet builds the per-device view shared by GET /v1/fleet, GET
// /v1/devices/{id} and the alerter: every device in devices.yaml with its
// liveness, colour, latest state and recent error stats, red first.
func (s *Server) Fleet(now time.Time) ([]model.FleetDevice, error) {
	th, window := s.thresholds()
	states, err := s.db.DeviceStates()
	if err != nil {
		return nil, err
	}
	all := s.devReg.All()
	ids := make([]string, len(all))
	for i, d := range all {
		ids[i] = d.DeviceID
	}
	stats, err := s.db.DeviceErrorStats(now, window, ids)
	if err != nil {
		return nil, err
	}

	out := []model.FleetDevice{}
	for _, d := range all {
		st := states[d.DeviceID]
		st.DeviceID = d.DeviceID
		lastSeen := latest(st.LastHeartbeatAt, st.LastEventAt)
		var seen time.Time
		if lastSeen != "" {
			seen, _ = time.Parse(time.RFC3339, lastSeen)
		}
		live := status.Derive(now, seen, th)
		es := stats[d.DeviceID]
		out = append(out, model.FleetDevice{
			DeviceID:         d.DeviceID,
			Location:         d.Location,
			Gate:             d.Gate,
			POS:              d.POS,
			Status:           string(live),
			Color:            string(status.GateColor(live, es.WorstRecent, st.OutboxPending)),
			LastSeen:         lastSeen,
			DeviceState:      st,
			DeviceErrorStats: es,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return status.ColorRank(status.Color(out[i].Color)) < status.ColorRank(status.Color(out[j].Color))
	})
	return out, nil
}

// latest returns the later of two RFC3339 UTC timestamps ("" = never).
// Both are written by the agent in the same format, so string order is
// time order.
func latest(a, b string) string {
	if a > b {
		return a
	}
	return b
}

func (s *Server) handleFleet(w http.ResponseWriter, r *http.Request) {
	now := s.now().UTC()
	fleet, err := s.Fleet(now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build fleet: "+err.Error())
		return
	}
	unmapped, err := s.db.CountUnmapped(now.Add(-24 * time.Hour))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	th, window := s.thresholds()
	writeJSON(w, http.StatusOK, map[string]any{
		"generated_at": now.Format(time.RFC3339),
		"thresholds": map[string]int{
			"online_minutes":       int(th.Online.Minutes()),
			"stale_minutes":        int(th.Stale.Minutes()),
			"error_window_minutes": int(window.Minutes()),
		},
		"devices":      fleet,
		"unmapped_24h": unmapped,
	})
}

func (s *Server) handleDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	now := s.now().UTC()
	fleet, err := s.Fleet(now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, d := range fleet {
		if d.DeviceID != id {
			continue
		}
		summary, err := s.db.Summary(24*time.Hour, model.EventFilter{DeviceID: id})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		hourly, err := s.db.HourlyErrors(id, now)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"device":      d,
			"summary_24h": summary,
			"hourly_24h":  hourly,
		})
		return
	}
	writeError(w, http.StatusNotFound, "unknown device "+strconv.Quote(id))
}

func (s *Server) handleGetEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := model.EventFilter{
		DeviceID:  q.Get("device_id"),
		Location:  q.Get("location"),
		Gate:      q.Get("gate"),
		ErrorCode: q.Get("error_code"),
		Event:     q.Get("event"),
		Severity:  q.Get("severity"),
	}
	for _, p := range []struct {
		key string
		dst *string
	}{{"since", &f.Since}, {"until", &f.Until}} {
		if v := q.Get(p.key); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				writeError(w, http.StatusBadRequest, p.key+" must be RFC3339, e.g. 2026-10-07T00:00:00Z")
				return
			}
			*p.dst = t.UTC().Format(time.RFC3339)
		}
	}
	if v := q.Get("limit"); v != "" {
		f.Limit, _ = strconv.Atoi(v)
	}
	if v := q.Get("before_id"); v != "" {
		f.BeforeID, _ = strconv.ParseInt(v, 10, 64)
	}

	events, err := s.db.QueryEvents(f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to query events: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	minutes := 30
	if v := r.URL.Query().Get("minutes"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			minutes = n
		}
	}
	f := model.EventFilter{DeviceID: r.URL.Query().Get("device_id"), Location: r.URL.Query().Get("location")}
	rows, err := s.db.Summary(time.Duration(minutes)*time.Minute, f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build summary: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	// Public on purpose (uptime checks): reveals no event or device data.
	writeJSON(w, http.StatusOK, map[string]any{
		"status":             "ok",
		"version":            s.Version,
		"devices_configured": s.devReg.Count(),
		"server_time":        s.now().UTC().Format(time.RFC3339),
	})
}
