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
	"time"

	"github.com/google/uuid"

	"parkways-sentinel/agent/internal/config"
	"parkways-sentinel/agent/internal/devices"
	"parkways-sentinel/agent/internal/model"
	"parkways-sentinel/agent/internal/registry"
	"parkways-sentinel/agent/internal/store"
)

// UnmappedLocation tags an event whose caller presented a valid token but
// claimed a device_id that doesn't match what that token is bound to (see
// docs/protocol-v1.md, "Auth and device resolution"). The event is still
// stored — never silently dropped — so an operator notices it.
const UnmappedLocation = "UNMAPPED"

type Server struct {
	cfg    *config.Config
	reg    *registry.Registry
	devReg *devices.Registry
	db     *store.Store
	mux    *http.ServeMux
}

func New(cfg *config.Config, reg *registry.Registry, devReg *devices.Registry, db *store.Store) *Server {
	s := &Server{cfg: cfg, reg: reg, devReg: devReg, db: db, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler {
	return withLogging(s.mux)
}

func (s *Server) routes() {
	s.mux.HandleFunc("POST /v1/events", s.handlePostEvent)
	s.mux.HandleFunc("GET /v1/events", s.handleGetEvents)
	s.mux.HandleFunc("GET /v1/summary", s.handleSummary)
	s.mux.HandleFunc("GET /v1/health", s.handleHealth)
}

func (s *Server) handlePostEvent(w http.ResponseWriter, r *http.Request) {
	// 1. Auth first — the token is the primary lookup key, so an
	// unauthenticated or unknown-token request is rejected before we even
	// bother parsing the body.
	token := bearerToken(r)
	if token == "" {
		writeError(w, http.StatusUnauthorized, "missing or malformed Authorization header")
		return
	}
	deviceEntry, ok := s.devReg.ByToken(token)
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid token")
		return
	}

	var in model.IncomingEvent
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body: "+err.Error())
		return
	}
	if strings.TrimSpace(in.Event) == "" {
		writeError(w, http.StatusBadRequest, "field 'event' is required")
		return
	}
	if strings.TrimSpace(in.DeviceID) == "" {
		writeError(w, http.StatusBadRequest, "field 'device_id' is required")
		return
	}

	// 2. Resolve location/gate/pos. A valid token always corresponds to a
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

	// 3. Idempotency: a client-supplied event_id lets an at-least-once
	// sender retry safely without creating duplicate rows.
	if in.EventID != "" {
		if existing, found, err := s.db.FindByEventID(in.EventID); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to check idempotency: "+err.Error())
			return
		} else if found {
			writeJSON(w, http.StatusOK, existing)
			return
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
		entry = s.reg.Allocate(in.Event, category, code, in.Severity)
		if err := s.db.SaveCustomRegistryEntry(entry); err != nil {
			log.Printf("warning: failed to persist learned event %q: %v", in.Event, err)
		} else {
			log.Printf("learned new event %q -> %s-%s-%s-%03d", in.Event, location, gate, entry.Code, entry.Number)
		}
	}

	severity := entry.Severity
	if strings.TrimSpace(in.Severity) != "" {
		severity = strings.ToUpper(in.Severity)
	}

	appVersion := in.AppVersion
	if appVersion == "" {
		appVersion = s.cfg.App.Version
	}

	receivedAt := time.Now().UTC().Format(time.RFC3339)
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
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store event: "+err.Error())
		return
	}
	ev.ID = id

	writeJSON(w, http.StatusCreated, ev)
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

func (s *Server) handleGetEvents(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}

	events, err := s.db.RecentEvents(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to query events: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	minutes := 30
	if v := r.URL.Query().Get("minutes"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			minutes = n
		}
	}

	rows, err := s.db.Summary(time.Duration(minutes) * time.Minute)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build summary: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	// Location/gate/pos are no longer single agent-wide values — this agent
	// serves multiple kiosks, each resolved per event. See GET /v1/summary
	// or /v1/events for per-device data.
	writeJSON(w, http.StatusOK, map[string]any{
		"status":             "ok",
		"devices_configured": s.devReg.Count(),
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}
