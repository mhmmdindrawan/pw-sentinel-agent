package alert

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"parkways-sentinel/agent/internal/config"
	"parkways-sentinel/agent/internal/model"
	"parkways-sentinel/agent/internal/store"
)

type sent struct {
	chat   string
	thread int
	text   string
}

type fakeSender struct {
	msgs     []sent
	fail     bool
	attempts int
	// chat id -> answer the next send to it with HTTP 429 and this wait
	limit map[string]time.Duration
}

func (f *fakeSender) Send(_ context.Context, to Destination, text string) error {
	f.attempts++
	if f.fail {
		return errors.New("network down")
	}
	if d, ok := f.limit[to.ChatID]; ok {
		delete(f.limit, to.ChatID)
		return &RateLimitError{RetryAfter: d}
	}
	f.msgs = append(f.msgs, sent{to.ChatID, to.ThreadID, text})
	return nil
}

func (f *fakeSender) to(chat string) []string {
	var out []string
	for _, m := range f.msgs {
		if m.chat == chat {
			out = append(out, m.text)
		}
	}
	return out
}

type env struct {
	t      *testing.T
	db     *store.Store
	sender *fakeSender
	fleet  map[string]model.FleetDevice
	now    time.Time
	a      *Alerter
	nextID int
}

var testCfg = config.Alert{
	IntervalSeconds: 30, CooldownMinutes: 15, OutboxBacklogThreshold: 100, Timezone: "Asia/Jakarta",
	Routes: []config.AlertRoute{
		{Name: "noc", ChatID: "noc-chat", MinSeverity: "HIGH"},
		{Name: "pribadi", ChatID: "me-chat", MinSeverity: "MEDIUM"},
		{Name: "lbi", ChatID: "lbi-chat", ThreadID: 7, MinSeverity: "HIGH", Locations: []string{"LBI"}},
	},
}

const grace = 10 * time.Minute

func newEnv(t *testing.T) *env {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e := &env{t: t, db: db, sender: &fakeSender{}, fleet: map[string]model.FleetDevice{},
		now: time.Date(2026, 10, 7, 5, 0, 0, 0, time.UTC)}
	e.setDevice("STI-G03-POS01", "STI", "G03", "ONLINE", 0)
	e.restart()
	return e
}

// restart builds a fresh Alerter on the same database, like an agent restart.
func (e *env) restart() {
	a, err := New(testCfg, "http://100.64.0.1:8787", grace, e.fleetFunc, e.db, e.sender, e.now)
	if err != nil {
		e.t.Fatal(err)
	}
	e.a = a
}

func (e *env) fleetFunc(time.Time) ([]model.FleetDevice, error) {
	var out []model.FleetDevice
	for _, d := range e.fleet {
		out = append(out, d)
	}
	return out, nil
}

func (e *env) setDevice(id, loc, gate, st string, outbox int) {
	d := model.FleetDevice{DeviceID: id, Location: loc, Gate: gate, Status: st, LastSeen: e.now.Format(time.RFC3339)}
	d.OutboxPending = outbox
	e.fleet[id] = d
}

func (e *env) event(device, loc, gate, name, code, sev string) {
	e.nextID++
	_, err := e.db.InsertEvent(model.StoredEvent{
		DeviceID: device, EventID: fmt.Sprintf("ev-%d", e.nextID), Location: loc, Gate: gate,
		Category: "X", ErrorCode: code, Event: name, Severity: sev, Message: "msg " + name,
		ReceivedAt: e.now.Format(time.RFC3339),
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) tick(d time.Duration) {
	e.t.Helper()
	e.now = e.now.Add(d)
	if err := e.a.Tick(context.Background(), e.now); err != nil {
		e.t.Fatal(err)
	}
}

func TestFirstRunDoesNotReplayHistory(t *testing.T) {
	e := newEnv(t)
	e.event("STI-G03-POS01", "STI", "G03", "READER_NOT_READY", "STI-G03-READER-003", "HIGH")
	e.tick(time.Second)
	if len(e.sender.msgs) != 0 {
		t.Fatalf("history replayed: %v", e.sender.msgs)
	}
}

func TestEventRoutingBySeverityAndLocation(t *testing.T) {
	e := newEnv(t)
	e.tick(time.Second) // baseline
	e.event("STI-G03-POS01", "STI", "G03", "READER_NOT_READY", "STI-G03-READER-003", "HIGH")
	e.event("STI-G03-POS01", "STI", "G03", "MQTT_DISCONNECTED", "STI-G03-MQTT-001", "MEDIUM")
	e.event("STI-G03-POS01", "STI", "G03", "APP_STARTED", "STI-G03-APP-001", "INFO")
	e.tick(30 * time.Second)

	noc := e.sender.to("noc-chat")
	if len(noc) != 1 || !strings.Contains(noc[0], "READER_NOT_READY") || strings.Contains(noc[0], "MQTT") {
		t.Fatalf("noc got %q", noc)
	}
	if !strings.Contains(noc[0], "http://100.64.0.1:8787/#/device/STI-G03-POS01") {
		t.Errorf("no dashboard link: %q", noc[0])
	}
	if !strings.Contains(noc[0], "12:00 WIB") {
		t.Errorf("time not in Asia/Jakarta: %q", noc[0])
	}
	me := e.sender.to("me-chat")
	if len(me) != 1 || !strings.Contains(me[0], "READER_NOT_READY") || !strings.Contains(me[0], "MQTT_DISCONNECTED") {
		t.Fatalf("pribadi got %q", me)
	}
	if strings.Contains(me[0], "APP_STARTED") {
		t.Error("INFO event alerted")
	}
	if got := e.sender.to("lbi-chat"); len(got) != 0 {
		t.Fatalf("LBI-only route got STI alert: %q", got)
	}
}

func TestCooldownAndInTickDedupe(t *testing.T) {
	e := newEnv(t)
	e.tick(time.Second)
	for i := 0; i < 5; i++ {
		e.event("STI-G03-POS01", "STI", "G03", "READER_NOT_READY", "STI-G03-READER-003", "HIGH")
	}
	e.tick(30 * time.Second)
	noc := e.sender.to("noc-chat")
	if len(noc) != 1 || strings.Count(noc[0], "STI-G03-READER-003 ·") != 1 || !strings.Contains(noc[0], "4 alert berulang") {
		t.Fatalf("burst: %q", noc)
	}

	e.event("STI-G03-POS01", "STI", "G03", "READER_NOT_READY", "STI-G03-READER-003", "HIGH")
	e.tick(5 * time.Minute)
	if n := len(e.sender.to("noc-chat")); n != 1 {
		t.Fatalf("within cooldown: %d messages, want still 1", n)
	}

	e.event("STI-G03-POS01", "STI", "G03", "READER_NOT_READY", "STI-G03-READER-003", "HIGH")
	e.tick(15 * time.Minute)
	if n := len(e.sender.to("noc-chat")); n != 2 {
		t.Fatalf("after cooldown: %d messages, want 2", n)
	}
}

func TestStatusTransitionsAfterGrace(t *testing.T) {
	e := newEnv(t)
	e.tick(time.Second) // baseline: ONLINE recorded

	e.setDevice("STI-G03-POS01", "STI", "G03", "DOWN", 0)
	e.tick(time.Minute)
	if len(e.sender.msgs) != 0 {
		t.Fatalf("status alert during grace: %v", e.sender.msgs)
	}

	e.tick(grace) // still DOWN, grace over → alert now
	noc := e.sender.to("noc-chat")
	if len(noc) != 1 || !strings.Contains(noc[0], "MATI") {
		t.Fatalf("down alert: %q", noc)
	}
	e.tick(time.Minute) // still DOWN: no repeat
	if len(e.sender.to("noc-chat")) != 1 {
		t.Fatal("DOWN alert repeated")
	}

	e.setDevice("STI-G03-POS01", "STI", "G03", "ONLINE", 0)
	e.tick(time.Minute)
	noc = e.sender.to("noc-chat")
	if len(noc) != 2 || !strings.Contains(noc[1], "kembali online") {
		t.Fatalf("recovery alert: %q", noc)
	}
}

func TestRestartDoesNotFloodWithDown(t *testing.T) {
	e := newEnv(t)
	e.tick(grace + time.Minute) // ONLINE recorded after grace

	// Agent restarts; until kiosks heartbeat again they all look DOWN.
	e.restart()
	e.setDevice("STI-G03-POS01", "STI", "G03", "DOWN", 0)
	e.tick(time.Second)
	e.setDevice("STI-G03-POS01", "STI", "G03", "ONLINE", 0)
	e.tick(time.Minute)
	e.tick(grace)
	if len(e.sender.msgs) != 0 {
		t.Fatalf("restart produced alerts: %v", e.sender.msgs)
	}
}

func TestOutboxBacklogAlert(t *testing.T) {
	e := newEnv(t)
	e.setDevice("STI-G03-POS01", "STI", "G03", "ONLINE", 250)
	e.tick(time.Second)
	if got := e.sender.to("me-chat"); len(got) != 1 || !strings.Contains(got[0], "250 event tertahan") {
		t.Fatalf("backlog alert: %q", got)
	}
	if got := e.sender.to("noc-chat"); len(got) != 0 {
		t.Fatalf("MEDIUM backlog reached HIGH-only route: %q", got)
	}
}

func TestFailedSendIsRetried(t *testing.T) {
	e := newEnv(t)
	e.tick(time.Second)
	e.event("STI-G03-POS01", "STI", "G03", "READER_NOT_READY", "STI-G03-READER-003", "HIGH")
	e.sender.fail = true
	e.tick(30 * time.Second)
	e.sender.fail = false
	e.tick(30 * time.Second)
	if got := e.sender.to("noc-chat"); len(got) != 1 || !strings.Contains(got[0], "READER_NOT_READY") {
		t.Fatalf("retry: %q", got)
	}
}

func TestUnreachableTelegramIsTriedOncePerTick(t *testing.T) {
	e := newEnv(t)
	e.tick(time.Second)
	e.sender.fail = true
	for i := 0; i < 5; i++ {
		e.event("STI-G03-POS01", "STI", "G03", fmt.Sprintf("EV_%d", i), fmt.Sprintf("STI-G03-APP-%03d", i+10), "HIGH")
		before := e.sender.attempts
		e.tick(30 * time.Second)
		// noc and pribadi want HIGH events from STI; lbi doesn't.
		if got := e.sender.attempts - before; got != 2 {
			t.Fatalf("tick %d: %d send attempts, want one per route (2)", i, got)
		}
	}
	e.sender.fail = false
	e.tick(30 * time.Second)
	if got := len(e.sender.to("noc-chat")); got != maxSendsPerChatPerTick {
		t.Fatalf("first tick after recovery sent %d to noc, want %d (backlog is paced)", got, maxSendsPerChatPerTick)
	}
	e.tick(30 * time.Second)
	got := e.sender.to("noc-chat")
	if len(got) != 5 {
		t.Fatalf("after recovery noc got %d messages, want all 5", len(got))
	}
	for i, msg := range got {
		if !strings.Contains(msg, fmt.Sprintf("EV_%d", i)) {
			t.Errorf("message %d out of order: %q", i, msg)
		}
	}
}

// queueBacklog leaves n undelivered messages per route while Telegram is
// unreachable, one HIGH event per tick.
func (e *env) queueBacklog(n int) {
	e.sender.fail = true
	for i := 0; i < n; i++ {
		e.event("STI-G03-POS01", "STI", "G03", fmt.Sprintf("EV_%d", i), fmt.Sprintf("STI-G03-APP-%03d", i+10), "HIGH")
		e.tick(30 * time.Second)
	}
	e.sender.fail = false
}

func TestBacklogDrainStaysUnderGroupLimit(t *testing.T) {
	e := newEnv(t)
	e.tick(time.Second)
	e.queueBacklog(maxPendingPerRoute)

	// Tick far faster than configured: the per-minute limit must still hold.
	var at []time.Time
	for i := 0; i < 60 && len(at) < maxPendingPerRoute; i++ {
		before := len(e.sender.to("noc-chat"))
		e.tick(5 * time.Second)
		for n := len(e.sender.to("noc-chat")); n > before; n-- {
			at = append(at, e.now)
		}
	}
	if len(at) != maxPendingPerRoute {
		t.Fatalf("delivered %d of %d", len(at), maxPendingPerRoute)
	}
	for i := range at {
		n := 0
		for _, u := range at[i:] {
			if u.Sub(at[i]) < time.Minute {
				n++
			}
		}
		if n > maxSendsPerChatPerMinute {
			t.Fatalf("%d messages within a minute from %s, limit %d", n, at[i].Format("15:04:05"), maxSendsPerChatPerMinute)
		}
	}
	got := e.sender.to("noc-chat")
	for i, msg := range got {
		if !strings.Contains(msg, fmt.Sprintf("EV_%d ", i)) {
			t.Errorf("message %d out of order: %q", i, msg)
		}
	}
}

func TestRateLimitIsHonoured(t *testing.T) {
	e := newEnv(t)
	e.tick(time.Second)
	e.event("STI-G03-POS01", "STI", "G03", "READER_NOT_READY", "STI-G03-READER-003", "HIGH")
	e.sender.limit = map[string]time.Duration{"noc-chat": 75 * time.Second}
	e.tick(30 * time.Second)
	if got := e.sender.to("me-chat"); len(got) != 1 {
		t.Fatalf("other chat held back by noc's limit: %q", got)
	}

	e.event("STI-G03-POS01", "STI", "G03", "MQTT_CONNECT_FAILED", "STI-G03-MQTT-002", "HIGH")
	before := e.sender.attempts
	e.tick(30 * time.Second) // 30s into a 75s wait
	if got := e.sender.attempts - before; got != 1 {
		t.Fatalf("%d send attempts during the wait, want 1 (me-chat only)", got)
	}

	e.tick(30 * time.Second) // 60s: still waiting
	if got := e.sender.to("noc-chat"); len(got) != 0 {
		t.Fatalf("sent during retry_after: %q", got)
	}
	e.tick(30 * time.Second) // 90s: wait over, both go out in order
	got := e.sender.to("noc-chat")
	if len(got) != 2 || !strings.Contains(got[0], "READER_NOT_READY") || !strings.Contains(got[1], "MQTT_CONNECT_FAILED") {
		t.Fatalf("after wait: %q", got)
	}
}

func TestUndeliveredSurvivesRestart(t *testing.T) {
	e := newEnv(t)
	e.tick(time.Second)
	e.event("STI-G03-POS01", "STI", "G03", "READER_NOT_READY", "STI-G03-READER-003", "HIGH")
	e.sender.fail = true
	e.tick(30 * time.Second)

	e.restart()
	e.sender.fail = false
	e.tick(time.Minute) // 90s after it was written: not late yet
	got := e.sender.to("noc-chat")
	if len(got) != 1 || !strings.Contains(got[0], "READER_NOT_READY") {
		t.Fatalf("after restart noc got %q", got)
	}
	if strings.Contains(got[0], "terlambat") {
		t.Errorf("marked late after 90s: %q", got[0])
	}

	// Delivered: nothing left to resend after another restart.
	e.restart()
	e.tick(30 * time.Second)
	if n := len(e.sender.to("noc-chat")); n != 1 {
		t.Fatalf("delivered message resent after restart: %d messages", n)
	}
}

func TestLateAndExpiredMessages(t *testing.T) {
	e := newEnv(t)
	e.tick(time.Second)
	e.event("STI-G03-POS01", "STI", "G03", "READER_NOT_READY", "STI-G03-READER-003", "HIGH")
	e.sender.fail = true
	e.tick(30 * time.Second) // written 12:00:31 WIB
	e.tick(10 * time.Minute)
	e.sender.fail = false
	e.tick(30 * time.Second)
	got := e.sender.to("noc-chat")
	if len(got) != 1 || !strings.Contains(got[0], "(terlambat terkirim; dibuat ") || !strings.Contains(got[0], "12:00 WIB)") {
		t.Fatalf("late message: %q", got)
	}

	e.event("STI-G03-POS01", "STI", "G03", "MQTT_CONNECT_FAILED", "STI-G03-MQTT-002", "HIGH")
	e.sender.fail = true
	e.tick(30 * time.Second)
	e.restart()
	e.tick(maxPendingAge + time.Minute)
	e.sender.fail = false
	e.tick(30 * time.Second)
	if n := len(e.sender.to("noc-chat")); n != 1 {
		t.Fatalf("message older than %s still sent: %q", maxPendingAge, e.sender.to("noc-chat"))
	}
}

func TestRouteThreadID(t *testing.T) {
	e := newEnv(t)
	e.setDevice("LBI-G01-POS01", "LBI", "G01", "ONLINE", 0)
	e.tick(time.Second)
	e.event("LBI-G01-POS01", "LBI", "G01", "READER_NOT_READY", "LBI-G01-READER-003", "HIGH")
	e.tick(30 * time.Second)
	for _, m := range e.sender.msgs {
		want := 0
		if m.chat == "lbi-chat" {
			want = 7
		}
		if m.thread != want {
			t.Errorf("%s: thread %d, want %d", m.chat, m.thread, want)
		}
	}
	if len(e.sender.to("lbi-chat")) != 1 {
		t.Fatalf("lbi got %v", e.sender.msgs)
	}

	bad := testCfg
	bad.Routes = []config.AlertRoute{{Name: "x", ChatID: "c", MinSeverity: "HIGH", ThreadID: -1}}
	if _, err := New(bad, "", grace, e.fleetFunc, e.db, e.sender, e.now); err == nil {
		t.Error("negative thread_id accepted")
	}
}

func TestTelegramErrors(t *testing.T) {
	var reply string
	var status int
	var gotThread string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotThread = r.Form.Get("message_thread_id")
		w.WriteHeader(status)
		w.Write([]byte(reply))
	}))
	defer srv.Close()
	tg := NewTelegram("123:SECRET")
	tg.BaseURL = srv.URL

	status, reply = 429, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 35","parameters":{"retry_after":35}}`
	err := tg.Send(context.Background(), Destination{ChatID: "-100", ThreadID: 12}, "x")
	var rl *RateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter != 35*time.Second {
		t.Fatalf("429: err = %v", err)
	}
	if gotThread != "12" {
		t.Errorf("message_thread_id = %q, want 12", gotThread)
	}

	status, reply = 429, `{"ok":false}`
	if err := tg.Send(context.Background(), Destination{ChatID: "-100"}, "x"); !errors.As(err, &rl) || rl.RetryAfter <= 0 {
		t.Fatalf("429 without retry_after: err = %v", err)
	}
	if gotThread != "" {
		t.Errorf("message_thread_id sent for thread 0: %q", gotThread)
	}

	status, reply = 400, `{"ok":false,"error_code":400,"description":"Bad Request: group chat was upgraded to a supergroup chat","parameters":{"migrate_to_chat_id":-1001234567890}}`
	if err := tg.Send(context.Background(), Destination{ChatID: "-4321"}, "x"); err == nil || !strings.Contains(err.Error(), "-1001234567890") {
		t.Fatalf("migrate: err = %v", err)
	}
}

func TestTelegramSender(t *testing.T) {
	var gotPath, gotChat, gotText, gotMode string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotPath, gotChat, gotText, gotMode = r.URL.Path, r.Form.Get("chat_id"), r.Form.Get("text"), r.Form.Get("parse_mode")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	tg := NewTelegram("123:SECRET")
	tg.BaseURL = srv.URL
	if err := tg.Send(context.Background(), Destination{ChatID: "-100"}, "<b>halo</b>"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/bot123:SECRET/sendMessage" || gotChat != "-100" || gotText != "<b>halo</b>" || gotMode != "" {
		t.Fatalf("path %q chat %q text %q mode %q", gotPath, gotChat, gotText, gotMode)
	}

	srv.Close() // now unreachable: the error must not leak the token
	if err := tg.Send(context.Background(), Destination{ChatID: "-100"}, "x"); err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("error = %v", err)
	}
}
