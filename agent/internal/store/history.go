package store

import (
	"fmt"

	"parkways-sentinel/agent/internal/model"
)

// RecordStatus appends a liveness change for a device.
func (s *Store) RecordStatus(deviceID, status, at string) error {
	_, err := s.db.Exec(`INSERT INTO status_history (device_id, status, since) VALUES (?, ?, ?)`,
		deviceID, status, at)
	if err != nil {
		return fmt.Errorf("record status: %w", err)
	}
	return nil
}

// LatestStatuses returns the most recent recorded change per device.
func (s *Store) LatestStatuses() (map[string]model.StatusChange, error) {
	rows, err := s.db.Query(`
		SELECT device_id, status, since FROM status_history
		WHERE id IN (SELECT MAX(id) FROM status_history GROUP BY device_id)`)
	if err != nil {
		return nil, fmt.Errorf("query latest statuses: %w", err)
	}
	defer rows.Close()
	out := map[string]model.StatusChange{}
	for rows.Next() {
		var id string
		var c model.StatusChange
		if err := rows.Scan(&id, &c.Status, &c.Since); err != nil {
			return nil, fmt.Errorf("scan status_history: %w", err)
		}
		out[id] = c
	}
	return out, rows.Err()
}

// StatusHistory returns a device's last `limit` changes, newest first.
func (s *Store) StatusHistory(deviceID string, limit int) ([]model.StatusChange, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := s.db.Query(`
		SELECT status, since FROM status_history WHERE device_id = ? ORDER BY id DESC LIMIT ?`,
		deviceID, limit)
	if err != nil {
		return nil, fmt.Errorf("query status_history: %w", err)
	}
	defer rows.Close()
	out := []model.StatusChange{}
	for rows.Next() {
		var c model.StatusChange
		if err := rows.Scan(&c.Status, &c.Since); err != nil {
			return nil, fmt.Errorf("scan status_history: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PurgeStatusHistoryBefore drops changes older than cutoff, keeping the
// latest row per device so "since" never disappears for a long-stable gate.
func (s *Store) PurgeStatusHistoryBefore(cutoff string) (int64, error) {
	res, err := s.db.Exec(`
		DELETE FROM status_history WHERE since < ?
		  AND id NOT IN (SELECT MAX(id) FROM status_history GROUP BY device_id)`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("purge status_history: %w", err)
	}
	return res.RowsAffected()
}
