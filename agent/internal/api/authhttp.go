package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"time"

	"parkways-sentinel/agent/internal/auth"
)

// maxBodyBytes bounds any request body (a full batch of 100 events is far
// below this).
const maxBodyBytes = 1 << 20

type ctxKey int

const userKey ctxKey = 0

// requireUser admits a request carrying a valid session cookie, or HTTP
// Basic credentials (handy for curl/jq scripts). Device tokens are never
// accepted here: a kiosk's credential must not read other kiosks' data.
func (s *Server) requireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		usr, ok := s.userFromRequest(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "login required")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userKey, usr)))
	}
}

func (s *Server) userFromRequest(r *http.Request) (auth.User, bool) {
	if c, err := r.Cookie(auth.CookieName); err == nil {
		if name, ok := s.sessions.Verify(c.Value, s.now()); ok {
			// Re-check the user still exists, so removing someone from
			// users.yaml (and restarting) revokes their sessions.
			return s.users.Lookup(name)
		}
	}
	if name, pw, ok := r.BasicAuth(); ok {
		ip := remoteIP(r)
		if !s.limiter.allow(ip, s.now()) {
			return auth.User{}, false
		}
		usr, ok := s.users.Verify(name, pw)
		if !ok {
			s.limiter.fail(ip, s.now())
		}
		return usr, ok
	}
	return auth.User{}, false
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	ip := remoteIP(r)
	if !s.limiter.allow(ip, s.now()) {
		writeError(w, http.StatusTooManyRequests, "too many failed logins, try again later")
		return
	}
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	usr, ok := s.users.Verify(req.Username, req.Password)
	if !ok {
		s.limiter.fail(ip, s.now())
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	s.limiter.reset(ip)
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    s.sessions.Issue(usr.Name, s.now()),
		Path:     "/",
		MaxAge:   int(s.sessions.TTL().Seconds()),
		HttpOnly: true,
		// Strict: the dashboard never needs the cookie on cross-site
		// requests, which also rules out CSRF on the POST endpoints.
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	})
	writeJSON(w, http.StatusOK, map[string]any{"username": usr.Name, "role": usr.Role})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: auth.CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	usr := r.Context().Value(userKey).(auth.User)
	writeJSON(w, http.StatusOK, map[string]any{"username": usr.Name, "role": usr.Role})
}

// loginLimiter blocks an IP for a while after too many failed logins.
type loginLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
}

const (
	loginMaxFailures = 10
	loginWindow      = 15 * time.Minute
)

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{failures: map[string][]time.Time{}}
}

func (l *loginLimiter) recent(ip string, now time.Time) []time.Time {
	kept := l.failures[ip][:0]
	for _, t := range l.failures[ip] {
		if now.Sub(t) < loginWindow {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.failures, ip)
		return nil
	}
	l.failures[ip] = kept
	return kept
}

func (l *loginLimiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(ip, now)) < loginMaxFailures
}

func (l *loginLimiter) fail(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures[ip] = append(l.recent(ip, now), now)
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, ip)
}

// remoteIP is the TCP peer address. The agent is reached directly over
// Tailscale, not through a proxy, so X-Forwarded-For is deliberately ignored
// (it would let a client pick its own rate-limit bucket).
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
