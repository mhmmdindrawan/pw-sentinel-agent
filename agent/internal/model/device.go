package model

// Heartbeat is what an SDK posts to POST /v1/heartbeat. The device is
// identified by its bearer token; DeviceID is only cross-checked.
type Heartbeat struct {
	ProtocolVersion int    `json:"protocol_version,omitempty"`
	DeviceID        string `json:"device_id"`
	AppVersion      string `json:"app_version,omitempty"`
	SDKVersion      string `json:"sdk_version,omitempty"`
	UptimeS         int64  `json:"uptime_s,omitempty"`
	OutboxPending   int    `json:"outbox_pending,omitempty"` // events queued on the kiosk, not yet delivered
	OutboxDropped   int    `json:"outbox_dropped,omitempty"` // events the SDK had to discard since it started
	SentAt          string `json:"sent_at,omitempty"`        // RFC3339, client clock — informational only
}

// DeviceState is the agent's latest knowledge of one device, kept in its
// own table so heartbeats don't bloat the events table. Timestamps are
// RFC3339 UTC on the agent's clock; empty means "never".
type DeviceState struct {
	DeviceID        string `json:"device_id"`
	LastHeartbeatAt string `json:"last_heartbeat_at,omitempty"`
	LastEventAt     string `json:"last_event_at,omitempty"`
	AppVersion      string `json:"app_version,omitempty"`
	SDKVersion      string `json:"sdk_version,omitempty"`
	UptimeS         int64  `json:"uptime_s,omitempty"`
	OutboxPending   int    `json:"outbox_pending"`
	OutboxDropped   int    `json:"outbox_dropped"`
	RemoteAddr      string `json:"remote_addr,omitempty"`
}

// EventFilter narrows GET /v1/events. Zero values mean "no filter".
// Since/Until compare against received_at (agent clock, RFC3339 UTC).
type EventFilter struct {
	DeviceID  string
	Location  string
	Gate      string
	ErrorCode string
	Event     string
	Severity  string // minimum severity: MEDIUM also returns HIGH and CRITICAL
	Since     string
	Until     string
	BeforeID  int64 // pagination: only rows with id < BeforeID
	Limit     int
}

// DeviceErrorStats summarises one device's recent non-INFO events for the
// fleet view.
type DeviceErrorStats struct {
	Errors1h    int          `json:"errors_1h"`
	Errors24h   int          `json:"errors_24h"`
	WorstRecent string       `json:"worst_recent_severity,omitempty"` // within the status error window
	LastError   *StoredEvent `json:"last_error,omitempty"`
}

// FleetDevice is one row of GET /v1/fleet.
type FleetDevice struct {
	DeviceID string `json:"device_id"`
	Location string `json:"location"`
	Gate     string `json:"gate"`
	POS      string `json:"pos,omitempty"`
	Status   string `json:"status"`
	Color    string `json:"color"`
	LastSeen string `json:"last_seen,omitempty"`
	DeviceState
	DeviceErrorStats
}
