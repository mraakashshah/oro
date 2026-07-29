package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/factoryhealth"
)

func TestCleanup_NothingToClean(t *testing.T) {
	fake := newFakeCmd()
	// tmux has-session fails (no session)
	fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
	// pgrep finds no workers
	fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
	// git branch --list returns empty (no agent branches)
	fake.output[key("git", "branch", "--list", "agent/*")] = ""
	// no epic branches
	fake.output[key("git", "branch", "--list", "epic/*")] = ""
	// bd list returns empty JSON array
	fake.output[key("bd", "list", "--status=in_progress", "--json")] = "[]"

	tmpDir := t.TempDir()
	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   fake,
		w:        &buf,
		tmuxName: TmuxSessionName(""),
		pidPath:  filepath.Join(tmpDir, "oro.pid"),  // no file
		sockPath: filepath.Join(tmpDir, "oro.sock"), // no file
		signalFn: func(int) error { return nil },
		aliveFn:  func(int) bool { return false },
	}

	err := runCleanup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "nothing to clean") {
		t.Errorf("expected 'nothing to clean' in output, got: %s", out)
	}
}

func TestCleanup_KillsRunningDispatcher(t *testing.T) {
	fake := newFakeCmd()
	// tmux has-session fails (no session)
	fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
	// pgrep finds no workers
	fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
	// git branch --list returns empty
	fake.output[key("git", "branch", "--list", "agent/*")] = ""
	fake.output[key("git", "branch", "--list", "epic/*")] = ""
	// bd list returns empty
	fake.output[key("bd", "list", "--status=in_progress", "--json")] = "[]"

	tmpDir := t.TempDir()

	// Write a PID file with a fake PID
	pidPath := filepath.Join(tmpDir, "oro.pid")
	if err := os.WriteFile(pidPath, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}

	signaled := false
	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   fake,
		w:        &buf,
		tmuxName: TmuxSessionName(""),
		pidPath:  pidPath,
		sockPath: filepath.Join(tmpDir, "oro.sock"),
		signalFn: func(pid int) error {
			if pid == 12345 {
				signaled = true
			}
			return nil
		},
		aliveFn: func(pid int) bool {
			return pid == 12345
		},
	}

	err := runCleanup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !signaled {
		t.Error("expected dispatcher PID 12345 to be signaled")
	}

	// PID file should be removed
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("expected PID file to be removed")
	}

	out := buf.String()
	if !strings.Contains(out, "dispatcher") {
		t.Errorf("expected output to mention dispatcher, got: %s", out)
	}
}

func TestCleanup_KillsTmux(t *testing.T) {
	fake := newFakeCmd()
	// tmux has-session succeeds (session exists)
	fake.output[key("tmux", "has-session", "-t", "oro")] = ""
	// pgrep finds no workers
	fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
	// git branch --list returns empty
	fake.output[key("git", "branch", "--list", "agent/*")] = ""
	fake.output[key("git", "branch", "--list", "epic/*")] = ""
	// bd list returns empty
	fake.output[key("bd", "list", "--status=in_progress", "--json")] = "[]"

	tmpDir := t.TempDir()
	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   fake,
		w:        &buf,
		tmuxName: TmuxSessionName(""),
		pidPath:  filepath.Join(tmpDir, "oro.pid"),
		sockPath: filepath.Join(tmpDir, "oro.sock"),
		signalFn: func(int) error { return nil },
		aliveFn:  func(int) bool { return false },
	}

	err := runCleanup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify kill-session was called
	if killCall := findCall(fake.calls, "kill-session"); killCall == nil {
		t.Error("expected tmux kill-session")
	}

	out := buf.String()
	if !strings.Contains(out, "tmux") {
		t.Errorf("expected output to mention tmux, got: %s", out)
	}
}

func TestCleanup_RemovesStaleFiles(t *testing.T) {
	fake := newFakeCmd()
	// tmux has-session fails
	fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
	// pgrep finds no workers
	fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
	// git branch --list returns empty
	fake.output[key("git", "branch", "--list", "agent/*")] = ""
	fake.output[key("git", "branch", "--list", "epic/*")] = ""
	// bd list returns empty
	fake.output[key("bd", "list", "--status=in_progress", "--json")] = "[]"

	tmpDir := t.TempDir()

	// Write stale PID file (process not alive)
	pidPath := filepath.Join(tmpDir, "oro.pid")
	if err := os.WriteFile(pidPath, []byte("99999"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Write stale socket file
	sockPath := filepath.Join(tmpDir, "oro.sock")
	if err := os.WriteFile(sockPath, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   fake,
		w:        &buf,
		tmuxName: TmuxSessionName(""),
		pidPath:  pidPath,
		sockPath: sockPath,
		signalFn: func(int) error { return nil },
		aliveFn:  func(int) bool { return false }, // process is dead
	}

	err := runCleanup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// PID file should be removed
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("expected stale PID file to be removed")
	}

	// Socket file should be removed
	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Error("expected stale socket file to be removed")
	}

	out := buf.String()
	if !strings.Contains(out, "pid") || !strings.Contains(out, "socket") {
		t.Errorf("expected output to mention pid and socket removal, got: %s", out)
	}
}

func TestCleanup_RemovesStaleStateDBLock(t *testing.T) {
	fake := newFakeCmd()
	fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
	fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
	fake.output[key("git", "branch", "--list", "agent/*")] = ""
	fake.output[key("git", "branch", "--list", "epic/*")] = ""

	tmpDir := t.TempDir()
	stateDBPath := filepath.Join(tmpDir, "state.db")
	lockPath := stateDBPath + ".lock"
	if err := os.WriteFile(lockPath, []byte("999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:      fake,
		w:           &buf,
		tmuxName:    TmuxSessionName(""),
		pidPath:     filepath.Join(tmpDir, "oro.pid"),
		sockPath:    filepath.Join(tmpDir, "oro.sock"),
		stateDBPath: stateDBPath,
		signalFn:    func(int) error { return nil },
		aliveFn:     func(int) bool { return false },
	}

	if err := runCleanup(context.Background(), cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Error("expected stale state DB lock to be removed")
	}
	if out := buf.String(); !strings.Contains(out, "state DB lock") {
		t.Errorf("expected output to mention state DB lock removal, got: %s", out)
	}
}

func TestCleanupPrunesSubprocessCache(t *testing.T) {
	fake := newFakeCmd()
	fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
	fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
	fake.output[key("git", "branch", "--list", "agent/*")] = ""
	fake.output[key("git", "branch", "--list", "epic/*")] = ""

	now := time.Now()
	tmpDir := t.TempDir()
	cacheRoot := filepath.Join(tmpDir, "subprocess")
	staleNamespace := filepath.Join(cacheRoot, "stale")
	recentNamespace := filepath.Join(cacheRoot, "recent")
	if err := os.MkdirAll(filepath.Join(staleNamespace, "go-build"), 0o755); err != nil {
		t.Fatalf("mkdir stale namespace: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(recentNamespace, "go-build"), 0o755); err != nil {
		t.Fatalf("mkdir recent namespace: %v", err)
	}
	if err := os.Chtimes(staleNamespace, now.Add(-8*24*time.Hour), now.Add(-8*24*time.Hour)); err != nil {
		t.Fatalf("chtimes stale namespace: %v", err)
	}
	if err := os.Chtimes(recentNamespace, now.Add(-2*24*time.Hour), now.Add(-2*24*time.Hour)); err != nil {
		t.Fatalf("chtimes recent namespace: %v", err)
	}

	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:                fake,
		w:                     &buf,
		tmuxName:              TmuxSessionName(""),
		pidPath:               filepath.Join(tmpDir, "oro.pid"),
		sockPath:              filepath.Join(tmpDir, "oro.sock"),
		subprocessCacheRoot:   cacheRoot,
		subprocessCacheMaxAge: 7 * 24 * time.Hour,
		signalFn:              func(int) error { return nil },
		aliveFn:               func(int) bool { return false },
	}

	if err := runCleanup(context.Background(), cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(staleNamespace); !os.IsNotExist(err) {
		t.Fatalf("expected stale subprocess cache namespace to be removed, stat err: %v", err)
	}
	if _, err := os.Stat(recentNamespace); err != nil {
		t.Fatalf("expected recent subprocess cache namespace to be preserved: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "pruned 1 stale subprocess cache namespace") {
		t.Fatalf("expected subprocess cache pruning output, got:\n%s", out)
	}
	if strings.Contains(out, "nothing to clean") {
		t.Fatalf("cleanup reported nothing to clean after pruning cache:\n%s", out)
	}
}

func TestCleanup_PrunesWorktrees(t *testing.T) {
	fake := newFakeCmd()
	// tmux has-session fails
	fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
	// pgrep finds no workers
	fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
	// git branch --list returns empty
	fake.output[key("git", "branch", "--list", "agent/*")] = ""
	fake.output[key("git", "branch", "--list", "epic/*")] = ""
	// bd list returns empty
	fake.output[key("bd", "list", "--status=in_progress", "--json")] = "[]"

	tmpDir := t.TempDir()
	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   fake,
		w:        &buf,
		tmuxName: TmuxSessionName(""),
		pidPath:  filepath.Join(tmpDir, "oro.pid"),
		sockPath: filepath.Join(tmpDir, "oro.sock"),
		signalFn: func(int) error { return nil },
		aliveFn:  func(int) bool { return false },
	}

	err := runCleanup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify git worktree prune was called
	var pruned bool
	for _, call := range fake.calls {
		if len(call) >= 3 && call[0] == "git" && call[1] == "worktree" && call[2] == "prune" {
			pruned = true
		}
	}
	if !pruned {
		t.Error("expected git worktree prune to be called")
	}
}

func TestCleanup_DeletesAgentBranches(t *testing.T) {
	fake := newFakeCmd()
	// tmux has-session fails
	fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
	// pgrep finds no workers
	fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
	// git branch --list returns agent branches
	fake.output[key("git", "branch", "--list", "agent/*")] = "  agent/cleanup-cli\n  agent/fix-bug\n"
	fake.output[key("git", "worktree", "list", "--porcelain")] = ""
	fake.output[key("git", "merge-base", "--is-ancestor", "agent/cleanup-cli", "main")] = ""
	fake.output[key("git", "merge-base", "--is-ancestor", "agent/fix-bug", "main")] = ""
	fake.output[key("git", "branch", "--list", "epic/*")] = ""
	// bd list returns empty
	fake.output[key("bd", "list", "--status=in_progress", "--json")] = "[]"

	tmpDir := t.TempDir()
	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   fake,
		w:        &buf,
		tmuxName: TmuxSessionName(""),
		pidPath:  filepath.Join(tmpDir, "oro.pid"),
		sockPath: filepath.Join(tmpDir, "oro.sock"),
		signalFn: func(int) error { return nil },
		aliveFn:  func(int) bool { return false },
	}

	err := runCleanup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify git branch -d was called for each merged agent branch
	var deletedBranches []string
	for _, call := range fake.calls {
		if len(call) >= 4 && call[0] == "git" && call[1] == "branch" && call[2] == "-d" {
			deletedBranches = append(deletedBranches, call[3])
		}
	}

	if len(deletedBranches) != 2 {
		t.Fatalf("expected 2 branch deletions, got %d: %v", len(deletedBranches), deletedBranches)
	}

	found := map[string]bool{}
	for _, b := range deletedBranches {
		found[b] = true
	}
	if !found["agent/cleanup-cli"] {
		t.Error("expected agent/cleanup-cli to be deleted")
	}
	if !found["agent/fix-bug"] {
		t.Error("expected agent/fix-bug to be deleted")
	}

	out := buf.String()
	if !strings.Contains(out, "agent/cleanup-cli") || !strings.Contains(out, "agent/fix-bug") {
		t.Errorf("expected output to mention deleted branches, got: %s", out)
	}
}

func TestCleanupReportsMergedAndUnmergedAgentBranches(t *testing.T) {
	fake := newFakeCmd()
	fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
	fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
	fake.output[key("git", "branch", "--list", "agent/*")] = "  agent/merged\n  agent/unmerged\n  agent/checked\n"
	fake.output[key("git", "worktree", "list", "--porcelain")] = strings.Join([]string{
		"worktree /repo",
		"HEAD abc123",
		"branch refs/heads/main",
		"",
		"worktree /repo/.worktrees/oro-checked",
		"HEAD def456",
		"branch refs/heads/agent/checked",
		"",
	}, "\n")
	fake.output[key("git", "merge-base", "--is-ancestor", "agent/merged", "main")] = ""
	fake.errs[key("git", "merge-base", "--is-ancestor", "agent/unmerged", "main")] = fmt.Errorf("not merged")
	fake.output[key("git", "rev-list", "--count", "main..agent/unmerged")] = "3\n"
	fake.output[key("git", "branch", "--list", "epic/*")] = ""
	fake.output[key("bd", "list", "--status=in_progress", "--json")] = "[]"

	tmpDir := t.TempDir()
	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   fake,
		w:        &buf,
		tmuxName: TmuxSessionName(""),
		pidPath:  filepath.Join(tmpDir, "oro.pid"),
		sockPath: filepath.Join(tmpDir, "oro.sock"),
		signalFn: func(int) error { return nil },
		aliveFn:  func(int) bool { return false },
	}

	err := runCleanup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var deletedBranches []string
	for _, call := range fake.calls {
		if len(call) >= 4 && call[0] == "git" && call[1] == "branch" && call[2] == "-d" {
			deletedBranches = append(deletedBranches, call[3])
		}
		if len(call) >= 4 && call[0] == "git" && call[1] == "branch" && call[2] == "-D" &&
			strings.HasPrefix(call[3], "agent/") {
			t.Fatalf("agent branch was force-deleted: %v", call)
		}
	}
	if got, want := deletedBranches, []string{"agent/merged"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("deleted branches = %v, want %v", got, want)
	}

	out := buf.String()
	for _, want := range []string{
		"deleting merged branch agent/merged",
		"preserving unmerged branch agent/unmerged (3 unique commit(s))",
		"preserving checked-out branch agent/checked",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected output to contain %q, got:\n%s", want, out)
		}
	}
}

func TestCleanupPreservesUncertainAgentBranchOnGitError(t *testing.T) {
	fake := newFakeCmd()
	fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
	fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
	fake.output[key("git", "branch", "--list", "agent/*")] = "  agent/uncertain\n"
	fake.output[key("git", "worktree", "list", "--porcelain")] = ""
	fake.errs[key("git", "merge-base", "--is-ancestor", "agent/uncertain", "main")] = fmt.Errorf("not merged")
	fake.errs[key("git", "rev-list", "--count", "main..agent/uncertain")] = fmt.Errorf("rev-list failed")
	fake.output[key("git", "branch", "--list", "epic/*")] = ""
	fake.output[key("bd", "list", "--status=in_progress", "--json")] = "[]"

	tmpDir := t.TempDir()
	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   fake,
		w:        &buf,
		tmuxName: TmuxSessionName(""),
		pidPath:  filepath.Join(tmpDir, "oro.pid"),
		sockPath: filepath.Join(tmpDir, "oro.sock"),
		signalFn: func(int) error { return nil },
		aliveFn:  func(int) bool { return false },
	}

	err := runCleanup(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected classification error")
	}
	if !strings.Contains(err.Error(), "count unique commits for agent/uncertain") {
		t.Fatalf("expected unique commit count error, got: %v", err)
	}
	for _, call := range fake.calls {
		if len(call) >= 4 && call[0] == "git" && call[1] == "branch" &&
			(call[2] == "-d" || call[2] == "-D") && call[3] == "agent/uncertain" {
			t.Fatalf("uncertain branch was deleted: %v", call)
		}
	}
	if out := buf.String(); !strings.Contains(out, "preserving uncertain branch agent/uncertain") {
		t.Fatalf("expected uncertain branch preservation message, got:\n%s", out)
	}
}

func TestCleanup_ResetsInProgressBeads(t *testing.T) {
	fake := newFakeCmd()
	// tmux has-session fails
	fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
	// pgrep finds no workers
	fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
	// git branch --list returns empty
	fake.output[key("git", "branch", "--list", "agent/*")] = ""
	fake.output[key("git", "branch", "--list", "epic/*")] = ""

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	store, err := beadstore.OpenSQLiteStore(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("open bead store: %v", err)
	}
	for _, id := range []string{"oro-abc1", "oro-xyz2"} {
		if _, err := store.Create(context.Background(), beadstore.CreateParams{
			ID:       id,
			Title:    id,
			Type:     "task",
			Priority: 1,
		}); err != nil {
			t.Fatalf("create bead %s: %v", id, err)
		}
		status := "in_progress"
		if err := store.Update(context.Background(), id, beadstore.UpdateParams{Status: &status}); err != nil {
			t.Fatalf("mark bead %s in_progress: %v", id, err)
		}
	}
	if _, err := store.Create(context.Background(), beadstore.CreateParams{
		ID:       "oro-assigned-open",
		Title:    "assigned open",
		Type:     "task",
		Priority: 1,
	}); err != nil {
		t.Fatalf("create assigned open bead: %v", err)
	}
	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES ('oro-assigned-open', 'worker-1', '/tmp/oro-assigned-open', 'active')`); err != nil {
		t.Fatalf("insert active assignment: %v", err)
	}

	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:      fake,
		w:           &buf,
		tmuxName:    TmuxSessionName(""),
		pidPath:     filepath.Join(tmpDir, "oro.pid"),
		sockPath:    filepath.Join(tmpDir, "oro.sock"),
		stateDBPath: dbPath,
		signalFn:    func(int) error { return nil },
		aliveFn:     func(int) bool { return false },
	}

	err = runCleanup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, call := range fake.calls {
		if len(call) > 0 && call[0] == "bd" {
			t.Fatalf("cleanup used bd runner call: %v", call)
		}
	}

	var activeAssignments int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM assignments WHERE bead_id='oro-assigned-open' AND status='active'`).Scan(&activeAssignments); err != nil {
		t.Fatalf("count active assignments: %v", err)
	}
	if activeAssignments != 0 {
		t.Fatalf("active assignment count = %d, want 0", activeAssignments)
	}
	for _, id := range []string{"oro-abc1", "oro-xyz2"} {
		bead, err := store.Show(context.Background(), id)
		if err != nil {
			t.Fatalf("show bead %s: %v", id, err)
		}
		if bead == nil {
			t.Fatalf("bead %s missing", id)
		}
		if bead.Status != "open" {
			t.Errorf("bead %s status = %q, want open", id, bead.Status)
		}
	}
	assigned, err := store.Show(context.Background(), "oro-assigned-open")
	if err != nil {
		t.Fatalf("show assigned bead: %v", err)
	}
	if assigned == nil {
		t.Fatal("assigned bead missing")
	}
	if assigned.Status != "open" {
		t.Errorf("assigned bead status = %q, want open", assigned.Status)
	}

	out := buf.String()
	if !strings.Contains(out, "oro-abc1") || !strings.Contains(out, "oro-xyz2") {
		t.Errorf("expected output to mention reset beads, got: %s", out)
	}
	if !strings.Contains(out, "cleared active assignment for bead oro-assigned-open") {
		t.Errorf("expected output to mention cleared assignment, got: %s", out)
	}
}

func TestCleanupAssignmentsOnlyPreservesBranchesAndWorktrees(t *testing.T) {
	ctx := context.Background()
	repo := initLeakscanGitRepo(t)
	worktree := filepath.Join(repo, ".worktrees", "oro-fixture")
	if err := os.MkdirAll(filepath.Dir(worktree), 0o750); err != nil {
		t.Fatalf("create worktrees directory: %v", err)
	}
	runGit(t, repo, "worktree", "add", "-b", "agent/oro-fixture", worktree, "HEAD")

	dbPath := filepath.Join(repo, "state.db")
	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	store := beadstore.NewSQLiteStore(db)
	for _, id := range []string{"oro-stale", "oro-live"} {
		if _, err := store.Create(ctx, beadstore.CreateParams{ID: id, Title: id, Type: "task", Priority: 1}); err != nil {
			t.Fatalf("create bead %s: %v", id, err)
		}
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES
  ('oro-stale', 'crashed-worker', ?, 'active'),
  ('oro-live', 'live-worker', '/tmp/oro-live', 'active')`, worktree); err != nil {
		t.Fatalf("seed assignments: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seeded state db: %v", err)
	}

	pidPath := filepath.Join(repo, "oro.pid")
	sockPath := filepath.Join(repo, "oro.sock")
	for _, path := range []string{pidPath, sockPath} {
		if err := os.WriteFile(path, []byte("preserve"), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	fake := newFakeCmd()
	var buf bytes.Buffer
	signaled := false
	cfg := &cleanupConfig{
		AssignmentsOnly: true,
		runner:          fake,
		w:               &buf,
		pidPath:         pidPath,
		sockPath:        sockPath,
		stateDBPath:     dbPath,
		worktreesDir:    filepath.Join(repo, ".worktrees"),
		signalFn: func(int) error {
			signaled = true
			return nil
		},
		liveWorkerIDs: func(context.Context) (map[string]bool, error) {
			return map[string]bool{"live-worker": true}, nil
		},
	}
	if err := runCleanup(ctx, cfg); err != nil {
		t.Fatalf("run assignments-only cleanup: %v", err)
	}
	if signaled || len(fake.getCalls()) != 0 {
		t.Fatalf("assignments-only cleanup touched processes or git: signaled=%t calls=%v", signaled, fake.getCalls())
	}

	db, err = openStateDB(dbPath)
	if err != nil {
		t.Fatalf("reopen state db: %v", err)
	}
	defer func() { _ = db.Close() }()
	active, err := factoryhealth.LoadActiveAssignments(ctx, db, time.Now())
	if err != nil {
		t.Fatalf("load active assignments: %v", err)
	}
	health := factoryhealth.Evaluate(factoryhealth.Snapshot{
		DaemonRunning:     true,
		Workers:           []factoryhealth.WorkerSnapshot{{ID: "live-worker", BeadID: "oro-live"}},
		ActiveAssignments: active,
		Storage:           &factoryhealth.StorageHealth{Available: true},
	})
	if health.Metrics.OrphanAssignments != 0 {
		t.Fatalf("orphan assignments = %d, want 0; health=%+v", health.Metrics.OrphanAssignments, health)
	}

	var staleStatus, liveStatus string
	if err := db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE worker_id='crashed-worker'`).Scan(&staleStatus); err != nil {
		t.Fatalf("read stale assignment: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE worker_id='live-worker'`).Scan(&liveStatus); err != nil {
		t.Fatalf("read live assignment: %v", err)
	}
	if staleStatus != "completed" || liveStatus != "active" {
		t.Fatalf("assignment statuses stale/live = %q/%q, want completed/active", staleStatus, liveStatus)
	}

	branchCmd := exec.Command("git", "branch", "--list", "agent/oro-fixture")
	branchCmd.Dir = repo
	branchOut, err := branchCmd.Output()
	if err != nil {
		t.Fatalf("list fixture branch: %v", err)
	}
	if strings.TrimSpace(string(branchOut)) == "" {
		t.Fatal("fixture agent branch was removed")
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("fixture worktree was removed: %v", err)
	}
	for _, path := range []string{pidPath, sockPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("assignments-only cleanup removed %s: %v", path, err)
		}
	}
}

func TestCleanup_ContinuesOnErrors(t *testing.T) {
	fake := newFakeCmd()
	// tmux has-session succeeds but kill fails
	fake.output[key("tmux", "has-session", "-t", "oro")] = ""
	fake.errs[key("tmux", "kill-session", "-t", "oro")] = fmt.Errorf("kill failed")
	// pgrep fails
	fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("pgrep error")
	// git worktree prune fails
	fake.errs[key("git", "worktree", "prune")] = fmt.Errorf("prune failed")
	// git branch --list fails
	fake.errs[key("git", "branch", "--list", "agent/*")] = fmt.Errorf("branch list failed")
	fake.errs[key("git", "branch", "--list", "epic/*")] = fmt.Errorf("branch list failed")
	// bd list fails
	fake.errs[key("bd", "list", "--status=in_progress", "--json")] = fmt.Errorf("bd failed")

	tmpDir := t.TempDir()

	// Write a PID file with a process that's alive but signal fails
	pidPath := filepath.Join(tmpDir, "oro.pid")
	if err := os.WriteFile(pidPath, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   fake,
		w:        &buf,
		tmuxName: TmuxSessionName(""),
		pidPath:  pidPath,
		sockPath: filepath.Join(tmpDir, "oro.sock"),
		signalFn: func(int) error { return fmt.Errorf("signal failed") },
		aliveFn:  func(int) bool { return true },
	}

	// Should return an error for uncertain branch cleanup while still attempting later steps.
	err := runCleanup(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected branch cleanup classification error")
	}
	if !strings.Contains(err.Error(), "list agent branches") {
		t.Fatalf("expected agent branch list error, got: %v", err)
	}

	out := buf.String()
	// Should report warnings for failures
	if !strings.Contains(out, "warning") {
		t.Errorf("expected warnings in output, got: %s", out)
	}

	// Verify later non-bead cleanup was attempted without falling back to bd.
	var hasGitCall bool
	for _, call := range fake.calls {
		if call[0] == "git" {
			hasGitCall = true
		}
		if call[0] == "bd" {
			t.Fatalf("cleanup used bd runner call: %v", call)
		}
	}
	if !hasGitCall {
		t.Error("expected git commands to be attempted despite earlier failures")
	}
}

func TestCleanup_KillsWorkerProcesses(t *testing.T) {
	fake := newFakeCmd()
	// tmux has-session fails
	fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
	// pgrep finds worker PIDs
	fake.output[key("pgrep", "-f", "ORO_ROLE")] = "11111\n22222"
	// git branch --list returns empty
	fake.output[key("git", "branch", "--list", "agent/*")] = ""
	fake.output[key("git", "branch", "--list", "epic/*")] = ""
	// bd list returns empty
	fake.output[key("bd", "list", "--status=in_progress", "--json")] = "[]"

	tmpDir := t.TempDir()
	var signaledPIDs []int
	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   fake,
		w:        &buf,
		tmuxName: TmuxSessionName(""),
		pidPath:  filepath.Join(tmpDir, "oro.pid"),
		sockPath: filepath.Join(tmpDir, "oro.sock"),
		signalFn: func(pid int) error {
			signaledPIDs = append(signaledPIDs, pid)
			return nil
		},
		aliveFn: func(int) bool { return false },
	}

	err := runCleanup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check that worker PIDs were signaled
	found := map[int]bool{}
	for _, pid := range signaledPIDs {
		found[pid] = true
	}
	if !found[11111] {
		t.Error("expected worker PID 11111 to be signaled")
	}
	if !found[22222] {
		t.Error("expected worker PID 22222 to be signaled")
	}

	out := buf.String()
	if !strings.Contains(out, "worker") {
		t.Errorf("expected output to mention worker processes, got: %s", out)
	}
}

func TestCleanup_RefusedWhenNotTTY(t *testing.T) {
	tmpDir := t.TempDir()
	pidPath := filepath.Join(tmpDir, "oro.pid")

	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   newFakeCmd(),
		w:        &buf,
		tmuxName: TmuxSessionName(""),
		pidPath:  pidPath,
		sockPath: filepath.Join(tmpDir, "oro.sock"),
		isTTY:    func() bool { return false },
	}

	err := runCleanup(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error when stdin is not a TTY, got nil")
	}
	if !strings.Contains(err.Error(), "TTY") {
		t.Errorf("expected TTY error, got: %v", err)
	}
}

func TestCleanup_SendsSIGINTToDispatcher(t *testing.T) {
	fake := newFakeCmd()
	fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
	fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
	fake.output[key("git", "branch", "--list", "agent/*")] = ""
	fake.output[key("git", "branch", "--list", "epic/*")] = ""
	fake.output[key("bd", "list", "--status=in_progress", "--json")] = "[]"

	tmpDir := t.TempDir()
	pidPath := filepath.Join(tmpDir, "oro.pid")
	if err := os.WriteFile(pidPath, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}

	signaled := false
	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   fake,
		w:        &buf,
		tmuxName: TmuxSessionName(""),
		pidPath:  pidPath,
		sockPath: filepath.Join(tmpDir, "oro.sock"),
		signalFn: func(pid int) error { signaled = true; return nil },
		aliveFn:  func(pid int) bool { return pid == 12345 },
	}

	err := runCleanup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !signaled {
		t.Error("expected SIGINT to be sent to dispatcher")
	}
}

func TestCleanupWorktreeDir(t *testing.T) {
	t.Run("removes .worktrees directory when it exists", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
		fake.output[key("git", "branch", "--list", "agent/*")] = ""
		fake.output[key("git", "branch", "--list", "epic/*")] = ""
		fake.output[key("bd", "list", "--status=in_progress", "--json")] = "[]"

		tmpDir := t.TempDir()
		worktreeDir := filepath.Join(tmpDir, ".worktrees")

		// Create .worktrees directory with a file
		if err := os.MkdirAll(worktreeDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(worktreeDir, "test.txt"), []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}

		// Change to tmpDir so cleanup looks for .worktrees in the right place
		origDir, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.Chdir(origDir); err != nil {
				t.Error(err)
			}
		}()
		if err := os.Chdir(tmpDir); err != nil {
			t.Fatal(err)
		}

		var buf bytes.Buffer
		cfg := &cleanupConfig{
			runner:       fake,
			w:            &buf,
			tmuxName:     TmuxSessionName(""),
			pidPath:      filepath.Join(tmpDir, "oro.pid"),
			sockPath:     filepath.Join(tmpDir, "oro.sock"),
			worktreesDir: worktreeDir,
			signalFn:     func(int) error { return nil },
			aliveFn:      func(int) bool { return false },
		}

		err = runCleanup(context.Background(), cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify .worktrees directory was removed
		if _, err := os.Stat(worktreeDir); !os.IsNotExist(err) {
			t.Error("expected .worktrees directory to be removed")
		}

		// Check output
		out := buf.String()
		if !strings.Contains(out, "removing "+worktreeDir+" directory") {
			t.Errorf("expected output to contain removing worktreeDir, got: %s", out)
		}
	})

	t.Run("no error when directory doesn't exist", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
		fake.output[key("git", "branch", "--list", "agent/*")] = ""
		fake.output[key("git", "branch", "--list", "epic/*")] = ""
		fake.output[key("bd", "list", "--status=in_progress", "--json")] = "[]"

		tmpDir := t.TempDir()

		// Change to tmpDir (no .worktrees directory exists)
		origDir, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.Chdir(origDir); err != nil {
				t.Error(err)
			}
		}()
		if err := os.Chdir(tmpDir); err != nil {
			t.Fatal(err)
		}

		var buf bytes.Buffer
		cfg := &cleanupConfig{
			runner:   fake,
			w:        &buf,
			tmuxName: TmuxSessionName(""),
			pidPath:  filepath.Join(tmpDir, "oro.pid"),
			sockPath: filepath.Join(tmpDir, "oro.sock"),
			signalFn: func(int) error { return nil },
			aliveFn:  func(int) bool { return false },
		}

		err = runCleanup(context.Background(), cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Should complete without error even when directory doesn't exist
		// Output should be "nothing to clean" since nothing needed cleanup
		out := buf.String()
		if !strings.Contains(out, "nothing to clean") {
			t.Errorf("expected 'nothing to clean' in output, got: %s", out)
		}
	})
}

func TestCleanup_DeletesEpicBranches(t *testing.T) {
	fake := newFakeCmd()
	// tmux has-session fails
	fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
	// pgrep finds no workers
	fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
	// git branch --list returns epic branches
	fake.output[key("git", "branch", "--list", "epic/*")] = "  epic/oro-5bsn\n  epic/oro-xyz9\n"
	// no agent branches (testing epic separately from agent)
	fake.output[key("git", "branch", "--list", "agent/*")] = ""
	// bd list returns empty
	fake.output[key("bd", "list", "--status=in_progress", "--json")] = "[]"

	tmpDir := t.TempDir()
	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   fake,
		w:        &buf,
		tmuxName: TmuxSessionName(""),
		pidPath:  filepath.Join(tmpDir, "oro.pid"),
		sockPath: filepath.Join(tmpDir, "oro.sock"),
		signalFn: func(int) error { return nil },
		aliveFn:  func(int) bool { return false },
	}

	err := runCleanup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify git branch -D was called for each epic branch
	var deletedBranches []string
	for _, call := range fake.calls {
		if len(call) >= 4 && call[0] == "git" && call[1] == "branch" && call[2] == "-D" {
			deletedBranches = append(deletedBranches, call[3])
		}
	}

	if len(deletedBranches) != 2 {
		t.Fatalf("expected 2 branch deletions, got %d: %v", len(deletedBranches), deletedBranches)
	}

	found := map[string]bool{}
	for _, b := range deletedBranches {
		found[b] = true
	}
	if !found["epic/oro-5bsn"] {
		t.Error("expected epic/oro-5bsn to be deleted")
	}
	if !found["epic/oro-xyz9"] {
		t.Error("expected epic/oro-xyz9 to be deleted")
	}

	out := buf.String()
	if !strings.Contains(out, "epic/oro-5bsn") || !strings.Contains(out, "epic/oro-xyz9") {
		t.Errorf("expected output to mention deleted epic branches, got: %s", out)
	}
}

func TestCleanup_DeletesAgentAndEpicBranches(t *testing.T) {
	fake := newFakeCmd()
	// tmux has-session fails
	fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
	// pgrep finds no workers
	fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
	// git branch --list returns both agent and epic branches
	fake.output[key("git", "branch", "--list", "agent/*")] = "  agent/cleanup-cli\n  agent/fix-bug\n"
	fake.output[key("git", "worktree", "list", "--porcelain")] = ""
	fake.output[key("git", "merge-base", "--is-ancestor", "agent/cleanup-cli", "main")] = ""
	fake.output[key("git", "merge-base", "--is-ancestor", "agent/fix-bug", "main")] = ""
	fake.output[key("git", "branch", "--list", "epic/*")] = "  epic/oro-5bsn\n  epic/oro-xyz9\n"
	// bd list returns empty
	fake.output[key("bd", "list", "--status=in_progress", "--json")] = "[]"

	tmpDir := t.TempDir()
	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   fake,
		w:        &buf,
		tmuxName: TmuxSessionName(""),
		pidPath:  filepath.Join(tmpDir, "oro.pid"),
		sockPath: filepath.Join(tmpDir, "oro.sock"),
		signalFn: func(int) error { return nil },
		aliveFn:  func(int) bool { return false },
	}

	err := runCleanup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify git branch -d was called for each merged agent branch and -D for each epic branch.
	var deletedBranches []string
	for _, call := range fake.calls {
		if len(call) >= 4 && call[0] == "git" && call[1] == "branch" &&
			(call[2] == "-d" || call[2] == "-D") {
			deletedBranches = append(deletedBranches, call[3])
		}
	}

	if len(deletedBranches) != 4 {
		t.Fatalf("expected 4 branch deletions, got %d: %v", len(deletedBranches), deletedBranches)
	}

	found := map[string]bool{}
	for _, b := range deletedBranches {
		found[b] = true
	}
	if !found["agent/cleanup-cli"] {
		t.Error("expected agent/cleanup-cli to be deleted")
	}
	if !found["agent/fix-bug"] {
		t.Error("expected agent/fix-bug to be deleted")
	}
	if !found["epic/oro-5bsn"] {
		t.Error("expected epic/oro-5bsn to be deleted")
	}
	if !found["epic/oro-xyz9"] {
		t.Error("expected epic/oro-xyz9 to be deleted")
	}

	out := buf.String()
	if !strings.Contains(out, "agent/cleanup-cli") || !strings.Contains(out, "agent/fix-bug") ||
		!strings.Contains(out, "epic/oro-5bsn") || !strings.Contains(out, "epic/oro-xyz9") {
		t.Errorf("expected output to mention all deleted branches, got: %s", out)
	}
}

func TestCleanup_DoesNotCallBdDaemon(t *testing.T) {
	fake := newFakeCmd()
	// tmux has-session fails (no session)
	fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
	// no workers
	fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
	// no agent branches
	fake.output[key("git", "branch", "--list", "agent/*")] = ""
	// no epic branches
	fake.output[key("git", "branch", "--list", "epic/*")] = ""
	// no in_progress beads
	fake.output[key("bd", "list", "--status=in_progress", "--json")] = "[]"

	tmpDir := t.TempDir()
	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   fake,
		w:        &buf,
		tmuxName: TmuxSessionName(""),
		pidPath:  filepath.Join(tmpDir, "oro.pid"),
		sockPath: filepath.Join(tmpDir, "oro.sock"),
		signalFn: func(int) error { return nil },
		aliveFn:  func(int) bool { return false },
	}

	err := runCleanup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify bd daemon stop was NOT called.
	for _, call := range fake.calls {
		if len(call) >= 3 && call[0] == "bd" && call[1] == "daemon" && call[2] == "stop" {
			t.Errorf("unexpected 'bd daemon stop' call; calls = %v", fake.calls)
		}
	}
}

// TestCleanupEpicBranches verifies that cleanupAgentBranches deletes epic/* branches.
func TestCleanupEpicBranches(t *testing.T) {
	fake := newFakeCmd()
	// tmux has-session fails
	fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
	// pgrep finds no workers
	fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
	// git branch --list returns epic branches
	fake.output[key("git", "branch", "--list", "epic/*")] = "  epic/branch-1\n  epic/branch-2\n"
	// no agent branches
	fake.output[key("git", "branch", "--list", "agent/*")] = ""
	// bd list returns empty
	fake.output[key("bd", "list", "--status=in_progress", "--json")] = "[]"

	tmpDir := t.TempDir()
	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   fake,
		w:        &buf,
		tmuxName: TmuxSessionName(""),
		pidPath:  filepath.Join(tmpDir, "oro.pid"),
		sockPath: filepath.Join(tmpDir, "oro.sock"),
		signalFn: func(int) error { return nil },
		aliveFn:  func(int) bool { return false },
	}

	err := runCleanup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify git branch -D was called for each epic branch
	var deletedBranches []string
	for _, call := range fake.calls {
		if len(call) >= 4 && call[0] == "git" && call[1] == "branch" && call[2] == "-D" {
			deletedBranches = append(deletedBranches, call[3])
		}
	}

	if len(deletedBranches) != 2 {
		t.Fatalf("expected 2 branch deletions, got %d: %v", len(deletedBranches), deletedBranches)
	}

	found := map[string]bool{}
	for _, b := range deletedBranches {
		found[b] = true
	}
	if !found["epic/branch-1"] {
		t.Error("expected epic/branch-1 to be deleted")
	}
	if !found["epic/branch-2"] {
		t.Error("expected epic/branch-2 to be deleted")
	}

	out := buf.String()
	if !strings.Contains(out, "epic/branch-1") || !strings.Contains(out, "epic/branch-2") {
		t.Errorf("expected output to mention deleted epic branches, got: %s", out)
	}
}
