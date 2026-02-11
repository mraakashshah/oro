package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"os"
	"testing"
	"time"
)

func TestSocketPermissions(t *testing.T) {
	// Create a dispatcher with a test socket path.
	d, _, _, _, _, _ := newTestDispatcher(t)
	sockPath := d.cfg.SocketPath

	// Start the dispatcher, which creates the Unix socket.
	startDispatcher(t, d)

	// Wait briefly for the socket to be created.
	time.Sleep(50 * time.Millisecond)

	// Verify the socket file exists.
	info, err := os.Stat(sockPath)
	if err != nil {
		t.Fatalf("socket file does not exist: %v", err)
	}

	// Check that the socket file has 0600 permissions (owner read/write only).
	mode := info.Mode().Perm()
	expected := os.FileMode(0o600)
	if mode != expected {
		t.Errorf("socket permissions = %#o, want %#o", mode, expected)
	}
}
