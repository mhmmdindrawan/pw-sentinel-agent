// Command sentinel-demo builds a self-contained demo/test installation:
// config, devices, a dashboard user, and a database with realistic,
// deterministic history — every gate state the dashboard can show (green,
// yellow, red, STALE, DOWN, NEVER) plus 30 days of events.
//
// It is a development tool (E2E tests, screenshots, NOC training) and is not
// part of release builds.
//
//	sentinel-demo -dir ./demo -registry agent/registry.yaml [-gates 200]
//	cd demo && ../sentinel-agent -config config.yaml
//	sentinel-demo -dir ./demo -heartbeat http://127.0.0.1:18787   # keep live gates ONLINE
//
// Dashboard login: demo / demo-password-1. Device tokens: "demo-token-<device_id>".
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"parkways-sentinel/agent/internal/auth"
	"parkways-sentinel/agent/internal/model"
	"parkways-sentinel/agent/internal/registry"
	"parkways-sentinel/agent/internal/store"
)

const (
	DemoUser     = "demo"
	DemoPassword = "demo-password-1"
)

// gate describes one demo device and the state it should be in at seed time.
type gate struct {
	id, location, gate, name string
	// lastSeenAgo < 0 means NEVER; live gates get fresh heartbeats.
	lastSeenAgo time.Duration
	live        bool
	outbox      int
	recent      []string // events in the last few minutes (drive the colour)
	noisy       float64  // relative amount of 30-day history
}

func fixture(n int) []gate {
	if n <= 8 {
		return []gate{
			{id: "STI-G01-POS01", location: "STI", gate: "G01", name: "Gate Keluar 1", live: true, noisy: 0.5},
			{id: "STI-G02-POS01", location: "STI", gate: "G02", name: "Gate Keluar 2", live: true, noisy: 2,
				recent: []string{"READER_NOT_READY", "READER_NOT_READY", "PAYMENT_TIMEOUT"}},
			{id: "STI-G03-POS01", location: "STI", gate: "G03", name: "Gate Keluar 3", live: true, outbox: 37, noisy: 1,
				recent: []string{"MQTT_DISCONNECTED"}},
			{id: "STI-G04-POS01", location: "STI", gate: "G04", name: "Gate Keluar Basement", lastSeenAgo: 25 * time.Minute, noisy: 1},
			{id: "STI-G05-POS01", location: "STI", gate: "G05", name: "Gate Motor", lastSeenAgo: 6 * time.Minute, noisy: 0.3},
			{id: "STI-G06-POS01", location: "STI", gate: "G06", name: "Gate VIP", live: true, noisy: 0.2},
			{id: "LBI-G01-POS01", location: "LBI", gate: "G01", name: "Gate Utama", live: true, noisy: 0.6},
			{id: "LBI-G02-POS01", location: "LBI", gate: "G02", name: "Gate Belakang", lastSeenAgo: -1},
		}
	}
	// Performance fixture: n gates spread over locations of 10 gates each.
	rng := rand.New(rand.NewSource(42))
	var out []gate
	for i := 0; i < n; i++ {
		loc := fmt.Sprintf("L%02d", i/10+1)
		g := gate{id: fmt.Sprintf("%s-G%02d-POS01", loc, i%10+1), location: loc, gate: fmt.Sprintf("G%02d", i%10+1),
			live: true, noisy: rng.Float64()}
		switch r := rng.Float64(); {
		case r < 0.05:
			g.live, g.lastSeenAgo = false, 30*time.Minute
		case r < 0.10:
			g.recent = []string{"READER_NOT_READY"}
		case r < 0.18:
			g.recent = []string{"MQTT_DISCONNECTED"}
		}
		out = append(out, g)
	}
	return out
}

// history events and their relative frequency.
var historyEvents = []struct {
	name string
	w    int
}{
	{"READER_DISCONNECTED", 5}, {"READER_NOT_READY", 3}, {"PAYMENT_TIMEOUT", 2}, {"HTTP_TIMEOUT", 2},
	{"MQTT_DISCONNECTED", 3}, {"INSUFFICIENT_BALANCE", 4}, {"PRINT_FAILED", 1}, {"APP_STARTED", 1},
}

var messages = map[string]string{
	"READER_DISCONNECTED":  "reader tidak terdeteksi di /dev/ttyUSB0",
	"READER_NOT_READY":     "reader tidak merespon status poll",
	"PAYMENT_TIMEOUT":      "debit timeout setelah 30 detik",
	"HTTP_TIMEOUT":         "POST /cashless/gateoutcashless timeout",
	"MQTT_DISCONNECTED":    "koneksi broker MQTT terputus",
	"INSUFFICIENT_BALANCE": "saldo kartu tidak cukup",
	"PRINT_FAILED":         "printer kehabisan kertas",
	"APP_STARTED":          "aplikasi dimulai",
}

func main() {
	dir := flag.String("dir", "demo", "output directory")
	regPath := flag.String("registry", "registry.yaml", "path to the agent's registry.yaml")
	n := flag.Int("gates", 8, "number of gates (8 = curated fixture, more = performance fixture)")
	days := flag.Int("days", 30, "days of history")
	listen := flag.String("listen", "127.0.0.1:18787", "listen_addr written to config.yaml")
	heartbeat := flag.String("heartbeat", "", "instead of seeding: keep live gates ONLINE by heartbeating this agent URL")
	flag.Parse()

	gates := fixture(*n)
	if *heartbeat != "" {
		runHeartbeats(*heartbeat, gates)
		return
	}
	if err := seed(*dir, *regPath, *listen, gates, *days, time.Now().UTC()); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("demo ready in %s (%d gates). Login: %s / %s\n", *dir, len(gates), DemoUser, DemoPassword)
}

func seed(dir, regPath, listen string, gates []gate, days int, now time.Time) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "data")); err == nil {
		return fmt.Errorf("%s already has data/; remove the directory to reseed", dir)
	}
	regRaw, err := os.ReadFile(regPath)
	if err != nil {
		return err
	}
	write := func(name, content string) error {
		return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600)
	}
	if err := write("registry.yaml", string(regRaw)); err != nil {
		return err
	}
	if err := write("config.yaml", fmt.Sprintf(`server:
  listen_addr: %q
  session_secret: "demo-session-secret"
  public_url: "http://%s"
retention:
  days: 3650
`, listen, listen)); err != nil {
		return err
	}
	var devs strings.Builder
	devs.WriteString("devices:\n")
	for _, g := range gates {
		fmt.Fprintf(&devs, "  %s:\n    location: %s\n    gate: %s\n    pos: POS01\n    token: \"demo-token-%s\"\n", g.id, g.location, g.gate, g.id)
		if g.name != "" {
			fmt.Fprintf(&devs, "    name: %q\n", g.name)
		}
	}
	if err := write("devices.yaml", devs.String()); err != nil {
		return err
	}
	if err := auth.AddUser(filepath.Join(dir, "users.yaml"), DemoUser, auth.RoleAdmin, DemoPassword); err != nil {
		return err
	}

	reg, err := registry.Load(filepath.Join(dir, "registry.yaml"))
	if err != nil {
		return err
	}
	db, err := store.Open(filepath.Join(dir, "data", "sentinel.db"))
	if err != nil {
		return err
	}
	defer db.Close()

	// Build every event first, then insert in time order so ids follow
	// received_at like they do in production.
	type ev struct {
		g    gate
		name string
		at   time.Time
	}
	rng := rand.New(rand.NewSource(7))
	var all []ev
	total := 0
	for _, w := range historyEvents {
		total += w.w
	}
	pick := func() string {
		r := rng.Intn(total)
		for _, w := range historyEvents {
			if r < w.w {
				return w.name
			}
			r -= w.w
		}
		return historyEvents[0].name
	}
	for _, g := range gates {
		if g.lastSeenAgo < 0 {
			continue // NEVER: no history at all
		}
		end := now.Add(-g.lastSeenAgo)
		count := int(float64(days) * 6 * g.noisy)
		for i := 0; i < count; i++ {
			at := end.Add(-time.Duration(rng.Int63n(int64(time.Duration(days) * 24 * time.Hour))))
			if now.Sub(at) < 15*time.Minute {
				at = at.Add(-15 * time.Minute) // keep the recent window for g.recent only
			}
			all = append(all, ev{g, pick(), at})
		}
		for i, name := range g.recent {
			all = append(all, ev{g, name, now.Add(-time.Duration(len(g.recent)-i) * time.Minute)})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at.Before(all[j].at) })

	for i, e := range all {
		entry, ok := reg.Lookup(e.name)
		if !ok {
			return fmt.Errorf("event %s missing from registry", e.name)
		}
		ts := e.at.Format(time.RFC3339)
		if _, err := db.InsertEvent(model.StoredEvent{
			DeviceID: e.g.id, EventID: fmt.Sprintf("demo-%d", i), OccurredAt: ts,
			Location: e.g.location, Gate: e.g.gate, POS: "POS01", Device: deviceFor(e.name),
			Category: entry.Category, ErrorCode: registry.ErrorCode(e.g.location, e.g.gate, entry),
			Event: e.name, Severity: entry.Severity, Message: messages[e.name], AppVersion: "3.1.4",
			ReceivedAt: ts,
		}); err != nil {
			return err
		}
		if err := db.TouchEventSeen(e.g.id, ts, "3.1.4"); err != nil {
			return err
		}
	}

	for _, g := range gates {
		if g.lastSeenAgo < 0 {
			continue
		}
		if err := db.UpsertHeartbeat(model.DeviceState{
			DeviceID: g.id, LastHeartbeatAt: now.Add(-g.lastSeenAgo).Format(time.RFC3339),
			AppVersion: "3.1.4", SDKVersion: "0.2.0", UptimeS: 3600*9 + 120, OutboxPending: g.outbox,
			RemoteAddr: "100.101.0.10",
		}); err != nil {
			return err
		}
	}
	return nil
}

func deviceFor(event string) string {
	switch {
	case strings.HasPrefix(event, "READER"):
		return "READER01"
	case strings.HasPrefix(event, "PRINT"):
		return "PRINTER01"
	}
	return ""
}

// runHeartbeats keeps every live gate ONLINE until interrupted, so a demo or
// E2E session doesn't decay to STALE after three minutes.
func runHeartbeats(base string, gates []gate) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	client := &http.Client{Timeout: 5 * time.Second}
	beat := func() {
		for _, g := range gates {
			if !g.live {
				continue
			}
			body, _ := json.Marshal(map[string]any{"device_id": g.id, "app_version": "3.1.4", "sdk_version": "0.2.0",
				"uptime_s": 3600 * 9, "outbox_pending": g.outbox})
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/v1/heartbeat", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer demo-token-"+g.id)
			if resp, err := client.Do(req); err == nil {
				resp.Body.Close()
			}
		}
	}
	beat()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			beat()
		}
	}
}
