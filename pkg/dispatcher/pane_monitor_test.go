package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
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

// newPaneTestDispatcher creates a minimal Dispatcher for unit tests of pane monitor functions.
func newPaneTestDispatcher(t *testing.T, threshold int, panesDir string) *Dispatcher {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("failed to init schema: %v", err)
	}
	cfg := Config{
		PaneContextThreshold: threshold,
		PaneMonitorInterval:  100 * time.Millisecond,
	}
	cfg = cfg.withDefaults()
	return &Dispatcher{
		cfg:           cfg,
		db:            db,
		panesDir:      panesDir,
		nowFunc:       time.Now,
		signaledPanes: make(map[string]bool),
		paneStates:    make(map[string]*paneState),
	}
}

// TestCheckPaneContext_ExactThreshold kills mutant .go.6 (>= vs >):
// pct == threshold must trigger signalHandoff (>= is correct, > would miss this case).
func TestCheckPaneContext_ExactThreshold(t *testing.T) {
	tmpDir := t.TempDir()
	panesDir := filepath.Join(tmpDir, "panes")
	roleDir := filepath.Join(panesDir, "architect")
	if err := os.MkdirAll(roleDir, 0o755); err != nil { //nolint:gosec
		t.Fatalf("mkdir: %v", err)
	}

	const threshold = 60
	pctFile := filepath.Join(roleDir, "context_pct")
	if err := os.WriteFile(pctFile, []byte(strconv.Itoa(threshold)), 0o644); err != nil { //nolint:gosec
		t.Fatalf("write pct: %v", err)
	}

	d := newPaneTestDispatcher(t, threshold, panesDir)
	d.checkPaneContext(context.Background(), "architect")

	handoffFile := filepath.Join(roleDir, "handoff_requested")
	if _, err := os.Stat(handoffFile); os.IsNotExist(err) {
		t.Error("handoff_requested must be created when pct == threshold (>= not >)")
	}
}

// TestCheckPaneContext_BelowThreshold ensures no signal when pct < threshold.
func TestCheckPaneContext_BelowThreshold(t *testing.T) {
	tmpDir := t.TempDir()
	panesDir := filepath.Join(tmpDir, "panes")
	roleDir := filepath.Join(panesDir, "architect")
	if err := os.MkdirAll(roleDir, 0o755); err != nil { //nolint:gosec
		t.Fatalf("mkdir: %v", err)
	}

	const threshold = 60
	pctFile := filepath.Join(roleDir, "context_pct")
	if err := os.WriteFile(pctFile, []byte("59"), 0o644); err != nil { //nolint:gosec
		t.Fatalf("write pct: %v", err)
	}

	d := newPaneTestDispatcher(t, threshold, panesDir)
	d.checkPaneContext(context.Background(), "architect")

	handoffFile := filepath.Join(roleDir, "handoff_requested")
	if _, err := os.Stat(handoffFile); err == nil {
		t.Error("handoff_requested must NOT be created when pct < threshold")
	}
}

// TestCheckPaneContext_AlreadySignaled kills mutant .go.1 (remove return from alreadySignaled guard):
// once a pane is signaled, subsequent calls must not touch the handoff file again.
func TestCheckPaneContext_AlreadySignaled(t *testing.T) {
	tmpDir := t.TempDir()
	panesDir := filepath.Join(tmpDir, "panes")
	roleDir := filepath.Join(panesDir, "architect")
	if err := os.MkdirAll(roleDir, 0o755); err != nil { //nolint:gosec
		t.Fatalf("mkdir: %v", err)
	}

	const threshold = 60
	pctFile := filepath.Join(roleDir, "context_pct")
	if err := os.WriteFile(pctFile, []byte("80"), 0o644); err != nil { //nolint:gosec
		t.Fatalf("write pct: %v", err)
	}

	d := newPaneTestDispatcher(t, threshold, panesDir)

	// First call: should signal and set signaledPanes.
	d.checkPaneContext(context.Background(), "architect")

	handoffFile := filepath.Join(roleDir, "handoff_requested")
	if _, err := os.Stat(handoffFile); os.IsNotExist(err) {
		t.Fatal("handoff_requested must be created on first call")
	}

	// Verify signaledPanes is set.
	d.mu.Lock()
	if !d.signaledPanes["architect"] {
		t.Error("signaledPanes[architect] must be true after first signal")
	}
	d.mu.Unlock()

	// Remove handoff file, then call again — should NOT re-create it.
	if err := os.Remove(handoffFile); err != nil {
		t.Fatalf("remove handoff: %v", err)
	}

	d.checkPaneContext(context.Background(), "architect")

	// File must still be absent (early return via alreadySignaled guard).
	if _, err := os.Stat(handoffFile); err == nil {
		t.Error("handoff_requested must not be re-created for already-signaled pane")
	}
}

// TestSignalHandoff_SetsSignaledPanes kills mutant .go.14 (signaledPanes[role]=true removed):
// signalHandoff must mark the pane in signaledPanes after writing the file.
func TestSignalHandoff_SetsSignaledPanes(t *testing.T) {
	tmpDir := t.TempDir()
	panesDir := filepath.Join(tmpDir, "panes")
	roleDir := filepath.Join(panesDir, "manager")
	if err := os.MkdirAll(roleDir, 0o755); err != nil { //nolint:gosec
		t.Fatalf("mkdir: %v", err)
	}

	d := newPaneTestDispatcher(t, 60, panesDir)
	d.signalHandoff(context.Background(), "manager", roleDir, 75)

	handoffFile := filepath.Join(roleDir, "handoff_requested")
	if _, err := os.Stat(handoffFile); os.IsNotExist(err) {
		t.Error("handoff_requested must be created by signalHandoff")
	}

	d.mu.Lock()
	signaled := d.signaledPanes["manager"]
	d.mu.Unlock()
	if !signaled {
		t.Error("signaledPanes[manager] must be true after signalHandoff")
	}
}

// TestSignalHandoff_DeduplicatesOnLoop kills mutant .go.14 via the full loop path:
// after signaling once, subsequent polls must not re-create the handoff file.
func TestSignalHandoff_DeduplicatesOnLoop(t *testing.T) {
	tmpDir := t.TempDir()
	panesDir := filepath.Join(tmpDir, "panes")
	roleDir := filepath.Join(panesDir, "architect")
	if err := os.MkdirAll(roleDir, 0o755); err != nil { //nolint:gosec
		t.Fatalf("mkdir: %v", err)
	}

	const threshold = 60
	pctFile := filepath.Join(roleDir, "context_pct")
	if err := os.WriteFile(pctFile, []byte("75"), 0o644); err != nil { //nolint:gosec
		t.Fatalf("write pct: %v", err)
	}

	d := newPaneTestDispatcher(t, threshold, panesDir)
	awaitPolls := pollCounter(d)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		d.paneMonitorLoop(ctx)
		close(done)
	}()

	handoffFile := filepath.Join(roleDir, "handoff_requested")
	waitFor(t, func() bool {
		_, err := os.Stat(handoffFile)
		return err == nil
	}, 2*time.Second)

	if err := os.Remove(handoffFile); err != nil {
		t.Fatalf("remove: %v", err)
	}

	afterRemove := awaitPolls(2)
	waitFor(t, afterRemove, 2*time.Second)

	if _, err := os.Stat(handoffFile); err == nil {
		t.Error("handoff_requested must not be re-created for already-signaled pane")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("loop did not exit")
	}
}

// TestCheckPaneContext_SignalsHandoffAboveThreshold kills mutant .go.4 (signalHandoff not called):
// directly verifies that checkPaneContext calls signalHandoff when pct > threshold.
func TestCheckPaneContext_SignalsHandoffAboveThreshold(t *testing.T) {
	tmpDir := t.TempDir()
	panesDir := filepath.Join(tmpDir, "panes")
	roleDir := filepath.Join(panesDir, "manager")
	if err := os.MkdirAll(roleDir, 0o755); err != nil { //nolint:gosec
		t.Fatalf("mkdir: %v", err)
	}

	pctFile := filepath.Join(roleDir, "context_pct")
	if err := os.WriteFile(pctFile, []byte("90"), 0o644); err != nil { //nolint:gosec
		t.Fatalf("write pct: %v", err)
	}

	d := newPaneTestDispatcher(t, 60, panesDir)
	d.checkPaneContext(context.Background(), "manager")

	handoffFile := filepath.Join(roleDir, "handoff_requested")
	if _, err := os.Stat(handoffFile); os.IsNotExist(err) {
		t.Error("handoff_requested must be created when pct > threshold")
	}

	d.mu.Lock()
	signaled := d.signaledPanes["manager"]
	d.mu.Unlock()
	if !signaled {
		t.Error("signaledPanes[manager] must be set after checkPaneContext triggers handoff")
	}
}

// --- Restart tests ---

// mockPaneRestarter records Restart calls for assertion in tests.
type mockPaneRestarter struct {
	mu    sync.Mutex
	calls []string
}

func (m *mockPaneRestarter) Restart(role string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, role)
	return nil
}

func (m *mockPaneRestarter) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *mockPaneRestarter) firstCall() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return ""
	}
	return m.calls[0]
}

// newPaneRestartTestDispatcher creates a Dispatcher with restart-capable config for tests.
func newPaneRestartTestDispatcher(t *testing.T, panesDir string, restarter PaneRestarter) *Dispatcher {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("failed to init schema: %v", err)
	}
	cfg := Config{
		PaneContextThreshold:  60,
		PaneMonitorInterval:   50 * time.Millisecond,
		PaneRestartCooldown:   5 * time.Minute,
		PaneInactivityTimeout: 10 * time.Minute,
	}
	cfg = cfg.withDefaults()
	return &Dispatcher{
		cfg:           cfg,
		db:            db,
		panesDir:      panesDir,
		nowFunc:       time.Now,
		signaledPanes: make(map[string]bool),
		paneStates:    make(map[string]*paneState),
		paneRestarter: restarter,
	}
}

// TestPaneMonitor_RestartOnThreshold verifies that when manager context_pct exceeds
// the threshold and a PaneRestarter is wired up, the manager pane is restarted
// instead of writing a handoff_requested file.
func TestPaneMonitor_RestartOnThreshold(t *testing.T) {
	tmpDir := t.TempDir()
	panesDir := filepath.Join(tmpDir, "panes")
	managerDir := filepath.Join(panesDir, "manager")
	if err := os.MkdirAll(managerDir, 0o755); err != nil { //nolint:gosec
		t.Fatalf("mkdir: %v", err)
	}

	pctFile := filepath.Join(managerDir, "context_pct")
	if err := os.WriteFile(pctFile, []byte("80"), 0o644); err != nil { //nolint:gosec
		t.Fatalf("write pct: %v", err)
	}

	restarter := &mockPaneRestarter{}
	d := newPaneRestartTestDispatcher(t, panesDir, restarter)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		d.paneMonitorLoop(ctx)
		close(done)
	}()

	// Wait for at least one restart call.
	waitFor(t, func() bool { return restarter.callCount() > 0 }, 2*time.Second)

	if restarter.callCount() == 0 {
		t.Fatal("expected Restart to be called for manager pane on threshold")
	}
	if restarter.firstCall() != "manager" {
		t.Errorf("Restart called with %q, want manager", restarter.firstCall())
	}

	// Verify handoff file NOT created (restart path replaces signalHandoff).
	managerHandoff := filepath.Join(managerDir, "handoff_requested")
	if _, err := os.Stat(managerHandoff); err == nil {
		t.Error("manager handoff_requested should not be created (restart path, not signalHandoff)")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("loop did not exit")
	}
}

// TestPaneMonitor_RestartOnInactivity verifies that when manager context_pct has
// not been updated for longer than PaneInactivityTimeout, the pane is restarted
// even if the context percentage is below the handoff threshold.
func TestPaneMonitor_RestartOnInactivity(t *testing.T) {
	tmpDir := t.TempDir()
	panesDir := filepath.Join(tmpDir, "panes")
	managerDir := filepath.Join(panesDir, "manager")
	if err := os.MkdirAll(managerDir, 0o755); err != nil { //nolint:gosec
		t.Fatalf("mkdir: %v", err)
	}

	// Write context_pct BELOW threshold so only inactivity triggers restart.
	pctFile := filepath.Join(managerDir, "context_pct")
	if err := os.WriteFile(pctFile, []byte("10"), 0o644); err != nil { //nolint:gosec
		t.Fatalf("write pct: %v", err)
	}

	restarter := &mockPaneRestarter{}
	d := newPaneRestartTestDispatcher(t, panesDir, restarter)

	// Simulate the file being older than the inactivity timeout by advancing nowFunc.
	const inactivityTimeout = 10 * time.Minute
	fakeNow := time.Now().Add(inactivityTimeout + time.Second)
	d.nowFunc = func() time.Time { return fakeNow }
	d.cfg.PaneInactivityTimeout = inactivityTimeout

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		d.paneMonitorLoop(ctx)
		close(done)
	}()

	// Wait for restart to be triggered by inactivity.
	waitFor(t, func() bool { return restarter.callCount() > 0 }, 2*time.Second)

	if restarter.callCount() == 0 {
		t.Fatal("expected Restart to be called for manager pane on inactivity")
	}
	if restarter.firstCall() != "manager" {
		t.Errorf("Restart called with %q, want manager", restarter.firstCall())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("loop did not exit")
	}
}

// restartingFileRestarter checks that the restarting sentinel file exists on disk
// when Restart is called, and records whether it was found.
type restartingFileRestarter struct {
	mu             sync.Mutex
	calls          []string
	fileExisted    bool // true if restarting file existed during Restart()
	restartingPath string
}

func (r *restartingFileRestarter) Restart(role string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, role)
	_, err := os.Stat(r.restartingPath)
	r.fileExisted = err == nil
	return nil
}

func (r *restartingFileRestarter) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// TestCheckManagerPane_WritesRestartingFile verifies that checkManagerPane writes
// a "restarting" sentinel file before calling Restart() and removes it afterward.
// This prevents double-respawn: the pane-died hook checks for this file to
// determine whether a restart is already in progress.
func TestCheckManagerPane_WritesRestartingFile(t *testing.T) {
	tmpDir := t.TempDir()
	panesDir := filepath.Join(tmpDir, "panes")
	managerDir := filepath.Join(panesDir, "manager")
	if err := os.MkdirAll(managerDir, 0o755); err != nil { //nolint:gosec
		t.Fatalf("mkdir: %v", err)
	}

	// Write context_pct above threshold to trigger restart.
	pctFile := filepath.Join(managerDir, "context_pct")
	if err := os.WriteFile(pctFile, []byte("80"), 0o644); err != nil { //nolint:gosec
		t.Fatalf("write pct: %v", err)
	}

	restartingFile := filepath.Join(managerDir, "restarting")
	restarter := &restartingFileRestarter{restartingPath: restartingFile}

	d := newPaneRestartTestDispatcher(t, panesDir, restarter)

	// Call checkManagerPane directly (synchronous, no loop needed).
	d.checkManagerPane(context.Background())

	// Restart must have been called.
	if restarter.callCount() != 1 {
		t.Fatalf("expected 1 Restart call, got %d", restarter.callCount())
	}

	// The restarting file must have existed on disk during Restart().
	restarter.mu.Lock()
	existed := restarter.fileExisted
	restarter.mu.Unlock()
	if !existed {
		t.Error("restarting sentinel file must exist on disk when Restart() is called")
	}

	// After checkManagerPane returns, the restarting file must be cleaned up.
	if _, err := os.Stat(restartingFile); err == nil {
		t.Error("restarting sentinel file must be removed after checkManagerPane returns")
	}
}

// TestPaneMonitor_CooldownPreventsRestart verifies that once the manager pane has
// been restarted, subsequent polls within PaneRestartCooldown do not trigger
// additional restarts.
func TestPaneMonitor_CooldownPreventsRestart(t *testing.T) {
	tmpDir := t.TempDir()
	panesDir := filepath.Join(tmpDir, "panes")
	managerDir := filepath.Join(panesDir, "manager")
	if err := os.MkdirAll(managerDir, 0o755); err != nil { //nolint:gosec
		t.Fatalf("mkdir: %v", err)
	}

	// context_pct above threshold so every poll would restart without cooldown.
	pctFile := filepath.Join(managerDir, "context_pct")
	if err := os.WriteFile(pctFile, []byte("80"), 0o644); err != nil { //nolint:gosec
		t.Fatalf("write pct: %v", err)
	}

	restarter := &mockPaneRestarter{}
	d := newPaneRestartTestDispatcher(t, panesDir, restarter)
	d.cfg.PaneRestartCooldown = 5 * time.Minute // long cooldown prevents second restart

	awaitPolls := pollCounter(d)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		d.paneMonitorLoop(ctx)
		close(done)
	}()

	// Wait for the first restart.
	waitFor(t, func() bool { return restarter.callCount() > 0 }, 2*time.Second)

	// Wait for 3 more polls to allow a potential second restart.
	afterFirst := awaitPolls(3)
	waitFor(t, afterFirst, 2*time.Second)

	// Cooldown must prevent any additional restarts.
	if count := restarter.callCount(); count != 1 {
		t.Errorf("expected exactly 1 Restart call (cooldown active), got %d", count)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("loop did not exit")
	}
}

// failingMockPaneRestarter always returns an error from Restart.
type failingMockPaneRestarter struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (m *failingMockPaneRestarter) Restart(role string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, role)
	return m.err
}

func (m *failingMockPaneRestarter) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// TestCheckManagerPane_NoUpdateOnFailure verifies that when Restart() returns an
// error, lastRestartAt and restartCount are NOT updated. This ensures a failed
// restart does not burn the cooldown window, allowing the next poll to retry.
func TestCheckManagerPane_NoUpdateOnFailure(t *testing.T) {
	tmpDir := t.TempDir()
	panesDir := filepath.Join(tmpDir, "panes")
	managerDir := filepath.Join(panesDir, "manager")
	if err := os.MkdirAll(managerDir, 0o755); err != nil { //nolint:gosec
		t.Fatalf("mkdir: %v", err)
	}

	// context_pct above threshold to trigger restart.
	pctFile := filepath.Join(managerDir, "context_pct")
	if err := os.WriteFile(pctFile, []byte("80"), 0o644); err != nil { //nolint:gosec
		t.Fatalf("write pct: %v", err)
	}

	restarter := &failingMockPaneRestarter{err: errors.New("tmux not found")}
	d := newPaneRestartTestDispatcher(t, panesDir, restarter)

	// Snapshot state before calling checkManagerPane.
	beforeTime := time.Time{} // zero value
	beforeCount := 0

	d.checkManagerPane(context.Background())

	// Restart must have been attempted.
	if restarter.callCount() != 1 {
		t.Fatalf("expected 1 Restart call, got %d", restarter.callCount())
	}

	// Cooldown state must be unchanged because Restart returned an error.
	d.mu.Lock()
	state := d.paneStates["manager"]
	d.mu.Unlock()

	if state == nil {
		t.Fatal("paneStates[manager] should exist after checkManagerPane")
	}

	if state.lastRestartAt != beforeTime {
		t.Errorf("lastRestartAt should remain zero after failed restart, got %v", state.lastRestartAt)
	}
	if state.restartCount != beforeCount {
		t.Errorf("restartCount should remain %d after failed restart, got %d", beforeCount, state.restartCount)
	}

	// Verify restarting flag is cleared (so next poll can retry).
	if state.restarting {
		t.Error("restarting flag should be cleared after failed restart")
	}
}
