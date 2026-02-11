package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// shortSockPath returns a short /tmp socket path safe for macOS (108 char limit).
func shortSockPath(t *testing.T, name string) string {
	t.Helper()
	p := fmt.Sprintf("/tmp/oro-ss-%s-%d.sock", name, time.Now().UnixNano())
	t.Cleanup(func() { _ = os.Remove(p) })
	return p
}

func TestStaleSocketCleanup_RemovesStaleSocket(t *testing.T) {
	sockPath := shortSockPath(t, "stale")

	// Simulate a stale socket left behind after a crash. On macOS,
	// net.Listener.Close() removes the socket file automatically, but a
	// crash (SIGKILL, power loss) won't call Close(), leaving the file.
	// We create the socket file via syscall to mimic this.
	//
	// Use a regular file as the stand-in: the real-world crash artifact is
	// an inode that stat() finds but nobody listens on.
	if err := os.WriteFile(sockPath, nil, 0o600); err != nil {
		t.Fatalf("create stale socket file: %v", err)
	}

	// The socket file exists on disk but nobody is listening.
	if _, err := os.Stat(sockPath); os.IsNotExist(err) {
		t.Fatal("expected stale socket file to exist")
	}

	// cleanStaleSocket should detect it as stale and remove it.
	if err := cleanStaleSocket(sockPath); err != nil {
		t.Fatalf("cleanStaleSocket: %v", err)
	}

	// Socket file should be gone.
	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Fatal("expected stale socket file to be removed")
	}
}

func TestStaleSocketCleanup_RegularFileRemovedAsStale(t *testing.T) {
	// A regular file at the socket path (e.g. leftover from a crash) should
	// be treated as stale and removed so the dispatcher can bind.
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "test.sock")

	if err := os.WriteFile(sockPath, []byte("garbage"), 0o600); err != nil {
		t.Fatalf("create file: %v", err)
	}

	if err := cleanStaleSocket(sockPath); err != nil {
		t.Fatalf("cleanStaleSocket: %v", err)
	}

	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Fatal("expected regular file to be removed")
	}
}

func TestStaleSocketCleanup_NoFileIsNoop(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "nonexistent.sock")

	// No file exists — should succeed silently.
	if err := cleanStaleSocket(sockPath); err != nil {
		t.Fatalf("cleanStaleSocket: %v", err)
	}
}

func TestStaleSocketCleanup_ActiveSocketReturnsError(t *testing.T) {
	sockPath := shortSockPath(t, "active")

	// Start a real listener to simulate an active dispatcher.
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	// cleanStaleSocket should detect the active listener and return an error.
	err = cleanStaleSocket(sockPath)
	if err == nil {
		t.Fatal("expected error for active socket, got nil")
	}

	// Socket file should still exist (we must NOT delete an active socket).
	if _, statErr := os.Stat(sockPath); os.IsNotExist(statErr) {
		t.Fatal("active socket file should not be removed")
	}
}

func TestStaleSocketCleanup_DispatcherRunUsesIt(t *testing.T) {
	// End-to-end: start a dispatcher, stop it (leaving stale socket), then
	// start a second dispatcher on the same socket path — it should succeed.
	d1, _, _, _, _, _ := newTestDispatcher(t)
	sockPath := d1.cfg.SocketPath

	cancel1 := startDispatcher(t, d1)

	// Verify first dispatcher is listening.
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial first dispatcher: %v", err)
	}
	_ = conn.Close()

	// Stop the first dispatcher. This closes the listener but may leave
	// the socket file on disk (which is the actual bug scenario).
	cancel1()
	time.Sleep(100 * time.Millisecond)

	// Force-create a stale socket file to guarantee the scenario.
	// The graceful shutdown may or may not leave one, so we create a
	// deterministic stale file.
	_ = os.Remove(sockPath) // clean slate
	if err := os.WriteFile(sockPath, []byte("stale"), 0o600); err != nil {
		t.Fatalf("create stale file: %v", err)
	}

	// Verify the stale socket file exists.
	if _, err := os.Stat(sockPath); os.IsNotExist(err) {
		t.Fatal("expected stale socket file to exist")
	}

	// Start a second dispatcher on the same socket path.
	d2, _, _, _, _, _ := newTestDispatcher(t)
	d2.cfg.SocketPath = sockPath

	cancel2 := startDispatcher(t, d2)
	defer cancel2()

	// The second dispatcher should be listening successfully.
	conn2, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial second dispatcher (stale socket should have been cleaned): %v", err)
	}
	_ = conn2.Close()
}

func TestStaleSocketCleanup_ActiveDispatcherBlocksSecond(t *testing.T) {
	// If a dispatcher is already running, a second one must NOT start on the
	// same socket. cleanStaleSocket should detect the active socket and Run()
	// should return an error.
	d1, _, _, _, _, _ := newTestDispatcher(t)
	sockPath := d1.cfg.SocketPath
	_ = startDispatcher(t, d1)

	// Wait for listener to be ready.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", sockPath)
		if err == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Create a second dispatcher pointing to the same socket.
	d2, _, _, _, _, _ := newTestDispatcher(t)
	d2.cfg.SocketPath = sockPath

	// Attempt to run the second dispatcher — this should fail because the
	// first dispatcher's socket is active.
	err := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return d2.Run(ctx)
	}()

	if err == nil {
		t.Fatal("expected error when binding to active socket, got nil")
	}
	t.Logf("got expected error: %v", err)

	// Error should mention "another dispatcher".
	errMsg := err.Error()
	if !containsSubstr(errMsg, "another dispatcher") {
		t.Fatalf("error should indicate active socket conflict, got: %s", errMsg)
	}
}

// containsSubstr is a simple substring check helper.
func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
