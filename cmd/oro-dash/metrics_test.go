package main

import (
	"sync"
	"testing"
	"time"
)

func TestMetricsBuffer_RecordAndLen(t *testing.T) {
	buf := NewMetricsBuffer()

	if buf.Len() != 0 {
		t.Fatalf("new buffer Len() = %d, want 0", buf.Len())
	}

	buf.Record(MetricsSample{Timestamp: time.Now(), WorkersActive: 1})

	if buf.Len() != 1 {
		t.Fatalf("after 1 Record, Len() = %d, want 1", buf.Len())
	}

	buf.Record(MetricsSample{Timestamp: time.Now(), WorkersActive: 2})
	buf.Record(MetricsSample{Timestamp: time.Now(), WorkersActive: 3})

	if buf.Len() != 3 {
		t.Fatalf("after 3 Records, Len() = %d, want 3", buf.Len())
	}
}

func TestMetricsBuffer_LastReturnsRecentN(t *testing.T) {
	buf := NewMetricsBuffer()

	for i := 0; i < 5; i++ {
		buf.Record(MetricsSample{
			Timestamp:     time.Now(),
			WorkersActive: i + 1,
		})
	}

	got := buf.Last(3)
	if len(got) != 3 {
		t.Fatalf("Last(3) returned %d samples, want 3", len(got))
	}

	// Should return the 3 most recent: WorkersActive 3, 4, 5
	// Ordered oldest to newest.
	if got[0].WorkersActive != 3 {
		t.Errorf("Last(3)[0].WorkersActive = %d, want 3", got[0].WorkersActive)
	}
	if got[1].WorkersActive != 4 {
		t.Errorf("Last(3)[1].WorkersActive = %d, want 4", got[1].WorkersActive)
	}
	if got[2].WorkersActive != 5 {
		t.Errorf("Last(3)[2].WorkersActive = %d, want 5", got[2].WorkersActive)
	}
}

func TestMetricsBuffer_LastNGreaterThanCount(t *testing.T) {
	buf := NewMetricsBuffer()

	buf.Record(MetricsSample{Timestamp: time.Now(), WorkersActive: 1})
	buf.Record(MetricsSample{Timestamp: time.Now(), WorkersActive: 2})

	got := buf.Last(10)
	if len(got) != 2 {
		t.Fatalf("Last(10) with 2 samples returned %d, want 2", len(got))
	}

	if got[0].WorkersActive != 1 {
		t.Errorf("Last(10)[0].WorkersActive = %d, want 1", got[0].WorkersActive)
	}
	if got[1].WorkersActive != 2 {
		t.Errorf("Last(10)[1].WorkersActive = %d, want 2", got[1].WorkersActive)
	}
}

func TestMetricsBuffer_EmptyBufferReturnsNil(t *testing.T) {
	buf := NewMetricsBuffer()

	got := buf.Last(5)
	if got != nil {
		t.Fatalf("Last(5) on empty buffer = %v, want nil", got)
	}

	got = buf.Last(0)
	if got != nil {
		t.Fatalf("Last(0) on empty buffer = %v, want nil", got)
	}
}

func TestMetricsBuffer_LastZeroReturnsNil(t *testing.T) {
	buf := NewMetricsBuffer()
	buf.Record(MetricsSample{Timestamp: time.Now(), WorkersActive: 1})

	got := buf.Last(0)
	if got != nil {
		t.Fatalf("Last(0) on non-empty buffer = %v, want nil", got)
	}
}

func TestMetricsBuffer_RingWrapsAt900(t *testing.T) {
	buf := NewMetricsBuffer()

	// Fill past capacity
	for i := 0; i < 1000; i++ {
		buf.Record(MetricsSample{
			Timestamp:     time.Now(),
			WorkersActive: i,
		})
	}

	// Count should be capped at 900
	if buf.Len() != 900 {
		t.Fatalf("after 1000 Records, Len() = %d, want 900", buf.Len())
	}

	// Last(5) should return samples 995-999 (the most recent 5)
	got := buf.Last(5)
	if len(got) != 5 {
		t.Fatalf("Last(5) returned %d samples, want 5", len(got))
	}

	for i, s := range got {
		want := 995 + i
		if s.WorkersActive != want {
			t.Errorf("Last(5)[%d].WorkersActive = %d, want %d", i, s.WorkersActive, want)
		}
	}

	// Last(900) should return all 900 samples, oldest first
	all := buf.Last(900)
	if len(all) != 900 {
		t.Fatalf("Last(900) returned %d samples, want 900", len(all))
	}

	// First sample after wrap should be sample 100 (oldest surviving)
	if all[0].WorkersActive != 100 {
		t.Errorf("Last(900)[0].WorkersActive = %d, want 100", all[0].WorkersActive)
	}
	// Last sample should be 999
	if all[899].WorkersActive != 999 {
		t.Errorf("Last(900)[899].WorkersActive = %d, want 999", all[899].WorkersActive)
	}
}

func TestMetricsBuffer_ConcurrentRWSafe(t *testing.T) {
	buf := NewMetricsBuffer()

	var wg sync.WaitGroup
	const numWriters = 4
	const numReaders = 4
	const writesPerGoroutine = 500

	// Writers
	for w := 0; w < numWriters; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < writesPerGoroutine; i++ {
				buf.Record(MetricsSample{
					Timestamp:     time.Now(),
					WorkersActive: i,
				})
			}
		}()
	}

	// Readers
	for r := 0; r < numReaders; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < writesPerGoroutine; i++ {
				_ = buf.Last(10)
				_ = buf.Len()
			}
		}()
	}

	wg.Wait()

	// After all writes, buffer should have samples (exact count depends on ordering)
	if buf.Len() == 0 {
		t.Fatal("buffer is empty after concurrent writes")
	}
	if buf.Len() > metricsBufferCap {
		t.Fatalf("Len() = %d exceeds capacity %d", buf.Len(), metricsBufferCap)
	}
}

func TestMetricsBuffer_MetricsSampleFields(t *testing.T) {
	// Verify all expected fields can be set and retrieved.
	sample := MetricsSample{
		Timestamp:     time.Now(),
		BeadsClosed:   12,
		QueueReady:    5,
		QueueWIP:      3,
		QueueBlocked:  1,
		WorkersActive: 2,
		WorkersIdle:   1,
		WorkersTotal:  3,
		Workers: []WorkerSample{
			{ID: "w-1", ContextPct: 34, State: "executing", BeadID: "oro-abc"},
			{ID: "w-2", ContextPct: 0, State: "idle", BeadID: ""},
		},
	}

	buf := NewMetricsBuffer()
	buf.Record(sample)

	got := buf.Last(1)
	if len(got) != 1 {
		t.Fatalf("Last(1) returned %d samples, want 1", len(got))
	}

	s := got[0]
	if s.BeadsClosed != 12 {
		t.Errorf("BeadsClosed = %d, want 12", s.BeadsClosed)
	}
	if s.QueueReady != 5 {
		t.Errorf("QueueReady = %d, want 5", s.QueueReady)
	}
	if s.QueueWIP != 3 {
		t.Errorf("QueueWIP = %d, want 3", s.QueueWIP)
	}
	if s.QueueBlocked != 1 {
		t.Errorf("QueueBlocked = %d, want 1", s.QueueBlocked)
	}
	if s.WorkersActive != 2 {
		t.Errorf("WorkersActive = %d, want 2", s.WorkersActive)
	}
	if s.WorkersIdle != 1 {
		t.Errorf("WorkersIdle = %d, want 1", s.WorkersIdle)
	}
	if s.WorkersTotal != 3 {
		t.Errorf("WorkersTotal = %d, want 3", s.WorkersTotal)
	}
	if len(s.Workers) != 2 {
		t.Fatalf("len(Workers) = %d, want 2", len(s.Workers))
	}
	if s.Workers[0].ID != "w-1" || s.Workers[0].ContextPct != 34 {
		t.Errorf("Workers[0] = %+v, want ID=w-1, ContextPct=34", s.Workers[0])
	}
}

func TestMetricsBuffer_LastNegativeReturnsNil(t *testing.T) {
	buf := NewMetricsBuffer()
	buf.Record(MetricsSample{Timestamp: time.Now()})

	got := buf.Last(-1)
	if got != nil {
		t.Fatalf("Last(-1) = %v, want nil", got)
	}
}
