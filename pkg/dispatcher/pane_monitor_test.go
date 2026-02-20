package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"oro/pkg/protocol"

	_ "modernc.org/sqlite"
)

// pollCounter installs a testPanePollDone hook that increments a counter and
// returns a function that checks whether at least n polls have completed.
func pollCounter(d *Dispatcher) func(n int64) func() bool {
	var count atomic.Int64
	d.testPanePollDone = func() { count.Add(1) }
	return func(n int64) func() bool {
		baseline := count.Load()
		return func() bool { return count.Load() >= baseline+n }
	}
}

func TestPaneMonitorLoop_SignalsHandoff(t *testing.T) {
	// Create temporary test directory
	tmpDir := t.TempDir()
	panesDir := filepath.Join(tmpDir, ".oro", "panes")
	architectDir := filepath.Join(panesDir, "architect")
	managerDir := filepath.Join(panesDir, "manager")

	//nolint:gosec // test directory permissions
	if err := os.MkdirAll(architectDir, 0o755); err != nil {
		t.Fatalf("failed to create architect dir: %v", err)
	}
	//nolint:gosec // test directory permissions
	if err := os.MkdirAll(managerDir, 0o755); err != nil {
		t.Fatalf("failed to create manager dir: %v", err)
	}

	// Create context_pct files with values below threshold
	architectPctFile := filepath.Join(architectDir, "context_pct")
	managerPctFile := filepath.Join(managerDir, "context_pct")

	//nolint:gosec // test file permissions
	if err := os.WriteFile(architectPctFile, []byte("50"), 0o644); err != nil {
		t.Fatalf("failed to write architect context_pct: %v", err)
	}
	//nolint:gosec // test file permissions
	if err := os.WriteFile(managerPctFile, []byte("40"), 0o644); err != nil {
		t.Fatalf("failed to write manager context_pct: %v", err)
	}

	// Create dispatcher with test database
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("failed to init schema: %v", err)
	}

	cfg := Config{
		PaneContextThreshold: 60,
		PaneMonitorInterval:  100 * time.Millisecond, // Fast polling for test
	}
	cfg = cfg.withDefaults()

	d := &Dispatcher{
		cfg:           cfg,
		db:            db,
		panesDir:      panesDir,
		nowFunc:       time.Now,
		signaledPanes: make(map[string]bool),
	}

	// Install poll-completion hook for synchronization
	awaitPolls := pollCounter(d)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start monitor loop
	done := make(chan struct{})
	go func() {
		d.paneMonitorLoop(ctx)
		close(done)
	}()

	// Wait for at least one poll to complete (replaces time.Sleep)
	pollDone := awaitPolls(1)
	waitFor(t, pollDone, 2*time.Second)

	// Verify no handoff files created yet (below threshold)
	architectHandoffFile := filepath.Join(architectDir, "handoff_requested")
	managerHandoffFile := filepath.Join(managerDir, "handoff_requested")

	if _, err := os.Stat(architectHandoffFile); err == nil {
		t.Error("architect handoff_requested should not exist yet")
	}
	if _, err := os.Stat(managerHandoffFile); err == nil {
		t.Error("manager handoff_requested should not exist yet")
	}

	// Update architect to exceed threshold
	//nolint:gosec // test file permissions
	if err := os.WriteFile(architectPctFile, []byte("65"), 0o644); err != nil {
		t.Fatalf("failed to update architect context_pct: %v", err)
	}

	// Wait for handoff file to appear (replaces time.Sleep)
	waitFor(t, func() bool {
		_, statErr := os.Stat(architectHandoffFile)
		return statErr == nil
	}, 2*time.Second)

	// Verify handoff file created for architect
	if _, err := os.Stat(architectHandoffFile); os.IsNotExist(err) {
		t.Error("architect handoff_requested should exist after exceeding threshold")
	}

	// Manager should still not have handoff file
	if _, err := os.Stat(managerHandoffFile); err == nil {
		t.Error("manager handoff_requested should not exist (below threshold)")
	}

	// Update manager to exceed threshold
	//nolint:gosec // test file permissions
	if err := os.WriteFile(managerPctFile, []byte("70"), 0o644); err != nil {
		t.Fatalf("failed to update manager context_pct: %v", err)
	}

	// Wait for manager handoff file to appear (replaces time.Sleep)
	waitFor(t, func() bool {
		_, statErr := os.Stat(managerHandoffFile)
		return statErr == nil
	}, 2*time.Second)

	// Verify handoff file created for manager
	if _, err := os.Stat(managerHandoffFile); os.IsNotExist(err) {
		t.Error("manager handoff_requested should exist after exceeding threshold")
	}

	// Update architect back below threshold
	//nolint:gosec // test file permissions
	if err := os.WriteFile(architectPctFile, []byte("50"), 0o644); err != nil {
		t.Fatalf("failed to update architect context_pct: %v", err)
	}

	// Wait for at least one more poll cycle (replaces time.Sleep)
	pollAfterLower := awaitPolls(1)
	waitFor(t, pollAfterLower, 2*time.Second)

	// Verify architect is still signaled (no re-signal, stays in map)
	d.mu.Lock()
	architectSignaled := d.signaledPanes["architect"]
	d.mu.Unlock()

	if !architectSignaled {
		t.Error("architect should remain in signaledPanes map")
	}

	// Cancel context and wait for loop to exit
	cancel()
	select {
	case <-done:
		// Loop exited cleanly
	case <-time.After(2 * time.Second):
		t.Error("paneMonitorLoop did not exit after context cancellation")
	}
}

func TestPaneMonitorLoop_SkipsMissingFiles(t *testing.T) {
	// Create temporary test directory with only architect dir
	tmpDir := t.TempDir()
	panesDir := filepath.Join(tmpDir, ".oro", "panes")
	architectDir := filepath.Join(panesDir, "architect")

	//nolint:gosec // test directory permissions
	if err := os.MkdirAll(architectDir, 0o755); err != nil {
		t.Fatalf("failed to create architect dir: %v", err)
	}

	// Don't create manager dir or any context_pct files

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("failed to init schema: %v", err)
	}

	cfg := Config{
		PaneContextThreshold: 60,
		PaneMonitorInterval:  100 * time.Millisecond, // Fast polling for test
	}
	cfg = cfg.withDefaults()

	d := &Dispatcher{
		cfg:           cfg,
		db:            db,
		panesDir:      panesDir,
		nowFunc:       time.Now,
		signaledPanes: make(map[string]bool),
	}

	// Install poll-completion hook for synchronization
	awaitPolls := pollCounter(d)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	// Start monitor loop - should not panic or error
	done := make(chan struct{})
	go func() {
		d.paneMonitorLoop(ctx)
		close(done)
	}()

	// Wait for at least one poll to complete (replaces time.Sleep)
	pollDone := awaitPolls(1)
	waitFor(t, pollDone, 2*time.Second)

	// Cancel and verify clean exit
	cancel()
	select {
	case <-done:
		// Loop exited cleanly
	case <-time.After(1 * time.Second):
		t.Error("paneMonitorLoop did not exit after context cancellation")
	}
}

func TestPaneMonitorLoop_ParseError(t *testing.T) {
	// Create temporary test directory
	tmpDir := t.TempDir()
	panesDir := filepath.Join(tmpDir, ".oro", "panes")
	architectDir := filepath.Join(panesDir, "architect")

	//nolint:gosec // test directory permissions
	if err := os.MkdirAll(architectDir, 0o755); err != nil {
		t.Fatalf("failed to create architect dir: %v", err)
	}

	// Create context_pct file with invalid content
	architectPctFile := filepath.Join(architectDir, "context_pct")
	//nolint:gosec // test file permissions
	if err := os.WriteFile(architectPctFile, []byte("not-a-number"), 0o644); err != nil {
		t.Fatalf("failed to write architect context_pct: %v", err)
	}

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("failed to init schema: %v", err)
	}

	cfg := Config{
		PaneContextThreshold: 60,
		PaneMonitorInterval:  100 * time.Millisecond, // Fast polling for test
	}
	cfg = cfg.withDefaults()

	d := &Dispatcher{
		cfg:           cfg,
		db:            db,
		panesDir:      panesDir,
		nowFunc:       time.Now,
		signaledPanes: make(map[string]bool),
	}

	// Install poll-completion hook for synchronization
	awaitPolls := pollCounter(d)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	// Start monitor loop - should skip parse errors gracefully
	done := make(chan struct{})
	go func() {
		d.paneMonitorLoop(ctx)
		close(done)
	}()

	// Wait for at least one poll to complete (replaces time.Sleep)
	pollDone := awaitPolls(1)
	waitFor(t, pollDone, 2*time.Second)

	// Verify no handoff file created (parse error should skip)
	architectHandoffFile := filepath.Join(architectDir, "handoff_requested")
	if _, err := os.Stat(architectHandoffFile); err == nil {
		t.Error("architect handoff_requested should not exist (parse error)")
	}

	// Cancel and verify clean exit
	cancel()
	select {
	case <-done:
		// Loop exited cleanly
	case <-time.After(1 * time.Second):
		t.Error("paneMonitorLoop did not exit after context cancellation")
	}
}
