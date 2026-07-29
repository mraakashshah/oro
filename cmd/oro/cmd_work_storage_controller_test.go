package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/dispatcher"
	"oro/pkg/protocol"
	"oro/pkg/storage"
)

type storageControllerWorktreeManager struct {
	dispatcher.WorktreeManager
	path string
}

func (m storageControllerWorktreeManager) Create(context.Context, string, string) (string, string, error) {
	return m.path, "agent/oro-storage-pause", nil
}

func TestStandaloneStoragePauseStopsCommands(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	oroHome := t.TempDir()
	t.Setenv("ORO_HOME", oroHome)
	t.Setenv("ORO_PROJECT", "storage-pause")

	catalog, err := storage.OpenCatalog(ctx, filepath.Join(oroHome, "catalog.db"))
	if err != nil {
		t.Fatalf("open storage catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	identity, err := storage.InspectProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatalf("inspect process identity: %v", err)
	}
	if err := catalog.UpsertController(ctx, storage.Controller{
		ID: "standalone-work", OwnerID: "test", PID: os.Getpid(), ProcessStart: now,
		Identity: identity, HeartbeatAt: now,
	}); err != nil {
		t.Fatalf("register storage controller: %v", err)
	}

	drainStarted := make(chan struct{})
	drainRelease := make(chan struct{})
	controller, err := storage.NewController(storage.ControllerConfig{
		Catalog: catalog,
		ID:      "standalone-work",
		Drain: func(context.Context) error {
			close(drainStarted)
			<-drainRelease
			return nil
		},
	})
	if err != nil {
		t.Fatalf("new storage controller: %v", err)
	}
	pause, err := storage.NewPauseEpochProtocol(catalog, nil).RequestPause(ctx, now)
	if err != nil {
		t.Fatalf("request pause: %v", err)
	}

	commandStarts := make(chan struct{}, 1)
	worktree := t.TempDir()
	store := beadstore.NewFakeStore(protocol.Bead{
		ID: "oro-storage-pause", Title: "storage pause", Status: "open", AcceptanceCriteria: "Test: cmd/oro/cmd_work_storage_controller_test.go:TestStandaloneStoragePauseStopsCommands",
	})
	cfg := &workConfig{beadID: "oro-storage-pause", skipReview: true, storageController: controller}
	deps := &workDeps{
		beadSrc:       store,
		wtMgr:         storageControllerWorktreeManager{path: worktree},
		repoRoot:      t.TempDir(),
		defaultBranch: "main",
		hasNewWork:    func(string, string, string) bool { return true },
		runQG: func(context.Context, string, bool) (bool, string, error) {
			commandStarts <- struct{}{}
			return true, "", nil
		},
	}

	done := make(chan error, 1)
	go func() { done <- executeWork(ctx, cfg, deps) }()
	select {
	case <-drainStarted:
	case <-time.After(time.Second):
		t.Fatal("standalone work did not start storage drain")
	}
	select {
	case <-commandStarts:
		t.Fatal("standalone work started a command while storage drain was active")
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := catalog.PauseAcknowledgement(ctx, pause.Epoch, "standalone-work"); err == nil {
		t.Fatal("pause was acknowledged before the storage drain completed")
	}

	close(drainRelease)
	if err := <-done; err == nil {
		t.Fatal("executeWork error = nil, want paused storage admission error")
	}
	if _, err := catalog.PauseAcknowledgement(ctx, pause.Epoch, "standalone-work"); err != nil {
		t.Fatalf("pause acknowledgement after drain: %v", err)
	}
}
