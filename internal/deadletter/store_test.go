package deadletter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileStore_Save(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(dir, true, 0)
	if err != nil {
		t.Fatal(err)
	}

	entry := Entry{
		RequestID:      "req-abc-123",
		Timestamp:      time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		RoutePath:      "/hooks/test",
		DestinationURL: "https://dest.example.com/hook",
		RequestBody:    []byte(`{"event":"test"}`),
		Headers:        map[string]string{"Content-Type": "application/json"},
		ErrorMessage:   "destination returned HTTP 500",
		AttemptCount:   3,
	}

	if err := store.Save(entry); err != nil {
		t.Fatal(err)
	}

	// Verify file exists with expected naming.
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}

	name := files[0].Name()
	if !strings.HasSuffix(name, ".json") {
		t.Errorf("expected .json extension: %s", name)
	}
	if !strings.Contains(name, "req-abc-123") {
		t.Errorf("expected request ID in filename: %s", name)
	}
	if !strings.Contains(name, "20240115") {
		t.Errorf("expected date in filename: %s", name)
	}

	// Verify file contents.
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}

	var got Entry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got.RequestID != "req-abc-123" {
		t.Errorf("request_id = %q, want req-abc-123", got.RequestID)
	}
	if got.ErrorMessage != "destination returned HTTP 500" {
		t.Errorf("error_message = %q", got.ErrorMessage)
	}
	if string(got.RequestBody) != `{"event":"test"}` {
		t.Errorf("request_body = %q", got.RequestBody)
	}
}

func TestFileStore_StoreBodyFalse(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(dir, false, 0)
	if err != nil {
		t.Fatal(err)
	}

	entry := Entry{
		RequestID:   "req-no-body",
		Timestamp:   time.Now().UTC(),
		RoutePath:   "/hooks/test",
		RequestBody: []byte(`{"sensitive":"data"}`),
	}

	if err := store.Save(entry); err != nil {
		t.Fatal(err)
	}

	files, _ := os.ReadDir(dir)
	data, _ := os.ReadFile(filepath.Join(dir, files[0].Name()))

	var got Entry
	json.Unmarshal(data, &got)
	if got.RequestBody != nil {
		t.Errorf("expected nil body when store_body=false, got %q", got.RequestBody)
	}
}

func TestFileStore_MaxBodyBytes(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(dir, true, 5)
	if err != nil {
		t.Fatal(err)
	}

	entry := Entry{
		RequestID:   "req-truncated",
		Timestamp:   time.Now().UTC(),
		RoutePath:   "/hooks/test",
		RequestBody: []byte("0123456789"),
	}

	if err := store.Save(entry); err != nil {
		t.Fatal(err)
	}

	files, _ := os.ReadDir(dir)
	data, _ := os.ReadFile(filepath.Join(dir, files[0].Name()))

	var got Entry
	json.Unmarshal(data, &got)
	if string(got.RequestBody) != "01234" {
		t.Errorf("body = %q, want first 5 bytes", got.RequestBody)
	}
}

func TestFileStore_AutoCreateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "deep")
	store, err := NewFileStore(dir, true, 0)
	if err != nil {
		t.Fatalf("expected auto-create: %v", err)
	}

	entry := Entry{
		RequestID: "req-auto",
		Timestamp: time.Now().UTC(),
		RoutePath: "/hooks/test",
	}
	if err := store.Save(entry); err != nil {
		t.Fatal(err)
	}

	files, _ := os.ReadDir(dir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
}

func TestNewFileStore_AutoCreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new", "dir")
	_, err := NewFileStore(dir, true, 0)
	if err != nil {
		t.Fatalf("expected directory to be auto-created: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatal("expected directory")
	}
}
