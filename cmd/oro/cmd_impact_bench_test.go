//go:build cgo

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"oro/pkg/codestruct"
)

// BenchmarkImpact runs ComputeImpact on a synthetic ≤5k-file fixture.
// Run with -benchtime=10x to enforce b.N=10 per the acceptance criteria.
// Fails if any single run exceeds 2 s.
func BenchmarkImpact(b *testing.B) {
	fixtureDir := setupImpactLargeFixture(b)
	targetFile := filepath.Join(fixtureDir, "pkg", "hub", "hub.go")

	b.ResetTimer()

	for i := range b.N {
		start := time.Now()

		_, err := codestruct.ComputeImpact(fixtureDir, targetFile, "Run")
		if err != nil {
			b.Fatalf("run %d: ComputeImpact failed: %v", i, err)
		}

		dur := time.Since(start)
		if dur > 2*time.Second {
			b.Fatalf("run %d took %v, exceeds 2 s p99 limit", i, dur)
		}
	}
}

func setupImpactLargeFixture(tb testing.TB) string {
	tb.Helper()

	dir := tb.TempDir()

	writeFixtureFile(tb, filepath.Join(dir, "go.mod"), "module bench\n\ngo 1.21\n")

	os.MkdirAll(filepath.Join(dir, "pkg", "hub"), 0o755) //nolint:errcheck
	writeFixtureFile(tb, filepath.Join(dir, "pkg", "hub", "hub.go"), `package hub

// Hub is the central coordinator.
type Hub struct{}

// Run starts the hub.
func (h *Hub) Run() error { return nil }
`)

	// One direct caller.
	os.MkdirAll(filepath.Join(dir, "pkg", "caller"), 0o755) //nolint:errcheck
	writeFixtureFile(tb, filepath.Join(dir, "pkg", "caller", "caller.go"), `package caller

import "bench/pkg/hub"

// Start calls the hub.
func Start(h *hub.Hub) {
	_ = h.Run()
}
`)

	// Noise: 4998 additional packages to reach ≤5k files total.
	for i := range 4998 {
		pkgDir := filepath.Join(dir, fmt.Sprintf("pkg/noise%04d", i))
		os.MkdirAll(pkgDir, 0o755) //nolint:errcheck
		writeFixtureFile(tb, filepath.Join(pkgDir, "file.go"), fmt.Sprintf(`package noise%04d

// Noise%04d is a placeholder symbol.
func Noise%04d() {}
`, i, i, i))
	}

	return dir
}

func writeFixtureFile(tb testing.TB, path, content string) {
	tb.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		tb.Fatalf("write fixture %s: %v", path, err)
	}
}
