package router

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanmoreau/webhook-gateway/internal/config"
	"github.com/ryanmoreau/webhook-gateway/internal/deadletter"
	"github.com/ryanmoreau/webhook-gateway/internal/idempotency"
)

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

type mockDLQ struct {
	mu      sync.Mutex
	entries []deadletter.Entry
}

func (m *mockDLQ) Save(e deadletter.Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, e)
	return nil
}

func (m *mockDLQ) Entries() []deadletter.Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]deadletter.Entry, len(m.entries))
	copy(cp, m.entries)
	return cp
}

func newTestRouter(t *testing.T, destURLs []string, secret string, opts ...func(*config.Config)) (*Router, *mockDLQ) {
	t.Helper()

	dests := make([]config.DestConfig, len(destURLs))
	for i, u := range destURLs {
		dests[i] = config.DestConfig{URL: u, Timeout: 5 * time.Second}
	}

	cfg := &config.Config{
		Server: config.ServerConfig{
			Port:        8080,
			MaxBodySize: 1 << 20,
		},
		Routes: []config.RouteConfig{
			{
				Path: "/hooks/test",
				Signature: config.SignatureConfig{
					Type:      "hmac-sha256",
					Header:    "X-Signature",
					SecretEnv: secret,
					Encoding:  "hex",
				},
				Destinations: dests,
				Retry: config.RetryConfig{
					MaxAttempts:     1,
					InitialInterval: 10 * time.Millisecond,
					MaxInterval:     100 * time.Millisecond,
				},
			},
		},
	}
	for _, opt := range opts {
		opt(cfg)
	}

	dlq := &mockDLQ{}
	idem := idempotency.NewMemoryStore(1 * time.Minute)
	t.Cleanup(idem.Close)

	return New(cfg, dlq, idem), dlq
}

func TestRouter_ValidRequest_DeliveredToAllDestinations(t *testing.T) {
	var mu sync.Mutex
	received := map[string][]byte{}

	dest1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		received["dest1"] = body
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer dest1.Close()

	dest2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		received["dest2"] = body
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer dest2.Close()

	secret := "testsecret"
	router, _ := newTestRouter(t, []string{dest1.URL, dest2.URL}, secret)
	gw := httptest.NewServer(router)
	defer gw.Close()

	body := []byte(`{"event":"push"}`)
	sig := sign(secret, body)

	req, _ := http.NewRequest("POST", gw.URL+"/hooks/test", strings.NewReader(string(body)))
	req.Header.Set("X-Signature", sig)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// Wait for async fan-out.
	waitForDeliveries(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) == 2
	})

	mu.Lock()
	defer mu.Unlock()
	if string(received["dest1"]) != `{"event":"push"}` {
		t.Errorf("dest1 body = %q", received["dest1"])
	}
	if string(received["dest2"]) != `{"event":"push"}` {
		t.Errorf("dest2 body = %q", received["dest2"])
	}
}

func TestRouter_UnknownPath_404(t *testing.T) {
	router, _ := newTestRouter(t, []string{"http://localhost:1"}, "secret")
	gw := httptest.NewServer(router)
	defer gw.Close()

	resp, err := http.DefaultClient.Post(gw.URL+"/unknown", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 404 {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestRouter_InvalidSignature_401(t *testing.T) {
	delivered := false
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delivered = true
		w.WriteHeader(200)
	}))
	defer dest.Close()

	router, _ := newTestRouter(t, []string{dest.URL}, "secret")
	gw := httptest.NewServer(router)
	defer gw.Close()

	req, _ := http.NewRequest("POST", gw.URL+"/hooks/test", strings.NewReader(`{}`))
	req.Header.Set("X-Signature", "invalidsig")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	// Give a moment to ensure no async delivery happened.
	time.Sleep(50 * time.Millisecond)
	if delivered {
		t.Fatal("should not deliver on invalid signature")
	}
}

func TestRouter_Idempotency_SkipsDuplicate(t *testing.T) {
	deliveryCount := 0
	var mu sync.Mutex

	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		deliveryCount++
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer dest.Close()

	secret := "secret"
	router, _ := newTestRouter(t, []string{dest.URL}, secret, func(cfg *config.Config) {
		cfg.Routes[0].Idempotency = config.IdempotencyConfig{
			Enabled: true,
			TTL:     1 * time.Hour,
			KeyPath: "id",
		}
	})
	gw := httptest.NewServer(router)
	defer gw.Close()

	body := []byte(`{"id":"evt_123","data":"test"}`)
	sig := sign(secret, body)

	// First request.
	req1, _ := http.NewRequest("POST", gw.URL+"/hooks/test", strings.NewReader(string(body)))
	req1.Header.Set("X-Signature", sig)
	resp1, _ := http.DefaultClient.Do(req1)
	if resp1.StatusCode != 200 {
		t.Fatalf("first request: status = %d", resp1.StatusCode)
	}

	waitForDeliveries(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return deliveryCount >= 1
	})

	// Second request — same event ID.
	req2, _ := http.NewRequest("POST", gw.URL+"/hooks/test", strings.NewReader(string(body)))
	req2.Header.Set("X-Signature", sig)
	resp2, _ := http.DefaultClient.Do(req2)
	if resp2.StatusCode != 200 {
		t.Fatalf("second request: status = %d", resp2.StatusCode)
	}

	// Wait to ensure no extra deliveries.
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	if deliveryCount != 1 {
		t.Errorf("delivery_count = %d, want 1 (dedup should skip second)", deliveryCount)
	}
	mu.Unlock()
}

func TestRouter_FailedDestination_DLQEntry(t *testing.T) {
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer dest.Close()

	secret := "secret"
	router, dlq := newTestRouter(t, []string{dest.URL}, secret)
	gw := httptest.NewServer(router)
	defer gw.Close()

	body := []byte(`{"event":"fail"}`)
	sig := sign(secret, body)

	req, _ := http.NewRequest("POST", gw.URL+"/hooks/test", strings.NewReader(string(body)))
	req.Header.Set("X-Signature", sig)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200 (async delivery)", resp.StatusCode)
	}

	waitForDeliveries(t, func() bool {
		return len(dlq.Entries()) > 0
	})

	entries := dlq.Entries()
	if len(entries) != 1 {
		t.Fatalf("dlq entries = %d, want 1", len(entries))
	}
	if entries[0].RoutePath != "/hooks/test" {
		t.Errorf("route_path = %q", entries[0].RoutePath)
	}
	if entries[0].RequestID == "" {
		t.Error("expected request_id in DLQ entry")
	}
}

func TestRouter_RequestIDInHeaders(t *testing.T) {
	var gotReqID string
	var mu sync.Mutex

	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotReqID = r.Header.Get("X-Webhook-Gateway-Request-Id")
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer dest.Close()

	secret := "secret"
	router, _ := newTestRouter(t, []string{dest.URL}, secret)
	gw := httptest.NewServer(router)
	defer gw.Close()

	body := []byte(`{}`)
	sig := sign(secret, body)

	req, _ := http.NewRequest("POST", gw.URL+"/hooks/test", strings.NewReader(string(body)))
	req.Header.Set("X-Signature", sig)
	http.DefaultClient.Do(req)

	waitForDeliveries(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return gotReqID != ""
	})

	mu.Lock()
	if gotReqID == "" {
		t.Error("expected X-Webhook-Gateway-Request-Id in forwarded headers")
	}
	mu.Unlock()
}

func TestRouter_MethodNotAllowed(t *testing.T) {
	router, _ := newTestRouter(t, []string{"http://localhost:1"}, "secret")
	gw := httptest.NewServer(router)
	defer gw.Close()

	resp, err := http.DefaultClient.Get(gw.URL + "/hooks/test")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 405 {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
}

func TestExtractKeyPath(t *testing.T) {
	tests := []struct {
		name string
		body string
		path string
		want string
	}{
		{"top-level string", `{"id":"evt_123"}`, "id", "evt_123"},
		{"nested", `{"data":{"object":{"id":"obj_456"}}}`, "data.object.id", "obj_456"},
		{"number", `{"id":42}`, "id", "42"},
		{"missing key", `{"name":"test"}`, "id", ""},
		{"invalid json", `not json`, "id", ""},
		{"empty path", `{"id":"x"}`, "", ""},
		{"deep missing", `{"data":{}}`, "data.object.id", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractKeyPath([]byte(tt.body), tt.path)
			if got != tt.want {
				t.Errorf("extractKeyPath(%q, %q) = %q, want %q", tt.body, tt.path, got, tt.want)
			}
		})
	}
}

func TestRouter_ForwardHeaders_Allowlist(t *testing.T) {
	var gotHeaders http.Header
	var mu sync.Mutex

	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotHeaders = r.Header.Clone()
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer dest.Close()

	secret := "secret"
	router, _ := newTestRouter(t, []string{dest.URL}, secret, func(cfg *config.Config) {
		cfg.Routes[0].ForwardHeaders = []string{"X-GitHub-Event"}
	})
	gw := httptest.NewServer(router)
	defer gw.Close()

	body := []byte(`{}`)
	sig := sign(secret, body)

	req, _ := http.NewRequest("POST", gw.URL+"/hooks/test", strings.NewReader(string(body)))
	req.Header.Set("X-Signature", sig)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Not-Allowed", "should-not-forward")
	http.DefaultClient.Do(req)

	waitForDeliveries(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return gotHeaders != nil
	})

	mu.Lock()
	defer mu.Unlock()

	if gotHeaders.Get("Content-Type") != "application/json" {
		t.Error("Content-Type should always be forwarded")
	}
	if gotHeaders.Get("X-GitHub-Event") != "push" {
		t.Error("allowlisted header X-GitHub-Event should be forwarded")
	}
	if gotHeaders.Get("X-Not-Allowed") != "" {
		t.Error("non-allowlisted header should not be forwarded")
	}
	if gotHeaders.Get("X-Webhook-Gateway-Request-Id") == "" {
		t.Error("request ID header should always be forwarded")
	}
}

func TestRouter_StripeRoute(t *testing.T) {
	var gotBody []byte
	var mu sync.Mutex

	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotBody, _ = io.ReadAll(r.Body)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer dest.Close()

	secret := "whsec_test"
	body := []byte(`{"id":"evt_stripe_1","type":"invoice.paid"}`)

	now := time.Now()
	ts := now.Unix()
	signed := fmt.Sprintf("%d.%s", ts, string(body))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signed))
	sigHex := hex.EncodeToString(mac.Sum(nil))
	header := fmt.Sprintf("t=%d,v1=%s", ts, sigHex)

	cfg := &config.Config{
		Server: config.ServerConfig{Port: 8080, MaxBodySize: 1 << 20},
		Routes: []config.RouteConfig{
			{
				Path: "/hooks/stripe",
				Signature: config.SignatureConfig{
					Type:      "stripe",
					Header:    "Stripe-Signature",
					SecretEnv: secret,
					Tolerance: 5 * time.Minute,
				},
				Destinations: []config.DestConfig{
					{URL: dest.URL, Timeout: 5 * time.Second},
				},
				Retry: config.RetryConfig{
					MaxAttempts:     1,
					InitialInterval: 10 * time.Millisecond,
					MaxInterval:     100 * time.Millisecond,
				},
			},
		},
	}

	dlq := &mockDLQ{}
	idem := idempotency.NewMemoryStore(1 * time.Minute)
	defer idem.Close()
	r := New(cfg, dlq, idem)
	gw := httptest.NewServer(r)
	defer gw.Close()

	req, _ := http.NewRequest("POST", gw.URL+"/hooks/stripe", strings.NewReader(string(body)))
	req.Header.Set("Stripe-Signature", header)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	waitForDeliveries(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return gotBody != nil
	})

	mu.Lock()
	defer mu.Unlock()

	var parsed map[string]any
	json.Unmarshal(gotBody, &parsed)
	if parsed["id"] != "evt_stripe_1" {
		t.Errorf("body id = %v, want evt_stripe_1", parsed["id"])
	}
}

func TestRouter_MixedDestinations_PartialFailure(t *testing.T) {
	var mu sync.Mutex
	var dest1Got, dest2Got, dest3Got bool

	// dest1: succeeds
	dest1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		dest1Got = true
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer dest1.Close()

	// dest2: returns 500 (retryable, but max_attempts=1 so goes to DLQ)
	dest2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		dest2Got = true
		mu.Unlock()
		w.WriteHeader(500)
	}))
	defer dest2.Close()

	// dest3: succeeds
	dest3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		dest3Got = true
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer dest3.Close()

	secret := "secret"
	router, dlq := newTestRouter(t, []string{dest1.URL, dest2.URL, dest3.URL}, secret)
	gw := httptest.NewServer(router)
	defer gw.Close()

	body := []byte(`{"event":"mixed"}`)
	sig := sign(secret, body)

	req, _ := http.NewRequest("POST", gw.URL+"/hooks/test", strings.NewReader(string(body)))
	req.Header.Set("X-Signature", sig)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// Wait for all three destinations to be hit.
	waitForDeliveries(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return dest1Got && dest2Got && dest3Got
	})

	// Only dest2 should produce a DLQ entry.
	waitForDeliveries(t, func() bool {
		return len(dlq.Entries()) > 0
	})

	entries := dlq.Entries()
	if len(entries) != 1 {
		t.Fatalf("dlq entries = %d, want 1", len(entries))
	}
	if entries[0].DestinationURL != dest2.URL {
		t.Errorf("dlq destination = %q, want %q", entries[0].DestinationURL, dest2.URL)
	}
}

func TestRouter_AllDestinationsFail(t *testing.T) {
	dest1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
	}))
	defer dest1.Close()

	dest2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	}))
	defer dest2.Close()

	secret := "secret"
	router, dlq := newTestRouter(t, []string{dest1.URL, dest2.URL}, secret)
	gw := httptest.NewServer(router)
	defer gw.Close()

	body := []byte(`{"event":"allfail"}`)
	sig := sign(secret, body)

	req, _ := http.NewRequest("POST", gw.URL+"/hooks/test", strings.NewReader(string(body)))
	req.Header.Set("X-Signature", sig)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200 (async)", resp.StatusCode)
	}

	// Both destinations should produce DLQ entries.
	waitForDeliveries(t, func() bool {
		return len(dlq.Entries()) >= 2
	})

	entries := dlq.Entries()
	if len(entries) != 2 {
		t.Fatalf("dlq entries = %d, want 2", len(entries))
	}

	urls := map[string]bool{}
	for _, e := range entries {
		urls[e.DestinationURL] = true
	}
	if !urls[dest1.URL] || !urls[dest2.URL] {
		t.Errorf("expected DLQ entries for both destinations, got %v", urls)
	}
}

func TestRouter_ConcurrencyLimit(t *testing.T) {
	var mu sync.Mutex
	concurrent := 0
	maxConcurrent := 0

	// Each destination sleeps briefly so we can observe concurrency.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		concurrent++
		if concurrent > maxConcurrent {
			maxConcurrent = concurrent
		}
		mu.Unlock()

		time.Sleep(50 * time.Millisecond)

		mu.Lock()
		concurrent--
		mu.Unlock()

		w.WriteHeader(200)
	})

	// 4 destination servers.
	var destURLs []string
	for i := 0; i < 4; i++ {
		srv := httptest.NewServer(handler)
		defer srv.Close()
		destURLs = append(destURLs, srv.URL)
	}

	secret := "secret"
	router, _ := newTestRouter(t, destURLs, secret, func(cfg *config.Config) {
		cfg.Server.ConcurrencyLimit = 2 // only 2 concurrent deliveries allowed
	})
	gw := httptest.NewServer(router)
	defer gw.Close()

	body := []byte(`{"event":"concurrent"}`)
	sig := sign(secret, body)

	req, _ := http.NewRequest("POST", gw.URL+"/hooks/test", strings.NewReader(string(body)))
	req.Header.Set("X-Signature", sig)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// Wait for all 4 deliveries to complete.
	waitForDeliveries(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return concurrent == 0 && maxConcurrent > 0
	})

	mu.Lock()
	if maxConcurrent > 2 {
		t.Errorf("max concurrent deliveries = %d, want <= 2 (concurrency limit)", maxConcurrent)
	}
	mu.Unlock()
}

// waitForDeliveries polls a condition with a timeout, since fan-out is async.
func waitForDeliveries(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for async deliveries")
}
