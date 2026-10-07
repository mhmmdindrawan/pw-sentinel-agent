package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"parkways-sentinel/agent/internal/auth"
	"parkways-sentinel/agent/internal/config"
	"parkways-sentinel/agent/internal/devices"
	"parkways-sentinel/agent/internal/model"
	"parkways-sentinel/agent/internal/registry"
	"parkways-sentinel/agent/internal/store"
)

const (
	testToken  = "test-token-g01"
	testToken2 = "test-token-g02"
	testUser   = "noc"
	testPass   = "noc-password-123"
)

type testEnv struct {
	s   *Server
	db  *store.Store
	now time.Time
}

// newTestServer runs the real handler against the shipped registry.yaml,
// a throwaway SQLite database, two devices, one dashboard user, and a
// clock the test controls.
func newTestServer(t *testing.T) *testEnv {
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
  TST-G02:
    location: TST
    gate: G02
    token: "`+testToken2+`"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	usersPath := filepath.Join(dir, "users.yaml")
	if err := auth.AddUser(usersPath, testUser, auth.RoleViewer, testPass); err != nil {
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
	users, err := auth.LoadUsers(usersPath)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "sentinel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	sessions, _ := auth.NewSessions("test-secret", time.Hour)
	env := &testEnv{db: db, now: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
	env.s = New(&config.Config{}, reg, devReg, db, users, sessions)
	env.s.now = func() time.Time { return env.now }
	return env
}

func (e *testEnv) do(method, path, token string, body any) *httptest.ResponseRecorder {
	var rd *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	return rec
}

// get performs a dashboard read with HTTP Basic credentials.
func (e *testEnv) get(t *testing.T, path string, out any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.SetBasicAuth(testUser, testPass)
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatal(err)
	}
}

func postEvent(t *testing.T, e *testEnv, body map[string]any) model.StoredEvent {
	t.Helper()
	rec := e.do(http.MethodPost, "/v1/events", testToken, body)
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
	e := newTestServer(t)
	cases := []struct{ category, wantCategory, wantPrefix string }{
		{"SYSTEM", "SYSTEM", "TST-G01-SYS-"},
		{"NETWORK", "NETWORK", "TST-G01-NET-"},
		{"DATABASE", "DATABASE", "TST-G01-DB-"},
		{"net", "NETWORK", "TST-G01-NET-"}, // code prefix accepted as a hint too
	}
	for i, c := range cases {
		ev := postEvent(t, e, map[string]any{
			"device_id": "TST-G01",
			"event":     "UNSEEN_EVENT_" + string(rune('A'+i)),
			"category":  c.category,
		})
		if ev.Category != c.wantCategory || !strings.HasPrefix(ev.ErrorCode, c.wantPrefix) {
			t.Errorf("category %q: got %s / %s, want %s / %s*", c.category, ev.Category, ev.ErrorCode, c.wantCategory, c.wantPrefix)
		}
	}
}

func TestUnknownCategoryFallsBackToAppAndKeepsHint(t *testing.T) {
	e := newTestServer(t)
	ev := postEvent(t, e, map[string]any{
		"device_id": "TST-G01",
		"event":     "WEIRD_THING",
		"category":  "Banana",
	})
	if ev.Category != "APP" || !strings.HasPrefix(ev.ErrorCode, "TST-G01-APP-") {
		t.Fatalf("got %s / %s, want APP / TST-G01-APP-*", ev.Category, ev.ErrorCode)
	}
	if ev.Context["category_hint"] != "Banana" {
		t.Fatalf("context = %v, want category_hint=Banana", ev.Context)
	}
}

func TestConcurrentDuplicateEventIDNeverFails(t *testing.T) {
	e := newTestServer(t)
	var wg sync.WaitGroup
	codes := make([]int, 20)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = e.do(http.MethodPost, "/v1/events", testToken, map[string]any{
				"device_id": "TST-G01", "event_id": "same-id", "event": "PAYMENT_TIMEOUT",
			}).Code
		}(i)
	}
	wg.Wait()
	created := 0
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusOK:
		default:
			t.Fatalf("got status %d for a duplicate event_id; want 200 or 201", c)
		}
	}
	if created != 1 {
		t.Fatalf("created = %d, want exactly 1", created)
	}
}

func TestBatchMixedResults(t *testing.T) {
	e := newTestServer(t)
	postEvent(t, e, map[string]any{"device_id": "TST-G01", "event_id": "dup-1", "event": "PAYMENT_TIMEOUT"})
	rec := e.do(http.MethodPost, "/v1/events/batch", testToken, map[string]any{
		"events": []map[string]any{
			{"device_id": "TST-G01", "event_id": "new-1", "event": "READER_DISCONNECTED"},
			{"device_id": "TST-G01", "event_id": "dup-1", "event": "PAYMENT_TIMEOUT"},
			{"device_id": "TST-G01", "event_id": "bad-1"}, // missing event
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct{ Results []batchItem }
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	want := []string{"created", "duplicate", "error"}
	for i, w := range want {
		if resp.Results[i].Status != w {
			t.Errorf("item %d: status %q, want %q (%+v)", i, resp.Results[i].Status, w, resp.Results[i])
		}
	}
	if resp.Results[2].Retryable {
		t.Error("validation error marked retryable")
	}
	if resp.Results[0].ErrorCode != "TST-G01-READER-001" {
		t.Errorf("error_code = %q", resp.Results[0].ErrorCode)
	}

	tooMany := make([]map[string]any, maxBatch+1)
	for i := range tooMany {
		tooMany[i] = map[string]any{"device_id": "TST-G01", "event": "X"}
	}
	if rec := e.do(http.MethodPost, "/v1/events/batch", testToken, map[string]any{"events": tooMany}); rec.Code != http.StatusBadRequest {
		t.Errorf("oversized batch: status %d, want 400", rec.Code)
	}
}

func TestReadEndpointsRequireDashboardLogin(t *testing.T) {
	e := newTestServer(t)
	for _, path := range []string{"/v1/events", "/v1/summary", "/v1/fleet", "/v1/devices/TST-G01", "/v1/auth/me"} {
		if rec := e.do(http.MethodGet, path, "", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without auth: %d, want 401", path, rec.Code)
		}
		// A kiosk's device token must not read anything.
		if rec := e.do(http.MethodGet, path, testToken, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s with device token: %d, want 401", path, rec.Code)
		}
	}
	if rec := e.do(http.MethodGet, "/v1/health", "", nil); rec.Code != http.StatusOK {
		t.Errorf("health: %d, want 200 (public)", rec.Code)
	}
}

func TestLoginCookieFlow(t *testing.T) {
	e := newTestServer(t)
	if rec := e.do(http.MethodPost, "/v1/auth/login", "", map[string]string{"username": testUser, "password": "wrong-password"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad login: %d", rec.Code)
	}
	rec := e.do(http.MethodPost, "/v1/auth/login", "", map[string]string{"username": testUser, "password": testPass})
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	cookie := rec.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("cookie flags: %+v", cookie)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/auth/me", nil)
	req.AddCookie(cookie)
	me := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(me, req)
	if me.Code != http.StatusOK || !strings.Contains(me.Body.String(), testUser) {
		t.Fatalf("me with cookie: %d %s", me.Code, me.Body.String())
	}

	e.now = e.now.Add(2 * time.Hour) // past the 1h test session TTL
	me = httptest.NewRecorder()
	e.s.Handler().ServeHTTP(me, req)
	if me.Code != http.StatusUnauthorized {
		t.Fatalf("expired cookie: %d, want 401", me.Code)
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newTestServer(t)
	for i := 0; i < loginMaxFailures; i++ {
		e.do(http.MethodPost, "/v1/auth/login", "", map[string]string{"username": testUser, "password": "nope-nope-nope"})
	}
	rec := e.do(http.MethodPost, "/v1/auth/login", "", map[string]string{"username": testUser, "password": testPass})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after %d failures: %d, want 429", loginMaxFailures, rec.Code)
	}
	e.now = e.now.Add(loginWindow)
	if rec := e.do(http.MethodPost, "/v1/auth/login", "", map[string]string{"username": testUser, "password": testPass}); rec.Code != http.StatusOK {
		t.Fatalf("after window: %d, want 200", rec.Code)
	}
}

type fleetResp struct {
	Devices     []model.FleetDevice `json:"devices"`
	Unmapped24h int                 `json:"unmapped_24h"`
}

func (e *testEnv) fleet(t *testing.T) map[string]model.FleetDevice {
	t.Helper()
	var fr fleetResp
	e.get(t, "/v1/fleet", &fr)
	out := map[string]model.FleetDevice{}
	for _, d := range fr.Devices {
		out[d.DeviceID] = d
	}
	return out
}

func TestHeartbeatDrivesFleetStatus(t *testing.T) {
	e := newTestServer(t)

	f := e.fleet(t)
	if f["TST-G01"].Status != "NEVER" || f["TST-G01"].Color != "red" {
		t.Fatalf("before any contact: %+v", f["TST-G01"])
	}

	rec := e.do(http.MethodPost, "/v1/heartbeat", testToken, map[string]any{
		"device_id": "TST-G01", "app_version": "3.1.0", "sdk_version": "0.2.0", "uptime_s": 120,
	})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "server_time") {
		t.Fatalf("heartbeat: %d %s", rec.Code, rec.Body.String())
	}
	f = e.fleet(t)
	g1 := f["TST-G01"]
	if g1.Status != "ONLINE" || g1.Color != "green" || g1.AppVersion != "3.1.0" || g1.UptimeS != 120 {
		t.Fatalf("after heartbeat: %+v", g1)
	}

	e.now = e.now.Add(5 * time.Minute)
	if f = e.fleet(t); f["TST-G01"].Status != "STALE" || f["TST-G01"].Color != "yellow" {
		t.Fatalf("after 5m: %+v", f["TST-G01"])
	}
	e.now = e.now.Add(10 * time.Minute)
	if f = e.fleet(t); f["TST-G01"].Status != "DOWN" || f["TST-G01"].Color != "red" {
		t.Fatalf("after 15m: %+v", f["TST-G01"])
	}

	// An event also counts as a sign of life.
	postEvent(t, e, map[string]any{"device_id": "TST-G01", "event": "APP_STARTED"})
	if f = e.fleet(t); f["TST-G01"].Status != "ONLINE" {
		t.Fatalf("after event: %+v", f["TST-G01"])
	}
}

func TestFleetColorFromRecentErrors(t *testing.T) {
	e := newTestServer(t)
	e.do(http.MethodPost, "/v1/heartbeat", testToken, map[string]any{"device_id": "TST-G01"})
	postEvent(t, e, map[string]any{"device_id": "TST-G01", "event": "READER_NOT_READY"}) // HIGH

	g1 := e.fleet(t)["TST-G01"]
	if g1.Color != "red" || g1.WorstRecent != "HIGH" || g1.Errors1h != 1 || g1.LastError == nil {
		t.Fatalf("after HIGH error: %+v", g1)
	}

	// Outside the 10-minute error window the gate turns green again, but
	// the error still counts toward errors_24h and stays as last_error.
	e.now = e.now.Add(11 * time.Minute)
	e.do(http.MethodPost, "/v1/heartbeat", testToken, map[string]any{"device_id": "TST-G01"})
	g1 = e.fleet(t)["TST-G01"]
	if g1.Color != "green" || g1.Errors24h != 1 || g1.LastError.Event != "READER_NOT_READY" {
		t.Fatalf("after window: %+v", g1)
	}

	// Outbox backlog reported by the SDK → yellow.
	e.do(http.MethodPost, "/v1/heartbeat", testToken, map[string]any{"device_id": "TST-G01", "outbox_pending": 12})
	if g1 = e.fleet(t)["TST-G01"]; g1.Color != "yellow" {
		t.Fatalf("with outbox backlog: %+v", g1)
	}
}

func TestUnmappedEventsDoNotMarkDeviceAlive(t *testing.T) {
	e := newTestServer(t)
	// G01's token, but claiming to be G02.
	postEvent(t, e, map[string]any{"device_id": "TST-G02", "event": "PAYMENT_TIMEOUT"})
	var fr fleetResp
	e.get(t, "/v1/fleet", &fr)
	for _, d := range fr.Devices {
		if d.Status != "NEVER" {
			t.Errorf("%s: status %s, want NEVER", d.DeviceID, d.Status)
		}
	}
	if fr.Unmapped24h != 1 {
		t.Errorf("unmapped_24h = %d, want 1", fr.Unmapped24h)
	}
}

func TestEventFilters(t *testing.T) {
	e := newTestServer(t)
	postEvent(t, e, map[string]any{"device_id": "TST-G01", "event": "PAYMENT_TIMEOUT"})    // PAY, HIGH
	postEvent(t, e, map[string]any{"device_id": "TST-G01", "event": "APP_STARTED"})        // INFO
	postEvent(t, e, map[string]any{"device_id": "TST-G01", "event": "READER_DISCONNECTED"}) // READER
	e.do(http.MethodPost, "/v1/events", testToken2, map[string]any{"device_id": "TST-G02", "event": "PAYMENT_TIMEOUT"})

	var evs []model.StoredEvent
	e.get(t, "/v1/events?device_id=TST-G02", &evs)
	if len(evs) != 1 || evs[0].Gate != "G02" {
		t.Fatalf("device filter: %+v", evs)
	}
	e.get(t, "/v1/events?error_code=TST-G01-PAY-002", &evs)
	if len(evs) != 1 {
		t.Fatalf("error_code filter: %d events", len(evs))
	}
	e.get(t, "/v1/events?severity=MEDIUM&gate=G01", &evs)
	for _, ev := range evs {
		if ev.Severity == "INFO" {
			t.Fatalf("severity filter let INFO through")
		}
	}
	// Pagination: newest first, before_id walks back.
	e.get(t, "/v1/events?limit=2", &evs)
	if len(evs) != 2 {
		t.Fatalf("limit: %d", len(evs))
	}
	var older []model.StoredEvent
	e.get(t, fmt.Sprintf("/v1/events?limit=10&before_id=%d", evs[1].ID), &older)
	if len(older) != 2 || older[0].ID >= evs[1].ID {
		t.Fatalf("before_id page: %+v", older)
	}
}

func TestRetentionPurge(t *testing.T) {
	e := newTestServer(t)
	postEvent(t, e, map[string]any{"device_id": "TST-G01", "event": "PAYMENT_TIMEOUT"})
	e.now = e.now.Add(48 * time.Hour)
	postEvent(t, e, map[string]any{"device_id": "TST-G01", "event": "PAYMENT_TIMEOUT"})

	n, err := e.db.PurgeEventsBefore(e.now.Add(-24 * time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("purged %d, err %v; want 1", n, err)
	}
	var evs []model.StoredEvent
	e.get(t, "/v1/events", &evs)
	if len(evs) != 1 {
		t.Fatalf("remaining = %d, want 1", len(evs))
	}
}
