package router

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ryanmoreau/webhook-gateway/internal/config"
	"github.com/ryanmoreau/webhook-gateway/internal/deadletter"
	"github.com/ryanmoreau/webhook-gateway/internal/delivery"
	"github.com/ryanmoreau/webhook-gateway/internal/idempotency"
	"github.com/ryanmoreau/webhook-gateway/internal/logging"
	"github.com/ryanmoreau/webhook-gateway/internal/signature"
)

// hopByHopHeaders are headers that must not be forwarded to destinations.
var hopByHopHeaders = map[string]bool{
	"Connection":          true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailers":            true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

// Router matches incoming webhook requests to configured routes, verifies
// signatures, checks idempotency, and fans out deliveries asynchronously.
type Router struct {
	routes     []routeEntry
	dlq        deadletter.Store
	idem       idempotency.Store
	inFlight   sync.WaitGroup
	sem        chan struct{} // concurrency limiter; nil if unlimited
}

type routeEntry struct {
	cfg      config.RouteConfig
	verifier signature.Verifier
	secret   string
}

// New creates a Router from the loaded config.
func New(cfg *config.Config, dlq deadletter.Store, idem idempotency.Store) *Router {
	r := &Router{
		dlq:  dlq,
		idem: idem,
	}

	if cfg.Server.ConcurrencyLimit > 0 {
		r.sem = make(chan struct{}, cfg.Server.ConcurrencyLimit)
	}

	for _, rc := range cfg.Routes {
		var v signature.Verifier
		switch rc.Signature.Type {
		case "stripe":
			v = &signature.StripeVerifier{Tolerance: rc.Signature.Tolerance}
		default: // hmac-sha256
			v = &signature.HMACVerifier{
				Prefix:   rc.Signature.Prefix,
				Encoding: rc.Signature.Encoding,
			}
		}

		r.routes = append(r.routes, routeEntry{
			cfg:      rc,
			verifier: v,
			secret:   rc.Signature.SecretEnv, // already resolved to the actual secret
		})
	}

	return r
}

// WaitInFlight blocks until all in-flight deliveries complete or ctx expires.
func (r *Router) WaitInFlight(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		r.inFlight.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var matched *routeEntry
	for i := range r.routes {
		if r.routes[i].cfg.Path == req.URL.Path {
			matched = &r.routes[i]
			break
		}
	}
	if matched == nil {
		http.NotFound(w, req)
		return
	}

	// Assign request ID.
	reqID := newRequestID()
	ctx := logging.WithRequestID(context.Background(), reqID)
	logger := slog.With("request_id", reqID, "route", matched.cfg.Path)

	// Read body once.
	body, err := io.ReadAll(req.Body)
	if err != nil {
		logger.Error("reading request body", "error", err)
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	// Verify signature.
	sigHeader := req.Header.Get(matched.cfg.Signature.Header)
	if err := matched.verifier.Verify(sigHeader, matched.secret, body); err != nil {
		logger.Warn("signature verification failed", "error", err)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	// Check idempotency.
	if matched.cfg.Idempotency.Enabled && r.idem != nil {
		eventID := extractKeyPath(body, matched.cfg.Idempotency.KeyPath)
		if eventID != "" {
			seen, err := r.idem.Seen(eventID)
			if err != nil {
				logger.Warn("idempotency check failed", "error", err)
				// Continue delivery on error — don't block on dedup failures.
			} else if seen {
				logger.Info("duplicate event, skipping delivery", "event_id", eventID)
				w.WriteHeader(http.StatusOK)
				return
			} else {
				if err := r.idem.Mark(eventID, matched.cfg.Idempotency.TTL); err != nil {
					logger.Warn("idempotency mark failed", "error", err)
				}
			}
		} else {
			logger.Warn("could not extract event ID from body, skipping deduplication",
				"key_path", matched.cfg.Idempotency.KeyPath)
		}
	}

	// Build forwarded headers.
	fwdHeaders := buildForwardHeaders(req.Header, matched.cfg.ForwardHeaders, reqID)

	// Return 200 immediately — fan-out is async.
	w.WriteHeader(http.StatusOK)

	// Fan out to all destinations concurrently.
	route := matched // capture for goroutine
	r.inFlight.Add(1)
	go func() {
		defer r.inFlight.Done()
		r.fanOut(ctx, logger, route, fwdHeaders, body, reqID)
	}()
}

func (r *Router) fanOut(ctx context.Context, logger *slog.Logger, route *routeEntry, headers http.Header, body []byte, reqID string) {
	retryCfg := delivery.RetryConfig{
		MaxAttempts:     route.cfg.Retry.MaxAttempts,
		InitialInterval: route.cfg.Retry.InitialInterval,
		MaxInterval:     route.cfg.Retry.MaxInterval,
	}

	var wg sync.WaitGroup
	for _, d := range route.cfg.Destinations {
		dest := delivery.Destination{
			URL:     d.URL,
			Timeout: d.Timeout,
		}

		wg.Add(1)
		go func() {
			defer wg.Done()

			// Acquire semaphore if concurrency-limited.
			if r.sem != nil {
				r.sem <- struct{}{}
				defer func() { <-r.sem }()
			}

			err := delivery.WithRetry(ctx, retryCfg, func() error {
				return delivery.Deliver(ctx, dest, headers, body)
			})
			if err != nil {
				logger.Error("delivery failed",
					"destination", dest.URL,
					"error", err,
					"attempts", retryCfg.MaxAttempts)

				dlEntry := deadletter.Entry{
					RequestID:      reqID,
					Timestamp:      time.Now().UTC(),
					RoutePath:      route.cfg.Path,
					DestinationURL: dest.URL,
					RequestBody:    body,
					Headers:        flattenHeaders(headers),
					ErrorMessage:   err.Error(),
					AttemptCount:   retryCfg.MaxAttempts,
				}
				if dlErr := r.dlq.Save(dlEntry); dlErr != nil {
					logger.Error("saving dead letter", "error", dlErr)
				}
			} else {
				logger.Info("delivery succeeded", "destination", dest.URL)
			}
		}()
	}
	wg.Wait()
}

// buildForwardHeaders constructs the header set to forward to destinations.
// Always includes Content-Type and X-Webhook-Gateway-Request-Id.
// Includes any headers from the allowlist. Strips hop-by-hop and Host.
func buildForwardHeaders(src http.Header, allowlist []string, reqID string) http.Header {
	h := make(http.Header)

	// Always forward Content-Type.
	if ct := src.Get("Content-Type"); ct != "" {
		h.Set("Content-Type", ct)
	}

	// Forward allowlisted headers.
	for _, name := range allowlist {
		if hopByHopHeaders[http.CanonicalHeaderKey(name)] {
			continue
		}
		if http.CanonicalHeaderKey(name) == "Host" {
			continue
		}
		if vals := src.Values(name); len(vals) > 0 {
			for _, v := range vals {
				h.Add(name, v)
			}
		}
	}

	h.Set("X-Webhook-Gateway-Request-Id", reqID)

	return h
}

// extractKeyPath extracts a value from JSON using a dot-separated path.
// Returns "" if the body isn't valid JSON, the path doesn't exist, or the
// value is not a string. Arrays are not supported.
func extractKeyPath(body []byte, path string) string {
	if path == "" {
		return ""
	}

	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return ""
	}

	parts := strings.Split(path, ".")
	current := any(obj)

	for _, part := range parts {
		m, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current, ok = m[part]
		if !ok {
			return ""
		}
	}

	switch v := current.(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%.0f", v)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// flattenHeaders converts http.Header to a simple map (first value only).
func flattenHeaders(h http.Header) map[string]string {
	m := make(map[string]string, len(h))
	for k := range h {
		m[k] = h.Get(k)
	}
	return m
}

func newRequestID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%12x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
