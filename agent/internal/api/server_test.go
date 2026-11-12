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
	postEvent(t, e, map[string]any{"device_id": "TST-G01", "event": "PAYMENT_TIMEOUT"})     // PAY, HIGH
	postEvent(t, e, map[string]any{"device_id": "TST-G01", "event": "APP_STARTED"})         // INFO
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

func TestFavicon(t *testing.T) {
	e := newTestServer(t)
	if rec := e.do(http.MethodGet, "/favicon.ico", "", nil); rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/static/favicon.svg" {
		t.Fatalf("/favicon.ico: %d → %q", rec.Code, rec.Header().Get("Location"))
	}
	rec := e.do(http.MethodGet, "/static/favicon.svg", "", nil)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "image/svg+xml") {
		t.Fatalf("/static/favicon.svg: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestDashboardServedWithSecurityHeaders(t *testing.T) {
	e := newTestServer(t)
	for path, want := range map[string]string{"/": "<title>Parkways Sentinel</title>", "/static/js/core/dom.js": "textContent"} {
		rec := e.do(http.MethodGet, path, "", nil)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("GET %s: %d, body missing %q", path, rec.Code, want)
		}
		if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
			t.Errorf("GET %s: CSP = %q", path, csp)
		}
	}
	if rec := e.do(http.MethodGet, "/nope", "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown path: %d, want 404", rec.Code)
	}
}

func TestDashboardETagRevalidation(t *testing.T) {
	e := newTestServer(t)
	for _, path := range []string{"/", "/static/js/main.js", "/static/app.css"} {
		rec := e.do(http.MethodGet, path, "", nil)
		tag := rec.Header().Get("ETag")
		if rec.Code != http.StatusOK || tag == "" {
			t.Fatalf("GET %s: %d, ETag %q", path, rec.Code, tag)
		}
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("If-None-Match", tag)
		again := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(again, req)
		if again.Code != http.StatusNotModified {
			t.Errorf("GET %s with matching ETag: %d, want 304", path, again.Code)
		}
	}
}

func TestSeverityAliasesAndUnknownValues(t *testing.T) {
	e := newTestServer(t)
	cases := []struct {
		sent, want string
		hint       bool
	}{
		{"ERROR", "HIGH", false},     // Python logging level names
		{"warning", "MEDIUM", false}, // any case
		{"CRITICAL", "CRITICAL", false},
		{"URGENT", "HIGH", true}, // unknown: PAYMENT_TIMEOUT's default, original kept
	}
	for i, c := range cases {
		ev := postEvent(t, e, map[string]any{
			"device_id": "TST-G01", "event_id": fmt.Sprintf("sev-%d", i), "event": "PAYMENT_TIMEOUT", "severity": c.sent,
		})
		if ev.Severity != c.want {
			t.Errorf("severity %q stored as %q, want %q", c.sent, ev.Severity, c.want)
		}
		if got := ev.Context["severity_hint"]; (got != nil) != c.hint || c.hint && got != c.sent {
			t.Errorf("severity %q: context severity_hint = %v", c.sent, got)
		}
	}

	// A new event learned with a logging level as its default severity.
	ev := postEvent(t, e, map[string]any{"device_id": "TST-G01", "event_id": "sev-new", "event": "LOG_APP__DEBIT", "severity": "error"})
	if ev.Severity != "HIGH" {
		t.Errorf("learned event severity = %q, want HIGH", ev.Severity)
	}
}

func TestLongEventNameIsTruncated(t *testing.T) {
	e := newTestServer(t)
	long := strings.Repeat("É", maxEventName+50)
	ev := postEvent(t, e, map[string]any{"device_id": "TST-G01", "event_id": "long-1", "event": long})
	if got := []rune(ev.Event); len(got) != maxEventName {
		t.Fatalf("stored event name has %d characters, want %d", len(got), maxEventName)
	}
	again := postEvent(t, e, map[string]any{"device_id": "TST-G01", "event_id": "long-2", "event": long + "-other-suffix"})
	if again.ErrorCode != ev.ErrorCode {
		t.Errorf("same truncated name got a second code: %s vs %s", again.ErrorCode, ev.ErrorCode)
	}
}

func TestCatchAllWarningsAreLowButErrorsStayHigh(t *testing.T) {
	e := newTestServer(t)
	w := postEvent(t, e, map[string]any{"device_id": "TST-G01", "event": "LOG_CASHLESS__OPEN_CONNECTION", "severity": "WARNING"})
	if w.Severity != "LOW" {
		t.Fatalf("catch-all warning: %s", w.Severity)
	}
	er := postEvent(t, e, map[string]any{"device_id": "TST-G01", "event": "LOG_CASHLESS__API_OPEN_GATE", "severity": "ERROR"})
	if er.Severity != "HIGH" {
		t.Fatalf("catch-all error: %s", er.Severity)
	}
	// A registry event is not touched by the rule.
	if ev := postEvent(t, e, map[string]any{"device_id": "TST-G01", "event": "CARD_READ_TIMEOUT"}); ev.Severity != "MEDIUM" {
		t.Fatalf("registry MEDIUM: %s", ev.Severity)
	}
	// Customer behaviour and catch-all warnings never colour a gate.
	e.do(http.MethodPost, "/v1/heartbeat", testToken2, map[string]any{"device_id": "TST-G02"})
	for _, ev := range []map[string]any{
		{"device_id": "TST-G02", "event": "INSUFFICIENT_BALANCE"},
		{"device_id": "TST-G02", "event": "LOG_CASHLESS__OPEN_CONNECTION", "severity": "WARNING"},
	} {
		if rec := e.do(http.MethodPost, "/v1/events", testToken2, ev); rec.Code != http.StatusCreated {
			t.Fatalf("post %v: %d %s", ev, rec.Code, rec.Body.String())
		}
	}
	if f := e.fleet(t); f["TST-G02"].Color != "green" || f["TST-G02"].Errors1h != 2 {
		t.Fatalf("after LOW events: %+v", f["TST-G02"])
	}
}

func TestStackIsStoredAndCapped(t *testing.T) {
	e := newTestServer(t)
	stack := "Traceback (most recent call last):\n  File \"app/api/payment.py\", line 705, in debit\n    raise RuntimeError(\"x\")\nRuntimeError: x\n"
	ev := postEvent(t, e, map[string]any{"device_id": "TST-G01", "event": "APP_EXCEPTION", "stack": stack})
	if ev.Stack != stack || ev.Severity != "HIGH" || ev.ErrorCode != "TST-G01-APP-003" {
		t.Fatalf("stored: %+v", ev)
	}
	var got []model.StoredEvent
	e.get(t, "/v1/events?device_id=TST-G01", &got)
	if len(got) != 1 || got[0].Stack != stack {
		t.Fatalf("read back: %+v", got)
	}
	long := strings.Repeat("frame\n", maxStack)
	ev = postEvent(t, e, map[string]any{"device_id": "TST-G01", "event": "APP_EXCEPTION", "stack": long})
	if len([]rune(ev.Stack)) > maxStack+32 || !strings.HasSuffix(ev.Stack, "(dipotong)") {
		t.Fatalf("stack not capped: %d", len(ev.Stack))
	}
}

func TestStatusTrackerRecordsChanges(t *testing.T) {
	e := newTestServer(t)
	started := e.now
	e.do(http.MethodPost, "/v1/heartbeat", testToken, map[string]any{"device_id": "TST-G01", "platform": "Tongbao"})
	track := func() {
		if err := e.s.TrackStatus(e.now, started); err != nil {
			t.Fatal(err)
		}
	}
	track()
	f := e.fleet(t)
	if f["TST-G01"].StatusSince != started.Format(time.RFC3339) || f["TST-G01"].Platform != "tongbao" {
		t.Fatalf("online since / platform: %+v", f["TST-G01"])
	}
	if f["TST-G02"].StatusSince != "" {
		t.Fatalf("NEVER must not be recorded: %+v", f["TST-G02"])
	}

	// Within the start grace nothing but ONLINE is recorded.
	e.now = e.now.Add(5 * time.Minute)
	track()
	if f = e.fleet(t); f["TST-G01"].Status != "STALE" || f["TST-G01"].StatusSince != "" {
		t.Fatalf("stale inside grace: %+v", f["TST-G01"])
	}
	e.now = e.now.Add(10 * time.Minute) // 15m after start: DOWN, past grace
	track()
	downAt := e.now.Format(time.RFC3339)
	if f = e.fleet(t); f["TST-G01"].Status != "DOWN" || f["TST-G01"].StatusSince != downAt {
		t.Fatalf("down since: %+v", f["TST-G01"])
	}
	e.now = e.now.Add(time.Minute)
	track() // unchanged status: no new row
	e.do(http.MethodPost, "/v1/heartbeat", testToken, map[string]any{"device_id": "TST-G01"})
	track()
	var dev struct {
		Device  model.FleetDevice    `json:"device"`
		History []model.StatusChange `json:"status_history"`
	}
	e.get(t, "/v1/devices/TST-G01", &dev)
	if dev.Device.Status != "ONLINE" || dev.Device.StatusSince != e.now.Format(time.RFC3339) {
		t.Fatalf("online again: %+v", dev.Device)
	}
	want := []string{"ONLINE", "DOWN", "ONLINE"}
	if len(dev.History) != len(want) {
		t.Fatalf("history: %+v", dev.History)
	}
	for i, h := range dev.History {
		if h.Status != want[i] {
			t.Fatalf("history[%d] = %s, want %s (%+v)", i, h.Status, want[i], dev.History)
		}
	}
}

func enableEnroll(e *testEnv) {
	e.s.cfg.Site = config.Site{Code: "PCC", Name: "Plaza Central City", EnrollKey: "site-key-0123456789abcdef"}
}

func enroll(t *testing.T, e *testEnv, body map[string]any) (int, map[string]any) {
	t.Helper()
	rec := e.do(http.MethodPost, "/v1/enroll", "", body)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestEnrollIssuesTokenUnderSiteCode(t *testing.T) {
	e := newTestServer(t)
	if code, out := enroll(t, e, map[string]any{"site_key": "x", "device_id": "PCC-G01", "gate": "G01"}); code != http.StatusNotFound {
		t.Fatalf("enroll while off: %d %v", code, out)
	}
	enableEnroll(e)

	code, out := enroll(t, e, map[string]any{"site_key": "site-key-0123456789abcdef", "device_id": "pcc-kiosk-01",
		"gate": "g01", "pos": "POS01", "gate_name": "Gate Keluar Timur", "platform": "Tongbao", "app_version": "3.38.0"})
	if code != http.StatusCreated || out["location"] != "PCC" || out["gate"] != "G01" || out["site_name"] != "Plaza Central City" {
		t.Fatalf("enroll: %d %v", code, out)
	}
	token, _ := out["token"].(string)
	if len(token) != 64 {
		t.Fatalf("token: %q", token)
	}

	// The token works like a static one, and the event lands under PCC.
	rec := e.do(http.MethodPost, "/v1/events", token, map[string]any{"device_id": "pcc-kiosk-01", "event": "READER_NOT_READY"})
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"error_code":"PCC-G01-READER-003"`) {
		t.Fatalf("event with enrolled token: %d %s", rec.Code, rec.Body.String())
	}
	f := e.fleet(t)
	d := f["pcc-kiosk-01"]
	if !d.Enrolled || d.Name != "Gate Keluar Timur" || d.Platform != "tongbao" || d.AppVersion != "3.38.0" || d.POS != "POS01" {
		t.Fatalf("fleet row: %+v", d)
	}

	// Re-enrolling rotates the token: the old one stops working.
	_, out2 := enroll(t, e, map[string]any{"site_key": "site-key-0123456789abcdef", "device_id": "pcc-kiosk-01", "gate": "G01"})
	token2, _ := out2["token"].(string)
	if token2 == "" || token2 == token {
		t.Fatalf("re-enroll token: %q", token2)
	}
	if rec := e.do(http.MethodPost, "/v1/heartbeat", token, map[string]any{"device_id": "pcc-kiosk-01"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("old token after re-enroll: %d", rec.Code)
	}
	if rec := e.do(http.MethodPost, "/v1/heartbeat", token2, map[string]any{"device_id": "pcc-kiosk-01"}); rec.Code != http.StatusOK {
		t.Fatalf("new token: %d %s", rec.Code, rec.Body.String())
	}

	// A static devices.yaml device cannot be taken over.
	if code, _ := enroll(t, e, map[string]any{"site_key": "site-key-0123456789abcdef", "device_id": "TST-G01", "gate": "G09"}); code != http.StatusConflict {
		t.Fatalf("takeover of static device: %d", code)
	}

	// Enrolled devices survive a restart (loaded from the database).
	reg2 := devices.Empty()
	enrolled, hashes, err := e.db.EnrolledDevices()
	if err != nil || len(enrolled) != 1 {
		t.Fatalf("persisted: %v %v", enrolled, err)
	}
	for i, en := range enrolled {
		if err := reg2.PutEnrolled(en, hashes[i]); err != nil {
			t.Fatal(err)
		}
	}
	if d, ok := reg2.ByToken(token2); !ok || d.Location != "PCC" || d.Gate != "G01" {
		t.Fatalf("after reload: %+v %v", d, ok)
	}
}

func TestEnrollWrongKeyIsRateLimited(t *testing.T) {
	e := newTestServer(t)
	enableEnroll(e)
	var code int
	for i := 0; i < loginMaxFailures+1; i++ {
		code, _ = enroll(t, e, map[string]any{"site_key": "wrong-key-0123456789", "device_id": "x", "gate": "G01"})
		if i < loginMaxFailures && code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, code)
		}
	}
	if code != http.StatusTooManyRequests {
		t.Fatalf("after %d failures: %d", loginMaxFailures, code)
	}
	if code, _ := enroll(t, e, map[string]any{"site_key": "site-key-0123456789abcdef", "device_id": "bad id!", "gate": "G01"}); code != http.StatusTooManyRequests {
		t.Fatalf("blocked ip must stay blocked even with the right key: %d", code)
	}
}

func TestHeartbeatMovesEnrolledDeviceNotStatic(t *testing.T) {
	e := newTestServer(t)
	enableEnroll(e)
	_, out := enroll(t, e, map[string]any{"site_key": "site-key-0123456789abcdef", "device_id": "pcc-kiosk-02", "gate": "G02"})
	token := out["token"].(string)

	// The app's settings changed: gate G02 -> G04, new name.
	rec := e.do(http.MethodPost, "/v1/heartbeat", token, map[string]any{"device_id": "pcc-kiosk-02", "gate": "g04", "pos": "POS02", "gate_name": "Gate Motor"})
	if rec.Code != http.StatusOK {
		t.Fatalf("heartbeat: %d %s", rec.Code, rec.Body.String())
	}
	d := e.fleet(t)["pcc-kiosk-02"]
	if d.Gate != "G04" || d.POS != "POS02" || d.Name != "Gate Motor" {
		t.Fatalf("moved: %+v", d)
	}
	ev := e.do(http.MethodPost, "/v1/events", token, map[string]any{"device_id": "pcc-kiosk-02", "event": "APP_STARTED"})
	if !strings.Contains(ev.Body.String(), `"error_code":"PCC-G04-APP-001"`) {
		t.Fatalf("event after move: %s", ev.Body.String())
	}
	enrolled, _, _ := e.db.EnrolledDevices()
	if enrolled[0].Gate != "G04" {
		t.Fatalf("not persisted: %+v", enrolled)
	}

	// A static device keeps its operator-assigned gate; only the name follows.
	e.do(http.MethodPost, "/v1/heartbeat", testToken, map[string]any{"device_id": "TST-G01", "gate": "G07", "gate_name": "Gate Uji"})
	s := e.fleet(t)["TST-G01"]
	if s.Gate != "G01" || s.Name != "Gate Uji" {
		t.Fatalf("static device: %+v", s)
	}
	// Fleet carries site and agent info for the dashboard header.
	var raw map[string]any
	e.get(t, "/v1/fleet", &raw)
	site := raw["site"].(map[string]any)
	agent := raw["agent"].(map[string]any)
	if site["code"] != "PCC" || site["enroll_enabled"] != true || agent["db_size_bytes"].(float64) <= 0 {
		t.Fatalf("site/agent: %v %v", site, agent)
	}
}
