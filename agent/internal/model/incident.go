package model

// Incident is a run of trouble on one gate: every MEDIUM+ event (and a
// DOWN status) within the incident gap of the previous one is counted
// here instead of being a separate thing to look at.
type Incident struct {
	ID         int64            `json:"id"`
	DeviceID   string           `json:"device_id"`
	Location   string           `json:"location"`
	Gate       string           `json:"gate"`
	StartedAt  string           `json:"started_at"`
	LastAt     string           `json:"last_at"`
	ResolvedAt string           `json:"resolved_at,omitempty"`
	Count      int              `json:"count"`
	Severity   string           `json:"severity"` // worst seen
	Codes      map[string]int   `json:"codes"`    // event name -> count
	Alerts     map[string]Alert `json:"-"`        // route -> Telegram message
	Done       bool             `json:"-"`
}

// Alert is the Telegram message a route holds for an incident.
type Alert struct {
	ChatID    string `json:"chat_id"`
	ThreadID  int    `json:"thread_id,omitempty"`
	MessageID int64  `json:"message_id"`
	Count     int    `json:"count"`    // incident count when last sent/edited
	At        string `json:"at"`       // last send/edit time
	Skipped   bool   `json:"skipped"`  // route did not want it (severity/location)
	Resolved  bool   `json:"resolved"` // final edit delivered
}

// StatusDownEvent is the pseudo event name counted in an incident when the
// gate itself goes DOWN.
const StatusDownEvent = "GATE_DOWN"
