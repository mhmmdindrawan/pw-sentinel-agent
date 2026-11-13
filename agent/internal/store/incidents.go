package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"parkways-sentinel/agent/internal/model"
	"parkways-sentinel/agent/internal/status"
)

const incidentCols = `id, device_id, location, gate, started_at, last_at, resolved_at, count, severity, codes_json, alert_json, done`

func scanIncident(sc interface{ Scan(...any) error }) (model.Incident, error) {
	var in model.Incident
	var resolved sql.NullString
	var codes, alerts string
	var done int
	if err := sc.Scan(&in.ID, &in.DeviceID, &in.Location, &in.Gate, &in.StartedAt, &in.LastAt, &resolved,
		&in.Count, &in.Severity, &codes, &alerts, &done); err != nil {
		return in, err
	}
	in.ResolvedAt = resolved.String
	in.Done = done == 1
	in.Codes = map[string]int{}
	in.Alerts = map[string]model.Alert{}
	_ = json.Unmarshal([]byte(codes), &in.Codes)
	_ = json.Unmarshal([]byte(alerts), &in.Alerts)
	return in, nil
}

// OpenIncident returns the device's unresolved incident, if any.
func (s *Store) OpenIncident(deviceID string) (model.Incident, bool, error) {
	row := s.db.QueryRow(`SELECT `+incidentCols+` FROM incidents WHERE device_id = ? AND resolved_at IS NULL ORDER BY id DESC LIMIT 1`, deviceID)
	in, err := scanIncident(row)
	if errors.Is(err, sql.ErrNoRows) {
		return in, false, nil
	}
	return in, err == nil, err
}

// maxIncidentCodes bounds codes_json for a kiosk inventing event names.
const maxIncidentCodes = 20

// AttachToIncident counts one occurrence of `event` (an event name, or
// model.StatusDownEvent) against the device's open incident, opening one
// when there is none. It reports whether a new incident started.
func (s *Store) AttachToIncident(deviceID, location, gate, event, severity string, at time.Time) (model.Incident, bool, error) {
	ts := at.UTC().Format(time.RFC3339)
	cur, open, err := s.OpenIncident(deviceID)
	if err != nil {
		return cur, false, err
	}
	if !open {
		codes, _ := json.Marshal(map[string]int{event: 1})
		res, err := s.db.Exec(`INSERT INTO incidents (device_id, location, gate, started_at, last_at, count, severity, codes_json)
			VALUES (?, ?, ?, ?, ?, 1, ?, ?)`, deviceID, location, gate, ts, ts, severity, string(codes))
		if err != nil {
			return cur, false, fmt.Errorf("open incident: %w", err)
		}
		id, _ := res.LastInsertId()
		return model.Incident{ID: id, DeviceID: deviceID, Location: location, Gate: gate, StartedAt: ts, LastAt: ts,
			Count: 1, Severity: severity, Codes: map[string]int{event: 1}, Alerts: map[string]model.Alert{}}, true, nil
	}
	if _, known := cur.Codes[event]; known || len(cur.Codes) < maxIncidentCodes {
		cur.Codes[event]++
	} else {
		cur.Codes["(lainnya)"]++
	}
	if status.SeverityRank(severity) > status.SeverityRank(cur.Severity) {
		cur.Severity = severity
	}
	cur.Count++
	cur.LastAt = ts
	codes, _ := json.Marshal(cur.Codes)
	_, err = s.db.Exec(`UPDATE incidents SET last_at = ?, count = ?, severity = ?, codes_json = ? WHERE id = ?`,
		ts, cur.Count, cur.Severity, string(codes), cur.ID)
	if err != nil {
		return cur, false, fmt.Errorf("update incident: %w", err)
	}
	return cur, false, nil
}

// ResolveQuietIncidents closes incidents whose last activity is older than
// gap, except for devices in `stillDown` (a dead gate is still an incident).
func (s *Store) ResolveQuietIncidents(now time.Time, gap time.Duration, stillDown map[string]bool) ([]model.Incident, error) {
	rows, err := s.db.Query(`SELECT `+incidentCols+` FROM incidents WHERE resolved_at IS NULL AND last_at < ?`,
		now.Add(-gap).UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	var out []model.Incident
	for rows.Next() {
		in, err := scanIncident(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		if !stillDown[in.DeviceID] {
			out = append(out, in)
		}
	}
	rows.Close()
	ts := now.UTC().Format(time.RFC3339)
	for i := range out {
		if _, err := s.db.Exec(`UPDATE incidents SET resolved_at = ? WHERE id = ? AND resolved_at IS NULL`, ts, out[i].ID); err != nil {
			return nil, err
		}
		out[i].ResolvedAt = ts
	}
	return out, nil
}

// OpenIncidents lists every unresolved incident, oldest first.
func (s *Store) OpenIncidents() ([]model.Incident, error) {
	return s.queryIncidents(`SELECT ` + incidentCols + ` FROM incidents WHERE resolved_at IS NULL ORDER BY id`)
}

// IncidentsForDevice lists a device's incidents, newest first.
func (s *Store) IncidentsForDevice(deviceID string, limit int) ([]model.Incident, error) {
	return s.queryIncidents(`SELECT `+incidentCols+` FROM incidents WHERE device_id = ? ORDER BY id DESC LIMIT ?`, deviceID, limit)
}

// IncidentsToNotify lists incidents the alerter still has work on: open
// ones, and resolved ones whose final message edit is outstanding.
func (s *Store) IncidentsToNotify(limit int) ([]model.Incident, error) {
	return s.queryIncidents(`SELECT `+incidentCols+` FROM incidents WHERE done = 0 ORDER BY id LIMIT ?`, limit)
}

// SaveIncidentAlerts persists the per-route Telegram bookkeeping, and marks
// the incident done when nothing more will be sent for it.
func (s *Store) SaveIncidentAlerts(id int64, alerts map[string]model.Alert, done bool) error {
	b, _ := json.Marshal(alerts)
	d := 0
	if done {
		d = 1
	}
	_, err := s.db.Exec(`UPDATE incidents SET alert_json = ?, done = ? WHERE id = ?`, string(b), d, id)
	return err
}

// PurgeIncidentsBefore deletes resolved incidents older than cutoff.
func (s *Store) PurgeIncidentsBefore(cutoff time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM incidents WHERE resolved_at IS NOT NULL AND resolved_at < ?`, cutoff.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) queryIncidents(q string, args ...any) ([]model.Incident, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Incident{}
	for rows.Next() {
		in, err := scanIncident(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}
