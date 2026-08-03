package dispatcher //nolint:testpackage // white-box: shares serial-lane test helpers + guarded canary

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"oro/pkg/testutil/qgserial"
)

// TestSerialLaneRegressionCanary is a guarded no-op that proves the serial lane
// catches regressions: under ORO_QG_INJECT_TIMING_REGRESSION=1 it fails,
// simulating a broken timing invariant that the serialized lane must surface. It
// never fails in production — the inject env is set only by
// TestConcurrentGatesNoTimingFlakeSerialLaneCatchesRegression. It is on the
// canonical serial-lane list, so the main gate skips it and only the serial lane
// runs it.
func TestSerialLaneRegressionCanary(t *testing.T) {
	qgserial.RequireSerial(t)
	if os.Getenv("ORO_QG_INJECT_TIMING_REGRESSION") == "1" {
		t.Fatal("injected timing regression (serial-lane regression-catch proof)")
	}
}

// These tests exercise the quality-gate lock/phase skeleton (oro-hwx2): the main
// phase must run concurrently (no global cross-worktree lock), while the serial
// timing lane runs mutually exclusive under the FIFO lock. To stay fast and
// deterministic they drive the script in "phase marker" mode: the heavy check
// payloads are replaced by a peak-concurrency probe, but the real lock code, the
// inherited-lock fast path, and the serial-env neutralization all run unchanged.

// qgScript returns the absolute path to scripts/quality_gate.sh.
func qgScript(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "scripts", "quality_gate.sh"))
	if err != nil {
		t.Fatalf("abs script path: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("quality_gate.sh not found at %s: %v", p, err)
	}
	return p
}

type cacheProbeResult struct {
	id        string
	exit      int
	worktree  string
	qgDir     string
	goCache   string
	lintCache string
	output    string
}

type startedGateProbe struct {
	id          string
	worktree    string
	cacheReport string
	cmd         *exec.Cmd
	output      *bytes.Buffer
}

func startGateProbe(
	t *testing.T,
	repoRoot, worktreeParent, markerDir, lockRoot, qgTmp, ambientGo, ambientLint, id string,
) *startedGateProbe {
	t.Helper()
	worktree := filepath.Join(worktreeParent, id)
	cmd := exec.Command("git", "-C", repoRoot, "worktree", "add", "--detach", worktree, "HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create disposable worktree %s: %v\n%s", id, err, out)
	}
	t.Cleanup(func() {
		remove := exec.Command("git", "-C", repoRoot, "worktree", "remove", "--force", worktree)
		if out, err := remove.CombinedOutput(); err != nil {
			t.Errorf("remove disposable worktree %s: %v\n%s", id, err, out)
		}
	})

	shimDir := t.TempDir()
	sleepShim := filepath.Join(shimDir, "sleep")
	shim := `#!/bin/sh
set -eu
{
	printf 'QG_DIR=%s\n' "${GOCACHE%/go-build-cache}"
	printf 'GOCACHE=%s\n' "$GOCACHE"
	printf 'GOLANGCI_LINT_CACHE=%s\n' "$GOLANGCI_LINT_CACHE"
} >"$ORO_QG_CACHE_REPORT"
PATH="$ORO_QG_ORIGINAL_PATH"
export PATH
exec sleep "$@"
`
	if err := os.WriteFile(sleepShim, []byte(shim), 0o755); err != nil {
		t.Fatalf("write sleep probe for %s: %v", id, err)
	}

	cacheReport := filepath.Join(markerDir, "cache."+id)
	originalPath := os.Getenv("PATH")
	env := cleanQGEnv()
	env = append(env,
		"PATH="+shimDir+string(os.PathListSeparator)+originalPath,
		"TMPDIR="+qgTmp,
		"GOCACHE="+ambientGo,
		"GOLANGCI_LINT_CACHE="+ambientLint,
		"ORO_QG_ORIGINAL_PATH="+originalPath,
		"ORO_QG_CACHE_REPORT="+cacheReport,
		"ORO_QG_PHASE_MARKER_DIR="+markerDir,
		"ORO_QG_REPO_ROOT_OVERRIDE="+lockRoot,
		"ORO_QG_PROBE_ID="+id,
		"ORO_QG_LOCK_TIMEOUT_SECONDS=60",
		"ORO_QG_MAIN_SLEEP=3",
		"ORO_QG_SERIAL_SLEEP=0",
	)
	gate := exec.Command("bash", filepath.Join(worktree, "scripts", "quality_gate.sh"))
	gate.Dir = worktree
	gate.Env = env
	output := &bytes.Buffer{}
	gate.Stdout = output
	gate.Stderr = output
	if err := gate.Start(); err != nil {
		t.Fatalf("start quality-gate probe %s: %v", id, err)
	}
	return &startedGateProbe{
		id:          id,
		worktree:    worktree,
		cacheReport: cacheReport,
		cmd:         gate,
		output:      output,
	}
}

func (p *startedGateProbe) wait(t *testing.T) cacheProbeResult {
	t.Helper()
	err := p.cmd.Wait()
	exit := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exit = exitErr.ExitCode()
		} else {
			t.Fatalf("wait for quality-gate probe %s: %v", p.id, err)
		}
	}
	result := cacheProbeResult{
		id:       p.id,
		exit:     exit,
		worktree: p.worktree,
		output:   p.output.String(),
	}
	report, readErr := os.ReadFile(p.cacheReport)
	if readErr != nil {
		t.Errorf("read cache report for %s: %v\ngate output:\n%s", p.id, readErr, result.output)
		return result
	}
	for _, line := range strings.Split(string(report), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "QG_DIR":
			result.qgDir = value
		case "GOCACHE":
			result.goCache = value
		case "GOLANGCI_LINT_CACHE":
			result.lintCache = value
		}
	}
	return result
}

func TestConcurrentQualityGatesUseDistinctGoCaches(t *testing.T) {
	for _, tool := range []string{"bash", "git", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}

	root := t.TempDir()
	worktreeParent := filepath.Join(root, "worktrees")
	markerDir := filepath.Join(root, "markers")
	lockRoot := filepath.Join(root, "locks")
	qgTmp := filepath.Join(root, "qg-tmp")
	ambientGo := filepath.Join(root, "ambient", "go-build")
	ambientLint := filepath.Join(root, "ambient", "golangci-lint")
	for _, dir := range []string{worktreeParent, markerDir, lockRoot, qgTmp, ambientGo, ambientLint} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create fixture directory %s: %v", dir, err)
		}
	}

	first := startGateProbe(t, worktreeRoot(t), worktreeParent, markerDir, lockRoot, qgTmp, ambientGo, ambientLint, "cache-a")
	second := startGateProbe(t, worktreeRoot(t), worktreeParent, markerDir, lockRoot, qgTmp, ambientGo, ambientLint, "cache-b")
	results := []cacheProbeResult{first.wait(t), second.wait(t)}

	for _, result := range results {
		if result.exit != 0 {
			t.Fatalf("gate %s failed: exit=%d\n%s", result.id, result.exit, result.output)
		}
		t.Logf("gate %s worktree=%s QG_DIR=%s GOCACHE=%s GOLANGCI_LINT_CACHE=%s", result.id, result.worktree, result.qgDir, result.goCache, result.lintCache)
		if result.qgDir == "" || result.goCache != filepath.Join(result.qgDir, "go-build-cache") ||
			result.lintCache != filepath.Join(result.qgDir, "golangci-lint-cache") {
			t.Errorf("gate %s reported caches outside its QG_DIR: qg=%q go=%q lint=%q", result.id, result.qgDir, result.goCache, result.lintCache)
		}
	}
	if results[0].worktree == results[1].worktree || filepath.Dir(results[0].worktree) != worktreeParent ||
		filepath.Dir(results[1].worktree) != worktreeParent {
		t.Errorf("gates did not run from distinct sibling worktrees: %q and %q", results[0].worktree, results[1].worktree)
	}
	if results[0].qgDir == results[1].qgDir || results[0].goCache == results[1].goCache ||
		results[0].lintCache == results[1].lintCache {
		t.Errorf("concurrent gates reused cache paths: %#v / %#v", results[0], results[1])
	}
	if max(readIntMarker(markerDir, "peak.main.cache-a"), readIntMarker(markerDir, "peak.main.cache-b")) < 2 {
		t.Error("quality-gate probes did not overlap in the main phase")
	}

	for _, dir := range []string{ambientGo, ambientLint} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read ambient cache %s: %v", dir, err)
		}
		if len(entries) != 0 {
			t.Errorf("ambient cache %s was modified: %v", dir, entries)
		}
	}

	logs := strings.ToLower(results[0].output + "\n" + results[1].output)
	for _, signature := range []string{"could not import", "could not load export data", "no such cache file"} {
		if strings.Contains(logs, signature) {
			t.Errorf("concurrent gate logs contain %q:\n%s", signature, logs)
		}
	}
}

type probeResult struct {
	exit       int
	mainPeak   int
	serialPeak int
	mainEnv    string
	serialEnv  string
	output     string
}

func TestProbeFailureSummaryIncludesDiagnostics(t *testing.T) {
	markerDir := t.TempDir()
	r := runGateProbe(t, markerDir, t.TempDir(), "broken", "TMPDIR=/does-not-exist/oro-qg-probe")
	if r.exit == 0 {
		t.Fatal("probe unexpectedly passed with an unusable TMPDIR")
	}
	if strings.TrimSpace(r.output) == "" {
		t.Fatal("failed probe did not capture output")
	}

	summary := probeFailureSummary("serial lane", "broken", r)
	for _, want := range []string{"serial lane", "broken", "exit=" + strconv.Itoa(r.exit), "output:", strings.TrimSpace(r.output)} {
		if !strings.Contains(summary, want) {
			t.Errorf("failure summary missing %q:\n%s", want, summary)
		}
	}
}

// runGateProbe runs the script in phase-marker mode and returns the observed
// peaks/env. extraEnv entries (KEY=VALUE) are appended after the defaults.
func runGateProbe(t *testing.T, markerDir, repoRoot, id string, extraEnv ...string) probeResult {
	t.Helper()
	env := cleanQGEnv()
	env = append(env,
		"ORO_QG_PHASE_MARKER_DIR="+markerDir,
		"ORO_QG_REPO_ROOT_OVERRIDE="+repoRoot,
		"ORO_QG_PROBE_ID="+id,
		"ORO_QG_LOCK_TIMEOUT_SECONDS=60",
	)
	env = append(env, extraEnv...)

	cmd := exec.Command("bash", qgScript(t))
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	exit := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exit = ee.ExitCode()
		} else {
			t.Fatalf("probe %s run error: %v\n%s", id, err, out)
		}
	}
	return probeResult{
		exit:       exit,
		mainPeak:   readIntMarker(markerDir, "peak.main."+id),
		serialPeak: readIntMarker(markerDir, "peak.serial."+id),
		mainEnv:    readMarker(markerDir, "env.main."+id),
		serialEnv:  readMarker(markerDir, "env.serial."+id),
		output:     string(out),
	}
}

func probeFailureSummary(phase, id string, result probeResult) string {
	return "quality-gate probe failed: phase=" + phase + " id=" + id +
		" exit=" + strconv.Itoa(result.exit) + " output:\n" + result.output
}

func assertProbeSucceeded(t *testing.T, phase, id string, result probeResult) {
	t.Helper()
	if result.exit != 0 {
		t.Fatal(probeFailureSummary(phase, id, result))
	}
}

func cleanQGEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "ORO_QG_") {
			continue
		}
		env = append(env, kv)
	}
	return env
}

func readMarker(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func readIntMarker(dir, name string) int {
	s := readMarker(dir, name)
	if s == "" {
		return -1
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return -1
	}
	return n
}

// worktreeRoot returns the repo/worktree root (parent of scripts/), so the script
// can resolve `go test ./pkg/dispatcher` correctly regardless of the test CWD.
func worktreeRoot(t *testing.T) string {
	t.Helper()
	return filepath.Dir(filepath.Dir(qgScript(t)))
}

// runSerialLaneOnly runs the script's serial-lane-only path (real go test) and
// returns its exit code and wall time. It runs from the worktree root so the go
// test package path resolves, and isolates the lock under repoRoot.
func runSerialLaneOnly(t *testing.T, repoRoot, runOverride string, injectRegression bool) (int, time.Duration, string) {
	t.Helper()
	env := cleanQGEnv()
	env = append(env,
		"ORO_QG_SERIAL_LANE_ONLY=1",
		"ORO_QG_REPO_ROOT_OVERRIDE="+repoRoot,
		"ORO_QG_LOCK_TIMEOUT_SECONDS=60",
	)
	if runOverride != "" {
		env = append(env, "ORO_QG_SERIAL_LANE_RUN_OVERRIDE="+runOverride)
	}
	if injectRegression {
		env = append(env, "ORO_QG_INJECT_TIMING_REGRESSION=1")
	}
	cmd := exec.Command("bash", qgScript(t))
	cmd.Env = env
	cmd.Dir = worktreeRoot(t)
	start := time.Now()
	out, err := cmd.CombinedOutput()
	dur := time.Since(start)
	exit := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exit = ee.ExitCode()
		} else {
			t.Fatalf("serial-lane-only run error: %v\n%s", err, out)
		}
	}
	return exit, dur, string(out)
}

func TestRunSerialLaneOnlyRetainsNestedFailureOutput(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not available")
	}

	exit, _, output := runSerialLaneOnly(t, t.TempDir(), "^TestSerialLaneRegressionCanary$", true)
	if exit == 0 {
		t.Fatal("serial lane unexpectedly passed with an injected regression")
	}
	if !strings.Contains(output, "injected timing regression (serial-lane regression-catch proof)") {
		t.Fatalf("serial lane failure lost nested test output:\n%s", output)
	}
}

func TestConcurrentQualityGatesSerializeDispatcherAggregateSuite(t *testing.T) {
	assertConcurrentQualityGatesSerializeDispatcherAggregateSuite(t)
}

// TestConcurrentGatesNoTimingFlakeSerialLaneCatchesRegression is the integration
// proof for oro-eee8: concurrent main phases run lock-free and flake-free (guarded
// tests skipped, even when the serial-lane env leaks in ambiently), while the
// serialized lane still runs the guarded tests and catches a regression.
func TestConcurrentGatesNoTimingFlakeSerialLaneCatchesRegression(t *testing.T) {
	assertConcurrentQualityGatesSerializeDispatcherAggregateSuite(t)
}

func assertConcurrentQualityGatesSerializeDispatcherAggregateSuite(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not available")
	}

	t.Run("concurrent main phases are lock-free and neutralize leaked serial env", func(t *testing.T) {
		markerDir := t.TempDir()
		repoRoot := t.TempDir()
		const gates = 3
		var wg sync.WaitGroup
		results := make([]probeResult, gates)
		ids := []string{"g0", "g1", "g2"}
		for i := 0; i < gates; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				// Ambient leak: a stray ORO_QG_SERIAL_LANE=1 must NOT reach the main phase.
				results[i] = runGateProbe(t, markerDir, repoRoot, ids[i],
					"ORO_QG_SERIAL_LANE=1", "ORO_QG_MAIN_SLEEP=3", "ORO_QG_SERIAL_SLEEP=0")
			}(i)
		}
		wg.Wait()
		maxPeak := 0
		for i, r := range results {
			assertProbeSucceeded(t, "concurrent main phase", ids[i], r)
			maxPeak = max(maxPeak, r.mainPeak)
			// These are deterministic per gate regardless of overlap timing:
			if r.mainEnv != "" {
				t.Errorf("gate %s main phase saw ORO_QG_SERIAL_LANE=%q; leaked env must be neutralized so guarded tests stay skipped there", ids[i], r.mainEnv)
			}
			if r.serialEnv != "1" {
				t.Errorf("gate %s serial lane saw ORO_QG_SERIAL_LANE=%q, want 1 (only place guarded tests run)", ids[i], r.serialEnv)
			}
		}
		// A global lock would serialize every main phase to peak 1; concurrency
		// means at least one gate observed a sibling running.
		if maxPeak < 2 {
			t.Errorf("no concurrent main-phase overlap observed (all peaks <2); the global lock must be gone from the main phase")
		}
	})

	t.Run("serial lane catches a broken timing invariant", func(t *testing.T) {
		canary := "^TestSerialLaneRegressionCanary$"

		exitClean, _, output := runSerialLaneOnly(t, t.TempDir(), canary, false)
		if exitClean != 0 {
			t.Fatalf("serial lane failed with no regression injected: exit=%d\n%s", exitClean, output)
		}

		exitBroken, _, _ := runSerialLaneOnly(t, t.TempDir(), canary, true)
		if exitBroken == 0 {
			t.Fatal("serial lane did NOT catch the injected timing regression (exit=0)")
		}
	})

	t.Run("serial lane wall-time is a small minority of the gate", func(t *testing.T) {
		if testing.Short() {
			t.Skip("skipping timing quantification in -short mode")
		}
		// Run the full guarded set once (no override) and record wall time. The
		// full gate takes minutes (lint + full test suite + build + vet + vuln);
		// the serial lane running ~two dozen fast tests must be a small minority.
		exit, dur, output := runSerialLaneOnly(t, t.TempDir(), "", false)
		if exit != 0 {
			t.Fatalf("serial lane (full guarded set) failed: exit=%d\n%s", exit, output)
		}
		t.Logf("serial timing lane wall-time: %s (full guarded set)", dur.Round(time.Millisecond))
		const maxLaneTime = 90 * time.Second
		if dur > maxLaneTime {
			t.Errorf("serial lane took %s (> %s); it is no longer a small minority of the gate — reconsider the guarded set", dur, maxLaneTime)
		}
	})
}

func TestMainGateRunsConcurrentSerialLaneSerializes(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	t.Run("main phase runs concurrently without a global lock", func(t *testing.T) {
		markerDir := t.TempDir()
		repoRoot := t.TempDir()
		var wg sync.WaitGroup
		results := make([]probeResult, 2)
		for i, id := range []string{"a", "b"} {
			wg.Add(1)
			go func(i int, id string) {
				defer wg.Done()
				results[i] = runGateProbe(t, markerDir, repoRoot, id, "ORO_QG_MAIN_SLEEP=3", "ORO_QG_SERIAL_SLEEP=0")
			}(i, id)
		}
		wg.Wait()
		assertProbeSucceeded(t, "main phase", "a", results[0])
		assertProbeSucceeded(t, "main phase", "b", results[1])
		// A global lock would serialize the main phases, forcing every peak to 1.
		// Concurrency means at least one probe observed the other running (>=2).
		// (Requiring BOTH to see 2 flakes when process-startup skew approaches the
		// sleep window; max>=2 is the precise not-serialized discriminator.)
		if max(results[0].mainPeak, results[1].mainPeak) < 2 {
			t.Fatalf("main phase serialized: peaks a=%d b=%d, want at least one >=2 (no global lock in main phase)", results[0].mainPeak, results[1].mainPeak)
		}
	})

	t.Run("serial lane serializes under the FIFO lock", func(t *testing.T) {
		markerDir := t.TempDir()
		repoRoot := t.TempDir()
		var wg sync.WaitGroup
		results := make([]probeResult, 2)
		for i, id := range []string{"a", "b"} {
			wg.Add(1)
			go func(i int, id string) {
				defer wg.Done()
				results[i] = runGateProbe(t, markerDir, repoRoot, id, "ORO_QG_MAIN_SLEEP=0", "ORO_QG_SERIAL_SLEEP=2")
			}(i, id)
		}
		wg.Wait()
		assertProbeSucceeded(t, "serial lane", "a", results[0])
		assertProbeSucceeded(t, "serial lane", "b", results[1])
		// Serial lanes must be mutually exclusive: each sees only itself.
		if results[0].serialPeak != 1 || results[1].serialPeak != 1 {
			t.Fatalf("serial lane not serialized: peaks a=%d b=%d, want both ==1", results[0].serialPeak, results[1].serialPeak)
		}
	})

	t.Run("serial-lane env neutralized in main phase, set in serial lane", func(t *testing.T) {
		markerDir := t.TempDir()
		repoRoot := t.TempDir()
		// Ambient leak: ORO_QG_SERIAL_LANE=1 must NOT reach the concurrent main phase.
		r := runGateProbe(t, markerDir, repoRoot, "leak", "ORO_QG_SERIAL_LANE=1", "ORO_QG_MAIN_SLEEP=0", "ORO_QG_SERIAL_SLEEP=0")
		assertProbeSucceeded(t, "serial env probe", "leak", r)
		if r.mainEnv != "" {
			t.Errorf("main phase saw ORO_QG_SERIAL_LANE=%q, want neutralized (empty)", r.mainEnv)
		}
		if r.serialEnv != "1" {
			t.Errorf("serial lane saw ORO_QG_SERIAL_LANE=%q, want 1", r.serialEnv)
		}
	})

	t.Run("inherited lock short-circuits the nested gate", func(t *testing.T) {
		markerDir := t.TempDir()
		repoRoot := t.TempDir()
		lockDir := filepath.Join(repoRoot, ".oro-quality-gate.lock")
		if err := os.Mkdir(lockDir, 0o755); err != nil {
			t.Fatal(err)
		}
		token := "tok-nested-123"
		if err := os.WriteFile(filepath.Join(lockDir, "owner"), []byte("token="+token+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		r := runGateProbe(t, markerDir, repoRoot, "nested",
			"ORO_QG_INHERITED_LOCK_DIR="+lockDir,
			"ORO_QG_INHERITED_LOCK_TOKEN="+token,
			"ORO_QG_MAIN_SLEEP=0", "ORO_QG_SERIAL_SLEEP=0")
		if r.exit != 0 {
			t.Errorf("nested gate exit = %d, want 0 (fast short-circuit)", r.exit)
		}
		if r.mainPeak != -1 || r.serialPeak != -1 {
			t.Errorf("nested gate ran phases (mainPeak=%d serialPeak=%d), want none", r.mainPeak, r.serialPeak)
		}
	})

	t.Run("stale lock is archived and the serial lane proceeds", func(t *testing.T) {
		markerDir := t.TempDir()
		repoRoot := t.TempDir()
		lockDir := filepath.Join(repoRoot, ".oro-quality-gate.lock")
		if err := os.Mkdir(lockDir, 0o755); err != nil {
			t.Fatal(err)
		}
		// No owner file + stale-after 0 => unconditionally stale.
		time.Sleep(50 * time.Millisecond)
		r := runGateProbe(t, markerDir, repoRoot, "stale",
			"ORO_QG_STALE_LOCK_SECONDS=0", "ORO_QG_MAIN_SLEEP=0", "ORO_QG_SERIAL_SLEEP=0")
		assertProbeSucceeded(t, "stale-lock serial lane", "stale", r)
		// The serial lane must have run (archived the stale lock, then acquired).
		if r.serialPeak != 1 {
			t.Fatalf("serial lane did not proceed past stale lock (serialPeak=%d)", r.serialPeak)
		}
		archived, _ := filepath.Glob(lockDir + ".stale.*")
		if len(archived) == 0 {
			t.Errorf("expected a %s.stale.* archive, found none", lockDir)
		}
	})
}
