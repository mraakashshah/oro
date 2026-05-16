package dispatcher //nolint:testpackage // white-box tests for resolveEpicBranch

import (
	"context"
	"errors"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"
)

type showErrorStore struct {
	*beadstore.FakeStore
	err error
}

func (s showErrorStore) Show(context.Context, string) (*protocol.Bead, error) {
	return nil, s.err
}

func TestResolveEpicBranch_EmptyParent(t *testing.T) {
	bs := beadstore.NewFakeStore()
	branch, epicID, err := resolveEpicBranch(context.Background(), bs, "", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if branch != "main" {
		t.Errorf("branch = %q; want %q", branch, "main")
	}
	if epicID != "" {
		t.Errorf("epicID = %q; want empty", epicID)
	}
}

func TestResolveEpicBranch_DirectEpicParent(t *testing.T) {
	bs := beadstore.NewFakeStore(protocol.Bead{ID: "epic-1", Title: "Epic 1", Type: "epic"})
	branch, epicID, err := resolveEpicBranch(context.Background(), bs, "epic-1", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if branch != "epic/epic-1" {
		t.Errorf("branch = %q; want %q", branch, "epic/epic-1")
	}
	if epicID != "epic-1" {
		t.Errorf("epicID = %q; want %q", epicID, "epic-1")
	}
}

func TestResolveEpicBranch_NonEpicParent_ReturnsMain(t *testing.T) {
	bs := beadstore.NewFakeStore(protocol.Bead{ID: "task-1", Title: "Task 1", Type: "task"})
	branch, epicID, err := resolveEpicBranch(context.Background(), bs, "task-1", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if branch != "main" {
		t.Errorf("branch = %q; want %q", branch, "main")
	}
	if epicID != "" {
		t.Errorf("epicID = %q; want empty", epicID)
	}
}

func TestResolveEpicBranch_NonEpicParentWithEpicGrandparent(t *testing.T) {
	bs := beadstore.NewFakeStore(
		protocol.Bead{ID: "task-1", Title: "Task 1", Type: "task", Epic: "epic-2"},
		protocol.Bead{ID: "epic-2", Title: "Epic 2", Type: "epic"},
	)
	branch, epicID, err := resolveEpicBranch(context.Background(), bs, "task-1", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if branch != "epic/epic-2" {
		t.Errorf("branch = %q; want %q", branch, "epic/epic-2")
	}
	if epicID != "epic-2" {
		t.Errorf("epicID = %q; want %q", epicID, "epic-2")
	}
}

func TestResolveEpicBranch_ShowError_ReturnsMainWithError(t *testing.T) {
	bs := showErrorStore{FakeStore: beadstore.NewFakeStore(), err: errors.New("show failed")}
	branch, epicID, err := resolveEpicBranch(context.Background(), bs, "some-bead", "main")
	if err == nil {
		t.Fatal("expected error, got none")
	}
	if branch != "main" {
		t.Errorf("branch on error = %q; want %q", branch, "main")
	}
	if epicID != "" {
		t.Errorf("epicID on error = %q; want empty", epicID)
	}
}

func TestResolveEpicBranch_NilShowResult_ReturnsMainWithError(t *testing.T) {
	bs := beadstore.NewFakeStore()
	branch, epicID, err := resolveEpicBranch(context.Background(), bs, "missing-parent", "main")
	if err == nil {
		t.Fatal("expected error, got none")
	}
	if branch != "main" {
		t.Errorf("branch on error = %q; want %q", branch, "main")
	}
	if epicID != "" {
		t.Errorf("epicID on error = %q; want empty", epicID)
	}
}

// TestResolveEpicBranch_DefaultBranch verifies that resolveEpicBranch returns
// defaultBranch (not hardcoded "main") in all 4 non-epic return paths.
func TestResolveEpicBranch_DefaultBranch(t *testing.T) {
	const customDefault = "release/v2"

	t.Run("empty parent returns defaultBranch", func(t *testing.T) {
		bs := beadstore.NewFakeStore()
		branch, epicID, err := resolveEpicBranch(context.Background(), bs, "", customDefault)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if branch != customDefault {
			t.Errorf("branch = %q; want %q", branch, customDefault)
		}
		if epicID != "" {
			t.Errorf("epicID = %q; want empty", epicID)
		}
	})

	t.Run("non-epic chain exhausted returns defaultBranch", func(t *testing.T) {
		bs := beadstore.NewFakeStore(protocol.Bead{ID: "task-1", Title: "Task 1", Type: "task"})
		branch, epicID, err := resolveEpicBranch(context.Background(), bs, "task-1", customDefault)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if branch != customDefault {
			t.Errorf("branch = %q; want %q", branch, customDefault)
		}
		if epicID != "" {
			t.Errorf("epicID = %q; want empty", epicID)
		}
	})

	t.Run("show error returns defaultBranch", func(t *testing.T) {
		bs := showErrorStore{FakeStore: beadstore.NewFakeStore(), err: errors.New("show failed")}
		branch, _, err := resolveEpicBranch(context.Background(), bs, "some-bead", customDefault)
		if err == nil {
			t.Fatal("expected error, got none")
		}
		if branch != customDefault {
			t.Errorf("branch on error = %q; want %q", branch, customDefault)
		}
	})

	t.Run("cycle detected returns defaultBranch", func(t *testing.T) {
		bs := beadstore.NewFakeStore(
			// a -> b -> a (cycle)
			protocol.Bead{ID: "a", Title: "A", Type: "task", Epic: "b"},
			protocol.Bead{ID: "b", Title: "B", Type: "task", Epic: "a"},
		)
		branch, epicID, err := resolveEpicBranch(context.Background(), bs, "a", customDefault)
		if err == nil {
			t.Fatal("expected cycle error, got none")
		}
		if branch != customDefault {
			t.Errorf("branch on cycle = %q; want %q", branch, customDefault)
		}
		if epicID != "" {
			t.Errorf("epicID on cycle = %q; want empty", epicID)
		}
	})
}

// TestAssignBead_NonEpicParent_UsesMain verifies that a bead whose parent is a
// non-epic bead (task) is assigned with baseBranch="main", not "epic/<parentID>".
// This is the integration test for the acceptance criterion:
// "continuation of a non-epic bead does not produce an epic branch reference".
func TestAssignBead_NonEpicParent_UsesMain(t *testing.T) {
	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Register the parent bead as a task (not an epic).
	beadSrc.mu.Lock()
	beadSrc.shown["task-parent"] = &protocol.BeadDetail{
		ID:    "task-parent",
		Title: "Some task bead",
		Type:  "task",
	}
	beadSrc.mu.Unlock()

	// Track which baseBranch the worktree was created from.
	var capturedBase string
	wtMgr.mu.Lock()
	wtMgr.createFn = func(_ context.Context, beadID, baseBranch string) (string, string, error) {
		if beadID == "child-bead" {
			capturedBase = baseBranch
		}
		return "/tmp/worktree-" + beadID, "agent/" + beadID, nil
	}
	wtMgr.mu.Unlock()

	// Connect a worker.
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Enqueue a bead whose Epic (parent) is "task-parent" — a non-epic bead.
	beadSrc.SetBeads([]protocol.Bead{
		{
			ID:                 "child-bead",
			Title:              "Child of task",
			Priority:           1,
			Epic:               "task-parent", // bead.Epic = parent field, may be non-epic
			AcceptanceCriteria: "Test: passes",
		},
	})

	// Wait for the ASSIGN message.
	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN message")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", msg.Type)
	}

	// The worktree must have been created from "main", not "epic/task-parent".
	if capturedBase != "main" {
		t.Errorf("worktree baseBranch = %q; want %q (non-epic parent should not produce epic branch)", capturedBase, "main")
	}

	// The ASSIGN payload's TargetBranch must also be "main".
	if msg.Assign != nil && msg.Assign.TargetBranch != "main" {
		t.Errorf("ASSIGN.TargetBranch = %q; want %q", msg.Assign.TargetBranch, "main")
	}
}
