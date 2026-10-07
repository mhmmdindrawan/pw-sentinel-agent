// Command sentinel-agent runs the error tracking service for one or more
// gate/kiosk machines. It is designed to keep running independently of the
// main payment server: events are collected and stored in its own SQLite
// database even if the payment server or other central systems are down.
//
// Usage:
//
//	sentinel-agent [-config config.yaml]                  run the agent
//	sentinel-agent device add -id ID -location L -gate G [-pos P]
//	sentinel-agent device list
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

	devReg, err := devices.Load(cfg.Server.DevicesPath)
	if err != nil {
		log.Fatalf("devices: %v", err)
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
	for _, e := range custom {
		reg.Register(e)
	}
	if len(custom) > 0 {
		log.Printf("loaded %d previously learned event(s) from local database", len(custom))
	}

	srv := api.New(cfg, reg, devReg, db, users, sessions)
	srv.Version = version
	httpServer := &http.Server{
		Addr:              cfg.Server.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go runRetention(ctx, db, cfg.Retention.Days)

	if cfg.Alert.Enabled() {
		// Every kiosk looks DOWN right after a restart until it heartbeats
		// again, so hold status alerts for one stale period.
		grace := time.Duration(cfg.Status.StaleMinutes) * time.Minute
		al, err := alert.New(cfg.Alert, cfg.Server.PublicURL, grace, srv.Fleet, db,
			alert.NewTelegram(cfg.Alert.TelegramBotToken), time.Now())
		if err != nil {
			log.Fatalf("alert: %v", err)
		}
		go al.Run(ctx)
		log.Printf("alerts: Telegram to %d route(s), every %ds", len(cfg.Alert.Routes), cfg.Alert.IntervalSeconds)
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

// runRetention deletes events older than `days`, at startup and then daily.
func runRetention(ctx context.Context, db *store.Store, days int) {
	purge := func() {
		n, err := db.PurgeEventsBefore(time.Now().AddDate(0, 0, -days))
		if err != nil {
			log.Printf("retention: %v", err)
		} else if n > 0 {
			log.Printf("retention: deleted %d event(s) older than %d days", n, days)
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

func runDevice(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: sentinel-agent device add|list [flags]")
		return 2
	}
	fs := flag.NewFlagSet("device "+args[0], flag.ContinueOnError)
	path := fs.String("devices", "devices.yaml", "path to devices.yaml")
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
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		for _, e := range reg.All() {
			fmt.Printf("%-24s %-6s %-5s %s\n", e.DeviceID, e.Location, e.Gate, e.POS)
		}
		return 0
	}
	fmt.Fprintln(os.Stderr, "usage: sentinel-agent device add|list [flags]")
	return 2
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
		if err := tg.Send(context.Background(), r.ChatID, msg); err != nil {
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
