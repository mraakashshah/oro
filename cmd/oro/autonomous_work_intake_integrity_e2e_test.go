package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/dispatcher"
	"oro/pkg/merge"
	"oro/pkg/ops"
	"oro/pkg/protocol"
)

// TestAutonomousWorkIntakeIntegrityHarness proves that the production-shaped
// intake fixture keeps its socket, CLI, store, worker roles, and restart state
// deterministic. Scenario tests add assignments to this harness rather than
// replacing its dispatcher or subprocess boundaries with mocks.
func TestAutonomousWorkIntakeIntegrityHarness(t *testing.T) {
	h := newAutonomousIntakeHarness(t)
	h.start(t)

	h.directive(t, protocol.DirectiveStart, "")
	h.directive(t, protocol.DirectiveScale, "1")
	h.runCLI(t, "worker", "launch", "--id", h.externalWorkerID)

	status := h.waitForWorkers(t, 1, 1)
	if status.ManagedCount != 1 || status.UnmanagedCount != 1 {
		t.Fatalf("worker role counts = managed:%d unmanaged:%d, want 1:1", status.ManagedCount, status.UnmanagedCount)
	}
	if !h.managedWorkerUsesSocket() {
		t.Fatalf("managed worker did not launch against harness socket %q: %v", h.socketPath, h.managedWorkerArgs())
	}
	if h.managedWorkerID == "" {
		t.Fatal("dispatcher did not create a managed worker ID")
	}
	if h.eventCount(t) == 0 {
		t.Fatal("dispatcher did not persist deterministic intake events")
	}
	if got := h.assignmentCount(t); got != 0 {
		t.Fatalf("empty intake harness assignments = %d, want 0", got)
	}

	h.writeForeignCommit(t)
	foreignHead := h.git(t, h.externalWorktree, "rev-parse", "HEAD")
	if got := h.git(t, h.managedWorktree, "rev-parse", "HEAD"); got == foreignHead {
		t.Fatal("foreign worktree commit changed managed worktree HEAD")
	}

	beforeDB := h.db
	h.clock.Advance(time.Minute)
	h.restart(t)
	if h.db == beforeDB {
		t.Fatal("restart reused the old database handle")
	}
	if h.clock.Now().IsZero() || h.dbPath == "" {
		t.Fatal("restart lost deterministic clock or store fixture state")
	}
	if _, err := os.Stat(h.dbPath); err != nil {
		t.Fatalf("restart did not retain state database %q: %v", h.dbPath, err)
	}
}

func TestAutonomousIntakeHarnessStopsExternalWorker(t *testing.T) {
	h := newAutonomousIntakeHarness(t)
	h.start(t)
	h.directive(t, protocol.DirectiveStart, "")
	h.directive(t, protocol.DirectiveScale, "1")
	h.runCLI(t, "worker", "launch", "--id", h.externalWorkerID)

	h.waitForWorkers(t, 1, 1)
	externalPID := h.externalWorkerPID(t)
	h.close(t)

	if processExists(externalPID) {
		t.Fatalf("external worker PID %d still exists after harness close", externalPID)
	}
	// Cleanup is intentionally idempotent.
	h.close(t)
}

type autonomousIntakeHarness struct {
	rootDir, binDir, cliPath string
	dbPath, socketPath       string
	managedWorktree          string
	externalWorktree         string
	managedWorkerID          string
	externalWorkerID         string
	externalWorkerLaunched   bool
	clock                    *autonomousIntakeClock
	db                       *sql.DB
	dispatcher               *dispatcher.Dispatcher
	manager                  *dispatcher.ExecProcessManager
	cancel                   context.CancelFunc
	runErr                   chan error
	mu                       sync.Mutex
	managedArgs              [][]string
}

func (h *autonomousIntakeHarness) externalWorkerPID(t *testing.T) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		cmd := exec.Command("pgrep", "-f", "worker --socket "+h.socketPath+" --id "+h.externalWorkerID) //nolint:gosec // test-owned query
		if output, err := cmd.Output(); err == nil {
			var pid int
			if _, err := fmt.Sscanf(strings.TrimSpace(string(output)), "%d", &pid); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("external worker %q PID not found", h.externalWorkerID)
	return 0
}

func processExists(pid int) bool {
	return exec.Command("kill", "-0", fmt.Sprint(pid)).Run() == nil //nolint:gosec // test-owned PID probe
}

type autonomousIntakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *autonomousIntakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *autonomousIntakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newAutonomousIntakeHarness(t *testing.T) *autonomousIntakeHarness {
	t.Helper()

	rootDir := t.TempDir()
	h := &autonomousIntakeHarness{
		rootDir:          rootDir,
		binDir:           filepath.Join(rootDir, "bin"),
		cliPath:          filepath.Join(rootDir, "bin", "oro"),
		dbPath:           filepath.Join(rootDir, "state", "state.db"),
		socketPath:       filepath.Join(os.TempDir(), fmt.Sprintf("oro-intake-%d-%d.sock", os.Getpid(), time.Now().UnixNano())),
		managedWorktree:  filepath.Join(rootDir, "managed-worktree"),
		externalWorktree: filepath.Join(rootDir, "external-worktree"),
		externalWorkerID: "external-intake",
		clock:            &autonomousIntakeClock{now: time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)},
	}
	t.Cleanup(func() { h.close(t) })

	h.unsetWorkerOwnership(t)
	h.buildCLI(t)
	h.installLongLivedCodexShim(t)
	h.createWorktrees(t)
	return h
}

func (h *autonomousIntakeHarness) unsetWorkerOwnership(t *testing.T) {
	t.Helper()
	for _, key := range []string{"ORO_SOCKET_PATH", "ORO_WORKER_ID", "ORO_WORKER_BEAD_ID", "ORO_PROJECT", "ORO_ROLE"} {
		t.Setenv(key, "")
	}
	t.Setenv("ORO_HOME", filepath.Join(h.rootDir, "oro-home"))
	t.Setenv("ORO_DB_PATH", h.dbPath)
	t.Setenv("PATH", h.binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func (h *autonomousIntakeHarness) buildCLI(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(h.binDir, 0o755); err != nil {
		t.Fatalf("mkdir harness bin: %v", err)
	}
	cmd := exec.Command("go", "build", "-o", h.cliPath, "./cmd/oro") //nolint:gosec // fixed local build command
	cmd.Dir = walkUpForGoMod(mustGetwd(t))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build real oro CLI: %v\n%s", err, out)
	}
}

func (h *autonomousIntakeHarness) installLongLivedCodexShim(t *testing.T) {
	t.Helper()
	shim := filepath.Join(h.binDir, "codex")
	const script = "#!/bin/sh\nwhile IFS= read -r line; do printf '%s\\n' \"$line\"; done\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatalf("write long-lived codex shim: %v", err)
	}
}

func (h *autonomousIntakeHarness) createWorktrees(t *testing.T) {
	t.Helper()
	repo := filepath.Join(h.rootDir, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir harness repo: %v", err)
	}
	h.git(t, repo, "init", "-b", "main")
	h.git(t, repo, "config", "user.email", "intake@example.invalid")
	h.git(t, repo, "config", "user.name", "intake harness")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("intake harness\n"), 0o644); err != nil {
		t.Fatalf("write harness repository: %v", err)
	}
	h.git(t, repo, "add", "README.md")
	h.git(t, repo, "commit", "-m", "initial harness repository")
	h.git(t, repo, "worktree", "add", "-b", "harness-managed", h.managedWorktree)
	h.git(t, repo, "worktree", "add", "-b", "harness-external", h.externalWorktree)
}

func (h *autonomousIntakeHarness) start(t *testing.T) {
	t.Helper()
	db, err := openStateDB(h.dbPath)
	if err != nil {
		t.Fatalf("open restartable state db: %v", err)
	}
	h.db = db

	cfg := dispatcher.Config{
		SocketPath:       h.socketPath,
		DBPath:           h.dbPath,
		RepoRoot:         h.managedWorktree,
		InitialWorkers:   0,
		MaxWorkers:       2,
		AllowZeroWorkers: true,
		HeartbeatTimeout: 10 * time.Second,
		PollInterval:     time.Hour,
	}
	d, err := dispatcher.New(cfg, db, merge.NewCoordinator(&merge.ExecGitRunner{}), ops.NewSpawner(nil), beadstore.NewSQLiteStore(db), dispatcher.NewGitWorktreeManager(h.managedWorktree, "", "", &dispatcher.ExecCommandRunner{}), dispatcher.NoopEscalator{}, nil)
	if err != nil {
		t.Fatalf("create real dispatcher: %v", err)
	}
	pm := dispatcher.NewOroProcessManager(h.socketPath, filepath.Join(h.rootDir, "oro-home"))
	pm.SetCmdFactory(func(workerID string) *exec.Cmd {
		args := []string{"worker", "--socket", h.socketPath, "--id", workerID}
		h.mu.Lock()
		h.managedWorkerID = workerID
		h.managedArgs = append(h.managedArgs, append([]string(nil), args...))
		h.mu.Unlock()
		cmd := exec.Command(h.cliPath, args...) //nolint:gosec // cli and arguments are test-owned
		cmd.Env = h.childEnv()
		return cmd
	})
	d.SetProcessManager(pm)
	h.dispatcher, h.manager = d, pm
	h.cancel = nil
	h.runErr = make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { h.runErr <- d.Run(ctx) }()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(h.socketPath); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("dispatcher did not create intake socket")
}

func (h *autonomousIntakeHarness) restart(t *testing.T) {
	t.Helper()
	h.stop(t)
	h.start(t)
}

func (h *autonomousIntakeHarness) stop(t *testing.T) {
	t.Helper()
	if h.cancel != nil {
		h.cancel()
		select {
		case err := <-h.runErr:
			if err != nil {
				t.Fatalf("stop dispatcher: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("timed out stopping dispatcher")
		}
		h.cancel = nil
	}
	if h.manager != nil {
		_ = h.manager.Kill(h.managedWorkerID)
		h.manager.Wait()
	}
	if h.db != nil {
		if err := h.db.Close(); err != nil {
			t.Fatalf("close restartable state db: %v", err)
		}
		h.db = nil
	}
}

func (h *autonomousIntakeHarness) close(t *testing.T) {
	t.Helper()
	h.stopExternalWorker(t)
	if h.cancel != nil || h.db != nil {
		h.stop(t)
	}
	_ = os.Remove(h.socketPath)
}

func (h *autonomousIntakeHarness) stopExternalWorker(t *testing.T) {
	t.Helper()
	if !h.externalWorkerLaunched || h.cancel == nil {
		return
	}
	cmd := exec.Command(h.cliPath, "worker", "stop", h.externalWorkerID) //nolint:gosec // test-owned CLI and fixed worker ID
	cmd.Env = h.childEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		if !strings.Contains(string(out), "worker not found") {
			t.Errorf("stop external worker %q: %v\n%s", h.externalWorkerID, err, out)
		}
	}
	h.externalWorkerLaunched = false
}

func (h *autonomousIntakeHarness) directive(t *testing.T, op protocol.Directive, args string) {
	t.Helper()
	conn, err := dialDispatcher(context.Background(), h.socketPath)
	if err != nil {
		t.Fatalf("dial dispatcher for %s: %v", op, err)
	}
	defer func() { _ = conn.Close() }()
	if err := sendDirective(conn, string(op), args); err != nil {
		t.Fatalf("send %s directive: %v", op, err)
	}
	if _, err := readACK(conn); err != nil {
		t.Fatalf("read %s acknowledgement: %v", op, err)
	}
}

func (h *autonomousIntakeHarness) runCLI(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command(h.cliPath, args...) //nolint:gosec // test-owned CLI and fixed arguments
	cmd.Env = h.childEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("oro %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	if len(args) == 4 && args[0] == "worker" && args[1] == "launch" && args[2] == "--id" && args[3] == h.externalWorkerID {
		h.externalWorkerLaunched = true
	}
}

func (h *autonomousIntakeHarness) childEnv() []string {
	env := make([]string, 0, len(os.Environ())+5)
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "ORO_SOCKET_PATH=") && !strings.HasPrefix(item, "ORO_WORKER_ID=") && !strings.HasPrefix(item, "ORO_WORKER_BEAD_ID=") && !strings.HasPrefix(item, "ORO_PROJECT=") && !strings.HasPrefix(item, "ORO_ROLE=") {
			env = append(env, item)
		}
	}
	return append(env,
		"ORO_SOCKET_PATH="+h.socketPath,
		"ORO_HOME="+filepath.Join(h.rootDir, "oro-home"),
		"ORO_PROJECT="+filepath.Base(h.managedWorktree),
		"ORO_DB_PATH="+h.dbPath,
		"PATH="+h.binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
}

type intakeStatus struct {
	ManagedCount   int `json:"managed_count"`
	UnmanagedCount int `json:"unmanaged_count"`
}

func (h *autonomousIntakeHarness) waitForWorkers(t *testing.T, managed, unmanaged int) intakeStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out := h.cliOutput(t, "status", "--json")
		var status intakeStatus
		if err := json.Unmarshal(out, &status); err == nil && status.ManagedCount == managed && status.UnmanagedCount == unmanaged {
			return status
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("workers did not reach managed=%d unmanaged=%d", managed, unmanaged)
	return intakeStatus{}
}

func (h *autonomousIntakeHarness) cliOutput(t *testing.T, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(h.cliPath, args...) //nolint:gosec // test-owned CLI and fixed arguments
	cmd.Env = h.childEnv()
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	return out
}

func (h *autonomousIntakeHarness) managedWorkerUsesSocket() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, args := range h.managedArgs {
		for i := range args[:len(args)-1] {
			if args[i] == "--socket" && args[i+1] == h.socketPath {
				return true
			}
		}
	}
	return false
}

func (h *autonomousIntakeHarness) managedWorkerArgs() [][]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([][]string(nil), h.managedArgs...)
}

func (h *autonomousIntakeHarness) eventCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&count); err != nil {
		t.Fatalf("query dispatcher events: %v", err)
	}
	return count
}

func (h *autonomousIntakeHarness) assignmentCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM assignments`).Scan(&count); err != nil {
		t.Fatalf("count assignments: %v", err)
	}
	return count
}

func (h *autonomousIntakeHarness) writeForeignCommit(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(h.externalWorktree, "foreign.txt"), []byte("foreign worktree\n"), 0o644); err != nil {
		t.Fatalf("write foreign worktree: %v", err)
	}
	h.git(t, h.externalWorktree, "add", "foreign.txt")
	h.git(t, h.externalWorktree, "commit", "-m", "foreign intake fixture")
}

func (h *autonomousIntakeHarness) git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...) //nolint:gosec // test-owned git repository and fixed arguments
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	return dir
}
