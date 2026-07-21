package dispatcher //nolint:testpackage // white-box test verifies dispatcher-owned evidence persistence

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEvidenceRunBoundsAndManifest(t *testing.T) {
	if os.Getenv("ORO_EVIDENCE_RUN_HELPER") == "1" {
		switch os.Getenv("ORO_EVIDENCE_RUN_MODE") {
		case "large-output":
			_, _ = fmt.Fprint(os.Stdout, strings.Repeat("x", 32*1024+1))
		case "sleep":
			time.Sleep(time.Second)
		case "wait-cancel":
			if err := os.WriteFile(os.Getenv("ORO_EVIDENCE_RUN_SIGNAL"), []byte("started"), 0o600); err != nil {
				os.Exit(2)
			}
			time.Sleep(time.Second)
		case "exit-3":
			os.Exit(3)
		}
		return
	}

	ctx := context.Background()
	worktree := t.TempDir()
	runEvidenceGit(t, worktree, "init", "-q")
	runEvidenceGit(t, worktree, "config", "user.email", "evidence@example.test")
	runEvidenceGit(t, worktree, "config", "user.name", "Evidence Test")
	if err := os.WriteFile(filepath.Join(worktree, "README"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	runEvidenceGit(t, worktree, "add", "README")
	runEvidenceGit(t, worktree, "commit", "-qm", "fixture")
	branch := runEvidenceGit(t, worktree, "branch", "--show-current")
	head := runEvidenceGit(t, worktree, "rev-parse", "HEAD")

	d := &Dispatcher{db: newTestDB(t), repoRoot: filepath.Dir(worktree)}
	assignmentID, err := d.createAssignment(ctx, "bead-evidence", "worker-evidence", worktree)
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}
	request := EvidenceRunRequest{
		AssignmentID: assignmentID,
		WorkerID:     "worker-evidence",
		BeadID:       "bead-evidence",
		Argv:         []string{os.Args[0], "-test.run=TestEvidenceRunBoundsAndManifest"},
	}
	t.Setenv("ORO_EVIDENCE_RUN_HELPER", "1")
	manifest, err := d.RunEvidence(ctx, request)
	if err != nil {
		t.Fatalf("run evidence: %v", err)
	}
	if manifest.Project != filepath.Base(filepath.Dir(worktree)) || manifest.AssignmentID != assignmentID ||
		manifest.WorkerID != request.WorkerID || manifest.BeadID != request.BeadID || manifest.Worktree != worktree ||
		manifest.Branch != branch || manifest.HEAD != head || strings.Join(manifest.Argv, "\x00") != strings.Join(request.Argv, "\x00") ||
		manifest.StartedAt.IsZero() || manifest.CompletedAt.IsZero() || manifest.ExitCode != 0 || manifest.Truncated || manifest.Status != EvidenceRunCompleted {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}

	request.Argv = []string{strings.Repeat("x", 4*1024+1)}
	if _, err := d.RunEvidence(ctx, request); err == nil {
		t.Fatal("oversized argv was accepted")
	}

	request.Argv = []string{os.Args[0], "-test.run=TestEvidenceRunBoundsAndManifest"}
	t.Setenv("ORO_EVIDENCE_RUN_HELPER", "1")
	t.Setenv("ORO_EVIDENCE_RUN_MODE", "large-output")
	manifest, err = d.RunEvidence(ctx, request)
	if err != nil {
		t.Fatalf("run large-output evidence: %v", err)
	}
	if !manifest.Truncated || len(manifest.Output) != 32*1024 {
		t.Fatalf("large-output manifest = %+v, want truncated 32KiB output", manifest)
	}

	t.Setenv("ORO_EVIDENCE_RUN_MODE", "exit-3")
	manifest, err = d.RunEvidence(ctx, request)
	if err != nil || manifest.Status != EvidenceRunCompleted || manifest.ExitCode != 3 {
		t.Fatalf("non-zero manifest/error = %+v / %v", manifest, err)
	}

	t.Setenv("ORO_EVIDENCE_RUN_MODE", "sleep")
	request.Timeout = 10 * time.Millisecond
	manifest, err = d.RunEvidence(ctx, request)
	if err == nil || manifest.Status != EvidenceRunTimedOut || manifest.CompletedAt.IsZero() {
		t.Fatalf("timeout manifest/error = %+v / %v", manifest, err)
	}

	request.Timeout = 0
	request.Argv = []string{filepath.Join(worktree, "missing-evidence-command")}
	manifest, err = d.RunEvidence(ctx, request)
	if err == nil || manifest.Status != EvidenceRunCrashed || manifest.CompletedAt.IsZero() {
		t.Fatalf("crash manifest/error = %+v / %v", manifest, err)
	}
}

func TestEvidenceRunPersistsCancelledManifest(t *testing.T) {
	if os.Getenv("ORO_EVIDENCE_RUN_HELPER") == "1" {
		if os.Getenv("ORO_EVIDENCE_RUN_MODE") == "wait-cancel" {
			if err := os.WriteFile(os.Getenv("ORO_EVIDENCE_RUN_SIGNAL"), []byte("started"), 0o600); err != nil {
				os.Exit(2)
			}
			time.Sleep(time.Second)
		}
		return
	}

	ctx := context.Background()
	worktree := evidenceTestWorktree(t)
	d := &Dispatcher{db: newTestDB(t), repoRoot: filepath.Dir(worktree)}
	assignmentID, err := d.createAssignment(ctx, "bead-cancel", "worker-cancel", worktree)
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}

	signal := filepath.Join(t.TempDir(), "started")
	t.Setenv("ORO_EVIDENCE_RUN_HELPER", "1")
	t.Setenv("ORO_EVIDENCE_RUN_MODE", "wait-cancel")
	t.Setenv("ORO_EVIDENCE_RUN_SIGNAL", signal)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	result := make(chan evidenceRunResult, 1)
	go func() {
		manifest, runErr := d.RunEvidence(runCtx, EvidenceRunRequest{
			AssignmentID: assignmentID,
			WorkerID:     "worker-cancel",
			BeadID:       "bead-cancel",
			Argv:         []string{os.Args[0], "-test.run=TestEvidenceRunPersistsCancelledManifest"},
		})
		result <- evidenceRunResult{manifest: manifest, err: runErr}
	}()
	waitForEvidenceSignal(t, signal)
	cancel()

	got := <-result
	if got.err == nil || got.manifest.Status != EvidenceRunCancelled {
		t.Fatalf("cancelled manifest/error = %+v / %v", got.manifest, got.err)
	}
	var status string
	if err := d.db.QueryRowContext(ctx, "SELECT status FROM evidence_runs WHERE id=?", got.manifest.ID).Scan(&status); err != nil {
		t.Fatalf("load cancelled manifest: %v", err)
	}
	if status != string(EvidenceRunCancelled) {
		t.Fatalf("stored status = %q, want %q", status, EvidenceRunCancelled)
	}
}

func TestEvidenceRunTimeoutIsBounded(t *testing.T) {
	if got := evidenceRunTimeout(time.Hour); got != 10*time.Minute {
		t.Fatalf("evidenceRunTimeout(1h) = %s, want 10m", got)
	}
	if got := evidenceRunTimeout(0); got != 2*time.Minute {
		t.Fatalf("evidenceRunTimeout(0) = %s, want 2m", got)
	}
}

type evidenceRunResult struct {
	manifest EvidenceManifest
	err      error
}

func evidenceTestWorktree(t *testing.T) string {
	t.Helper()
	worktree := t.TempDir()
	runEvidenceGit(t, worktree, "init", "-q")
	runEvidenceGit(t, worktree, "config", "user.email", "evidence@example.test")
	runEvidenceGit(t, worktree, "config", "user.name", "Evidence Test")
	if err := os.WriteFile(filepath.Join(worktree, "README"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	runEvidenceGit(t, worktree, "add", "README")
	runEvidenceGit(t, worktree, "commit", "-qm", "fixture")
	return worktree
}

func waitForEvidenceSignal(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for evidence helper signal")
}

func runEvidenceGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}
