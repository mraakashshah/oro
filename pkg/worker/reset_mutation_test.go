package worker //nolint:testpackage // reset mutation owners exercise unexported worker state.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"oro/pkg/protocol"
)

type resetMutationProcess struct {
	mu     sync.Mutex
	killed bool
}

func (p *resetMutationProcess) Wait() error { return nil }

func (p *resetMutationProcess) Kill() error {
	p.mu.Lock()
	p.killed = true
	p.mu.Unlock()
	return nil
}

func (p *resetMutationProcess) Killed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.killed
}

func TestWorkerResetMutationOwners(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	t.Run("full reset clears prior assignment and rotates log", func(t *testing.T) {
		worktree := t.TempDir()
		oroDir := filepath.Join(worktree, protocol.OroDir)
		if err := os.MkdirAll(oroDir, 0o700); err != nil {
			t.Fatal(err)
		}
		staleHandoff := filepath.Join(oroDir, "handoff_done")
		staleContext := filepath.Join(oroDir, "context_pct")
		for _, path := range []string{staleHandoff, staleContext} {
			if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
				t.Fatal(err)
			}
		}

		w := &Worker{ID: "reset-owner"}
		if err := w.openLogFile(); err != nil {
			t.Fatal(err)
		}
		oldFile := w.logFile
		if _, err := w.logWriter.WriteString("old assignment\n"); err != nil {
			t.Fatal(err)
		}
		if err := w.logWriter.Flush(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(w.closeLogFile)

		oldProcess := &resetMutationProcess{}
		w.proc = oldProcess
		w.subprocKilledByUs = false
		w.assignmentGeneration = 41
		w.execution = WorkerExecutionContext{AssignmentID: 40, Generation: 40, WorkerID: "old-worker", Role: "old"}
		w.beadID = "old-bead"
		w.worktree = "old-worktree"
		w.assignmentID = 40
		w.qgEvidenceDir = "old-evidence-dir"
		w.targetSHA = strings.Repeat("a", 40)
		w.qgEvidencePath = "/old/evidence.json"
		w.qgEvidence = &protocol.QGEvidence{RunID: "40:1"}
		w.qgEvidenceRef = &protocol.QGEvidenceRef{RunID: "40:1"}
		w.sessionText.WriteString("stale session")
		w.pendingQGOutput = "stale QG"
		w.isEpicDecomposition = true
		atomic.StoreInt32(&w.streamContextPct, 77)

		execution := WorkerExecutionContext{
			AssignmentID:   42,
			Generation:     42,
			WorkerID:       "new-worker",
			Role:           "implementer",
			SocketPath:     "/tmp/new.sock",
			CapabilityFile: "/tmp/new-capability",
		}
		assignment := &protocol.AssignPayload{
			BeadID:              "new-bead",
			Worktree:            worktree,
			AssignmentID:        42,
			QGEvidenceDir:       filepath.Join(worktree, "evidence"),
			TargetSHA:           strings.Repeat("b", 40),
			TargetBranch:        "",
			Tier:                protocol.TierFast,
			IsEpicDecomposition: false,
		}

		runResetMutation(t, w, assignment, execution)

		if !oldProcess.Killed() {
			t.Fatal("reset did not terminate the prior process")
		}
		if w.proc != nil || !w.subprocKilledByUs {
			t.Fatalf("process state after reset = proc=%v killed=%v", w.proc, w.subprocKilledByUs)
		}
		if w.assignmentGeneration != 42 {
			t.Fatalf("assignment generation = %d, want 42", w.assignmentGeneration)
		}
		if w.execution != execution {
			t.Fatalf("execution context = %#v, want %#v", w.execution, execution)
		}
		if w.beadID != "new-bead" || w.worktree != worktree || w.assignmentID != 42 ||
			w.qgEvidenceDir != assignment.QGEvidenceDir || w.targetSHA != assignment.TargetSHA ||
			w.targetBranch != "main" {
			t.Fatalf("assignment identity = bead=%q worktree=%q id=%d evidenceDir=%q target=%q branch=%q",
				w.beadID, w.worktree, w.assignmentID, w.qgEvidenceDir, w.targetSHA, w.targetBranch)
		}
		if w.qgEvidencePath != "" || w.qgEvidence != nil || w.qgEvidenceRef != nil ||
			w.sessionText.Len() != 0 || w.pendingQGOutput != "" || w.isEpicDecomposition ||
			atomic.LoadInt32(&w.streamContextPct) != 0 {
			t.Fatalf("reset retained stale state: path=%q evidence=%#v ref=%#v session=%q pending=%q epic=%v context=%d",
				w.qgEvidencePath, w.qgEvidence, w.qgEvidenceRef, w.sessionText.String(), w.pendingQGOutput,
				w.isEpicDecomposition, atomic.LoadInt32(&w.streamContextPct))
		}
		for _, path := range []string{staleHandoff, staleContext} {
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("stale assignment file %s remains: %v", path, err)
			}
		}
		if _, err := oldFile.WriteString("should fail\n"); err == nil {
			t.Fatal("reset left the prior log file writable")
		}
		if w.logFile == nil || w.logWriter == nil {
			t.Fatal("reset did not initialize a new log")
		}
		if _, err := w.logWriter.WriteString("new assignment\n"); err != nil {
			t.Fatal(err)
		}
		if err := w.logWriter.Flush(); err != nil {
			t.Fatal(err)
		}
		logPath := filepath.Join(os.Getenv("HOME"), ".oro", "workers", w.ID, "output.log")
		logData, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(logData), "old assignment") || !strings.Contains(string(logData), "new assignment") {
			t.Fatalf("rotated log content = %q", logData)
		}
	})

	t.Run("repeated nil-worktree reset uses default main", func(t *testing.T) {
		w := &Worker{ID: "reset-repeat"}
		t.Cleanup(w.closeLogFile)
		runResetMutation(t, w, &protocol.AssignPayload{BeadID: "first", AssignmentID: 1}, WorkerExecutionContext{AssignmentID: 1})
		runResetMutation(t, w, &protocol.AssignPayload{BeadID: "second", AssignmentID: 2, TargetBranch: "release"}, WorkerExecutionContext{AssignmentID: 2})
		runResetMutation(t, w, &protocol.AssignPayload{BeadID: "third", AssignmentID: 3}, WorkerExecutionContext{AssignmentID: 3})
		if w.assignmentGeneration != 3 || w.beadID != "third" || w.assignmentID != 3 || w.targetBranch != "main" {
			t.Fatalf("repeated reset state = generation=%d bead=%q assignment=%d branch=%q", w.assignmentGeneration, w.beadID, w.assignmentID, w.targetBranch)
		}
		if w.worktree != "" || w.qgEvidencePath != "" || w.qgEvidence != nil || w.qgEvidenceRef != nil || w.sessionText.Len() != 0 {
			t.Fatalf("nil-worktree reset retained state: worktree=%q path=%q evidence=%#v ref=%#v session=%q", w.worktree, w.qgEvidencePath, w.qgEvidence, w.qgEvidenceRef, w.sessionText.String())
		}
	})
}

func runResetMutation(t *testing.T, w *Worker, assignment *protocol.AssignPayload, execution WorkerExecutionContext) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		w.resetForNewAssignment(assignment, execution)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("resetForNewAssignment did not finish within 1s")
	}
}
