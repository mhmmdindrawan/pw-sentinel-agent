// Package api exposes the local HTTP surface that SDKs (in any language)
// call to report events. It never talks to the gate-out application's own
// database — it only accepts structured events and stores them locally.
package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"parkways-sentinel/agent/internal/auth"
	"parkways-sentinel/agent/internal/config"
	"parkways-sentinel/agent/internal/devices"
	"parkways-sentinel/agent/internal/model"
	"parkways-sentinel/agent/internal/registry"
	"parkways-sentinel/agent/internal/status"
	"parkways-sentinel/agent/internal/store"
)

// UnmappedLocation tags an event whose caller presented a valid token but
// claimed a device_id that doesn't match what that token is bound to (see
// docs/protocol-v1.md, "Auth and device resolution"). The event is still
// stored — never silently dropped — so an operator notices it.
const UnmappedLocation = "UNMAPPED"

type Server struct {
	cfg      *config.Config
	reg      *registry.Registry
	devReg   *devices.Registry
	db       *store.Store
	users    *auth.Users
	sessions *auth.Sessions
	mux      *http.ServeMux
	limiter  *loginLimiter
	// overflowed records code groups already reported as full, so the
	// warning is logged once rather than for every event.
	overflowed sync.Map

	// Version is reported by /v1/health; set by main from build flags.
	Version string
	// now is swappable so tests can move the clock.
	now func() time.Time
}

func New(cfg *config.Config, reg *registry.Registry, devReg *devices.Registry, db *store.Store,
	users *auth.Users, sessions *auth.Sessions) *Server {
	s := &Server{
		cfg: cfg, reg: reg, devReg: devReg, db: db, users: users, sessions: sessions,
		mux: http.NewServeMux(), limiter: newLoginLimiter(), Version: "dev", now: time.Now,
	}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler {
	return withLogging(s.mux, s.cfg.Server.LogRequests)
}

func (s *Server) routes() {
	// Kiosk SDKs: device bearer token, write-only.
	s.mux.HandleFunc("POST /v1/events", s.handlePostEvent)
	s.mux.HandleFunc("POST /v1/events/batch", s.handlePostBatch)
	s.mux.HandleFunc("POST /v1/heartbeat", s.handleHeartbeat)

	// Dashboard / NOC: user session (or HTTP Basic for scripts), read-only.
	s.mux.HandleFunc("POST /v1/auth/login", s.handleLogin)
	s.mux.HandleFunc("POST /v1/auth/logout", s.handleLogout)
	s.mux.HandleFunc("GET /v1/auth/me", s.requireUser(s.handleMe))
	s.mux.HandleFunc("GET /v1/events", s.requireUser(s.handleGetEvents))
	s.mux.HandleFunc("GET /v1/summary", s.requireUser(s.handleSummary))
	s.mux.HandleFunc("GET /v1/fleet", s.requireUser(s.handleFleet))
	s.mux.HandleFunc("GET /v1/devices/{id}", s.requireUser(s.handleDevice))

	s.mux.HandleFunc("GET /v1/health", s.handleHealth)
	s.webRoutes()
}

// maxBatch caps POST /v1/events/batch so one request can't hold the
// database for long.
const maxBatch = 100

// maxEventName bounds event names, which the registry keeps for good.
const maxEventName = 128

// ingestResult is the outcome of storing one event.
type ingestResult struct {
	Event     model.StoredEvent
	Duplicate bool
	Status    int    // HTTP status this outcome maps to
	Err       string // set when Status >= 400
}

func (s *Server) handlePostEvent(w http.ResponseWriter, r *http.Request) {
	dev, ok := s.authDevice(w, r)
	if !ok {
		return
	}
	var in model.IncomingEvent
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body: "+err.Error())
		return
	}
	res := s.ingestOne(dev, in)
	if res.Err != "" {
		writeError(w, res.Status, res.Err)
		return
	}
	writeJSON(w, res.Status, res.Event)
}

type batchRequest struct {
	Events []model.IncomingEvent `json:"events"`
}

type batchItem struct {
	Index     int    `json:"index"`
	EventID   string `json:"event_id,omitempty"`
	Status    string `json:"status"` // created | duplicate | error
	ErrorCode string `json:"error_code,omitempty"`
	Error     string `json:"error,omitempty"`
	// Retryable is false for errors the same payload will always hit
	// (validation), so the SDK can drop it instead of retrying forever.
	Retryable bool `json:"retryable,omitempty"`
}

func (s *Server) handlePostBatch(w http.ResponseWriter, r *http.Request) {
	dev, ok := s.authDevice(w, r)
	if !ok {
		return
	}
	var req batchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body: "+err.Error())
		return
	}
	if len(req.Events) == 0 || len(req.Events) > maxBatch {
		writeError(w, http.StatusBadRequest, "batch must contain 1.."+strconv.Itoa(maxBatch)+" events")
		return
	}
	items := make([]batchItem, len(req.Events))
	for i, in := range req.Events {
		res := s.ingestOne(dev, in)
		it := batchItem{Index: i, EventID: in.EventID}
		switch {
		case res.Err != "":
			it.Status, it.Error, it.Retryable = "error", res.Err, res.Status >= 500
		case res.Duplicate:
			it.Status, it.ErrorCode = "duplicate", res.Event.ErrorCode
		default:
			it.Status, it.ErrorCode, it.EventID = "created", res.Event.ErrorCode, res.Event.EventID
		}
		items[i] = it
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": items})
}

// authDevice checks the device bearer token and writes 401 if it fails.
func (s *Server) authDevice(w http.ResponseWriter, r *http.Request) (devices.Entry, bool) {
	// Auth first — the token is the primary lookup key, so an
	// unauthenticated or unknown-token request is rejected before we even
	// bother parsing the body.
	token := bearerToken(r)
	if token == "" {
		writeError(w, http.StatusUnauthorized, "missing or malformed Authorization header")
		return devices.Entry{}, false
	}
	dev, ok := s.devReg.ByToken(token)
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid token")
		return devices.Entry{}, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	return dev, true
}

// ingestOne validates, resolves and stores one event for an authenticated
// device. It is shared by the single and batch endpoints.
func (s *Server) ingestOne(deviceEntry devices.Entry, in model.IncomingEvent) ingestResult {
	if strings.TrimSpace(in.Event) == "" {
		return ingestResult{Status: http.StatusBadRequest, Err: "field 'event' is required"}
	}
	if r := []rune(in.Event); len(r) > maxEventName {
		// Names become registry keys kept for good; a longer one is an app
		// bug, but the event itself is still worth keeping.
		log.Printf("warning: event name from device %q is %d characters; keeping the first %d", in.DeviceID, len(r), maxEventName)
		in.Event = string(r[:maxEventName])
	}
	if strings.TrimSpace(in.DeviceID) == "" {
		return ingestResult{Status: http.StatusBadRequest, Err: "field 'device_id' is required"}
	}

	// Resolve location/gate/pos. A valid token always corresponds to a
	// known device, but the payload's *claimed* device_id might not match
	// it (misconfigured SDK, token copied to the wrong kiosk, a renamed
	// device_id) — that mismatch is the "unmapped" case: accept and tag,
	// never silently drop, never 401 (see docs/protocol-v1.md).
	location, gate, pos := deviceEntry.Location, deviceEntry.Gate, deviceEntry.POS
	if in.DeviceID != deviceEntry.DeviceID {
		log.Printf("warning: device_id mismatch — token belongs to %q but payload claims %q; tagging UNMAPPED",
			deviceEntry.DeviceID, in.DeviceID)
		location, gate, pos = UnmappedLocation, in.DeviceID, ""
	}

	// Idempotency: a client-supplied event_id lets an at-least-once
	// sender retry safely without creating duplicate rows.
	if in.EventID != "" {
		if existing, found, err := s.db.FindByEventID(in.EventID); err != nil {
			return ingestResult{Status: http.StatusInternalServerError, Err: "failed to check idempotency: " + err.Error()}
		} else if found {
			return ingestResult{Event: existing, Duplicate: true, Status: http.StatusOK}
		}
	}
	eventID := in.EventID
	if eventID == "" {
		eventID = uuid.NewString()
		log.Printf("notice: event from device %q omitted event_id; generated %s (idempotency not available for this call)", in.DeviceID, eventID)
	}

	entry, known := s.reg.Lookup(in.Event)
	if !known {
		// Use the seed's prefix for the category (SYSTEM -> SYS), and never
		// mint a new prefix from client input: an unknown hint falls back
		// to APP, keeping the original text in context for triage.
		category, code, ok := s.reg.ResolveCategory(in.Category)
		if !ok {
			if hint := strings.TrimSpace(in.Category); hint != "" {
				log.Printf("warning: unknown category %q for event %q; allocating under APP", hint, in.Event)
				if in.Context == nil {
					in.Context = map[string]any{}
				}
				in.Context["category_hint"] = hint
			}
			category, code = "APP", "APP"
		}
		var isNew bool
		entry, isNew = s.reg.Allocate(in.Event, category, code, in.Severity)
		switch {
		case isNew:
			if err := s.db.SaveCustomRegistryEntry(entry); err != nil {
				log.Printf("warning: failed to persist learned event %q: %v", in.Event, err)
			} else {
				log.Printf("learned new event %q -> %s-%s-%s-%03d", in.Event, location, gate, entry.Code, entry.Number)
			}
		case entry.Number == registry.OverflowNumber:
			if _, seen := s.overflowed.LoadOrStore(entry.Code, true); !seen {
				log.Printf("warning: %s has no free numbers left; new event names (e.g. %q) now share %s-%03d — check the app for event names built from changing values",
					entry.Code, in.Event, entry.Code, entry.Number)
			}
		}
	}

	severity := entry.Severity
	if strings.TrimSpace(in.Severity) != "" {
		if sev, ok := status.NormalizeSeverity(in.Severity); ok {
			severity = sev
		} else {
			// Unknown values would rank below INFO: the event would never
			// colour a gate, match a filter or alert. Keep the default.
			log.Printf("warning: event %q from device %q has unknown severity %q; using %s", in.Event, in.DeviceID, in.Severity, severity)
			if in.Context == nil {
				in.Context = map[string]any{}
			}
			in.Context["severity_hint"] = in.Severity
		}
	}

	appVersion := in.AppVersion
	if appVersion == "" {
		appVersion = s.cfg.App.Version
	}

	receivedAt := s.now().UTC().Format(time.RFC3339)
	occurredAt := receivedAt
	if in.OccurredAt != "" {
		if _, err := time.Parse(time.RFC3339, in.OccurredAt); err == nil {
			occurredAt = in.OccurredAt
		} else {
			log.Printf("notice: event %s from device %q sent unparseable occurred_at %q, using received_at instead", eventID, in.DeviceID, in.OccurredAt)
		}
	}

	ev := model.StoredEvent{
		DeviceID:   in.DeviceID,
		EventID:    eventID,
		OccurredAt: occurredAt,
		Location:   location,
		Gate:       gate,
		POS:        pos,
		Device:     in.Device,
		Category:   entry.Category,
		ErrorCode:  registry.ErrorCode(location, gate, entry),
		Event:      in.Event,
		Severity:   severity,
		Message:    in.Message,
		TxRef:      in.TxRef,
		RetryCount: in.RetryCount,
		Endpoint:   in.Endpoint,
		HTTPStatus: in.HTTPStatus,
		DurationMS: in.DurationMS,
		Context:    in.Context,
		AppVersion: appVersion,
		ReceivedAt: receivedAt,
	}

	id, err := s.db.InsertEvent(ev)
	if store.IsUniqueViolation(err) {
		// A concurrent retry of the same event_id won the race between our
		// FindByEventID and InsertEvent — answer like any other duplicate.
		if existing, found, ferr := s.db.FindByEventID(eventID); ferr == nil && found {
			return ingestResult{Event: existing, Duplicate: true, Status: http.StatusOK}
		}
	}
	if err != nil {
		return ingestResult{Status: http.StatusInternalServerError, Err: "failed to store event: " + err.Error()}
	}
	ev.ID = id

	if location != UnmappedLocation {
		if err := s.db.TouchEventSeen(deviceEntry.DeviceID, receivedAt, appVersion); err != nil {
			log.Printf("warning: %v", err)
		}
	}
	return ingestResult{Event: ev, Status: http.StatusCreated}
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	dev, ok := s.authDevice(w, r)
	if !ok {
		return
	}
	var hb model.Heartbeat
	if err := json.NewDecoder(r.Body).Decode(&hb); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body: "+err.Error())
		return
	}
	// The token decides which device this is; a mismatching device_id is
	// only logged (an event would be tagged UNMAPPED, but a heartbeat has
	// nothing to tag — it just proves the token's kiosk is alive).
	if hb.DeviceID != "" && hb.DeviceID != dev.DeviceID {
		log.Printf("warning: heartbeat device_id mismatch — token belongs to %q but payload claims %q", dev.DeviceID, hb.DeviceID)
	}
	now := s.now().UTC()
	st := model.DeviceState{
		DeviceID:        dev.DeviceID,
		LastHeartbeatAt: now.Format(time.RFC3339),
		AppVersion:      hb.AppVersion,
		SDKVersion:      hb.SDKVersion,
		UptimeS:         hb.UptimeS,
		OutboxPending:   hb.OutboxPending,
		OutboxDropped:   hb.OutboxDropped,
		RemoteAddr:      remoteIP(r),
	}
	if err := s.db.UpsertHeartbeat(st); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// server_time lets the SDK notice a badly skewed kiosk clock.
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "server_time": now.Format(time.RFC3339)})
}

// bearerToken extracts the token from "Authorization: Bearer <token>",
// returning "" if the header is missing or malformed.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, prefix))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// slowRequest is logged even when request logging is off.
const slowRequest = 2 * time.Second

// withLogging logs failed and slow requests, or every request when all is
// set. Every kiosk heartbeats each minute, so logging all of them by default
// would bury the warnings that matter.
func withLogging(next http.Handler, all bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if took := time.Since(start); all || rec.status >= 400 || took >= slowRequest {
			log.Printf("%s %s %d %s %s", r.Method, r.URL.Path, rec.status, took.Round(time.Millisecond), remoteIP(r))
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
