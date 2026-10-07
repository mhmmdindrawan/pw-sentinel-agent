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
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"parkways-sentinel/agent/internal/model"
	"parkways-sentinel/agent/internal/registry"
	"parkways-sentinel/agent/internal/status"
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

	// WAL lets dashboard reads run alongside event ingest; busy_timeout
	// makes concurrent writers wait for the single write lock instead of
	// failing with "database is locked". The driver applies _pragma to
	// every pooled connection and strips the query before opening, so
	// plain Windows paths work too.
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	db.SetMaxOpenConns(4)

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
	return s.QueryEvents(model.EventFilter{Limit: limit})
}

// QueryEvents returns events matching f, newest first. Limit defaults to
// 100 and is capped at 1000; page further with f.BeforeID set to the
// smallest id of the previous page.
func (s *Store) QueryEvents(f model.EventFilter) ([]model.StoredEvent, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 100
	}
	where, args := eventWhere(f)
	q := `SELECT ` + eventColumns + ` FROM events` + where + ` ORDER BY id DESC LIMIT ?`
	rows, err := s.db.Query(q, append(args, f.Limit)...)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()

	out := []model.StoredEvent{}
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// eventWhere builds the WHERE clause shared by QueryEvents and Summary.
func eventWhere(f model.EventFilter) (string, []any) {
	var conds []string
	var args []any
	add := func(cond string, v any) {
		conds = append(conds, cond)
		args = append(args, v)
	}
	if f.DeviceID != "" {
		add("device_id = ?", f.DeviceID)
	}
	if f.Location != "" {
		add("location = ?", strings.ToUpper(f.Location))
	}
	if f.Gate != "" {
		add("gate = ?", strings.ToUpper(f.Gate))
	}
	if f.ErrorCode != "" {
		add("error_code = ?", strings.ToUpper(f.ErrorCode))
	}
	if f.Event != "" {
		add("event_name = ?", f.Event)
	}
	if f.Severity != "" {
		add(severityRankSQL+" >= ?", status.SeverityRank(f.Severity))
	}
	if f.Since != "" {
		add("received_at >= ?", f.Since)
	}
	if f.Until != "" {
		add("received_at < ?", f.Until)
	}
	if f.BeforeID > 0 {
		add("id < ?", f.BeforeID)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// severityRankSQL mirrors status.SeverityRank inside SQL.
const severityRankSQL = `(CASE severity WHEN 'INFO' THEN 1 WHEN 'LOW' THEN 2 WHEN 'MEDIUM' THEN 3 WHEN 'HIGH' THEN 4 WHEN 'CRITICAL' THEN 5 ELSE 0 END)`

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
//
// Only f.Location and f.DeviceID are honoured besides the time window.
func (s *Store) Summary(since time.Duration, f model.EventFilter) ([]model.SummaryRow, error) {
	where, args := eventWhere(model.EventFilter{
		DeviceID: f.DeviceID,
		Location: f.Location,
		Since:    time.Now().UTC().Add(-since).Format(time.RFC3339),
	})

	rows, err := s.db.Query(`
		SELECT error_code, event_name, severity, COUNT(*) as cnt,
		       MIN(received_at) as first_seen, MAX(received_at) as last_seen
		FROM events`+where+`
		GROUP BY error_code
		ORDER BY cnt DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("query summary: %w", err)
	}
	defer rows.Close()

	out := []model.SummaryRow{}
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
