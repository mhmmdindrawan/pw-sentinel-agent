// Package model defines the event shapes exchanged between SDKs, the
// normalizer/registry, and local storage.
package model

// IncomingEvent is what a caller (SDK) posts to the agent. Location/gate/pos
// are deliberately absent: the agent resolves them per event from an
// authenticated DeviceID (see internal/devices), since one agent now serves
// many kiosks and a gate's SDK should never need to know or trust its own
// location label.
type IncomingEvent struct {
	// Protocol envelope fields (see docs/protocol-v1.md).
	ProtocolVersion int    `json:"protocol_version,omitempty"`
	DeviceID        string `json:"device_id"`             // identifies the kiosk/gate machine; required
	EventID         string `json:"event_id,omitempty"`    // client-generated id for idempotent retries
	OccurredAt      string `json:"occurred_at,omitempty"` // RFC3339, client clock — untrusted

	Event      string         `json:"event"`              // symbolic name, e.g. "PAYMENT_TIMEOUT"
	Category   string         `json:"category,omitempty"` // optional hint if Event is not in the registry yet
	Severity   string         `json:"severity,omitempty"` // optional override of the registry default
	Message    string         `json:"message,omitempty"`
	Device     string         `json:"device,omitempty"` // sub-device at the gate, e.g. "READER01" — NOT the same as DeviceID, which identifies the kiosk machine itself
	AppVersion string         `json:"app_version,omitempty"`
	TxRef      string         `json:"transaction_ref,omitempty"` // reference id only, never transaction data
	RetryCount *int           `json:"retry_count,omitempty"`
	Endpoint   string         `json:"endpoint,omitempty"`
	HTTPStatus *int           `json:"http_status,omitempty"`
	DurationMS *int           `json:"duration_ms,omitempty"`
	Context    map[string]any `json:"context,omitempty"`
}

// StoredEvent is the fully normalized record persisted to SQLite and
// returned by query endpoints.
type StoredEvent struct {
	ID         int64          `json:"id"`
	DeviceID   string         `json:"device_id"`
	EventID    string         `json:"event_id"`
	OccurredAt string         `json:"occurred_at,omitempty"`
	Location   string         `json:"location"`
	Gate       string         `json:"gate"`
	POS        string         `json:"pos,omitempty"`
	Device     string         `json:"device,omitempty"` // sub-device at the gate — see IncomingEvent.Device
	Category   string         `json:"category"`
	ErrorCode  string         `json:"error_code"`
	Event      string         `json:"event"`
	Severity   string         `json:"severity"`
	Message    string         `json:"message,omitempty"`
	TxRef      string         `json:"transaction_ref,omitempty"`
	RetryCount *int           `json:"retry_count,omitempty"`
	Endpoint   string         `json:"endpoint,omitempty"`
	HTTPStatus *int           `json:"http_status,omitempty"`
	DurationMS *int           `json:"duration_ms,omitempty"`
	Context    map[string]any `json:"context,omitempty"`
	AppVersion string         `json:"app_version,omitempty"`
	ReceivedAt string         `json:"received_at"`
}

// SummaryRow is one aggregated row for the /v1/summary endpoint.
type SummaryRow struct {
	ErrorCode string `json:"error_code"`
	Event     string `json:"event"`
	Severity  string `json:"severity"`
	Count     int    `json:"count"`
	FirstSeen string `json:"first_seen"`
	LastSeen  string `json:"last_seen"`
}
