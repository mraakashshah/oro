//go:build darwin && arm64 && !race

// Performance-budget tests for the journey hot-path (§4.5, harness architecture
// spec). Build constraints restrict to M-series Macs without the race detector:
// CI runs go test -race on linux/amd64 where race overhead + platform mismatch
// push p50/p99 past targets and produce noise failures.

//nolint:testpackage // accesses store.db for cleanup
package beadstore

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"oro/pkg/beadstore/migrations"
)

const (
	benchWarmup  = 10
	benchSamples = 1000
)

// TestJourneyBench asserts §4.5 hot-path latency targets:
//
//	AppendJourney (single event):        p50 < 1 ms,  p99 < 20 ms
//	LatestJourney (last 50 events):      p50 < 2 ms,  p99 < 30 ms
//	Journey (full bead, 1000 events):    p50 < 20 ms, p99 < 100 ms
//
// p99 uses 1000 samples (= 10th-highest) so that 1-2 OS scheduling preemptions
// (~10-20 ms each) cannot single-handedly push the percentile above the threshold.
func TestJourneyBench(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLiteStore(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = store.db.Close() })
	if err := migrations.MigrateToV3(ctx, store.db); err != nil {
		t.Fatalf("MigrateToV3: %v", err)
	}

	t.Run("AppendJourney", func(t *testing.T) {
		if _, err := store.Create(ctx, CreateParams{ID: "bench-append", Title: "append bench"}); err != nil {
			t.Fatalf("Create: %v", err)
		}
		ts := time.Now().UTC()
		counter := 0
		newEvt := func() JourneyEvent {
			counter++
			return JourneyEvent{
				BeadID: "bench-append",
				Ts:     ts.Add(time.Duration(counter) * time.Millisecond).Format(time.RFC3339Nano),
				Actor:  "bench",
				Event:  "note",
			}
		}
		for range benchWarmup {
			_ = store.AppendJourney(ctx, "bench-append", newEvt())
		}
		samples := make([]time.Duration, benchSamples)
		for i := range benchSamples {
			evt := newEvt()
			start := time.Now()
			if err := store.AppendJourney(ctx, "bench-append", evt); err != nil {
				t.Fatalf("AppendJourney: %v", err)
			}
			samples[i] = time.Since(start)
		}
		assertP50P99(t, "AppendJourney", samples, 1*time.Millisecond, 20*time.Millisecond)
	})

	t.Run("LatestJourney50", func(t *testing.T) {
		if _, err := store.Create(ctx, CreateParams{ID: "bench-latest", Title: "latest bench"}); err != nil {
			t.Fatalf("Create: %v", err)
		}
		ts := time.Now().UTC()
		for i := range 100 {
			evt := JourneyEvent{
				BeadID: "bench-latest",
				Ts:     ts.Add(time.Duration(i) * time.Millisecond).Format(time.RFC3339Nano),
				Actor:  "bench",
				Event:  "note",
			}
			if err := store.AppendJourney(ctx, "bench-latest", evt); err != nil {
				t.Fatalf("seed AppendJourney: %v", err)
			}
		}
		for range benchWarmup {
			_, _ = store.LatestJourney(ctx, "bench-latest", 50)
		}
		samples := make([]time.Duration, benchSamples)
		for i := range benchSamples {
			start := time.Now()
			if _, err := store.LatestJourney(ctx, "bench-latest", 50); err != nil {
				t.Fatalf("LatestJourney: %v", err)
			}
			samples[i] = time.Since(start)
		}
		assertP50P99(t, "LatestJourney(50)", samples, 2*time.Millisecond, 30*time.Millisecond)
	})

	t.Run("JourneyFull1000", func(t *testing.T) {
		if _, err := store.Create(ctx, CreateParams{ID: "bench-full", Title: "full bench"}); err != nil {
			t.Fatalf("Create: %v", err)
		}
		ts := time.Now().UTC()
		for i := range 1000 {
			evt := JourneyEvent{
				BeadID: "bench-full",
				Ts:     ts.Add(time.Duration(i) * time.Millisecond).Format(time.RFC3339Nano),
				Actor:  "bench",
				Event:  fmt.Sprintf("event-%04d", i),
			}
			if err := store.AppendJourney(ctx, "bench-full", evt); err != nil {
				t.Fatalf("seed AppendJourney: %v", err)
			}
		}
		epoch := time.Time{}
		for range benchWarmup {
			_, _ = store.Journey(ctx, "bench-full", epoch)
		}
		samples := make([]time.Duration, benchSamples)
		for i := range benchSamples {
			start := time.Now()
			if _, err := store.Journey(ctx, "bench-full", epoch); err != nil {
				t.Fatalf("Journey: %v", err)
			}
			samples[i] = time.Since(start)
		}
		assertP50P99(t, "Journey(full 1000 events)", samples, 20*time.Millisecond, 100*time.Millisecond)
	})
}

// assertP50P99 sorts samples, computes p50/p99, and fails t if either target
// is exceeded.
func assertP50P99(t *testing.T, op string, samples []time.Duration, p50Target, p99Target time.Duration) {
	t.Helper()
	sorted := make([]time.Duration, len(samples))
	copy(sorted, samples)
	slices.Sort(sorted)

	p50 := sorted[len(sorted)/2]
	p99 := sorted[int(float64(len(sorted)-1)*0.99)]

	t.Logf("%s  p50=%v p99=%v  (targets: p50<%v p99<%v)", op, p50, p99, p50Target, p99Target)

	if p50 >= p50Target {
		t.Errorf("%s p50 = %v, want < %v", op, p50, p50Target)
	}
	if p99 >= p99Target {
		t.Errorf("%s p99 = %v, want < %v", op, p99, p99Target)
	}
}

// BenchmarkAppendJourney measures raw AppendJourney throughput for profiling.
func BenchmarkAppendJourney(b *testing.B) {
	ctx := context.Background()
	store, err := OpenSQLiteStore(ctx, filepath.Join(b.TempDir(), "state.db"))
	if err != nil {
		b.Fatalf("OpenSQLiteStore: %v", err)
	}
	defer func() { _ = store.db.Close() }()
	if err := migrations.MigrateToV3(ctx, store.db); err != nil {
		b.Fatalf("MigrateToV3: %v", err)
	}
	if _, err := store.Create(ctx, CreateParams{ID: "bench-append", Title: "append bench"}); err != nil {
		b.Fatalf("Create: %v", err)
	}
	ts := time.Now().UTC()
	b.ResetTimer()
	i := 0
	for b.Loop() {
		evt := JourneyEvent{
			BeadID: "bench-append",
			Ts:     ts.Add(time.Duration(i) * time.Microsecond).Format(time.RFC3339Nano),
			Actor:  "bench",
			Event:  "note",
		}
		if err := store.AppendJourney(ctx, "bench-append", evt); err != nil {
			b.Fatalf("AppendJourney: %v", err)
		}
		i++
	}
}

// BenchmarkLatestJourney50 measures raw LatestJourney(50) throughput.
func BenchmarkLatestJourney50(b *testing.B) {
	ctx := context.Background()
	store, err := OpenSQLiteStore(ctx, filepath.Join(b.TempDir(), "state.db"))
	if err != nil {
		b.Fatalf("OpenSQLiteStore: %v", err)
	}
	defer func() { _ = store.db.Close() }()
	if err := migrations.MigrateToV3(ctx, store.db); err != nil {
		b.Fatalf("MigrateToV3: %v", err)
	}
	if _, err := store.Create(ctx, CreateParams{ID: "bench-latest", Title: "latest bench"}); err != nil {
		b.Fatalf("Create: %v", err)
	}
	ts := time.Now().UTC()
	for i := range 100 {
		evt := JourneyEvent{
			BeadID: "bench-latest",
			Ts:     ts.Add(time.Duration(i) * time.Millisecond).Format(time.RFC3339Nano),
			Actor:  "bench",
			Event:  "note",
		}
		if err := store.AppendJourney(ctx, "bench-latest", evt); err != nil {
			b.Fatalf("seed: %v", err)
		}
	}
	b.ResetTimer()
	for b.Loop() {
		if _, err := store.LatestJourney(ctx, "bench-latest", 50); err != nil {
			b.Fatalf("LatestJourney: %v", err)
		}
	}
}

// BenchmarkJourneyFull1000 measures raw Journey throughput over 1000 events.
func BenchmarkJourneyFull1000(b *testing.B) {
	ctx := context.Background()
	store, err := OpenSQLiteStore(ctx, filepath.Join(b.TempDir(), "state.db"))
	if err != nil {
		b.Fatalf("OpenSQLiteStore: %v", err)
	}
	defer func() { _ = store.db.Close() }()
	if err := migrations.MigrateToV3(ctx, store.db); err != nil {
		b.Fatalf("MigrateToV3: %v", err)
	}
	if _, err := store.Create(ctx, CreateParams{ID: "bench-full", Title: "full bench"}); err != nil {
		b.Fatalf("Create: %v", err)
	}
	ts := time.Now().UTC()
	for i := range 1000 {
		evt := JourneyEvent{
			BeadID: "bench-full",
			Ts:     ts.Add(time.Duration(i) * time.Millisecond).Format(time.RFC3339Nano),
			Actor:  "bench",
			Event:  fmt.Sprintf("event-%04d", i),
		}
		if err := store.AppendJourney(ctx, "bench-full", evt); err != nil {
			b.Fatalf("seed: %v", err)
		}
	}
	epoch := time.Time{}
	b.ResetTimer()
	for b.Loop() {
		if _, err := store.Journey(ctx, "bench-full", epoch); err != nil {
			b.Fatalf("Journey: %v", err)
		}
	}
}
