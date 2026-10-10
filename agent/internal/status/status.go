// Package status derives a device's liveness (ONLINE/STALE/DOWN/NEVER) and
// its gate colour from when it was last heard from and its recent errors.
// It is pure logic — callers pass `now` in, so it is trivially testable.
package status

import (
	"strings"
	"time"
)

type Liveness string

const (
	Online Liveness = "ONLINE"
	Stale  Liveness = "STALE"
	Down   Liveness = "DOWN"
	Never  Liveness = "NEVER" // configured in devices.yaml but never heard from
)

type Color string

const (
	Green  Color = "green"
	Yellow Color = "yellow"
	Red    Color = "red"
)

type Thresholds struct {
	Online time.Duration // last seen within this → ONLINE
	Stale  time.Duration // last seen within this → STALE, otherwise DOWN
}

// Derive computes liveness from the last time a device sent anything
// (heartbeat or event). A zero lastSeen means it never has.
func Derive(now, lastSeen time.Time, t Thresholds) Liveness {
	if lastSeen.IsZero() {
		return Never
	}
	age := now.Sub(lastSeen)
	switch {
	case age <= t.Online:
		return Online
	case age <= t.Stale:
		return Stale
	default:
		return Down
	}
}

// SeverityRank orders severities so callers can compare them; unknown
// values rank as 0, below INFO.
func SeverityRank(sev string) int {
	switch strings.ToUpper(sev) {
	case "INFO":
		return 1
	case "LOW":
		return 2
	case "MEDIUM":
		return 3
	case "HIGH":
		return 4
	case "CRITICAL":
		return 5
	}
	return 0
}

// NormalizeSeverity maps a severity to the agent's scale (INFO, LOW,
// MEDIUM, HIGH, CRITICAL), also accepting common logging level names, so an
// app that sends Python's "WARNING"/"ERROR" still colours gates and fires
// alerts. ok is false for anything else.
func NormalizeSeverity(sev string) (string, bool) {
	s := strings.ToUpper(strings.TrimSpace(sev))
	switch s {
	case "INFO", "LOW", "MEDIUM", "HIGH", "CRITICAL":
		return s, true
	case "DEBUG", "TRACE":
		return "INFO", true
	case "NOTICE":
		return "LOW", true
	case "WARN", "WARNING":
		return "MEDIUM", true
	case "ERR", "ERROR":
		return "HIGH", true
	case "CRIT", "FATAL", "ALERT", "EMERG", "EMERGENCY":
		return "CRITICAL", true
	}
	return "", false
}

// GateColor combines liveness with the worst error severity seen in the
// recent error window and the SDK's queued-event backlog:
//
//	red    — DOWN/NEVER, or a HIGH/CRITICAL error recently
//	yellow — STALE, a MEDIUM error recently, or events stuck in the outbox
//	green  — otherwise
func GateColor(l Liveness, worstRecentSeverity string, outboxPending int) Color {
	rank := SeverityRank(worstRecentSeverity)
	switch {
	case l == Down || l == Never || rank >= SeverityRank("HIGH"):
		return Red
	case l == Stale || rank >= SeverityRank("MEDIUM") || outboxPending > 0:
		return Yellow
	default:
		return Green
	}
}

// ColorRank sorts red before yellow before green.
func ColorRank(c Color) int {
	switch c {
	case Red:
		return 0
	case Yellow:
		return 1
	}
	return 2
}
