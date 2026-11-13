// Command sentinel-agent runs the error tracking service for one or more
// gate/kiosk machines. It is designed to keep running independently of the
// main payment server: events are collected and stored in its own SQLite
// database even if the payment server or other central systems are down.
//
// Usage:
//
//	sentinel-agent [-config config.yaml]                  run the agent
//	sentinel-agent device add -id ID -location L -gate G [-pos P]
//	sentinel-agent device list   [-config config.yaml]        static + enrolled
//	sentinel-agent device revoke -id ID [-config config.yaml]  remove an enrolled kiosk
//	sentinel-agent backup -out FILE [-config config.yaml]      copy the database while running
//	sentinel-agent user add -name NAME -role viewer|admin  (password on stdin)
//	sentinel-agent user list
//	sentinel-agent alert test [-config config.yaml]        send a test message to every route
//	sentinel-agent version
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	neturl "net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // alert timezones must resolve on Windows too

	"parkways-sentinel/agent/internal/alert"
	"parkways-sentinel/agent/internal/api"
	"parkways-sentinel/agent/internal/auth"
	"parkways-sentinel/agent/internal/config"
	"parkways-sentinel/agent/internal/devices"
	"parkways-sentinel/agent/internal/registry"
	"parkways-sentinel/agent/internal/store"
)

// version is set at build time: go build -ldflags "-X main.version=v0.2.0"
var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "device":
			os.Exit(runDevice(os.Args[2:]))
		case "user":
			os.Exit(runUser(os.Args[2:]))
		case "alert":
			os.Exit(runAlert(os.Args[2:]))
		case "backup":
			os.Exit(runBackup(os.Args[2:]))
		case "version":
			fmt.Println(version)
			return
		}
	}
	serve()
}

func serve() {
	configPath := flag.String("config", "config.yaml", "path to agent config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	reg, err := registry.Load(cfg.Server.RegistryPath)
	if err != nil {
		log.Fatalf("registry: %v", err)
	}

	// devices.yaml is optional once kiosks can enroll themselves.
	devReg, err := devices.Load(cfg.Server.DevicesPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) || !cfg.Site.EnrollEnabled() {
			log.Fatalf("devices: %v", err)
		}
		devReg = devices.Empty()
	}

	users, err := auth.LoadUsers(cfg.Server.UsersPath)
	if err != nil {
		log.Fatalf("users: %v", err)
	}
	if users.Count() == 0 {
		log.Printf("warning: no dashboard users in %s — nobody can log in; add one with `sentinel-agent user add`", cfg.Server.UsersPath)
	}
	sessions, generated := auth.NewSessions(cfg.Server.SessionSecret, 12*time.Hour)
	if generated {
		log.Printf("notice: server.session_secret is empty; using a random one (dashboard logins reset on restart)")
	}

	db, err := store.Open(cfg.Server.DBPath)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer db.Close()

	custom, err := db.LoadCustomRegistry()
	if err != nil {
		log.Fatalf("load learned events: %v", err)
	}
	// A newer registry.yaml may now name a learned event, or own its number.
	for _, final := range reg.ReconcileLearned(custom) {
		if err := db.SaveCustomRegistryEntry(final, true); err != nil {
			log.Fatalf("update learned event %q: %v", final.EventName, err)
		}
		log.Printf("notice: learned event %q now %s-%03d to follow registry.yaml", final.EventName, final.Code, final.Number)
	}
	if len(custom) > 0 {
		log.Printf("loaded %d previously learned event(s) from local database", len(custom))
	}

	enrolled, hashes, err := db.EnrolledDevices()
	if err != nil {
		log.Fatalf("load enrolled devices: %v", err)
	}
	for i, e := range enrolled {
		if err := devReg.PutEnrolled(e, hashes[i]); err != nil {
			log.Printf("warning: enrolled device ignored: %v", err)
		}
	}
	if cfg.Site.Code != "" {
		log.Printf("site %s %q: %d enrolled device(s), enrollment %s", cfg.Site.Code, cfg.Site.Name, len(enrolled),
			map[bool]string{true: "on", false: "off"}[cfg.Site.EnrollEnabled()])
	}

	srv := api.New(cfg, reg, devReg, db, users, sessions)
	srv.Version = version
	httpServer := &http.Server{
		Addr:              cfg.Server.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// Bodies are at most 1 MiB and answers are bounded queries; a client
		// slower than this is stuck, and its connection is better freed.
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  2 * time.Minute,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go runRetention(ctx, db, cfg.Retention.Days, int64(cfg.Retention.MaxDBMB)<<20)
	if h, m, ok := cfg.Retention.ResetClock(); ok {
		loc := time.Local
		if cfg.Retention.Timezone != "" {
			loc, _ = time.LoadLocation(cfg.Retention.Timezone)
		}
		zone, _ := time.Now().In(loc).Zone()
		go runDailyReset(ctx, db, h, m, loc)
		log.Printf("retention: monitoring data is wiped daily at %02d:%02d %s (kiosk registrations and users are kept)", h, m, zone)
	}
	go srv.RunStatusTracker(ctx, 30*time.Second)
	go srv.RunSquelchFlusher(ctx, time.Minute)
	if cfg.Monitor.PingURL != "" {
		go runPing(ctx, db, cfg.Monitor.PingURL, time.Duration(cfg.Monitor.PingIntervalSeconds)*time.Second)
		log.Printf("monitor: pinging %s every %ds", redactURL(cfg.Monitor.PingURL), cfg.Monitor.PingIntervalSeconds)
	}

	if cfg.Alert.Enabled() {
		// Every kiosk looks DOWN right after a restart until it heartbeats
		// again, so hold status alerts for one stale period.
		grace := time.Duration(cfg.Status.StaleMinutes) * time.Minute
		al, err := alert.New(cfg.Alert, cfg.Server.PublicURL, grace, srv.Fleet, db,
			alert.NewTelegram(cfg.Alert.TelegramBotToken), time.Now())
		if err != nil {
			log.Fatalf("alert: %v", err)
		}
		al.Incidents = !cfg.Incident.Disabled
		go al.Run(ctx)
		log.Printf("alerts: Telegram to %d route(s), every %ds, incidents %s", len(cfg.Alert.Routes), cfg.Alert.IntervalSeconds,
			map[bool]string{true: "on (one message per incident)", false: "off (one message per event)"}[al.Incidents])
	} else {
		log.Printf("notice: alerts off (set SENTINEL_TELEGRAM_BOT_TOKEN and alert.routes to enable)")
	}

	go func() {
		log.Printf("Parkways Sentinel Agent %s listening on %s (%d device(s), %d dashboard user(s))",
			version, cfg.Server.ListenAddr, devReg.Count(), users.Count())
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server: %v", err)
		}
	}()

	<-ctx.Done()

	log.Println("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}

// runRetention deletes events older than `days`, at startup and then daily,
// and trims the oldest events further whenever the database file is still
// over maxBytes, so a noisy kiosk cannot fill the site server's disk.
func runRetention(ctx context.Context, db *store.Store, days int, maxBytes int64) {
	purge := func() {
		cutoff := time.Now().AddDate(0, 0, -days)
		n, err := db.PurgeEventsBefore(cutoff)
		if err != nil {
			log.Printf("retention: %v", err)
		} else if n > 0 {
			log.Printf("retention: deleted %d event(s) older than %d days", n, days)
		}
		if _, err := db.PurgeStatusHistoryBefore(cutoff.UTC().Format(time.RFC3339)); err != nil {
			log.Printf("retention: %v", err)
		}
		if _, err := db.PurgeIncidentsBefore(cutoff); err != nil {
			log.Printf("retention: %v", err)
		}
		if n, err := db.TrimToSize(maxBytes); err != nil {
			log.Printf("retention: size limit: %v", err)
		} else if n > 0 {
			log.Printf("warning: database was over retention.max_db_mb (%d MB); dropped the oldest %d event(s). A kiosk is probably sending far too many events — check the event list for repeats.", maxBytes>>20, n)
		}
	}
	purge()
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			purge()
		}
	}
}

// lastResetKey records the cutoff of the last daily wipe, so an agent that
// was off at reset time catches up when it starts, and never wipes twice.
const lastResetKey = "retention:last_reset"

// runDailyReset wipes the previous operating day's monitoring data at
// hour:minute local time every day (see store.ResetBefore).
func runDailyReset(ctx context.Context, db *store.Store, hour, minute int, loc *time.Location) {
	reset := func(cutoff time.Time) {
		n, err := db.ResetBefore(cutoff)
		if err != nil {
			log.Printf("daily reset: %v", err)
			return
		}
		if err := db.SetKV(lastResetKey, cutoff.UTC().Format(time.RFC3339)); err != nil {
			log.Printf("daily reset: %v", err)
		}
		log.Printf("daily reset: deleted %d event(s) and the incidents/status history from before %s", n, cutoff.In(loc).Format("2006-01-02 15:04 MST"))
	}
	for {
		now := time.Now().In(loc)
		last := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, loc)
		if last.After(now) {
			last = last.AddDate(0, 0, -1)
		}
		// Missed (agent was off at reset time) or first run: catch up now.
		done, _ := db.GetKV(lastResetKey)
		if prev, err := time.Parse(time.RFC3339, done); err != nil || prev.Before(last) {
			reset(last)
		}
		next := last.AddDate(0, 0, 1)
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			reset(next)
		}
	}
}

// runPing requests url every interval while the database answers, so a
// dead-man's-switch service raises the alarm when the agent, its database
// or the VPS stops. Only changes between working and failing are logged.
func runPing(ctx context.Context, db *store.Store, url string, every time.Duration) {
	client := &http.Client{Timeout: 10 * time.Second}
	failing := false
	ping := func() {
		err := func() error {
			if _, err := db.MaxEventID(); err != nil {
				return fmt.Errorf("database check: %w", err)
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return err
			}
			resp, err := client.Do(req)
			if err != nil {
				return err
			}
			resp.Body.Close()
			if resp.StatusCode >= 300 {
				return fmt.Errorf("HTTP %d", resp.StatusCode)
			}
			return nil
		}()
		switch {
		case err != nil && !failing && ctx.Err() == nil:
			log.Printf("monitor: ping failed: %s", strings.ReplaceAll(err.Error(), url, redactURL(url)))
		case err == nil && failing:
			log.Printf("monitor: ping working again")
		}
		failing = err != nil
	}
	ping()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			ping()
		}
	}
}

// redactURL keeps a ping URL's host but hides its path, which for most
// services is the secret check id.
func redactURL(raw string) string {
	u, err := neturl.Parse(raw)
	if err != nil || u.Host == "" {
		return "<ping url>"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

func runDevice(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: sentinel-agent device add|list|revoke [flags]")
		return 2
	}
	fs := flag.NewFlagSet("device "+args[0], flag.ContinueOnError)
	path := fs.String("devices", "devices.yaml", "path to devices.yaml")
	configPath := fs.String("config", "config.yaml", "agent config (to reach enrolled devices in the database)")
	id := fs.String("id", "", "device id, e.g. STI-G01-POS01")
	location := fs.String("location", "", "location code, e.g. STI")
	gate := fs.String("gate", "", "gate code, e.g. G01")
	pos := fs.String("pos", "", "optional POS label, e.g. POS01")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	switch args[0] {
	case "add":
		token, err := devices.AddDevice(*path, *id, *location, *gate, *pos)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		fmt.Printf("added %s (%s %s) to %s\n", *id, strings.ToUpper(*location), strings.ToUpper(*gate), *path)
		fmt.Printf("\nSENTINEL_DEVICE_ID=%s\nSENTINEL_TOKEN=%s\n\n", *id, token)
		fmt.Println("Put these in the kiosk's config. The token is not shown again.")
		fmt.Println("Restart the agent to load the new device.")
		return 0
	case "list":
		reg, err := devices.Load(*path)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				fmt.Fprintln(os.Stderr, "error:", err)
				return 1
			}
			reg = devices.Empty()
		}
		if db, ok := openStoreQuiet(*configPath); ok {
			enrolled, hashes, err := db.EnrolledDevices()
			db.Close()
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				return 1
			}
			for i, e := range enrolled {
				_ = reg.PutEnrolled(e, hashes[i])
			}
		}
		for _, e := range reg.All() {
			src := "devices.yaml"
			if e.Enrolled {
				src = "enrolled"
			}
			fmt.Printf("%-24s %-6s %-5s %-8s %s\n", e.DeviceID, e.Location, e.Gate, e.POS, src)
		}
		return 0
	case "revoke":
		if *id == "" {
			fmt.Fprintln(os.Stderr, "usage: sentinel-agent device revoke -id ID")
			return 2
		}
		db, ok := openStoreQuiet(*configPath)
		if !ok {
			fmt.Fprintln(os.Stderr, "error: cannot open the agent database (is -config right?)")
			return 1
		}
		defer db.Close()
		removed, err := db.DeleteEnrolled(*id)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		if !removed {
			fmt.Fprintf(os.Stderr, "no enrolled device %q (static devices are removed by editing devices.yaml)\n", *id)
			return 1
		}
		fmt.Printf("revoked %s. Restart the agent so its token stops working.\n", *id)
		return 0
	}
	fmt.Fprintln(os.Stderr, "usage: sentinel-agent device add|list|revoke [flags]")
	return 2
}

// openStoreQuiet opens the database named in the config, if both exist.
func openStoreQuiet(configPath string) (*store.Store, bool) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, false
	}
	if _, err := os.Stat(cfg.Server.DBPath); err != nil {
		return nil, false
	}
	db, err := store.Open(cfg.Server.DBPath)
	if err != nil {
		return nil, false
	}
	return db, true
}

// runBackup copies the live database with VACUUM INTO: consistent, and the
// agent keeps serving while it runs.
func runBackup(args []string) int {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	configPath := fs.String("config", "config.yaml", "path to agent config file")
	out := fs.String("out", "", "destination file (must not exist), e.g. /backup/sentinel-2026-11-12.db")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *out == "" {
		fmt.Fprintln(os.Stderr, "usage: sentinel-agent backup -out FILE [-config config.yaml]")
		return 2
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	db, err := store.Open(cfg.Server.DBPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	defer db.Close()
	if err := db.Backup(*out); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fi, _ := os.Stat(*out)
	fmt.Printf("backup written to %s (%d bytes)\n", *out, fi.Size())
	return 0
}

func runUser(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: sentinel-agent user add|list [flags]")
		return 2
	}
	fs := flag.NewFlagSet("user "+args[0], flag.ContinueOnError)
	path := fs.String("users", "users.yaml", "path to users.yaml")
	name := fs.String("name", "", "username")
	role := fs.String("role", "viewer", "viewer or admin")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	switch args[0] {
	case "add":
		// Password comes from stdin so it never appears in shell history
		// or the process list.
		fmt.Fprint(os.Stderr, "password (min 10 chars): ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			fmt.Fprintln(os.Stderr, "\nerror: no password on stdin")
			return 1
		}
		fmt.Fprintln(os.Stderr)
		if err := auth.AddUser(*path, *name, auth.Role(strings.ToLower(*role)), strings.TrimRight(line, "\r\n")); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		fmt.Printf("saved user %s (%s) to %s — restart the agent to apply\n", *name, *role, *path)
		return 0
	case "list":
		u, err := auth.LoadUsers(*path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		for _, usr := range u.Names() {
			fmt.Printf("%-20s %s\n", usr.Name, usr.Role)
		}
		return 0
	}
	fmt.Fprintln(os.Stderr, "usage: sentinel-agent user add|list [flags]")
	return 2
}

func runAlert(args []string) int {
	if len(args) == 0 || args[0] != "test" {
		fmt.Fprintln(os.Stderr, "usage: sentinel-agent alert test [-config config.yaml]")
		return 2
	}
	fs := flag.NewFlagSet("alert test", flag.ContinueOnError)
	configPath := fs.String("config", "config.yaml", "path to agent config file")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if !cfg.Alert.Enabled() {
		fmt.Fprintln(os.Stderr, "alerts are off: set SENTINEL_TELEGRAM_BOT_TOKEN and at least one alert.routes entry")
		return 1
	}
	tg := alert.NewTelegram(cfg.Alert.TelegramBotToken)
	host, _ := os.Hostname()
	failed := 0
	for _, r := range cfg.Alert.Routes {
		msg := fmt.Sprintf("✅ Tes notifikasi Parkways Sentinel (%s)\nRute: %s · min severity %s\nDikirim dari %s.", version, r.Name, r.MinSeverity, host)
		if _, err := tg.Send(context.Background(), alert.Destination{ChatID: r.ChatID, ThreadID: r.ThreadID}, msg); err != nil {
			fmt.Printf("%-12s GAGAL: %v\n", r.Name, err)
			failed++
		} else {
			fmt.Printf("%-12s terkirim ke chat %s\n", r.Name, r.ChatID)
		}
	}
	if failed > 0 {
		return 1
	}
	return 0
}
