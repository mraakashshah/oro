package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// ttyStop returns a stopConfig that passes TTY confirmation (simulates interactive terminal).
func ttyStop(pidFile string, fake *fakeCmd, buf *bytes.Buffer) *stopConfig {
	return &stopConfig{
		pidPath:  pidFile,
		sockPath: filepath.Join(filepath.Dir(pidFile), "nonexistent.sock"),
		tmuxName: TmuxSessionName(""),
		runner:   fake,
		w:        buf,
		stdin:    strings.NewReader("YES\n"),
		signalFn: func(pid int) error { return nil },
		aliveFn:  func(pid int) bool { return false },
		killFn:   func(pid int) error { return nil },
		isTTY:    func() bool { return true },
	}
}

func TestStop_SIGINTSucceeds(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
		t.Fatalf("setup PID: %v", err)
	}

	fake := newFakeCmd()
	signaled := false

	var buf bytes.Buffer
	cfg := ttyStop(pidFile, fake, &buf)
	cfg.signalFn = func(pid int) error { signaled = true; return nil }

	if err := runStopSequence(context.Background(), cfg); err != nil {
		t.Fatalf("runStopSequence: %v", err)
	}

	if !signaled {
		t.Error("expected signalFn (SIGINT) to be called")
	}
	if killCall := findCall(fake.calls, "kill-session"); killCall == nil {
		t.Errorf("tmux kill-session not called; calls = %v", fake.calls)
	}
}

func TestStop_SIGINTFailsFallsBackToSIGKILL(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
		t.Fatalf("setup PID: %v", err)
	}

	fake := newFakeCmd()

	var killedWith int
	var buf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	cfg := ttyStop(pidFile, fake, &buf)
	cfg.signalFn = func(pid int) error { return nil }
	cfg.aliveFn = func(pid int) bool { return true } // process won't die
	cfg.killFn = func(pid int) error { killedWith = pid; return nil }

	if err := runStopSequence(ctx, cfg); err != nil {
		t.Fatalf("runStopSequence: %v", err)
	}

	if killedWith == 0 {
		t.Error("expected killFn (SIGKILL) to be called when process won't exit")
	}
}

func TestStop_NotRunning(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")

	var buf bytes.Buffer
	cfg := &stopConfig{
		pidPath:  pidFile,
		sockPath: filepath.Join(tmpDir, "nonexistent.sock"),
		w:        &buf,
	}

	if err := runStopSequence(context.Background(), cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(buf.String(), "not running") {
		t.Errorf("expected 'not running' message, got %q", buf.String())
	}
}

func TestStop_Stale(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	// PID 4000000 is almost certainly not running.
	if err := WritePIDFile(pidFile, 4000000); err != nil {
		t.Fatalf("setup PID: %v", err)
	}

	var buf bytes.Buffer
	cfg := &stopConfig{
		pidPath:  pidFile,
		sockPath: filepath.Join(tmpDir, "nonexistent.sock"),
		w:        &buf,
	}

	if err := runStopSequence(context.Background(), cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(buf.String(), "stale") {
		t.Errorf("expected 'stale' message, got %q", buf.String())
	}

	// PID file should be removed.
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Error("expected PID file to be removed")
	}
}

func TestStopStatusStaleRemovesSocket(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	sockFile := filepath.Join(tmpDir, "oro.sock")

	// PID 4000000 is almost certainly not running.
	if err := WritePIDFile(pidFile, 4000000); err != nil {
		t.Fatalf("setup PID: %v", err)
	}
	// Create a stale socket file.
	if err := os.WriteFile(sockFile, []byte("stale"), 0o600); err != nil {
		t.Fatalf("setup socket: %v", err)
	}

	var buf bytes.Buffer
	cfg := &stopConfig{
		pidPath:  pidFile,
		sockPath: sockFile,
		w:        &buf,
	}

	if err := runStopSequence(context.Background(), cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Error("expected PID file to be removed")
	}
	if _, err := os.Stat(sockFile); !os.IsNotExist(err) {
		t.Error("expected socket file to be removed")
	}
}

func TestStop_RefusedWhenNotTTY(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
		t.Fatalf("setup PID: %v", err)
	}

	var buf bytes.Buffer
	cfg := &stopConfig{
		pidPath:  pidFile,
		sockPath: filepath.Join(tmpDir, "nonexistent.sock"),
		tmuxName: TmuxSessionName(""),
		runner:   newFakeCmd(),
		w:        &buf,
		stdin:    strings.NewReader(""),
		isTTY:    func() bool { return false }, // not a terminal
	}

	err := runStopSequence(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error when stdin is not a TTY")
	}
	if !strings.Contains(err.Error(), "not a TTY") {
		t.Errorf("expected TTY error, got: %v", err)
	}
}

func TestStop_RefusedWhenConfirmationNotYES(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
		t.Fatalf("setup PID: %v", err)
	}

	var buf bytes.Buffer
	cfg := &stopConfig{
		pidPath:  pidFile,
		sockPath: filepath.Join(tmpDir, "nonexistent.sock"),
		tmuxName: TmuxSessionName(""),
		runner:   newFakeCmd(),
		w:        &buf,
		stdin:    strings.NewReader("no\n"),
		isTTY:    func() bool { return true },
	}

	err := runStopSequence(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error when confirmation is not YES")
	}
	if !strings.Contains(err.Error(), "aborted") {
		t.Errorf("expected aborted error, got: %v", err)
	}
}

func TestStop_ForceRequiresEnvVar(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
		t.Fatalf("setup PID: %v", err)
	}

	t.Run("force without env var fails", func(t *testing.T) {
		t.Setenv("ORO_HUMAN_CONFIRMED", "")

		var buf bytes.Buffer
		cfg := &stopConfig{
			pidPath:  pidFile,
			sockPath: filepath.Join(filepath.Dir(pidFile), "nonexistent.sock"),
			tmuxName: TmuxSessionName(""),
			runner:   newFakeCmd(),
			w:        &buf,
			force:    true,
			isTTY:    func() bool { return false },
		}

		err := runStopSequence(context.Background(), cfg)
		if err == nil {
			t.Fatal("expected error when --force used without ORO_HUMAN_CONFIRMED")
		}
		if !strings.Contains(err.Error(), "ORO_HUMAN_CONFIRMED") {
			t.Errorf("expected ORO_HUMAN_CONFIRMED error, got: %v", err)
		}
	})

	t.Run("force with env var succeeds", func(t *testing.T) {
		t.Setenv("ORO_HUMAN_CONFIRMED", "1")

		fake := newFakeCmd()
		var buf bytes.Buffer
		cfg := ttyStop(pidFile, fake, &buf)
		cfg.force = true
		cfg.isTTY = func() bool { return false } // doesn't matter with --force

		err := runStopSequence(context.Background(), cfg)
		if err != nil {
			t.Fatalf("unexpected error with --force and ORO_HUMAN_CONFIRMED=1: %v", err)
		}
	})
}

func TestStopForceKillsWorkerProcessGroups(t *testing.T) {
	t.Setenv("ORO_HUMAN_CONFIRMED", "1")
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
		t.Fatalf("setup PID: %v", err)
	}

	var treeKilledPID int
	var treeKillPatterns []string
	var killedWith int
	var buf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	cfg := ttyStop(pidFile, newFakeCmd(), &buf)
	cfg.force = true
	cfg.isTTY = func() bool { return false }
	cfg.aliveFn = func(pid int) bool { return true }
	cfg.killFn = func(pid int) error { killedWith = pid; return nil }
	cfg.treeKillFn = func(_ context.Context, pid int, patterns []string) error {
		treeKilledPID = pid
		treeKillPatterns = append([]string(nil), patterns...)
		return nil
	}

	if err := runStopSequence(ctx, cfg); err != nil {
		t.Fatalf("runStopSequence: %v", err)
	}
	if treeKilledPID != os.Getpid() {
		t.Fatalf("tree kill pid = %d, want dispatcher pid %d", treeKilledPID, os.Getpid())
	}
	if killedWith != 0 {
		t.Fatalf("single-pid kill fallback was used despite treeKillFn, pid=%d", killedWith)
	}
	wantMarker := "ORO_SOCKET_PATH=" + cfg.sockPath
	joinedPatterns := strings.Join(treeKillPatterns, "\n")
	if !strings.Contains(joinedPatterns, wantMarker) {
		t.Fatalf("tree kill patterns %q missing %q", joinedPatterns, wantMarker)
	}
	for _, tooBroad := range []string{"ORO_ROLE=", "ORO_WORKER_ID=", "ORO_SOCKET_PATH="} {
		if joinedPatterns == tooBroad || strings.Contains(joinedPatterns, tooBroad+"\n") {
			t.Fatalf("tree kill patterns include unscoped marker %q: %q", tooBroad, joinedPatterns)
		}
	}
}

func TestStopScansAndKillsOroOwnedResidualChildren(t *testing.T) {
	t.Setenv("ORO_HUMAN_CONFIRMED", "1")
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
		t.Fatalf("setup PID: %v", err)
	}

	var killed []int
	var buf bytes.Buffer
	cfg := ttyStop(pidFile, newFakeCmd(), &buf)
	cfg.force = true
	cfg.isTTY = func() bool { return false }
	cfg.residualRoots = []string{"/tmp/oro-owned-worktree"}
	cfg.residualMarkers = []string{"ORO_SOCKET_PATH=" + cfg.sockPath}
	snapshots := []processSnapshot{
		{
			PID: 2001, PPID: 1, PGID: 2001, Session: 2001,
			Command: "sh -c ./scripts/quality_gate.sh", Environment: []string{"ORO_SOCKET_PATH=" + cfg.sockPath},
		},
		{PID: 2002, PPID: 1, PGID: 2002, Session: 2002, Command: "go test ./pkg/dispatcher -worktree /tmp/oro-owned-worktree"},
		{PID: 2003, PPID: 1, PGID: 2003, Session: 2003, Command: "go test ./pkg/dispatcher"},
	}
	cfg.residualScanFn = func(_ context.Context, roots, markers []string) ([]ResidualProcess, error) {
		return scanOroResidualProcessSnapshots(snapshots, roots, markers), nil
	}
	cfg.residualKillFn = func(_ context.Context, residuals ...ResidualProcess) error {
		for _, residual := range residuals {
			killed = append(killed, residual.PID)
		}
		return nil
	}

	if err := runStopSequence(context.Background(), cfg); err != nil {
		t.Fatalf("runStopSequence: %v", err)
	}
	if got, want := killed, []int{2001}; !sameInts(got, want) {
		t.Fatalf("killed residual PIDs = %v, want %v", got, want)
	}
	if strings.Contains(buf.String(), "2003") {
		t.Fatalf("unrelated matching process was reported as killed:\n%s", buf.String())
	}
}

func TestResidualScanDoesNotTreatBareToolNamesAsOwnership(t *testing.T) {
	residuals := scanOroResidualProcessSnapshots([]processSnapshot{
		{PID: 2101, PPID: 1, PGID: 2101, Session: 2101, Command: "./scripts/quality_gate.sh"},
		{PID: 2102, PPID: 1, PGID: 2102, Session: 2102, Command: "ops-review --some-other-project"},
		{PID: 2103, PPID: 1, PGID: 2103, Session: 2103, Command: "go test ./pkg/dispatcher -worktree /tmp/oro-owned-worktree"},
	}, []string{"/tmp/oro-owned-worktree"}, defaultOroResidualMarkers("myproject", "/tmp/myproject/oro.sock"))

	if len(residuals) != 0 {
		t.Fatalf("residuals = %+v, want no matches without exact scoped markers", residuals)
	}
}

func TestResidualScanUsesScopedMarkers(t *testing.T) {
	residuals := scanOroResidualProcessSnapshots([]processSnapshot{
		{
			PID: 2111, PPID: 1, PGID: 2111, Session: 2111, Command: "go test ./pkg/dispatcher",
			Environment: []string{"ORO_ROLE=worker", "ORO_WORKER_ID=w1"},
		},
		{
			PID: 2112, PPID: 1, PGID: 2112, Session: 2112, Command: "./scripts/quality_gate.sh",
			Environment: []string{"ORO_SOCKET_PATH=/tmp/project-a/oro.sock", "ORO_WORKER_ID=w1"},
		},
		{
			PID: 2113, PPID: 1, PGID: 2113, Session: 2113, Command: "./scripts/quality_gate.sh",
			Environment: []string{"ORO_SOCKET_PATH=/tmp/project-ab/oro.sock", "ORO_WORKER_ID=w1"},
		},
		{
			PID: 2114, PPID: 1, PGID: 2114, Session: 2114, Command: "ops-review",
			Environment: []string{"ORO_SOCKET_PATH=/tmp/project-a/oro.sock", "ORO_WORKER_ID=w2"},
		},
		{PID: 2115, PPID: 1, PGID: 2115, Session: 2115, Command: "go test ./pkg/dispatcher -worktree /tmp/project-a"},
		{
			PID: 2116, PPID: 1, PGID: 2116, Session: 2116, Command: "sleep 3600",
			Environment: []string{"ORO_SOCKET_PATH=/tmp/project-a/oro.sock", "ORO_WORKER_ID=w10"},
		},
	}, []string{"/tmp/project-a"}, []string{"ORO_SOCKET_PATH=/tmp/project-a/oro.sock", "ORO_WORKER_ID=w1"})

	if got, want := residualPIDs(residuals), []int{2112}; !sameInts(got, want) {
		t.Fatalf("residual PIDs = %v, want scoped project/socket matches %v", got, want)
	}
}

func TestResidualScanUsesDefaultSocketOwnershipMarker(t *testing.T) {
	const socketPath = "/tmp/project-a/oro.sock"
	residuals := scanOroResidualProcessSnapshots([]processSnapshot{
		{
			PID: 2117, PPID: 1, PGID: 2117, Session: 2117, Command: "./scripts/quality_gate.sh",
			Environment: []string{"ORO_SOCKET_PATH=" + socketPath, "ORO_WORKER_ID=w1", "ORO_PROJECT=project-a"},
		},
	}, nil, defaultOroResidualMarkers("project-a", socketPath))

	if got, want := residualPIDs(residuals), []int{2117}; !sameInts(got, want) {
		t.Fatalf("residual PIDs = %v, want default socket ownership match %v", got, want)
	}
}

func TestResidualScanRejectsOwnershipMarkersPresentOnlyInArgv(t *testing.T) {
	markers := []string{
		"ORO_SOCKET_PATH=/tmp/project-a/oro.sock",
		"ORO_WORKER_ID=w1",
	}
	residuals := scanOroResidualProcessSnapshots([]processSnapshot{
		{
			PID: 2117, PPID: 1, PGID: 2117, Session: 2117,
			Command: "foreign-helper --note " + markers[0] + " --note " + markers[1],
		},
	}, nil, markers)

	if len(residuals) != 0 {
		t.Fatalf("residuals = %+v, want no ownership match from argv-only markers", residuals)
	}
}

func TestStopResidualScanUsesTypedEnvironmentEntries(t *testing.T) {
	markers := []string{
		"ORO_SOCKET_PATH=/tmp/project-a/oro.sock",
		"ORO_WORKER_ID=w1",
	}
	residuals := scanOroResidualProcessSnapshots([]processSnapshot{
		{
			PID: 2211, PPID: 1, PGID: 2211, Session: 2211,
			Command:     "foreign-helper",
			Environment: []string{"NOTE=foreign " + markers[0] + " " + markers[1] + " text"},
		},
		{
			PID: 2212, PPID: 1, PGID: 2212, Session: 2212,
			Command:     "owned-helper",
			Environment: []string{"PATH=/bin", markers[0], markers[1], "NOTE=ordinary value"},
		},
	}, nil, markers)

	if got, want := residualPIDs(residuals), []int{2212}; !sameInts(got, want) {
		t.Fatalf("residual PIDs = %v, want typed ownership match %v", got, want)
	}
}

func TestProcessSnapshotsFromOutputsSeparatesArgvFromEnvironment(t *testing.T) {
	const command = "foreign-helper --note ORO_SOCKET_PATH=/tmp/project-a/oro.sock"
	snapshots := processSnapshotsFromOutputs("2201 1 2201 2201 "+command+"\n", func(int) ([]string, error) {
		return []string{"ORO_SOCKET_PATH=/tmp/project-a/oro.sock", "ORO_WORKER_ID=w1", "PATH=/bin"}, nil
	})

	if len(snapshots) != 1 {
		t.Fatalf("snapshots = %#v, want one", snapshots)
	}
	if snapshots[0].Command != command {
		t.Fatalf("command = %q, want %q", snapshots[0].Command, command)
	}
	if want := []string{"ORO_SOCKET_PATH=/tmp/project-a/oro.sock", "ORO_WORKER_ID=w1", "PATH=/bin"}; !slices.Equal(snapshots[0].Environment, want) {
		t.Fatalf("environment = %#v, want %#v", snapshots[0].Environment, want)
	}
}

func TestResidualScanUsesPathBoundariesForRoots(t *testing.T) {
	residuals := scanOroResidualProcessSnapshots([]processSnapshot{
		{PID: 2121, PPID: 1, PGID: 2121, Session: 2121, Command: "go test -worktree /tmp/oro/projects/foo"},
		{PID: 2122, PPID: 1, PGID: 2122, Session: 2122, Command: "go test -worktree /tmp/oro/projects/foo/worktree"},
		{PID: 2123, PPID: 1, PGID: 2123, Session: 2123, Command: "go test -worktree /tmp/oro/projects/foobar"},
		{PID: 2124, PPID: 1, PGID: 2124, Session: 2124, Command: "go test -worktree /tmp/oro/projects/foo-bar"},
	}, []string{"/tmp/oro/projects/foo"}, nil)

	if got, want := residualPIDs(residuals), []int{2121, 2122}; !sameInts(got, want) {
		t.Fatalf("residual PIDs = %v, want only boundary-safe root matches %v", got, want)
	}
}

func TestStopResidualRootsIncludesCurrentProjectWorktreesDir(t *testing.T) {
	repoRoot := t.TempDir()
	t.Chdir(repoRoot)
	pidPath := filepath.Join(t.TempDir(), "oro.pid")

	got := stopResidualRoots(pidPath)
	wantWorktrees := filepath.Join(repoRoot, ".worktrees")
	if !containsString(got, filepath.Dir(pidPath)) || !containsString(got, wantWorktrees) {
		t.Fatalf("stop residual roots = %v, want PID dir and project worktrees dir %q", got, wantWorktrees)
	}
}

func TestActiveAssignmentWorktreeRoots(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	activeWorktree := filepath.Join(t.TempDir(), "active-worktree")
	completedWorktree := filepath.Join(t.TempDir(), "completed-worktree")
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES
			('oro-active', 'worker-1', ?, 'active'),
			('oro-completed', 'worker-2', ?, 'completed')`,
		activeWorktree, completedWorktree); err != nil {
		t.Fatalf("insert assignments: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close state db: %v", err)
	}

	got, err := activeAssignmentWorktreeRoots(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("activeAssignmentWorktreeRoots: %v", err)
	}
	if len(got) != 1 || got[0] != activeWorktree {
		t.Fatalf("active assignment roots = %v, want [%s]", got, activeWorktree)
	}
}

func TestResidualKillDeduplicatesProcessGroups(t *testing.T) {
	pids, pgids := uniqueResidualTargets([]ResidualProcess{
		{PID: 2201, PGID: 3301},
		{PID: 2202, PGID: 3301},
		{PID: 2201, PGID: 3301},
		{PID: 2203, PGID: 3303},
	})
	if got, want := pids, []int{2201, 2202, 2203}; !sameInts(got, want) {
		t.Fatalf("unique residual pids = %v, want %v", got, want)
	}
	if got, want := pgids, []int{3301, 3303}; !sameInts(got, want) {
		t.Fatalf("unique residual pgids = %v, want %v", got, want)
	}
}

func TestKillProcessTreeDoesNotSkipSigkillWhenContextDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := killProcessTree(ctx, 4000000, nil); err != nil {
		t.Fatalf("already-gone process tree should still complete after canceled wait: %v", err)
	}
}

func residualPIDs(residuals []ResidualProcess) []int {
	pids := make([]int, 0, len(residuals))
	for _, residual := range residuals {
		pids = append(pids, residual.PID)
	}
	return pids
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestRunStopSequence verifies the full stop sequence completes successfully and
// does NOT invoke bd daemon stop or bd sync (those are handled by the dispatcher
// itself on shutdown and the pre-commit hook, respectively).
func TestRunStopSequence(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
		t.Fatalf("setup PID: %v", err)
	}

	fake := newFakeCmd()
	var buf bytes.Buffer
	cfg := ttyStop(pidFile, fake, &buf)

	if err := runStopSequence(context.Background(), cfg); err != nil {
		t.Fatalf("runStopSequence: %v", err)
	}

	if !strings.Contains(buf.String(), "shutdown complete") {
		t.Errorf("expected 'shutdown complete' message, got %q", buf.String())
	}

	// Verify bd daemon stop was NOT called.
	for _, call := range fake.calls {
		if len(call) >= 3 && call[0] == "bd" && call[1] == "daemon" && call[2] == "stop" {
			t.Errorf("unexpected 'bd daemon stop' call; calls = %v", fake.calls)
		}
	}

	// Verify bd sync was NOT called.
	for _, call := range fake.calls {
		if len(call) >= 2 && call[0] == "bd" && call[1] == "sync" {
			t.Errorf("unexpected 'bd sync' call; calls = %v", fake.calls)
		}
	}
}

// --- discoverProjectDaemons tests ---

func TestDiscoverProjectDaemons_FindsRunning(t *testing.T) {
	oroHome := t.TempDir()

	// Create two project dirs with PID files — use our own PID (known alive).
	for _, name := range []string{"alpha", "beta"} {
		projDir := filepath.Join(oroHome, "projects", name)
		if err := os.MkdirAll(projDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := WritePIDFile(filepath.Join(projDir, "oro.pid"), os.Getpid()); err != nil {
			t.Fatal(err)
		}
	}

	daemons := discoverProjectDaemons(oroHome)
	if len(daemons) != 2 {
		t.Fatalf("expected 2 daemons, got %d: %+v", len(daemons), daemons)
	}

	names := map[string]bool{}
	for _, d := range daemons {
		names[d.Project] = true
		if d.PID != os.Getpid() {
			t.Errorf("expected PID %d, got %d for project %s", os.Getpid(), d.PID, d.Project)
		}
	}
	if !names["alpha"] || !names["beta"] {
		t.Errorf("expected projects alpha and beta, got %v", names)
	}
}

func TestDiscoverProjectDaemons_SkipsStalePIDs(t *testing.T) {
	oroHome := t.TempDir()

	projDir := filepath.Join(oroHome, "projects", "stale")
	if err := os.MkdirAll(projDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// PID 4000000 is almost certainly not running.
	if err := WritePIDFile(filepath.Join(projDir, "oro.pid"), 4000000); err != nil {
		t.Fatal(err)
	}

	daemons := discoverProjectDaemons(oroHome)
	if len(daemons) != 0 {
		t.Errorf("expected 0 daemons (stale PID), got %d: %+v", len(daemons), daemons)
	}
}

func TestDiscoverProjectDaemons_IncludesLegacyGlobal(t *testing.T) {
	oroHome := t.TempDir()

	// Legacy global PID file at ~/.oro/oro.pid
	if err := WritePIDFile(filepath.Join(oroHome, "oro.pid"), os.Getpid()); err != nil {
		t.Fatal(err)
	}

	daemons := discoverProjectDaemons(oroHome)
	if len(daemons) != 1 {
		t.Fatalf("expected 1 daemon (legacy global), got %d: %+v", len(daemons), daemons)
	}
	if daemons[0].Project != "(global)" {
		t.Errorf("expected project name '(global)', got %q", daemons[0].Project)
	}
}

func TestStop_NotRunning_SuggestsAllWhenOtherDaemonsExist(t *testing.T) {
	oroHome := t.TempDir()
	t.Setenv("ORO_HOME", oroHome)
	t.Setenv("ORO_PROJECT", "myproject")
	t.Setenv("ORO_PID_PATH", "")
	t.Setenv("ORO_SOCKET_PATH", "")

	// Create project dir for myproject (no daemon running)
	myProjDir := filepath.Join(oroHome, "projects", "myproject")
	if err := os.MkdirAll(myProjDir, 0o750); err != nil {
		t.Fatal(err)
	}

	// Create another project with a running daemon
	otherDir := filepath.Join(oroHome, "projects", "other")
	if err := os.MkdirAll(otherDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := WritePIDFile(filepath.Join(otherDir, "oro.pid"), os.Getpid()); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	cfg := &stopConfig{
		pidPath:  filepath.Join(myProjDir, "oro.pid"),
		sockPath: filepath.Join(myProjDir, "oro.sock"),
		w:        &buf,
		oroHome:  oroHome,
	}

	if err := runStopSequence(context.Background(), cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "not running") {
		t.Errorf("expected 'not running' message, got %q", output)
	}
	if !strings.Contains(output, "oro stop --all") {
		t.Errorf("expected suggestion to use --all, got %q", output)
	}
}

// TestStopAllProjectDaemons verifies that runStopAll stops discovered project daemons
// without requiring legacy project.root or Dolt cleanup state.
func TestStopAllProjectDaemons(t *testing.T) {
	t.Run("stops daemon", func(t *testing.T) {
		oroHome := t.TempDir()

		// Start a fake daemon subprocess; reap in background to avoid zombies.
		cmd := exec.CommandContext(context.Background(), "sleep", "60")
		if err := cmd.Start(); err != nil {
			t.Fatalf("start sleep process: %v", err)
		}
		go func() { _ = cmd.Wait() }()
		defer func() { _ = cmd.Process.Signal(os.Kill) }()

		pid := cmd.Process.Pid
		projName := "myproject"
		projDir := filepath.Join(oroHome, "projects", projName)
		if err := os.MkdirAll(projDir, 0o750); err != nil {
			t.Fatal(err)
		}

		if err := WritePIDFile(filepath.Join(projDir, "oro.pid"), pid); err != nil {
			t.Fatal(err)
		}

		t.Setenv("ORO_HUMAN_CONFIRMED", "1")
		var buf bytes.Buffer
		if err := runStopAll(context.Background(), oroHome, true, &buf); err != nil {
			t.Fatalf("runStopAll: %v", err)
		}

		if !strings.Contains(buf.String(), "stopping") {
			t.Errorf("expected stopping output, got %q", buf.String())
		}
	})

	t.Run("missing project.root does not matter", func(t *testing.T) {
		oroHome := t.TempDir()

		// Start a fake daemon subprocess; reap in background to avoid zombies.
		cmd := exec.CommandContext(context.Background(), "sleep", "60")
		if err := cmd.Start(); err != nil {
			t.Fatalf("start sleep process: %v", err)
		}
		go func() { _ = cmd.Wait() }()
		defer func() { _ = cmd.Process.Signal(os.Kill) }()

		pid := cmd.Process.Pid
		projName := "myproject"
		projDir := filepath.Join(oroHome, "projects", projName)
		if err := os.MkdirAll(projDir, 0o750); err != nil {
			t.Fatal(err)
		}

		if err := WritePIDFile(filepath.Join(projDir, "oro.pid"), pid); err != nil {
			t.Fatal(err)
		}

		t.Setenv("ORO_HUMAN_CONFIRMED", "1")
		var buf bytes.Buffer
		if err := runStopAll(context.Background(), oroHome, true, &buf); err != nil {
			t.Fatalf("runStopAll: %v", err)
		}

		output := buf.String()
		if strings.Contains(output, "project.root") || strings.Contains(output, "dolt cleanup") {
			t.Errorf("unexpected legacy project.root/dolt warning, got %q", output)
		}
		if !strings.Contains(output, "stopping") {
			t.Errorf("expected stopping output, got %q", output)
		}
	})
}

func TestStopSequenceDoesNotShellOutToBd(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
		t.Fatalf("setup PID: %v", err)
	}

	fake := newFakeCmd()
	var buf bytes.Buffer
	cfg := ttyStop(pidFile, fake, &buf)

	if err := runStopSequence(context.Background(), cfg); err != nil {
		t.Fatalf("runStopSequence: %v", err)
	}

	for _, call := range fake.calls {
		if len(call) > 0 && call[0] == "bd" {
			t.Fatalf("runStopSequence must not call bd; calls = %v", fake.calls)
		}
	}
	if !strings.Contains(buf.String(), "shutdown complete") {
		t.Errorf("expected 'shutdown complete', got %q", buf.String())
	}
}

// TestIsStdinTTY exercises the isStdinTTY function.
// In test environments stdin is not a TTY, so it returns false.
// This covers the non-error code paths in the function.
func TestIsStdinTTY(t *testing.T) {
	result := isStdinTTY()
	// In CI / test runner stdin is never a TTY — just verify it returns without panic.
	if result {
		// If somehow a TTY, that's fine too.
		t.Logf("isStdinTTY returned true (running in interactive terminal)")
	}
}

// TestDefaultKill_Success exercises the happy path of defaultKill by sending
// SIGKILL to a short-lived child process.
func TestDefaultKill_Success(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	go func() { _ = cmd.Wait() }()

	if err := defaultKill(cmd.Process.Pid); err != nil {
		t.Errorf("defaultKill: %v", err)
	}
}

// TestDefaultKill_DeadPID exercises the error path: sending SIGKILL to a
// process that does not exist should return an error.
func TestDefaultKill_DeadPID(t *testing.T) {
	// PID 4000000 is well beyond the Linux/macOS max (typically 4194304 or less
	// and certainly not running); os.FindProcess succeeds on Unix regardless, but
	// proc.Signal returns ESRCH.
	const deadPID = 4000000
	err := defaultKill(deadPID)
	if err == nil {
		t.Skipf("process %d unexpectedly exists; skipping error-path test", deadPID)
	}
	if !strings.Contains(err.Error(), "SIGKILL") {
		t.Errorf("expected SIGKILL in error message, got: %v", err)
	}
}

// TestDefaultSignalINT_DeadPID exercises the error path: sending SIGINT to a
// process that does not exist should return an error.
func TestDefaultSignalINT_DeadPID(t *testing.T) {
	const deadPID = 4000001
	err := defaultSignalINT(deadPID)
	if err == nil {
		t.Skipf("process %d unexpectedly exists; skipping error-path test", deadPID)
	}
	if !strings.Contains(err.Error(), "SIGINT") {
		t.Errorf("expected SIGINT in error message, got: %v", err)
	}
}

// TestWaitForExit_ContextCancellation verifies that waitForExit returns a
// context error when the context is cancelled while the process is still alive.
func TestWaitForExit_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	err := waitForExit(ctx, os.Getpid(), func(int) bool { return true })
	if err == nil {
		t.Fatal("expected error on context cancellation, got nil")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("expected 'context canceled' in error, got: %v", err)
	}
}

// TestRunStopAll_NoDaemons exercises the early-return path when no daemons are found.
func TestRunStopAll_NoDaemons(t *testing.T) {
	oroHome := t.TempDir()
	var buf bytes.Buffer
	if err := runStopAll(context.Background(), oroHome, false, &buf); err != nil {
		t.Fatalf("runStopAll: %v", err)
	}
	if !strings.Contains(buf.String(), "no running daemons found") {
		t.Errorf("expected 'no running daemons found', got %q", buf.String())
	}
}
