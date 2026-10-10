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

type sent struct{ chat, text string }

type fakeSender struct {
	msgs     []sent
	fail     bool
	attempts int
}

func (f *fakeSender) Send(_ context.Context, chat, text string) error {
	f.attempts++
	if f.fail {
		return errors.New("network down")
	}
	f.msgs = append(f.msgs, sent{chat, text})
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
		{Name: "lbi", ChatID: "lbi-chat", MinSeverity: "HIGH", Locations: []string{"LBI"}},
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
	if err := tg.Send(context.Background(), "-100", "<b>halo</b>"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/bot123:SECRET/sendMessage" || gotChat != "-100" || gotText != "<b>halo</b>" || gotMode != "" {
		t.Fatalf("path %q chat %q text %q mode %q", gotPath, gotChat, gotText, gotMode)
	}

	srv.Close() // now unreachable: the error must not leak the token
	if err := tg.Send(context.Background(), "-100", "x"); err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("error = %v", err)
	}
}
