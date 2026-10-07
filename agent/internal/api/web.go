package api

import (
	"io/fs"
	"net/http"

	"parkways-sentinel/agent/internal/web"
)

// webRoutes serves the embedded dashboard. The page itself is public (it
// holds no data); everything it shows comes from the login-protected API.
func (s *Server) webRoutes() {
	static, err := fs.Sub(web.Static, "static")
	if err != nil {
		panic(err) // embedded at build time; cannot fail at runtime
	}
	files := http.FileServerFS(static)
	s.mux.Handle("GET /static/", withWebHeaders(http.StripPrefix("/static/", files)))
	s.mux.Handle("GET /{$}", withWebHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, static, "index.html")
	})))
}

// withWebHeaders locks the dashboard down: same-origin scripts only, no
// framing, and no caching so a redeploy is picked up on the next load.
func withWebHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}
