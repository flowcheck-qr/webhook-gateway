package stats

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCountersSnapshot(t *testing.T) {
	c := New()

	c.RequestsReceived.Add(10)
	c.DeliveriesAttempted.Add(8)
	c.DeliveriesSucceeded.Add(7)
	c.DeliveriesFailed.Add(1)
	c.SignatureFailures.Add(2)
	c.DuplicatesSkipped.Add(3)
	c.DeadLettersWritten.Add(1)
	c.InFlight.Add(2)

	snap := c.Snapshot()

	if snap.RequestsReceived != 10 {
		t.Errorf("RequestsReceived = %d, want 10", snap.RequestsReceived)
	}
	if snap.DeliveriesAttempted != 8 {
		t.Errorf("DeliveriesAttempted = %d, want 8", snap.DeliveriesAttempted)
	}
	if snap.DeliveriesSucceeded != 7 {
		t.Errorf("DeliveriesSucceeded = %d, want 7", snap.DeliveriesSucceeded)
	}
	if snap.DeliveriesFailed != 1 {
		t.Errorf("DeliveriesFailed = %d, want 1", snap.DeliveriesFailed)
	}
	if snap.SignatureFailures != 2 {
		t.Errorf("SignatureFailures = %d, want 2", snap.SignatureFailures)
	}
	if snap.DuplicatesSkipped != 3 {
		t.Errorf("DuplicatesSkipped = %d, want 3", snap.DuplicatesSkipped)
	}
	if snap.DeadLettersWritten != 1 {
		t.Errorf("DeadLettersWritten = %d, want 1", snap.DeadLettersWritten)
	}
	if snap.InFlight != 2 {
		t.Errorf("InFlight = %d, want 2", snap.InFlight)
	}
	if snap.Uptime == "" {
		t.Error("Uptime should not be empty")
	}
}

func TestSnapshotJSON(t *testing.T) {
	c := New()
	c.RequestsReceived.Add(5)

	snap := c.Snapshot()
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("failed to marshal snapshot: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal snapshot: %v", err)
	}

	if decoded["requests_received"] != float64(5) {
		t.Errorf("requests_received = %v, want 5", decoded["requests_received"])
	}
	if _, ok := decoded["uptime"]; !ok {
		t.Error("uptime field missing from JSON")
	}
}

func TestConcurrentAccess(t *testing.T) {
	c := New()
	done := make(chan struct{})

	// Hammer the counters from multiple goroutines
	for i := 0; i < 100; i++ {
		go func() {
			c.RequestsReceived.Add(1)
			c.DeliveriesAttempted.Add(1)
			c.InFlight.Add(1)
			c.InFlight.Add(-1)
			done <- struct{}{}
		}()
	}

	for i := 0; i < 100; i++ {
		<-done
	}

	snap := c.Snapshot()
	if snap.RequestsReceived != 100 {
		t.Errorf("RequestsReceived = %d, want 100", snap.RequestsReceived)
	}
	if snap.InFlight != 0 {
		t.Errorf("InFlight = %d, want 0", snap.InFlight)
	}
}

func TestUptimeIncreases(t *testing.T) {
	c := &Counters{StartedAt: time.Now().Add(-5 * time.Second)}
	snap := c.Snapshot()

	if snap.Uptime == "0s" {
		t.Error("Uptime should be > 0s")
	}
}
