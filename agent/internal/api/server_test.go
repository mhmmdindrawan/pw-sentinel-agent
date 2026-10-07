package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"parkways-sentinel/agent/internal/config"
	"parkways-sentinel/agent/internal/devices"
	"parkways-sentinel/agent/internal/model"
	"parkways-sentinel/agent/internal/registry"
	"parkways-sentinel/agent/internal/store"
)

const testToken = "test-token-g01"

// newTestServer runs the real handler against the shipped registry.yaml
// and a throwaway SQLite database.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	devPath := filepath.Join(dir, "devices.yaml")
	if err := os.WriteFile(devPath, []byte(`
devices:
  TST-G01:
    location: TST
    gate: G01
    pos: POS01
    token: "`+testToken+`"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := registry.Load("../../registry.yaml")
	if err != nil {
		t.Fatal(err)
	}
	devReg, err := devices.Load(devPath)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "sentinel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return New(&config.Config{}, reg, devReg, db)
}

func postEvent(t *testing.T, s *Server, body map[string]any) model.StoredEvent {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("POST %v: status %d: %s", body, rec.Code, rec.Body.String())
	}
	var ev model.StoredEvent
	if err := json.Unmarshal(rec.Body.Bytes(), &ev); err != nil {
		t.Fatal(err)
	}
	return ev
}

func TestUnknownEventUsesSeedCodePrefix(t *testing.T) {
	s := newTestServer(t)
	cases := []struct{ category, wantCategory, wantPrefix string }{
		{"SYSTEM", "SYSTEM", "TST-G01-SYS-"},
		{"NETWORK", "NETWORK", "TST-G01-NET-"},
		{"DATABASE", "DATABASE", "TST-G01-DB-"},
		{"net", "NETWORK", "TST-G01-NET-"}, // code prefix accepted as a hint too
	}
	for i, c := range cases {
		ev := postEvent(t, s, map[string]any{
			"device_id": "TST-G01",
			"event":     "UNSEEN_EVENT_" + string(rune('A'+i)),
			"category":  c.category,
		})
		if ev.Category != c.wantCategory || len(ev.ErrorCode) < len(c.wantPrefix) || ev.ErrorCode[:len(c.wantPrefix)] != c.wantPrefix {
			t.Errorf("category %q: got %s / %s, want %s / %s*", c.category, ev.Category, ev.ErrorCode, c.wantCategory, c.wantPrefix)
		}
	}
}

func TestUnknownCategoryFallsBackToAppAndKeepsHint(t *testing.T) {
	s := newTestServer(t)
	ev := postEvent(t, s, map[string]any{
		"device_id": "TST-G01",
		"event":     "WEIRD_THING",
		"category":  "Banana",
	})
	if ev.Category != "APP" || ev.ErrorCode[:len("TST-G01-APP-")] != "TST-G01-APP-" {
		t.Fatalf("got %s / %s, want APP / TST-G01-APP-*", ev.Category, ev.ErrorCode)
	}
	if ev.Context["category_hint"] != "Banana" {
		t.Fatalf("context = %v, want category_hint=Banana", ev.Context)
	}
}
