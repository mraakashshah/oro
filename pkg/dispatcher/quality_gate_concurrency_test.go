package dispatcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

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

type probeResult struct {
	exit       int
	mainPeak   int
	serialPeak int
	mainEnv    string
	serialEnv  string
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
		if ee, ok := err.(*exec.ExitError); ok {
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
				results[i] = runGateProbe(t, markerDir, repoRoot, id, "ORO_QG_MAIN_SLEEP=2", "ORO_QG_SERIAL_SLEEP=0")
			}(i, id)
		}
		wg.Wait()
		// Both main phases must have seen the other running: peak concurrency >= 2.
		if results[0].mainPeak < 2 || results[1].mainPeak < 2 {
			t.Fatalf("main phase serialized: peaks a=%d b=%d, want both >=2 (no global lock in main phase)", results[0].mainPeak, results[1].mainPeak)
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
