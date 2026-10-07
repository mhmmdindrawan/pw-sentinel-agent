// Package store persists normalized events to a local SQLite database.
// This database belongs solely to the Sentinel Agent — it is never shared
// with, or read from, the gate-out application's own transaction database.
package store

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"parkways-sentinel/agent/internal/model"
	"parkways-sentinel/agent/internal/registry"
)

//go:embed schema.sql
var schema string

type Store struct {
	db *sql.DB
}

func Open(dbPath string) (*Store, error) {
	if dir := filepath.Dir(dbPath); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db directory: %w", err)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	// SQLite handles one writer at a time; keep the pool small so concurrent
	// HTTP requests queue instead of hitting "database is locked".
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}

	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) InsertEvent(ev model.StoredEvent) (int64, error) {
	var contextJSON *string
	if len(ev.Context) > 0 {
		b, err := json.Marshal(ev.Context)
		if err != nil {
			return 0, fmt.Errorf("marshal context: %w", err)
		}
		s := string(b)
		contextJSON = &s
	}

	res, err := s.db.Exec(`
		INSERT INTO events (
			device_id, event_id, occurred_at,
			location, gate, pos, device, category, error_code, event_name,
			severity, message, transaction_ref, retry_count, endpoint,
			http_status, duration_ms, context_json, app_version, received_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.DeviceID, nullable(ev.EventID), nullable(ev.OccurredAt),
		ev.Location, ev.Gate, nullable(ev.POS), nullable(ev.Device), ev.Category,
		ev.ErrorCode, ev.Event, ev.Severity, nullable(ev.Message), nullable(ev.TxRef),
		ev.RetryCount, nullable(ev.Endpoint), ev.HTTPStatus, ev.DurationMS,
		contextJSON, nullable(ev.AppVersion), ev.ReceivedAt,
	)
	if err != nil {
		return 0, fmt.Errorf("insert event: %w", err)
	}
	return res.LastInsertId()
}

const eventColumns = `
	id, device_id, event_id, occurred_at, location, gate, pos, device, category,
	error_code, event_name, severity, message, transaction_ref, retry_count,
	endpoint, http_status, duration_ms, context_json, app_version, received_at`

// scanEvent scans one row shaped like eventColumns into a StoredEvent.
func scanEvent(row interface{ Scan(...any) error }) (model.StoredEvent, error) {
	var ev model.StoredEvent
	var eventID, occurredAt, pos, device, message, txRef, endpoint, appVersion, contextJSON sql.NullString
	var retryCount, httpStatus, durationMS sql.NullInt64

	if err := row.Scan(&ev.ID, &ev.DeviceID, &eventID, &occurredAt, &ev.Location, &ev.Gate, &pos, &device,
		&ev.Category, &ev.ErrorCode, &ev.Event, &ev.Severity, &message, &txRef, &retryCount,
		&endpoint, &httpStatus, &durationMS, &contextJSON, &appVersion, &ev.ReceivedAt); err != nil {
		return ev, fmt.Errorf("scan event: %w", err)
	}

	ev.EventID = eventID.String
	ev.OccurredAt = occurredAt.String
	ev.POS = pos.String
	ev.Device = device.String
	ev.Message = message.String
	ev.TxRef = txRef.String
	ev.Endpoint = endpoint.String
	ev.AppVersion = appVersion.String
	if retryCount.Valid {
		v := int(retryCount.Int64)
		ev.RetryCount = &v
	}
	if httpStatus.Valid {
		v := int(httpStatus.Int64)
		ev.HTTPStatus = &v
	}
	if durationMS.Valid {
		v := int(durationMS.Int64)
		ev.DurationMS = &v
	}
	if contextJSON.Valid {
		var ctx map[string]any
		if err := json.Unmarshal([]byte(contextJSON.String), &ctx); err == nil {
			ev.Context = ctx
		}
	}

	return ev, nil
}

func (s *Store) RecentEvents(limit int) ([]model.StoredEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT `+eventColumns+` FROM events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent events: %w", err)
	}
	defer rows.Close()

	var out []model.StoredEvent
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// FindByEventID looks up an already-stored event by its client-supplied
// event_id, used to make POST /v1/events idempotent under at-least-once
// delivery. Returns ok=false if no such event_id has been seen (or eventID
// is empty, which never matches since blank ids aren't deduplicated).
func (s *Store) FindByEventID(eventID string) (model.StoredEvent, bool, error) {
	if eventID == "" {
		return model.StoredEvent{}, false, nil
	}
	row := s.db.QueryRow(`SELECT `+eventColumns+` FROM events WHERE event_id = ? LIMIT 1`, eventID)
	ev, err := scanEvent(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.StoredEvent{}, false, nil
		}
		return model.StoredEvent{}, false, err
	}
	return ev, true, nil
}

// Summary aggregates event counts per error_code over the last `since`
// duration (e.g. 30 minutes) — the shape an AI analyzer or dashboard would
// consume, rather than a raw log dump.
func (s *Store) Summary(since time.Duration) ([]model.SummaryRow, error) {
	cutoff := time.Now().UTC().Add(-since).Format(time.RFC3339)

	rows, err := s.db.Query(`
		SELECT error_code, event_name, severity, COUNT(*) as cnt,
		       MIN(received_at) as first_seen, MAX(received_at) as last_seen
		FROM events
		WHERE received_at >= ?
		GROUP BY error_code
		ORDER BY cnt DESC`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("query summary: %w", err)
	}
	defer rows.Close()

	var out []model.SummaryRow
	for rows.Next() {
		var r model.SummaryRow
		if err := rows.Scan(&r.ErrorCode, &r.Event, &r.Severity, &r.Count, &r.FirstSeen, &r.LastSeen); err != nil {
			return nil, fmt.Errorf("scan summary row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) LoadCustomRegistry() ([]registry.Entry, error) {
	rows, err := s.db.Query(`SELECT event_name, category, code, number, severity FROM event_registry`)
	if err != nil {
		return nil, fmt.Errorf("query event_registry: %w", err)
	}
	defer rows.Close()

	var out []registry.Entry
	for rows.Next() {
		var e registry.Entry
		if err := rows.Scan(&e.EventName, &e.Category, &e.Code, &e.Number, &e.Severity); err != nil {
			return nil, fmt.Errorf("scan event_registry row: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) SaveCustomRegistryEntry(e registry.Entry) error {
	_, err := s.db.Exec(`
		INSERT INTO event_registry (event_name, category, code, number, severity)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(event_name) DO NOTHING`,
		e.EventName, e.Category, e.Code, e.Number, e.Severity)
	if err != nil {
		return fmt.Errorf("save event_registry entry: %w", err)
	}
	return nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
