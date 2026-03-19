package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ryanmoreau/webhook-gateway/internal/stats"
)

// Config holds HTTP server settings.
type Config struct {
	Port         int
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	MaxBodySize  int64
}

// WaitDrainer is called during shutdown to wait for in-flight work to complete.
type WaitDrainer interface {
	WaitInFlight(ctx context.Context)
}

// Server wraps net/http.Server with body size limiting and graceful shutdown.
type Server struct {
	httpServer *http.Server
	drainer    WaitDrainer
}

// New creates a Server that limits request body size and delegates to handler.
func New(cfg Config, handler http.Handler, drainer WaitDrainer, counters *stats.Counters) *Server {
	maxBody := cfg.MaxBodySize
	if maxBody <= 0 {
		maxBody = 1 << 20
	}

	mux := http.NewServeMux()

	// Health endpoint returns JSON stats — no dependencies, works for anyone.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		snap := counters.Snapshot()
		json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"stats":  snap,
		})
	})

	// All other requests go to the webhook router with body size limit.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		handler.ServeHTTP(w, r)
	})

	return &Server{
		httpServer: &http.Server{
			Addr:         fmt.Sprintf(":%d", cfg.Port),
			Handler:      mux,
			ReadTimeout:  cfg.ReadTimeout,
			WriteTimeout: cfg.WriteTimeout,
		},
		drainer: drainer,
	}
}

// ListenAndServe starts the server and blocks until a shutdown signal is
// received. It then stops accepting new connections, waits for in-flight
// deliveries to drain (bounded by shutdownTimeout), and returns.
func (s *Server) ListenAndServe(shutdownTimeout time.Duration) error {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		slog.Info("server starting", "addr", s.httpServer.Addr)
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("server error: %w", err)
	case sig := <-quit:
		slog.Info("shutdown signal received", "signal", sig)
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	// Stop accepting new requests.
	if err := s.httpServer.Shutdown(ctx); err != nil {
		slog.Error("http server shutdown error", "error", err)
	}

	// Wait for in-flight deliveries.
	if s.drainer != nil {
		slog.Info("draining in-flight deliveries")
		s.drainer.WaitInFlight(ctx)
	}

	slog.Info("server stopped")
	return nil
}

// Serve accepts connections on the given listener. Used in tests.
func (s *Server) Serve(ln net.Listener) error {
	return s.httpServer.Serve(ln)
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}
