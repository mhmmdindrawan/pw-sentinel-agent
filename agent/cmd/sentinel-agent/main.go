// Command sentinel-agent runs the error tracking service for one or more
// gate/kiosk machines on the LAN. It is designed to keep running
// independently of the main payment server: events are collected and
// stored locally in SQLite even if the network or central systems are
// unavailable.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"parkways-sentinel/agent/internal/api"
	"parkways-sentinel/agent/internal/config"
	"parkways-sentinel/agent/internal/devices"
	"parkways-sentinel/agent/internal/registry"
	"parkways-sentinel/agent/internal/store"
)

func main() {
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

	srv := api.New(cfg, reg, devReg, db)
	httpServer := &http.Server{
		Addr:    cfg.Server.ListenAddr,
		Handler: srv.Handler(),
	}

	go func() {
		log.Printf("Parkways Sentinel Agent listening on %s (%d device(s) configured)", cfg.Server.ListenAddr, devReg.Count())
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server: %v", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	log.Println("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}
