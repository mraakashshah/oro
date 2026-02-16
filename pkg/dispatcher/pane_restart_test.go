package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"oro/pkg/protocol"

	_ "modernc.org/sqlite"
)

// mockTmuxSession tracks Kill and RespawnPane calls for testing
type mockTmuxSession struct {
	mu           sync.Mutex
	killCalls    []string
	respawnCalls []respawnCall
	killErr      error
	respawnErr   error
}

type respawnCall struct {
	paneTarget string
	command    string
}

func (m *mockTmuxSession) KillPane(paneTarget string) error {
	m.mu.Lock()
	m.killCalls = append(m.killCalls, paneTarget)
	err := m.killErr
	m.mu.Unlock()
	return err
}

func (m *mockTmuxSession) RespawnPane(paneTarget, command string) error {
	m.mu.Lock()
	m.respawnCalls = append(m.respawnCalls, respawnCall{paneTarget, command})
	err := m.respawnErr
	m.mu.Unlock()
	return err
}

func TestPaneRestart_OnHandoffComplete(t *testing.T) {
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

	mockSession := &mockTmuxSession{}
	d := &Dispatcher{
		cfg:           cfg,
		db:            db,
		panesDir:      panesDir,
		nowFunc:       time.Now,
		signaledPanes: make(map[string]bool),
		tmuxSession:   mockSession,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Simulate handoff flow for architect:
	// 1. Create context_pct above threshold
	architectPctFile := filepath.Join(architectDir, "context_pct")
	//nolint:gosec // test file permissions
	if err := os.WriteFile(architectPctFile, []byte("65"), 0o644); err != nil {
		t.Fatalf("failed to write architect context_pct: %v", err)
	}

	// 2. Start monitor loop
	done := make(chan struct{})
	go func() {
		d.paneMonitorLoop(ctx)
		close(done)
	}()

	// 3. Wait for handoff_requested to be created
	architectHandoffReq := filepath.Join(architectDir, "handoff_requested")
	timeout := time.After(2 * time.Second)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()

waitForHandoffReq:
	for {
		select {
		case <-timeout:
			t.Fatal("handoff_requested was not created")
		case <-tick.C:
			if _, err := os.Stat(architectHandoffReq); err == nil {
				break waitForHandoffReq
			}
		}
	}

	// 4. Simulate pane creating handoff_complete signal
	architectHandoffComplete := filepath.Join(architectDir, "handoff_complete")
	//nolint:gosec // test file permissions
	if err := os.WriteFile(architectHandoffComplete, []byte{}, 0o644); err != nil {
		t.Fatalf("failed to write handoff_complete: %v", err)
	}

	// 5. Wait for restart to be triggered
	timeout2 := time.After(2 * time.Second)
	tick2 := time.NewTicker(50 * time.Millisecond)
	defer tick2.Stop()

waitForRestart:
	for {
		select {
		case <-timeout2:
			t.Fatal("pane restart was not triggered")
		case <-tick2.C:
			mockSession.mu.Lock()
			hasKills := len(mockSession.killCalls) > 0
			hasRespawns := len(mockSession.respawnCalls) > 0
			mockSession.mu.Unlock()
			if hasKills || hasRespawns {
				break waitForRestart
			}
		}
	}

	// 6. Verify signal files were deleted
	if _, err := os.Stat(architectHandoffReq); err == nil {
		t.Error("handoff_requested should be deleted after restart")
	}
	if _, err := os.Stat(architectHandoffComplete); err == nil {
		t.Error("handoff_complete should be deleted after restart")
	}
	if _, err := os.Stat(architectPctFile); err == nil {
		t.Error("context_pct should be deleted after restart")
	}

	// 7. Verify tmux operations were called
	mockSession.mu.Lock()
	killCount := len(mockSession.killCalls)
	var killTarget string
	if killCount > 0 {
		killTarget = mockSession.killCalls[0]
	}
	respawnCount := len(mockSession.respawnCalls)
	var respawnTarget, respawnCommand string
	if respawnCount > 0 {
		respawnTarget = mockSession.respawnCalls[0].paneTarget
		respawnCommand = mockSession.respawnCalls[0].command
	}
	mockSession.mu.Unlock()

	if killCount != 1 {
		t.Errorf("expected 1 kill call, got %d", killCount)
	} else if killTarget != "oro:architect" {
		t.Errorf("expected kill call for oro:architect, got %s", killTarget)
	}

	if respawnCount != 1 {
		t.Errorf("expected 1 respawn call, got %d", respawnCount)
	} else {
		if respawnTarget != "oro:architect" {
			t.Errorf("expected respawn call for oro:architect, got %s", respawnTarget)
		}
		// Verify command contains expected elements
		if respawnCommand == "" {
			t.Error("respawn command should not be empty")
		}
	}

	// 8. Verify signaledPanes was reset for architect
	d.mu.Lock()
	if d.signaledPanes["architect"] {
		t.Error("architect should be removed from signaledPanes after restart")
	}
	d.mu.Unlock()

	// Cancel context and wait for loop to exit
	cancel()
	select {
	case <-done:
		// Loop exited cleanly
	case <-time.After(2 * time.Second):
		t.Error("paneMonitorLoop did not exit after context cancellation")
	}
}

func TestPaneRestart_KillFailsButRelaunchSucceeds(t *testing.T) {
	// Create temporary test directory
	tmpDir := t.TempDir()
	panesDir := filepath.Join(tmpDir, ".oro", "panes")
	architectDir := filepath.Join(panesDir, "architect")

	//nolint:gosec // test directory permissions
	if err := os.MkdirAll(architectDir, 0o755); err != nil {
		t.Fatalf("failed to create architect dir: %v", err)
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
		PaneMonitorInterval:  100 * time.Millisecond,
	}
	cfg = cfg.withDefaults()

	// Mock session that fails kill but succeeds respawn
	mockSession := &mockTmuxSession{
		killErr: os.ErrNotExist, // Simulate pane already dead
	}
	d := &Dispatcher{
		cfg:           cfg,
		db:            db,
		panesDir:      panesDir,
		nowFunc:       time.Now,
		signaledPanes: make(map[string]bool),
		tmuxSession:   mockSession,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create handoff_complete signal directly (skip handoff_requested flow)
	architectHandoffComplete := filepath.Join(architectDir, "handoff_complete")
	//nolint:gosec // test file permissions
	if err := os.WriteFile(architectHandoffComplete, []byte{}, 0o644); err != nil {
		t.Fatalf("failed to write handoff_complete: %v", err)
	}

	// Start monitor loop
	done := make(chan struct{})
	go func() {
		d.paneMonitorLoop(ctx)
		close(done)
	}()

	// Wait for restart to be triggered
	timeout := time.After(2 * time.Second)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()

waitForRestart:
	for {
		select {
		case <-timeout:
			t.Fatal("pane restart was not triggered")
		case <-tick.C:
			mockSession.mu.Lock()
			hasRespawns := len(mockSession.respawnCalls) > 0
			mockSession.mu.Unlock()
			if hasRespawns {
				break waitForRestart
			}
		}
	}

	// Verify respawn was attempted despite kill failure
	mockSession.mu.Lock()
	respawnCount := len(mockSession.respawnCalls)
	mockSession.mu.Unlock()

	if respawnCount != 1 {
		t.Errorf("expected 1 respawn call, got %d", respawnCount)
	}

	// Cancel and cleanup
	cancel()
	<-done
}
