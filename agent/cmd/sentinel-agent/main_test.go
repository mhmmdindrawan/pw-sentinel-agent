package main

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"parkways-sentinel/agent/internal/store"
)

func TestRunPingLogsOnlyChanges(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "sentinel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var hits, failing atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if failing.Load() == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()

	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(orig)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runPing(ctx, db, srv.URL+"/secret-check-id", 20*time.Millisecond); close(done) }()

	waitFor := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(2 * time.Second); !cond(); time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s; log:\n%s", what, buf.String())
			}
		}
	}
	waitFor("first pings", func() bool { return hits.Load() >= 2 })
	failing.Store(1)
	n := hits.Load()
	waitFor("failing pings", func() bool { return hits.Load() >= n+3 })
	failing.Store(0)
	n = hits.Load()
	waitFor("recovered pings", func() bool { return hits.Load() >= n+2 })
	cancel()
	<-done

	out := buf.String()
	if c := strings.Count(out, "ping failed"); c != 1 {
		t.Errorf("logged %d failures, want 1:\n%s", c, out)
	}
	if !strings.Contains(out, "ping working again") {
		t.Errorf("recovery not logged:\n%s", out)
	}
	if strings.Contains(out, "secret-check-id") {
		t.Errorf("ping URL path leaked into the log:\n%s", out)
	}
}
