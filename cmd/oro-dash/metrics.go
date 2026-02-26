package main

import (
	"sync"
	"time"
)

// metricsBufferCap is the fixed ring buffer capacity.
// 900 samples at 1 sample/sec = 15 minutes of history.
const metricsBufferCap = 900

// WorkerSample captures a single worker's state at a point in time.
type WorkerSample struct {
	ID         string
	ContextPct int
	State      string
	BeadID     string
}

// MetricsSample holds all metrics collected at a single point in time.
type MetricsSample struct {
	Timestamp time.Time

	// Pipeline metrics
	BeadsClosed  int // cumulative beads closed since session start
	QueueReady   int // beads in ready state
	QueueWIP     int // beads in_progress
	QueueBlocked int // beads blocked

	// Worker metrics
	WorkersActive int // workers in executing state
	WorkersIdle   int // workers in idle state
	WorkersTotal  int // total worker count

	// Per-worker snapshots (for per-worker sparklines)
	Workers []WorkerSample
}

// MetricsBuffer is an in-memory ring buffer for time-series metrics.
// Thread-safe via sync.RWMutex. Stores up to metricsBufferCap samples;
// older samples are overwritten when capacity is reached.
type MetricsBuffer struct {
	mu      sync.RWMutex
	samples [metricsBufferCap]MetricsSample
	head    int // next write index
	count   int // number of valid samples (0..metricsBufferCap)
}

// NewMetricsBuffer creates a new MetricsBuffer ready for use.
func NewMetricsBuffer() *MetricsBuffer {
	return &MetricsBuffer{}
}

// Record appends a sample to the ring buffer.
// When the buffer is full, the oldest sample is overwritten.
func (b *MetricsBuffer) Record(s MetricsSample) {
	b.mu.Lock()
	b.samples[b.head] = s
	b.head = (b.head + 1) % metricsBufferCap
	if b.count < metricsBufferCap {
		b.count++
	}
	b.mu.Unlock()
}

// Last returns the most recent n samples ordered oldest to newest.
// Returns nil if the buffer is empty, n <= 0, or n is negative.
// If n > Len(), returns all available samples.
func (b *MetricsBuffer) Last(n int) []MetricsSample {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if n <= 0 || b.count == 0 {
		return nil
	}

	if n > b.count {
		n = b.count
	}

	result := make([]MetricsSample, n)
	// head points to the next write slot, so the most recent sample is at head-1.
	// We want the last n samples oldest-first: from head-n to head-1.
	start := (b.head - n + metricsBufferCap) % metricsBufferCap
	for i := 0; i < n; i++ {
		idx := (start + i) % metricsBufferCap
		result[i] = b.samples[idx]
	}

	return result
}

// Len returns the number of valid samples in the buffer.
func (b *MetricsBuffer) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.count
}
