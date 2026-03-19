package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/ryanmoreau/webhook-gateway/internal/config"
	"github.com/ryanmoreau/webhook-gateway/internal/deadletter"
	"github.com/ryanmoreau/webhook-gateway/internal/delivery"
	"github.com/ryanmoreau/webhook-gateway/internal/idempotency"
	"github.com/ryanmoreau/webhook-gateway/internal/logging"
	"github.com/ryanmoreau/webhook-gateway/internal/router"
	"github.com/ryanmoreau/webhook-gateway/internal/server"
)

func main() {
	configPath := flag.String("config", "./config.yaml", "path to configuration file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	logging.Setup(cfg.Logging.Level, cfg.Logging.Format)

	delivery.SetClient(delivery.NewClient(cfg.Server.AllowInsecure))

	// Initialize idempotency store.
	idemStore := idempotency.NewMemoryStore(1 * time.Minute)
	defer idemStore.Close()

	// Initialize dead letter store.
	storeBody := true
	if cfg.DeadLetter.StoreBody != nil {
		storeBody = *cfg.DeadLetter.StoreBody
	}
	dlPath := cfg.DeadLetter.Path
	if dlPath == "" {
		dlPath = "./dead_letters"
	}
	dlStore, err := deadletter.NewFileStore(dlPath, storeBody, cfg.DeadLetter.MaxBodyBytes)
	if err != nil {
		slog.Error("initializing dead letter store", "error", err)
		os.Exit(1)
	}

	// Build router.
	r := router.New(cfg, dlStore, idemStore)

	// Build and start server.
	srv := server.New(server.Config{
		Port:         cfg.Server.Port,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		MaxBodySize:  cfg.Server.MaxBodySize,
	}, r, r, r.Stats)

	if err := srv.ListenAndServe(30 * time.Second); err != nil {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}
