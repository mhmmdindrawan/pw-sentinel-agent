package api

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"parkways-sentinel/agent/internal/devices"
	"parkways-sentinel/agent/internal/model"
)

// maxName bounds the gate name a kiosk reports about itself.
const maxName = 64

// handleEnroll registers the calling kiosk under this agent's site and
// returns its own device token. The site enroll key (one per location,
// shared by its gates) is the only credential; it is used for this call
// alone, after which the kiosk authenticates with the token. Re-enrolling
// an already-enrolled device_id rotates its token, which is how a
// reinstalled kiosk recovers without an operator. Static devices.yaml
// entries cannot be taken over.
func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Site.EnrollEnabled() {
		writeError(w, http.StatusNotFound, "enrollment is off on this agent (set site.code and site.enroll_key)")
		return
	}
	ip := remoteIP(r)
	now := s.now()
	if !s.enrollLimiter.allow(ip, now) {
		writeError(w, http.StatusTooManyRequests, "too many failed enrollment attempts; try again later")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var req model.EnrollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body: "+err.Error())
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.SiteKey), []byte(s.cfg.Site.EnrollKey)) != 1 {
		s.enrollLimiter.fail(ip, now)
		log.Printf("warning: enrollment with a wrong site key from %s (device_id %q)", ip, req.DeviceID)
		writeError(w, http.StatusUnauthorized, "invalid site key")
		return
	}
	req.DeviceID = strings.TrimSpace(req.DeviceID)
	req.Gate = strings.ToUpper(strings.TrimSpace(req.Gate))
	req.POS = strings.TrimSpace(req.POS)
	if !devices.ValidDeviceID(req.DeviceID) {
		writeError(w, http.StatusBadRequest, "device_id must be 1-64 characters: letters, digits, . _ -")
		return
	}
	if !devices.ValidLabel(req.Gate) {
		writeError(w, http.StatusBadRequest, "gate must be a short code like G01")
		return
	}
	if req.POS != "" && !devices.ValidDeviceID(req.POS) {
		writeError(w, http.StatusBadRequest, "invalid pos")
		return
	}

	token, err := devices.NewToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	entry := devices.Entry{DeviceID: req.DeviceID, Location: s.cfg.Site.Code, Gate: req.Gate, POS: req.POS}
	hash := devices.HashToken(token)
	if err := s.devReg.PutEnrolled(entry, hash); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	ts := now.UTC().Format(time.RFC3339)
	if err := s.db.SaveEnrolled(entry, hash, ts); err != nil {
		s.devReg.RemoveEnrolled(entry.DeviceID)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Name/platform/version are state, not identity: keep them with the
	// heartbeat data so the card is complete before the first heartbeat.
	if err := s.db.UpsertHeartbeat(model.DeviceState{
		DeviceID: entry.DeviceID, LastHeartbeatAt: ts, AppVersion: req.AppVersion,
		Platform: strings.ToLower(strings.TrimSpace(req.Platform)), Name: trimName(req.Name), RemoteAddr: ip,
	}); err != nil {
		log.Printf("warning: %v", err)
	}
	s.enrollLimiter.reset(ip)
	log.Printf("enrolled %s as %s %s %s from %s", entry.DeviceID, entry.Location, entry.Gate, entry.POS, ip)
	writeJSON(w, http.StatusCreated, map[string]any{
		"device_id": entry.DeviceID,
		"location":  entry.Location,
		"gate":      entry.Gate,
		"pos":       entry.POS,
		"site_name": s.cfg.Site.Name,
		"token":     token,
	})
}

// applyIdentityClaims moves an enrolled device to the gate/pos its kiosk
// now reports (the app's settings changed). Static devices only get their
// name updated — their gate is the operator's decision.
func (s *Server) applyIdentityClaims(dev devices.Entry, hb model.Heartbeat, now time.Time) {
	gate := strings.ToUpper(strings.TrimSpace(hb.Gate))
	pos := strings.TrimSpace(hb.POS)
	if !dev.Enrolled || gate == "" || !devices.ValidLabel(gate) || (pos != "" && !devices.ValidDeviceID(pos)) {
		return
	}
	if gate == dev.Gate && (pos == "" || pos == dev.POS) {
		return
	}
	if pos == "" {
		pos = dev.POS
	}
	if _, ok := s.devReg.UpdateEnrolled(dev.DeviceID, gate, pos); !ok {
		return
	}
	if err := s.db.UpdateEnrolledIdentity(dev.DeviceID, gate, pos, now.UTC().Format(time.RFC3339)); err != nil {
		log.Printf("warning: %v", err)
		return
	}
	log.Printf("device %s moved itself from %s %s to %s %s", dev.DeviceID, dev.Gate, dev.POS, gate, pos)
}

func trimName(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxName {
		return string(r[:maxName])
	}
	return s
}
