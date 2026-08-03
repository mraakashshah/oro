package dispatcher //nolint:testpackage // white-box: shares serial-lane test helpers + guarded canary

import (
	"bytes"
	"context"
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

const fullGateCacheProbeChildEnv = "ORO_QG_FULL_CACHE_PROBE_CHILD"

type fullGateCacheEvidence struct {
	qgDir     string
	goCache   string
	lintCache string
	goCalls   int
	lintCalls int
}

type fullGateProbeResult struct {
	id            string
	exit          int
	worktree      string
	output        string
	trackedStatus string
	evidence      fullGateCacheEvidence
}

type startedFullGateProbe struct {
	id          string
	repoRoot    string
	worktree    string
	evidenceDir string
	cmd         *exec.Cmd
	output      *bytes.Buffer
	cleanupOnce sync.Once
	cleanupErr  error
}

func startFullGateProbe(
	ctx context.Context,
	t *testing.T,
	repoRoot, worktreeParent, proxyDir, evidenceRoot, oroHomeRoot, lockRoot, qgTmp, ambientGo, ambientLint, id string,
) *startedFullGateProbe {
	t.Helper()
	worktree := filepath.Join(worktreeParent, id)
	cmd := exec.Command("git", "-C", repoRoot, "worktree", "add", "--detach", worktree, "HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create full-gate worktree %s: %v\n%s", id, err, out)
	}

	writeFullGateProxies(t, proxyDir)
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("resolve real go: %v", err)
	}
	realLint, err := exec.LookPath("golangci-lint")
	if err != nil {
		t.Fatalf("resolve real golangci-lint: %v", err)
	}
	evidenceDir := filepath.Join(evidenceRoot, id)
	if err := os.MkdirAll(evidenceDir, 0o755); err != nil {
		t.Fatalf("create evidence directory for %s: %v", id, err)
	}
	oroHome := prepareFullGateOroHome(t, repoRoot, oroHomeRoot, id)

	env := cleanQGEnv()
	env = append(env,
		"PATH="+proxyDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"TMPDIR="+qgTmp,
		"GOCACHE="+ambientGo,
		"GOLANGCI_LINT_CACHE="+ambientLint,
		"ORO_HOME="+oroHome,
		"ORO_QG_REPO_ROOT_OVERRIDE="+lockRoot,
		"ORO_QG_LOCK_TIMEOUT_SECONDS=900",
		fullGateCacheProbeChildEnv+"=1",
		"ORO_QG_FULL_CACHE_EVIDENCE="+evidenceDir,
		"ORO_QG_REAL_GO="+realGo,
		"ORO_QG_REAL_GOLANGCI_LINT="+realLint,
	)
	gate := exec.CommandContext(ctx, "bash", filepath.Join(worktree, "scripts", "quality_gate.sh"))
	gate.Dir = worktree
	gate.Env = env
	gate.WaitDelay = 15 * time.Second
	output := &bytes.Buffer{}
	gate.Stdout = output
	gate.Stderr = output
	if err := gate.Start(); err != nil {
		t.Fatalf("start full quality gate %s: %v", id, err)
	}
	probe := &startedFullGateProbe{
		id:          id,
		repoRoot:    repoRoot,
		worktree:    worktree,
		evidenceDir: evidenceDir,
		cmd:         gate,
		output:      output,
	}
	t.Cleanup(func() {
		if err := probe.cleanup(); err != nil {
			t.Errorf("fallback cleanup for full gate %s: %v", id, err)
		}
	})
	return probe
}

func prepareFullGateSharedRoot(t *testing.T, repoRoot, lockRoot string) {
	t.Helper()
	cmd := exec.Command("git", "-C", repoRoot, "rev-parse", "--path-format=absolute", "--git-common-dir")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("resolve common checkout root: %v\n%s", err, output)
	}
	commonRoot := filepath.Dir(strings.TrimSpace(string(output)))
	for _, name := range []string{"node_modules", ".venv"} {
		source := filepath.Join(commonRoot, name)
		if _, err := os.Stat(source); err != nil {
			t.Fatalf("full quality gate dependency %s: %v", source, err)
		}
		if err := os.Symlink(source, filepath.Join(lockRoot, name)); err != nil {
			t.Fatalf("link full quality gate dependency %s: %v", name, err)
		}
	}
}

func prepareFullGateOroHome(t *testing.T, repoRoot, oroHomeRoot, id string) string {
	t.Helper()
	oroHome := filepath.Join(oroHomeRoot, id)
	if err := os.MkdirAll(oroHome, 0o755); err != nil {
		t.Fatalf("create ORO_HOME for %s: %v", id, err)
	}
	for _, name := range []string{"hooks", "beacons"} {
		source := filepath.Join(repoRoot, "assets", name)
		if _, err := os.Stat(source); err != nil {
			t.Fatalf("full quality gate ORO_HOME source %s: %v", source, err)
		}
		if err := os.Symlink(source, filepath.Join(oroHome, name)); err != nil {
			t.Fatalf("link full quality gate ORO_HOME %s for %s: %v", name, id, err)
		}
	}
	return oroHome
}

func (p *startedFullGateProbe) wait(t *testing.T) fullGateProbeResult {
	t.Helper()
	err := p.cmd.Wait()
	exit := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exit = exitErr.ExitCode()
		} else {
			t.Fatalf("wait for full quality gate %s: %v", p.id, err)
		}
	}
	status, statusErr := exec.Command("git", "-C", p.worktree, "status", "--porcelain", "--untracked-files=no").CombinedOutput()
	if statusErr != nil {
		t.Errorf("read tracked status for full gate %s: %v\n%s", p.id, statusErr, status)
	}
	return fullGateProbeResult{
		id:            p.id,
		exit:          exit,
		worktree:      p.worktree,
		output:        p.output.String(),
		trackedStatus: strings.TrimSpace(string(status)),
		evidence:      collectFullGateEvidence(t, p.evidenceDir),
	}
}

func (p *startedFullGateProbe) cleanup() error {
	p.cleanupOnce.Do(func() {
		if p.cmd != nil && p.cmd.Process != nil && p.cmd.ProcessState == nil {
			_ = p.cmd.Process.Kill()
			_ = p.cmd.Wait()
		}
		remove := exec.Command("git", "-C", p.repoRoot, "worktree", "remove", "--force", p.worktree)
		if out, err := remove.CombinedOutput(); err != nil {
			p.cleanupErr = errors.New(err.Error() + ": " + strings.TrimSpace(string(out)))
		}
	})
	return p.cleanupErr
}

func writeFullGateProxies(t *testing.T, proxyDir string) {
	t.Helper()
	proxy := func(tool, realToolEnv string) string {
		return `#!/bin/sh
set -eu
evidence_dir="$ORO_QG_FULL_CACHE_EVIDENCE"
mkdir -p "$evidence_dir"
record="$evidence_dir/` + tool + `.$$.env"
{
	printf 'QG_DIR=%s\n' "${GOCACHE%/go-build-cache}"
	printf 'GOCACHE=%s\n' "$GOCACHE"
	printf 'GOLANGCI_LINT_CACHE=%s\n' "$GOLANGCI_LINT_CACHE"
	printf 'ARGS='; printf '%s ' "$@"; printf '\n'
} >"$record"
exec "$` + realToolEnv + `" "$@"
`
	}
	for _, spec := range []struct {
		name    string
		realEnv string
	}{
		{name: "go", realEnv: "ORO_QG_REAL_GO"},
		{name: "golangci-lint", realEnv: "ORO_QG_REAL_GOLANGCI_LINT"},
	} {
		if err := os.WriteFile(filepath.Join(proxyDir, spec.name), []byte(proxy(spec.name, spec.realEnv)), 0o755); err != nil {
			t.Fatalf("write %s proxy: %v", spec.name, err)
		}
	}
}

func collectFullGateEvidence(t *testing.T, evidenceDir string) fullGateCacheEvidence {
	t.Helper()
	entries, err := os.ReadDir(evidenceDir)
	if err != nil {
		t.Errorf("read full-gate evidence %s: %v", evidenceDir, err)
		return fullGateCacheEvidence{}
	}
	var evidence fullGateCacheEvidence
	setPath := func(label string, dst *string, value string) {
		t.Helper()
		if *dst != "" && *dst != value {
			t.Errorf("full-gate evidence changed %s from %q to %q", label, *dst, value)
			return
		}
		*dst = value
	}
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case strings.HasPrefix(name, "go."):
			evidence.goCalls++
		case strings.HasPrefix(name, "golangci-lint."):
			evidence.lintCalls++
		default:
			continue
		}
		content, readErr := os.ReadFile(filepath.Join(evidenceDir, name))
		if readErr != nil {
			t.Errorf("read proxy evidence %s: %v", name, readErr)
			continue
		}
		for _, line := range strings.Split(string(content), "\n") {
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			switch key {
			case "QG_DIR":
				setPath("QG_DIR", &evidence.qgDir, value)
			case "GOCACHE":
				setPath("GOCACHE", &evidence.goCache, value)
			case "GOLANGCI_LINT_CACHE":
				setPath("GOLANGCI_LINT_CACHE", &evidence.lintCache, value)
			}
		}
	}
	return evidence
}

func TestConcurrentFullQualityGatesUseIsolatedGoCaches(t *testing.T) {
	if os.Getenv(fullGateCacheProbeChildEnv) == "1" {
		t.Skip("nested full quality gate")
	}
	for _, tool := range []string{"bash", "git", "go", "golangci-lint"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	root := t.TempDir()
	worktreeParent := filepath.Join(root, "worktrees")
	proxyDir := filepath.Join(root, "proxy")
	evidenceRoot := filepath.Join(root, "evidence")
	oroHomeRoot := filepath.Join(root, "oro-home")
	lockRoot := filepath.Join(root, "locks")
	qgTmp := filepath.Join(root, "qg-tmp")
	ambientGo := filepath.Join(root, "ambient", "go-build")
	ambientLint := filepath.Join(root, "ambient", "golangci-lint")
	for _, dir := range []string{worktreeParent, proxyDir, evidenceRoot, oroHomeRoot, lockRoot, qgTmp, ambientGo, ambientLint} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create full-gate fixture directory %s: %v", dir, err)
		}
	}

	repoRoot := worktreeRoot(t)
	prepareFullGateSharedRoot(t, repoRoot, lockRoot)
	first := startFullGateProbe(ctx, t, repoRoot, worktreeParent, proxyDir, evidenceRoot, oroHomeRoot, lockRoot, qgTmp, ambientGo, ambientLint, "full-a")
	second := startFullGateProbe(ctx, t, repoRoot, worktreeParent, proxyDir, evidenceRoot, oroHomeRoot, lockRoot, qgTmp, ambientGo, ambientLint, "full-b")
	results := []fullGateProbeResult{first.wait(t), second.wait(t)}

	for _, result := range results {
		if result.exit != 0 {
			t.Fatalf("full gate %s failed: exit=%d\n%s", result.id, result.exit, result.output)
		}
		t.Logf("full gate %s worktree=%s QG_DIR=%s GOCACHE=%s GOLANGCI_LINT_CACHE=%s\n%s", result.id, result.worktree, result.evidence.qgDir, result.evidence.goCache, result.evidence.lintCache, result.output)
		for _, marker := range []string{"GO TIER 2", "golangci-lint", "GO TIER 3", "go test + coverage", "Quality gate PASSED"} {
			if !strings.Contains(result.output, marker) {
				t.Errorf("full gate %s did not execute marker %q", result.id, marker)
			}
		}
		if result.evidence.goCalls == 0 || result.evidence.lintCalls == 0 {
			t.Errorf("full gate %s proxy calls: go=%d lint=%d, want both nonzero", result.id, result.evidence.goCalls, result.evidence.lintCalls)
		}
		if result.evidence.qgDir == "" || result.evidence.goCache != filepath.Join(result.evidence.qgDir, "go-build-cache") ||
			result.evidence.lintCache != filepath.Join(result.evidence.qgDir, "golangci-lint-cache") {
			t.Errorf("full gate %s caches outside QG_DIR: %#v", result.id, result.evidence)
		}
		if result.trackedStatus != "" {
			t.Errorf("full gate %s changed tracked files:\n%s", result.id, result.trackedStatus)
		}
		if _, err := os.Stat(result.evidence.qgDir); !os.IsNotExist(err) {
			t.Errorf("full gate %s left QG_DIR %s: %v", result.id, result.evidence.qgDir, err)
		}
		logs := strings.ToLower(result.output)
		for _, signature := range []string{"could not import", "could not load export data", "no such cache file"} {
			if strings.Contains(logs, signature) {
				t.Errorf("full gate %s log contains %q", result.id, signature)
			}
		}
	}
	if results[0].worktree == results[1].worktree || filepath.Dir(results[0].worktree) != worktreeParent ||
		filepath.Dir(results[1].worktree) != worktreeParent {
		t.Errorf("full gates did not use distinct sibling worktrees: %q and %q", results[0].worktree, results[1].worktree)
	}
	if results[0].evidence.qgDir == results[1].evidence.qgDir ||
		results[0].evidence.goCache == results[1].evidence.goCache ||
		results[0].evidence.lintCache == results[1].evidence.lintCache {
		t.Errorf("full gates reused cache paths: %#v / %#v", results[0].evidence, results[1].evidence)
	}
	for _, dir := range []string{ambientGo, ambientLint} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read hostile ambient cache %s: %v", dir, err)
		}
		if len(entries) != 0 {
			t.Errorf("hostile ambient cache %s was modified: %v", dir, entries)
		}
	}

	for _, probe := range []*startedFullGateProbe{first, second} {
		if err := probe.cleanup(); err != nil {
			t.Errorf("clean up full gate %s: %v", probe.id, err)
		}
		if probe.cmd.ProcessState == nil || !probe.cmd.ProcessState.Exited() {
			t.Errorf("full gate %s process did not exit", probe.id)
		}
		if _, err := os.Stat(probe.worktree); !os.IsNotExist(err) {
			t.Errorf("full gate %s left worktree %s: %v", probe.id, probe.worktree, err)
		}
	}
	registry, err := exec.Command("git", "-C", repoRoot, "worktree", "list", "--porcelain").CombinedOutput()
	if err != nil {
		t.Fatalf("list worktree registry: %v\n%s", err, registry)
	}
	for _, result := range results {
		if strings.Contains(string(registry), result.worktree) {
			t.Errorf("worktree registry retained %s", result.worktree)
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
