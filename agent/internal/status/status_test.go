package status

import (
	"testing"
	"time"
)

func TestDerive(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	th := Thresholds{Online: 3 * time.Minute, Stale: 10 * time.Minute}
	cases := []struct {
		name     string
		lastSeen time.Time
		want     Liveness
	}{
		{"never", time.Time{}, Never},
		{"just now", now, Online},
		{"at online edge", now.Add(-3 * time.Minute), Online},
		{"just past online", now.Add(-3*time.Minute - time.Second), Stale},
		{"at stale edge", now.Add(-10 * time.Minute), Stale},
		{"past stale", now.Add(-11 * time.Minute), Down},
		{"clock skew future", now.Add(time.Minute), Online},
	}
	for _, c := range cases {
		if got := Derive(now, c.lastSeen, th); got != c.want {
			t.Errorf("%s: Derive = %s, want %s", c.name, got, c.want)
		}
	}
}

func TestGateColor(t *testing.T) {
	cases := []struct {
		l       Liveness
		sev     string
		pending int
		want    Color
	}{
		{Online, "", 0, Green},
		{Online, "INFO", 0, Green},
		{Online, "LOW", 0, Green},
		{Online, "MEDIUM", 0, Yellow},
		{Online, "high", 0, Red},
		{Online, "CRITICAL", 0, Red},
		{Online, "", 5, Yellow},
		{Stale, "", 0, Yellow},
		{Stale, "HIGH", 0, Red},
		{Down, "", 0, Red},
		{Never, "", 0, Red},
	}
	for _, c := range cases {
		if got := GateColor(c.l, c.sev, c.pending); got != c.want {
			t.Errorf("GateColor(%s, %q, %d) = %s, want %s", c.l, c.sev, c.pending, got, c.want)
		}
	}
}
