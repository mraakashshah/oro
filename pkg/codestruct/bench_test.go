//go:build cgo && darwin && arm64 && !race

// Performance-budget tests for pkg/codestruct (§6.10, harness architecture spec).
// Build constraints restrict to M-series Macs without the race detector:
// CI runs go test -race on linux/amd64 where race overhead + platform mismatch
// push p50/p99 past targets and produce noise failures.

package codestruct_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"oro/pkg/codestruct"
)

const (
	benchWarmup  = 10
	benchSamples = 1000
)

// TestBench asserts §6.10 latency targets:
//
//	SymbolExtraction (1 file, ~500 LOC): p50 < 5 ms,  p99 < 150 ms
//	CallGraph (1 package, 10 files):     p50 < 50 ms, p99 < 200 ms
//
// p99 uses 1000 samples (= 10th-highest) so that 1-2 OS scheduling preemptions
// (~20 ms each) cannot single-handedly push the percentile above the threshold.
//
// Threshold history:
//   - Original §6.10 target: SymbolExtraction p99 < 20 ms.
//   - oro-012r (commit 59ca5098) raised to 50 ms to absorb single-event jitter.
//   - oro-9m1g raised to 150 ms after observing 69 ms p99 under combined
//     SymbolExtraction+CallGraph+QG load with -count=5. In isolation the test
//     still measures ~1 ms p99; the 50 ms gate flaked only when other parallel
//     `go test ./...` packages contended for CPU/GC. 150 ms = ~2× the worst
//     observed wall-clock spike, leaving headroom for real regressions while
//     surviving QG noise. BenchmarkSymbolExtraction remains the authoritative
//     regression-tracking signal — this gate exists to catch order-of-magnitude
//     regressions, not microsecond drift.
func TestBench(t *testing.T) {
	dir := t.TempDir()

	t.Run("SymbolExtraction", func(t *testing.T) {
		file := filepath.Join(dir, "bench500.go")
		if err := writeLargeGoFile(file); err != nil {
			t.Fatalf("writeLargeGoFile: %v", err)
		}
		for range benchWarmup {
			_, _ = codestruct.ExtractGoSymbols(file)
		}
		samples := make([]time.Duration, benchSamples)
		for i := range benchSamples {
			start := time.Now()
			if _, err := codestruct.ExtractGoSymbols(file); err != nil {
				t.Fatalf("ExtractGoSymbols: %v", err)
			}
			samples[i] = time.Since(start)
		}
		assertP50P99(t, "SymbolExtraction", samples, 5*time.Millisecond, 150*time.Millisecond)
	})

	t.Run("CallGraph", func(t *testing.T) {
		pkgDir := filepath.Join(dir, "bench10pkg")
		files, pkgSymbols, err := setup10FilePkg(pkgDir)
		if err != nil {
			t.Fatalf("setup10FilePkg: %v", err)
		}
		for range benchWarmup {
			_, _, _ = codestruct.BuildCallGraph(files, pkgSymbols)
		}
		samples := make([]time.Duration, benchSamples)
		for i := range benchSamples {
			start := time.Now()
			if _, _, err := codestruct.BuildCallGraph(files, pkgSymbols); err != nil {
				t.Fatalf("BuildCallGraph: %v", err)
			}
			samples[i] = time.Since(start)
		}
		assertP50P99(t, "CallGraph", samples, 50*time.Millisecond, 200*time.Millisecond)
	})
}

// assertP50P99 sorts samples, computes p50/p99, and fails t if either target is exceeded.
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

// BenchmarkSymbolExtraction measures raw ExtractGoSymbols throughput for profiling.
func BenchmarkSymbolExtraction(b *testing.B) {
	dir := b.TempDir()
	file := filepath.Join(dir, "bench500.go")
	if err := writeLargeGoFile(file); err != nil {
		b.Fatalf("writeLargeGoFile: %v", err)
	}
	b.ResetTimer()
	for b.Loop() {
		if _, err := codestruct.ExtractGoSymbols(file); err != nil {
			b.Fatalf("ExtractGoSymbols: %v", err)
		}
	}
}

// BenchmarkCallGraph measures raw BuildCallGraph throughput for profiling.
func BenchmarkCallGraph(b *testing.B) {
	dir := b.TempDir()
	pkgDir := filepath.Join(dir, "bench10pkg")
	files, pkgSymbols, err := setup10FilePkg(pkgDir)
	if err != nil {
		b.Fatalf("setup10FilePkg: %v", err)
	}
	b.ResetTimer()
	for b.Loop() {
		if _, _, err := codestruct.BuildCallGraph(files, pkgSymbols); err != nil {
			b.Fatalf("BuildCallGraph: %v", err)
		}
	}
}

// writeLargeGoFile writes a synthetic ~500-LOC valid Go file to path.
// 40 funcs × 8 lines + 18 struct+method blocks × 10 lines + 4 header = ~504 lines.
func writeLargeGoFile(path string) error {
	var b strings.Builder
	b.WriteString("package bench\n\nimport \"fmt\"\n\n")
	for i := range 40 {
		fmt.Fprintf(&b,
			"// Func%d returns a formatted string.\nfunc Func%d(x int) string {\n\tif x < 0 {\n\t\treturn fmt.Sprintf(\"neg%%d\", x)\n\t}\n\treturn fmt.Sprintf(\"val%%d\", x)\n}\n\n",
			i, i)
	}
	for i := range 18 {
		fmt.Fprintf(&b,
			"// Type%d is a generated struct.\ntype Type%d struct {\n\tField1 string\n\tField2 int\n\tField3 bool\n}\n\n// Method%d returns Field1.\nfunc (t Type%d) Method%d() string { return t.Field1 }\n\n",
			i, i, i, i, i)
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

// setup10FilePkg creates a 10-file Go package in dir, extracts symbols, and
// returns the file list and pre-populated pkgSymbols map for BuildCallGraph.
func setup10FilePkg(dir string) ([]string, map[string][]codestruct.Symbol, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, nil, fmt.Errorf("mkdir: %w", err)
	}
	files := make([]string, 10)
	pkgSymbols := make(map[string][]codestruct.Symbol, 10)
	for i := range 10 {
		fp := filepath.Join(dir, fmt.Sprintf("file%d.go", i))
		if err := writeBenchPkgFile(fp, i); err != nil {
			return nil, nil, err
		}
		syms, err := codestruct.ExtractGoSymbols(fp)
		if err != nil {
			return nil, nil, fmt.Errorf("ExtractGoSymbols %s: %w", fp, err)
		}
		files[i] = fp
		pkgSymbols[fp] = syms
	}
	return files, pkgSymbols, nil
}

// writeBenchPkgFile writes one file in the bench package.
// Each function calls two others from the package to create resolvable call edges.
func writeBenchPkgFile(path string, fileIdx int) error {
	var b strings.Builder
	b.WriteString("package bench\n\nimport \"fmt\"\n\n")
	for j := range 10 {
		n := fileIdx*10 + j
		callee1 := (n + 1) % 100
		callee2 := (n + 2) % 100
		fmt.Fprintf(&b,
			"// BenchFunc%d does computation.\nfunc BenchFunc%d(x int) string {\n\ta := BenchFunc%d(x)\n\tb := BenchFunc%d(x+1)\n\treturn fmt.Sprintf(\"%d:%%s%%s\", a, b)\n}\n\n",
			n, n, callee1, callee2, n)
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}
