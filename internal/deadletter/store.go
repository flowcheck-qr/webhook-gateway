package deadletter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Store persists failed delivery attempts for later inspection or replay.
type Store interface {
	Save(entry Entry) error
}

// Entry represents a single failed delivery.
type Entry struct {
	RequestID      string            `json:"request_id"`
	Timestamp      time.Time         `json:"timestamp"`
	RoutePath      string            `json:"route_path"`
	DestinationURL string            `json:"destination_url"`
	RequestBody    []byte            `json:"request_body,omitempty"`
	Headers        map[string]string `json:"headers"`
	ErrorMessage   string            `json:"error_message"`
	AttemptCount   int               `json:"attempt_count"`
}

// FileStore writes dead letter entries as JSON files to a directory.
// Writes are atomic: data is written to a temp file then renamed.
type FileStore struct {
	Dir          string
	StoreBody    bool
	MaxBodyBytes int64
}

// NewFileStore creates a FileStore and ensures the target directory exists.
func NewFileStore(dir string, storeBody bool, maxBodyBytes int64) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating dead letter directory: %w", err)
	}
	return &FileStore{
		Dir:          dir,
		StoreBody:    storeBody,
		MaxBodyBytes: maxBodyBytes,
	}, nil
}

func (s *FileStore) Save(entry Entry) error {
	// Apply body policy.
	if !s.StoreBody {
		entry.RequestBody = nil
	} else if s.MaxBodyBytes > 0 && int64(len(entry.RequestBody)) > s.MaxBodyBytes {
		entry.RequestBody = entry.RequestBody[:s.MaxBodyBytes]
	}

	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling dead letter entry: %w", err)
	}

	ts := entry.Timestamp.UTC().Format("20060102T150405Z")
	name := fmt.Sprintf("%s_%s.json", ts, entry.RequestID)
	finalPath := filepath.Join(s.Dir, name)

	// Atomic write: temp file + rename.
	tmp, err := os.CreateTemp(s.Dir, ".dl-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("writing dead letter: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Rename(tmpName, finalPath); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("renaming dead letter file: %w", err)
	}

	return nil
}
