package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"parkways-sentinel/agent/internal/model"
)

// TouchEventSeen records that a device just sent an event, so a kiosk that
// reports errors counts as alive even between heartbeats.
func (s *Store) TouchEventSeen(deviceID, at, appVersion string) error {
	_, err := s.db.Exec(`
		INSERT INTO device_state (device_id, last_event_at, app_version)
		VALUES (?, ?, ?)
		ON CONFLICT(device_id) DO UPDATE SET
			last_event_at = excluded.last_event_at,
			app_version   = COALESCE(excluded.app_version, device_state.app_version)`,
		deviceID, at, nullable(appVersion))
	if err != nil {
		return fmt.Errorf("touch device_state: %w", err)
	}
	return nil
}

// UpsertHeartbeat stores the latest heartbeat for a device.
func (s *Store) UpsertHeartbeat(st model.DeviceState) error {
	_, err := s.db.Exec(`
		INSERT INTO device_state (device_id, last_heartbeat_at, app_version, sdk_version,
		                          uptime_s, outbox_pending, outbox_dropped, remote_addr)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(device_id) DO UPDATE SET
			last_heartbeat_at = excluded.last_heartbeat_at,
			app_version       = COALESCE(excluded.app_version, device_state.app_version),
			sdk_version       = COALESCE(excluded.sdk_version, device_state.sdk_version),
			uptime_s          = excluded.uptime_s,
			outbox_pending    = excluded.outbox_pending,
			outbox_dropped    = excluded.outbox_dropped,
			remote_addr       = excluded.remote_addr`,
		st.DeviceID, st.LastHeartbeatAt, nullable(st.AppVersion), nullable(st.SDKVersion),
		st.UptimeS, st.OutboxPending, st.OutboxDropped, nullable(st.RemoteAddr))
	if err != nil {
		return fmt.Errorf("upsert heartbeat: %w", err)
	}
	return nil
}

// DeviceStates returns every known device state keyed by device_id.
func (s *Store) DeviceStates() (map[string]model.DeviceState, error) {
	rows, err := s.db.Query(`
		SELECT device_id, last_heartbeat_at, last_event_at, app_version, sdk_version,
		       uptime_s, outbox_pending, outbox_dropped, remote_addr
		FROM device_state`)
	if err != nil {
		return nil, fmt.Errorf("query device_state: %w", err)
	}
	defer rows.Close()

	out := map[string]model.DeviceState{}
	for rows.Next() {
		var st model.DeviceState
		var hb, ev, appV, sdkV, addr sql.NullString
		if err := rows.Scan(&st.DeviceID, &hb, &ev, &appV, &sdkV,
			&st.UptimeS, &st.OutboxPending, &st.OutboxDropped, &addr); err != nil {
			return nil, fmt.Errorf("scan device_state: %w", err)
		}
		st.LastHeartbeatAt, st.LastEventAt = hb.String, ev.String
		st.AppVersion, st.SDKVersion, st.RemoteAddr = appV.String, sdkV.String, addr.String
		out[st.DeviceID] = st
	}
	return out, rows.Err()
}

// The fleet view runs these on every dashboard refresh and alerter tick, so
// they must stay index-bound as events pile up (millions of rows after 90
// days on a busy fleet). Left alone, the planner walked a whole index for
// the GROUP BY and took seconds; see TestQueryPlansStayIndexed.
const (
	// Counts read only the last day of rows.
	deviceStatsSQL = `
		SELECT device_id,
		       SUM(received_at >= ?),
		       COUNT(*),
		       MAX(CASE WHEN received_at >= ? THEN ` + severityRankSQL + ` ELSE 0 END)
		FROM events INDEXED BY idx_events_received_at
		WHERE received_at >= ? AND severity != 'INFO' AND location != 'UNMAPPED'
		GROUP BY device_id`
	// One newest-first index probe per device (ids as a JSON array).
	lastErrorsSQL = `SELECT ` + eventColumns + ` FROM events WHERE id IN (
		SELECT (SELECT e.id FROM events e
		        WHERE e.device_id = j.value AND e.severity != 'INFO' AND e.location != 'UNMAPPED'
		        ORDER BY e.id DESC LIMIT 1)
		FROM json_each(?) AS j)`
	hourlyErrorsSQL = `
		SELECT substr(received_at, 1, 13) AS hour, COUNT(*)
		FROM events
		WHERE device_id = ? AND received_at >= ? AND severity != 'INFO' AND location != 'UNMAPPED'
		GROUP BY hour`
	countUnmappedSQL = `SELECT COUNT(*) FROM events WHERE location = 'UNMAPPED' AND received_at >= ?`
)

// DeviceErrorStats summarises non-INFO events for the given devices: counts
// over the last hour and day, the worst severity within `window`, and the
// latest error. UNMAPPED events are excluded — they don't belong to the
// device whose id they claim.
func (s *Store) DeviceErrorStats(now time.Time, window time.Duration, deviceIDs []string) (map[string]model.DeviceErrorStats, error) {
	h1 := now.Add(-time.Hour).UTC().Format(time.RFC3339)
	h24 := now.Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	win := now.Add(-window).UTC().Format(time.RFC3339)

	rows, err := s.db.Query(deviceStatsSQL, h1, win, h24)
	if err != nil {
		return nil, fmt.Errorf("query device error stats: %w", err)
	}
	defer rows.Close()

	out := map[string]model.DeviceErrorStats{}
	for rows.Next() {
		var id string
		var st model.DeviceErrorStats
		var worst int
		if err := rows.Scan(&id, &st.Errors1h, &st.Errors24h, &worst); err != nil {
			return nil, fmt.Errorf("scan device error stats: %w", err)
		}
		st.WorstRecent = severityName(worst)
		out[id] = st
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Latest error per device, regardless of age, so the fleet view can
	// show "last problem was X, 3 days ago".
	ids, err := json.Marshal(deviceIDs)
	if err != nil {
		return nil, err
	}
	last, err := s.db.Query(lastErrorsSQL, string(ids))
	if err != nil {
		return nil, fmt.Errorf("query last errors: %w", err)
	}
	defer last.Close()
	for last.Next() {
		ev, err := scanEvent(last)
		if err != nil {
			return nil, err
		}
		st := out[ev.DeviceID]
		st.LastError = &ev
		out[ev.DeviceID] = st
	}
	return out, last.Err()
}

// HourlyErrors returns non-INFO event counts for one device for each of the
// 24 hours ending at now, oldest first, keyed "2026-10-07T03" (UTC).
func (s *Store) HourlyErrors(deviceID string, now time.Time) ([]HourCount, error) {
	now = now.UTC().Truncate(time.Hour)
	start := now.Add(-23 * time.Hour)
	rows, err := s.db.Query(hourlyErrorsSQL, deviceID, start.Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("query hourly errors: %w", err)
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var h string
		var n int
		if err := rows.Scan(&h, &n); err != nil {
			return nil, err
		}
		counts[h] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]HourCount, 0, 24)
	for t := start; !t.After(now); t = t.Add(time.Hour) {
		h := t.Format("2006-01-02T15")
		out = append(out, HourCount{Hour: h, Count: counts[h]})
	}
	return out, nil
}

type HourCount struct {
	Hour  string `json:"hour"`
	Count int    `json:"count"`
}

// CountUnmapped returns how many UNMAPPED events arrived since `since` —
// a misconfigured kiosk the fleet view should point out.
func (s *Store) CountUnmapped(since time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(countUnmappedSQL, since.UTC().Format(time.RFC3339)).Scan(&n)
	return n, err
}

// purgeBatch bounds one DELETE, so a large purge (the first run after
// retention was lowered, or after a long outage) never holds the write lock
// long enough for ingest to give up waiting (busy_timeout). A var so tests
// can make it small.
var purgeBatch = 5000

// PurgeEventsBefore deletes events received before cutoff, plus alert log
// rows older than that, and returns how many events were removed.
func (s *Store) PurgeEventsBefore(cutoff time.Time) (int64, error) {
	c := cutoff.UTC().Format(time.RFC3339)
	var total int64
	for {
		res, err := s.db.Exec(`DELETE FROM events WHERE id IN (
			SELECT id FROM events WHERE received_at < ? ORDER BY received_at LIMIT ?)`, c, purgeBatch)
		if err != nil {
			return total, fmt.Errorf("purge events: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += n
		if n < int64(purgeBatch) {
			break
		}
	}
	if _, err := s.db.Exec(`DELETE FROM alert_log WHERE sent_at < ?`, c); err != nil {
		return total, fmt.Errorf("purge alert log: %w", err)
	}
	return total, nil
}

// GetKV returns the value for key, or "" if unset.
func (s *Store) GetKV(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM agent_kv WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetKV(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO agent_kv (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// IsUniqueViolation reports whether err came from a UNIQUE constraint —
// i.e. a concurrent request already stored the same event_id.
func IsUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func severityName(rank int) string {
	return [...]string{"", "INFO", "LOW", "MEDIUM", "HIGH", "CRITICAL"}[rank]
}

// MaxEventID returns the newest event id, or 0 for an empty table.
func (s *Store) MaxEventID() (int64, error) {
	var id sql.NullInt64
	err := s.db.QueryRow(`SELECT MAX(id) FROM events`).Scan(&id)
	return id.Int64, err
}

// LastAlertSent returns when an alert with this key last reached this
// route successfully.
func (s *Store) LastAlertSent(key, route string) (time.Time, bool, error) {
	var at string
	err := s.db.QueryRow(`SELECT sent_at FROM alert_log WHERE alert_key = ? AND route = ? AND ok = 1
		ORDER BY sent_at DESC LIMIT 1`, key, route).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	t, err := time.Parse(time.RFC3339, at)
	return t, err == nil, err
}

// LogAlert records one alert delivery attempt.
func (s *Store) LogAlert(key, route, message string, at time.Time, sendErr error) error {
	var errText any
	ok := 1
	if sendErr != nil {
		ok, errText = 0, sendErr.Error()
	}
	_, err := s.db.Exec(`INSERT INTO alert_log (alert_key, route, message, sent_at, ok, error) VALUES (?, ?, ?, ?, ?, ?)`,
		key, route, message, at.UTC().Format(time.RFC3339), ok, errText)
	return err
}
