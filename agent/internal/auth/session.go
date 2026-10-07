package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

const CookieName = "sentinel_session"

// Sessions issues and verifies stateless session tokens:
// base64url(json payload) + "." + base64url(HMAC-SHA256(payload)).
type Sessions struct {
	secret []byte
	ttl    time.Duration
}

type sessionPayload struct {
	User string `json:"u"`
	Exp  int64  `json:"e"`
}

// NewSessions uses secret if non-empty, otherwise a random one (sessions
// then don't survive an agent restart). It reports whether it generated one.
func NewSessions(secret string, ttl time.Duration) (*Sessions, bool) {
	if secret != "" {
		return &Sessions{secret: []byte(secret), ttl: ttl}, false
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return &Sessions{secret: b, ttl: ttl}, true
}

func (s *Sessions) TTL() time.Duration { return s.ttl }

func (s *Sessions) Issue(user string, now time.Time) string {
	payload, _ := json.Marshal(sessionPayload{User: user, Exp: now.Add(s.ttl).Unix()})
	p := base64.RawURLEncoding.EncodeToString(payload)
	return p + "." + base64.RawURLEncoding.EncodeToString(s.sign(p))
}

// Verify returns the username in a valid, unexpired token. The caller must
// still check that the user exists, so deleting a user from users.yaml
// (and restarting) revokes their sessions.
func (s *Sessions) Verify(token string, now time.Time) (string, bool) {
	p, sig, ok := strings.Cut(token, ".")
	if !ok {
		return "", false
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, s.sign(p)) {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil {
		return "", false
	}
	var sp sessionPayload
	if json.Unmarshal(raw, &sp) != nil || sp.User == "" || now.Unix() >= sp.Exp {
		return "", false
	}
	return sp.User, true
}

func (s *Sessions) sign(payload string) []byte {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte(payload))
	return m.Sum(nil)
}
