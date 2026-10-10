package api

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"parkways-sentinel/agent/internal/web"
)

// webRoutes serves the embedded dashboard. The page itself is public (it
// holds no data); everything it shows comes from the login-protected API.
func (s *Server) webRoutes() {
	static, err := fs.Sub(web.Static, "static")
	if err != nil {
		panic(err) // embedded at build time; cannot fail at runtime
	}
	etags := hashFiles(static)
	files := http.FileServerFS(static)
	s.mux.Handle("GET /static/", withWebHeaders(etags, http.StripPrefix("/static/", files)))
	s.mux.Handle("GET /{$}", withWebHeaders(etags, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, static, "index.html")
	})))
	// Browsers and tools still probe /favicon.ico despite the <link>.
	s.mux.Handle("GET /favicon.ico", http.RedirectHandler("/static/favicon.svg", http.StatusMovedPermanently))
}

// hashFiles computes a strong ETag per embedded file once at startup.
// Embedded files carry no modification time, so without this every page
// load would re-download every module.
func hashFiles(fsys fs.FS) map[string]string {
	out := map[string]string{}
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		out[p] = `"` + hex.EncodeToString(sum[:8]) + `"`
		return nil
	})
	return out
}

// withWebHeaders locks the dashboard down (same-origin scripts only, no
// framing) and sets the file's ETag: the browser revalidates every load
// (no-cache) and gets a 304 when nothing changed, so a redeploy is picked
// up immediately without re-downloading unchanged files.
func withWebHeaders(etags map[string]string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		// img-src data: is for the inline SVG icons in app.css (CSS masks and
		// backgrounds); images can't run script.
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cache-Control", "no-cache")
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/static/")
		if r.URL.Path == "/" {
			name = "index.html"
		}
		if tag, ok := etags[name]; ok {
			// http.ServeContent compares If-None-Match against this header.
			h.Set("ETag", tag)
		}
		next.ServeHTTP(w, r)
	})
}
