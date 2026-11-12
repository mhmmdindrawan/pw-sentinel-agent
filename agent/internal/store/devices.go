package store

import (
	"database/sql"
	"fmt"

	"parkways-sentinel/agent/internal/devices"
)

// EnrolledDevices returns every self-registered kiosk with its token hash.
func (s *Store) EnrolledDevices() ([]devices.Entry, []string, error) {
	rows, err := s.db.Query(`SELECT device_id, location, gate, pos, token_hash FROM devices`)
	if err != nil {
		return nil, nil, fmt.Errorf("query devices: %w", err)
	}
	defer rows.Close()
	var out []devices.Entry
	var hashes []string
	for rows.Next() {
		var e devices.Entry
		var pos sql.NullString
		var hash string
		if err := rows.Scan(&e.DeviceID, &e.Location, &e.Gate, &pos, &hash); err != nil {
			return nil, nil, fmt.Errorf("scan devices: %w", err)
		}
		e.POS, e.Enrolled = pos.String, true
		out = append(out, e)
		hashes = append(hashes, hash)
	}
	return out, hashes, rows.Err()
}

// SaveEnrolled inserts or replaces an enrolled device (re-enrollment
// rotates the token).
func (s *Store) SaveEnrolled(e devices.Entry, tokenHash, now string) error {
	_, err := s.db.Exec(`
		INSERT INTO devices (device_id, location, gate, pos, token_hash, enrolled_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(device_id) DO UPDATE SET
			location = excluded.location, gate = excluded.gate, pos = excluded.pos,
			token_hash = excluded.token_hash, updated_at = excluded.updated_at`,
		e.DeviceID, e.Location, e.Gate, nullable(e.POS), tokenHash, now, now)
	if err != nil {
		return fmt.Errorf("save enrolled device: %w", err)
	}
	return nil
}

// UpdateEnrolledIdentity changes gate/pos the kiosk now reports for itself.
func (s *Store) UpdateEnrolledIdentity(deviceID, gate, pos, now string) error {
	_, err := s.db.Exec(`UPDATE devices SET gate = ?, pos = ?, updated_at = ? WHERE device_id = ?`,
		gate, nullable(pos), now, deviceID)
	if err != nil {
		return fmt.Errorf("update enrolled device: %w", err)
	}
	return nil
}

// DeleteEnrolled revokes a self-registered kiosk.
func (s *Store) DeleteEnrolled(deviceID string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM devices WHERE device_id = ?`, deviceID)
	if err != nil {
		return false, fmt.Errorf("delete enrolled device: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
