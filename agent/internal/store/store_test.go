package store

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"parkways-sentinel/agent/internal/devices"
	"parkways-sentinel/agent/internal/model"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "sentinel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// The fleet queries run on every dashboard refresh and alerter tick. A full
// scan of events is fine in a test database but takes seconds after 90 days
// of a busy fleet (measured: 7s per /v1/fleet at 2.7M events), so check the
// plans rather than the timing.
func TestQueryPlansStayIndexed(t *testing.T) {
	s := openTemp(t)
	since := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	summaryAll, summaryAllArgs := summarySQL(time.Now(), model.EventFilter{Location: "STI"})
	summaryDev, summaryDevArgs := summarySQL(time.Now(), model.EventFilter{DeviceID: "STI-G01-POS01"})

	cases := []struct {
		name string
		sql  string
		args []any
	}{
		{"idempotency lookup", findByEventIDSQL, []any{"6f1c…"}},
		{"device stats", deviceStatsSQL, []any{since, since, since}},
		{"last errors", lastErrorsSQL, []any{`["STI-G01-POS01","STI-G02-POS01"]`}},
		{"hourly errors", hourlyErrorsSQL, []any{"STI-G01-POS01", since}},
		{"count unmapped", countUnmappedSQL, []any{since}},
		{"summary all", summaryAll, summaryAllArgs},
		{"summary device", summaryDev, summaryDevArgs},
	}
	fullScan := regexp.MustCompile(`^SCAN (events|e)\b`)
	for _, c := range cases {
		rows, err := s.db.Query("EXPLAIN QUERY PLAN "+c.sql, c.args...)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		for _, line := range plan {
			if fullScan.MatchString(line) {
				t.Errorf("%s scans the whole events table:\n  %s", c.name, strings.Join(plan, "\n  "))
				break
			}
		}
	}
}

func TestDeviceErrorStatsLatestErrorPerDevice(t *testing.T) {
	s := openTemp(t)
	now := time.Now().UTC()
	add := func(dev, loc, event, sev string, ago time.Duration) {
		t.Helper()
		ts := now.Add(-ago).Format(time.RFC3339)
		if _, err := s.InsertEvent(model.StoredEvent{
			DeviceID: dev, EventID: dev + event + ts + loc, Location: loc, Gate: "G01", Category: "APP",
			ErrorCode: loc + "-G01-APP-001", Event: event, Severity: sev, ReceivedAt: ts,
		}); err != nil {
			t.Fatal(err)
		}
	}
	add("A", "STI", "OLD_ERROR", "HIGH", 48*time.Hour)
	add("A", "STI", "RECENT_ERROR", "MEDIUM", 30*time.Minute)
	add("A", "STI", "APP_STARTED", "INFO", 5*time.Minute)    // INFO never counts
	add("A", "UNMAPPED", "SPOOFED", "CRITICAL", time.Minute) // claimed A, wrong token
	add("B", "STI", "ONLY_OLD", "LOW", 72*time.Hour)         // outside 24h, still "last"
	add("C", "STI", "NOT_CONFIGURED", "HIGH", time.Minute)   // not asked for

	stats, err := s.DeviceErrorStats(now, 10*time.Minute, []string{"A", "B", "D"})
	if err != nil {
		t.Fatal(err)
	}
	a := stats["A"]
	if a.Errors1h != 1 || a.Errors24h != 1 || a.WorstRecent != "" || a.LastError == nil || a.LastError.Event != "RECENT_ERROR" {
		t.Errorf("A = %+v (last %+v), want 1/1, no recent, last RECENT_ERROR", a, a.LastError)
	}
	if b := stats["B"]; b.Errors24h != 0 || b.LastError == nil || b.LastError.Event != "ONLY_OLD" {
		t.Errorf("B = %+v, want no 24h errors but last ONLY_OLD", b)
	}
	if c := stats["C"]; c.LastError != nil {
		t.Errorf("C was not asked for but got last error %+v", c.LastError)
	}
	if d, ok := stats["D"]; ok {
		t.Errorf("D has no events but got %+v", d)
	}
}

func TestPurgeEventsBeforeInBatches(t *testing.T) {
	s := openTemp(t)
	defer func(n int) { purgeBatch = n }(purgeBatch)
	purgeBatch = 3

	now := time.Now().UTC()
	for i := 0; i < 12; i++ {
		ago := 48 * time.Hour // 10 old events…
		if i >= 10 {
			ago = time.Hour // …and 2 recent ones
		}
		ts := now.Add(-ago).Format(time.RFC3339)
		if _, err := s.InsertEvent(model.StoredEvent{DeviceID: "A", EventID: fmt.Sprint(i), Location: "STI", Gate: "G01",
			Category: "APP", ErrorCode: "STI-G01-APP-001", Event: "X", Severity: "LOW", ReceivedAt: ts}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.LogAlert("k", "noc", "old", now.Add(-48*time.Hour), nil); err != nil {
		t.Fatal(err)
	}
	if err := s.LogAlert("k", "noc", "new", now, nil); err != nil {
		t.Fatal(err)
	}

	n, err := s.PurgeEventsBefore(now.Add(-24 * time.Hour))
	if err != nil || n != 10 {
		t.Fatalf("purged %d (err %v), want 10", n, err)
	}
	left, err := s.QueryEvents(model.EventFilter{})
	if err != nil || len(left) != 2 {
		t.Fatalf("left %d events (err %v), want 2", len(left), err)
	}
	if at, ok, err := s.LastAlertSent("k", "noc"); err != nil || !ok || now.Sub(at) > time.Minute {
		t.Fatalf("recent alert log row gone: %v %v %v", at, ok, err)
	}
	var alerts int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM alert_log`).Scan(&alerts); err != nil || alerts != 1 {
		t.Fatalf("alert_log rows = %d (err %v), want 1", alerts, err)
	}
}

func TestTrimToSizeDropsOldestAndBackupCopies(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	big := strings.Repeat("x", 2000)
	for i := 0; i < 600; i++ {
		if _, err := s.InsertEvent(model.StoredEvent{DeviceID: "d", EventID: fmt.Sprintf("e%d", i), Location: "L", Gate: "G",
			Category: "APP", ErrorCode: "L-G-APP-001", Event: "X", Severity: "LOW", Message: big, ReceivedAt: "2026-01-01T00:00:00Z"}); err != nil {
			t.Fatal(err)
		}
	}
	size, free, err := s.Usage()
	if err != nil || size < 1_000_000 || free <= 0 {
		t.Fatalf("usage: %d %d %v", size, free, err)
	}
	n, err := s.TrimToSize(400_000)
	if err != nil || n == 0 {
		t.Fatalf("trim: %d %v", n, err)
	}
	if size, _, _ = s.Usage(); size > 400_000 {
		t.Fatalf("still %d bytes", size)
	}
	// The newest rows survive.
	evs, _ := s.RecentEvents(1)
	if len(evs) != 1 || evs[0].EventID != "e599" {
		t.Fatalf("newest: %+v", evs)
	}
	dst := filepath.Join(dir, "backup.db")
	if err := s.Backup(dst); err != nil {
		t.Fatal(err)
	}
	b, err := Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	got, _ := b.RecentEvents(1)
	if len(got) != 1 || got[0].EventID != "e599" {
		t.Fatalf("backup content: %+v", got)
	}
	if err := s.Backup(dst); err == nil {
		t.Fatal("backup must refuse to overwrite")
	}
}

// The daily reset clears the day's monitoring data but never what keeps
// kiosks working: their enrollment and current state.
func TestResetBeforeKeepsRegistrations(t *testing.T) {
	s := openTemp(t)
	cutoff := time.Date(2026, 11, 13, 19, 0, 0, 0, time.UTC) // 02:00 WIB
	before, after := cutoff.Add(-time.Hour), cutoff.Add(time.Minute)
	for i, at := range []time.Time{before, before, after} {
		if _, err := s.InsertEvent(model.StoredEvent{DeviceID: "PCC-G01", EventID: fmt.Sprintf("r-%d", i), Location: "PCC", Gate: "G01",
			Category: "APP", ErrorCode: "PCC-G01-APP-001", Event: "X", Severity: "HIGH", ReceivedAt: at.Format(time.RFC3339)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.AttachToIncident("PCC-G01", "PCC", "G01", "X", "HIGH", before); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordStatus("PCC-G01", "DOWN", before.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveEnrolled(devices.Entry{DeviceID: "PCC-G01", Location: "PCC", Gate: "G01"}, "hash", before.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertHeartbeat(model.DeviceState{DeviceID: "PCC-G01", LastHeartbeatAt: before.Format(time.RFC3339), Name: "Gate Utara"}); err != nil {
		t.Fatal(err)
	}

	n, err := s.ResetBefore(cutoff)
	if err != nil || n != 2 {
		t.Fatalf("deleted %d, err %v; want 2", n, err)
	}
	left, _ := s.QueryEvents(model.EventFilter{Limit: 10})
	if len(left) != 1 { // checked before the second reset below
		t.Fatalf("%d events left, want the one after cutoff", len(left))
	}
	if inc, _ := s.IncidentsForDevice("PCC-G01", 10); len(inc) != 0 {
		t.Fatalf("incidents kept: %+v", inc)
	}
	// Only the gate's current status survives, so "Mati sejak" stays right.
	if err := s.RecordStatus("PCC-G01", "ONLINE", after.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResetBefore(cutoff.Add(2 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.StatusHistory("PCC-G01", 10); len(h) != 1 || h[0].Status != "ONLINE" {
		t.Fatalf("status history after reset: %+v", h)
	}
	if enr, _, _ := s.EnrolledDevices(); len(enr) != 1 {
		t.Fatal("enrollment was wiped: kiosk would have to re-enroll")
	}
	if st, _ := s.DeviceStates(); st["PCC-G01"].Name != "Gate Utara" {
		t.Fatal("device state was wiped")
	}
}
