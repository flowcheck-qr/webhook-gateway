package logging

import (
	"context"
	"testing"
)

func TestWithRequestID_And_RequestID(t *testing.T) {
	ctx := context.Background()

	// Empty context returns "".
	if got := RequestID(ctx); got != "" {
		t.Errorf("empty context: got %q, want empty", got)
	}

	// Set and retrieve.
	ctx = WithRequestID(ctx, "req-abc-123")
	if got := RequestID(ctx); got != "req-abc-123" {
		t.Errorf("got %q, want req-abc-123", got)
	}

	// Overwrite.
	ctx = WithRequestID(ctx, "req-xyz-456")
	if got := RequestID(ctx); got != "req-xyz-456" {
		t.Errorf("got %q, want req-xyz-456", got)
	}

	// Original context is unaffected.
	if got := RequestID(context.Background()); got != "" {
		t.Errorf("background context should be empty, got %q", got)
	}
}

func TestSetup_Levels(t *testing.T) {
	levels := []string{"debug", "info", "warn", "warning", "error", "unknown"}
	for _, lvl := range levels {
		t.Run(lvl, func(t *testing.T) {
			// Should not panic.
			Setup(lvl, "json")
		})
	}
}

func TestSetup_Formats(t *testing.T) {
	formats := []string{"json", "text", "unknown"}
	for _, fmt := range formats {
		t.Run(fmt, func(t *testing.T) {
			// Should not panic.
			Setup("info", fmt)
		})
	}
}
