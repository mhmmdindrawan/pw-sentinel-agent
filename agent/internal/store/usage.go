package store

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Usage reports the database file size (main file + WAL) and the free
// space on its volume, so the dashboard can warn before a site server's
// disk fills up.
func (s *Store) Usage() (sizeBytes, freeBytes int64, err error) {
	for _, p := range []string{s.path, s.path + "-wal"} {
		if fi, statErr := os.Stat(p); statErr == nil {
			sizeBytes += fi.Size()
		}
	}
	freeBytes, err = diskFree(filepath.Dir(s.path))
	return sizeBytes, freeBytes, err
}

// TrimToSize deletes the oldest events, in batches, until the main database
// file is below maxBytes (after a checkpoint and vacuum-free shrink attempt
// via incremental deletes, the file only shrinks on VACUUM; counting rows
// deleted is what matters for not growing further). It returns how many
// events were removed.
func (s *Store) TrimToSize(maxBytes int64) (int64, error) {
	size, _, err := s.Usage()
	if err != nil || size <= maxBytes {
		return 0, err
	}
	var total int64
	for size > maxBytes {
		var n int64
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n); err != nil || n == 0 {
			return total, err
		}
		// Drop the oldest 10% of rows per round so one call can't hold the
		// write lock for long.
		res, err := s.db.Exec(`DELETE FROM events WHERE id IN (SELECT id FROM events ORDER BY id LIMIT ?)`, (n+9)/10)
		if err != nil {
			return total, fmt.Errorf("trim events: %w", err)
		}
		k, _ := res.RowsAffected()
		total += k
		if _, err := s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			return total, err
		}
		if _, err := s.db.Exec(`VACUUM`); err != nil {
			return total, fmt.Errorf("vacuum: %w", err)
		}
		if size, _, err = s.Usage(); err != nil {
			return total, err
		}
	}
	return total, nil
}

// Backup writes a consistent copy of the database to dst while the agent
// keeps running (SQLite's VACUUM INTO). dst must not exist.
func (s *Store) Backup(dst string) error {
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("%s already exists", dst)
	}
	if _, err := s.db.Exec(`VACUUM INTO ?`, dst); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	return nil
}

// ResetBefore is the daily wipe: everything the dashboard shows about the
// day (events, incidents, status history, alert log) from before cutoff is
// deleted and the file compacted; each gate keeps its current status row. Kiosk registrations, current device
// state, users and learned error codes are kept, so kiosks keep reporting
// without re-enrolling.
func (s *Store) ResetBefore(cutoff time.Time) (events int64, err error) {
	events, err = s.PurgeEventsBefore(cutoff)
	if err != nil {
		return events, err
	}
	c := cutoff.UTC().Format(time.RFC3339)
	if _, err := s.PurgeStatusHistoryBefore(c); err != nil {
		return events, err
	}
	if _, err := s.db.Exec(`DELETE FROM incidents WHERE last_at < ?`, c); err != nil {
		return events, fmt.Errorf("reset incidents: %w", err)
	}
	if _, err := s.db.Exec(`VACUUM`); err != nil {
		return events, fmt.Errorf("vacuum: %w", err)
	}
	return events, nil
}
