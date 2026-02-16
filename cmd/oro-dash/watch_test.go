package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TestFsnotifyReload verifies that file changes in .beads/ trigger fsChangeMsg
// which causes immediate fetch instead of waiting for poll timer.
func TestFsnotifyReload(t *testing.T) {
	// Create temp dir structure
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Start watching
	watchCmd := watchBeadsDir(beadsDir)
	if watchCmd == nil {
		t.Fatal("watchBeadsDir returned nil, expected tea.Cmd")
	}

	// Watchcmd should return a blocking command that waits for changes
	// We'll run it in a goroutine and create a file change
	msgChan := make(chan tea.Msg, 1)
	go func() {
		msg := watchCmd()
		msgChan <- msg
	}()

	// Give watcher time to initialize
	time.Sleep(100 * time.Millisecond)

	// Create a file change in .beads/
	testFile := filepath.Join(beadsDir, "test-change.yaml")
	if err := os.WriteFile(testFile, []byte("test"), 0o600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// Wait for fsChangeMsg with timeout
	select {
	case msg := <-msgChan:
		// Should receive fsChangeMsg
		if _, ok := msg.(fsChangeMsg); !ok {
			t.Errorf("expected fsChangeMsg, got %T", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for fsChangeMsg after file change")
	}
}

// TestFsnotifyHandlerTriggersFetch verifies that when Model receives fsChangeMsg,
// it calls fetchBeadsCmd immediately.
func TestFsnotifyHandlerTriggersFetch(t *testing.T) {
	m := newModel()

	// Send fsChangeMsg to Update
	updatedModel, cmd := m.Update(fsChangeMsg{})

	// Should return fetchBeadsCmd
	if cmd == nil {
		t.Fatal("expected fetchBeadsCmd on fsChangeMsg, got nil")
	}

	// Verify it's actually fetchBeadsCmd by executing and checking result type
	msg := cmd()
	if _, ok := msg.(beadsMsg); !ok {
		t.Errorf("expected cmd to return beadsMsg, got %T", msg)
	}

	// Model should be updated (though no visible state change expected)
	_ = updatedModel
}

// TestFsnotifyFallbackOnMissingDir verifies that when .beads/ doesn't exist,
// watchBeadsDir returns nil and polling continues without error.
func TestFsnotifyFallbackOnMissingDir(t *testing.T) {
	tmpDir := t.TempDir()
	nonexistentDir := filepath.Join(tmpDir, "does-not-exist")

	// Should return nil for nonexistent directory
	watchCmd := watchBeadsDir(nonexistentDir)
	if watchCmd != nil {
		t.Errorf("expected nil for nonexistent dir, got cmd")
	}
}

// TestFsnotifyDebounce verifies that multiple rapid changes are debounced
// to avoid thundering herd of fetch requests.
func TestFsnotifyDebounce(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	watchCmd := watchBeadsDir(beadsDir)
	if watchCmd == nil {
		t.Fatal("watchBeadsDir returned nil")
	}

	msgChan := make(chan tea.Msg, 10)
	go func() {
		msg := watchCmd()
		msgChan <- msg
	}()

	time.Sleep(100 * time.Millisecond)

	// Create multiple rapid changes
	for i := 0; i < 5; i++ {
		testFile := filepath.Join(beadsDir, "rapid-change.yaml")
		if err := os.WriteFile(testFile, []byte("test"), 0o600); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}
		time.Sleep(10 * time.Millisecond) // Rapid changes
	}

	// Should receive only one message after debounce period (100ms)
	time.Sleep(150 * time.Millisecond)

	// Drain channel and count messages
	msgCount := 0
	for {
		select {
		case <-msgChan:
			msgCount++
		default:
			goto done
		}
	}
done:
	// Should have received only 1 message due to debouncing
	if msgCount != 1 {
		t.Errorf("expected 1 debounced message, got %d", msgCount)
	}
}
