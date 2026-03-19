// Package stats provides atomic counters for gateway observability.
// No external dependencies — stats are exposed via a JSON /health endpoint.
package stats

import (
	"sync/atomic"
	"time"
)

// Counters tracks gateway-wide metrics using lock-free atomics.
type Counters struct {
	RequestsReceived    atomic.Int64
	SignatureFailures   atomic.Int64
	DuplicatesSkipped   atomic.Int64
	DeliveriesAttempted atomic.Int64
	DeliveriesSucceeded atomic.Int64
	DeliveriesFailed    atomic.Int64
	DeadLettersWritten  atomic.Int64
	InFlight            atomic.Int64
	StartedAt           time.Time
}

// New creates a Counters instance with the start time set to now.
func New() *Counters {
	return &Counters{StartedAt: time.Now()}
}

// Snapshot returns a point-in-time copy of all counters as a plain struct.
type Snapshot struct {
	Uptime              string `json:"uptime"`
	RequestsReceived    int64  `json:"requests_received"`
	SignatureFailures   int64  `json:"signature_failures"`
	DuplicatesSkipped   int64  `json:"duplicates_skipped"`
	DeliveriesAttempted int64  `json:"deliveries_attempted"`
	DeliveriesSucceeded int64  `json:"deliveries_succeeded"`
	DeliveriesFailed    int64  `json:"deliveries_failed"`
	DeadLettersWritten  int64  `json:"dead_letters_written"`
	InFlight            int64  `json:"in_flight"`
}

func (c *Counters) Snapshot() Snapshot {
	return Snapshot{
		Uptime:              time.Since(c.StartedAt).Round(time.Second).String(),
		RequestsReceived:    c.RequestsReceived.Load(),
		SignatureFailures:   c.SignatureFailures.Load(),
		DuplicatesSkipped:   c.DuplicatesSkipped.Load(),
		DeliveriesAttempted: c.DeliveriesAttempted.Load(),
		DeliveriesSucceeded: c.DeliveriesSucceeded.Load(),
		DeliveriesFailed:    c.DeliveriesFailed.Load(),
		DeadLettersWritten:  c.DeadLettersWritten.Load(),
		InFlight:            c.InFlight.Load(),
	}
}
