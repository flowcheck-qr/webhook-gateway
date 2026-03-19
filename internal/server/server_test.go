package server

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ryanmoreau/webhook-gateway/internal/stats"
)

func TestServer_RespondsOnPort(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	})

	srv := New(Config{
		Port:         0,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		MaxBodySize:  1 << 20,
	}, handler, nil, stats.New())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()

	go srv.Serve(ln)
	defer srv.Shutdown(context.Background())

	resp, err := http.Get(fmt.Sprintf("http://%s/", addr))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("body = %q, want ok", body)
	}
}

func TestServer_MaxBodySize_413(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(200)
	})

	srv := New(Config{
		Port:         0,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		MaxBodySize:  10, // 10 bytes
	}, handler, nil, stats.New())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()

	go srv.Serve(ln)
	defer srv.Shutdown(context.Background())

	// Send body larger than 10 bytes.
	bigBody := strings.NewReader(strings.Repeat("x", 100))
	resp, err := http.Post(fmt.Sprintf("http://%s/", addr), "text/plain", bigBody)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 413 {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
}

func TestServer_MaxBodySize_ExactLimit(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(200)
		w.Write(body)
	})

	srv := New(Config{
		Port:         0,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		MaxBodySize:  10,
	}, handler, nil, stats.New())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()

	go srv.Serve(ln)
	defer srv.Shutdown(context.Background())

	// Exactly at limit — should succeed.
	resp, err := http.Post(fmt.Sprintf("http://%s/", addr), "text/plain", strings.NewReader("0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("exact limit: status = %d, want 200", resp.StatusCode)
	}

	// One byte over — should fail.
	resp2, err := http.Post(fmt.Sprintf("http://%s/", addr), "text/plain", strings.NewReader("01234567890"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 413 {
		t.Fatalf("over limit: status = %d, want 413", resp2.StatusCode)
	}
}

func TestServer_GracefulShutdown(t *testing.T) {
	requestStarted := make(chan struct{})
	requestDone := make(chan struct{})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(200)
		w.Write([]byte("done"))
		close(requestDone)
	})

	srv := New(Config{
		Port:         0,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		MaxBodySize:  1 << 20,
	}, handler, nil, stats.New())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()

	go srv.Serve(ln)

	// Start a request.
	respCh := make(chan *http.Response, 1)
	go func() {
		resp, err := http.Get(fmt.Sprintf("http://%s/", addr))
		if err == nil {
			respCh <- resp
		}
	}()

	// Wait for request to start, then initiate shutdown.
	<-requestStarted
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx)

	// The in-flight request should complete.
	<-requestDone

	select {
	case resp := <-respCh:
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if string(body) != "done" {
			t.Errorf("body = %q, want done", body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for response")
	}
}
