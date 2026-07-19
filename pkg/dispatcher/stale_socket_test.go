package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"oro/pkg/testutil/qgserial"
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
	qgserial.RequireSerial(t)
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
	qgserial.RequireSerial(t)
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

	// Wait for the first dispatcher to stop accepting connections.
	waitFor(t, func() bool {
		conn, err := net.Dial("unix", sockPath) //nolint:noctx // test setup
		if err != nil {
			return true // connection refused means dispatcher stopped
		}
		_ = conn.Close()
		return false
	}, 2*time.Second)

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
	qgserial.RequireSerial(t)
	// If a dispatcher is already running, a second one must NOT start on the
	// same socket. cleanStaleSocket should detect the active socket and Run()
	// should return an error.
	d1, _, _, _, _, _ := newTestDispatcher(t)
	sockPath := d1.cfg.SocketPath
	_ = startDispatcher(t, d1)

	// Wait for listener to be ready.
	waitFor(t, func() bool {
		conn, err := net.Dial("unix", sockPath) //nolint:noctx // test setup
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}, 2*time.Second)

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

// TestStaleSocketCleanup_StatErrorReturnsError tests that a non-IsNotExist
// stat error is propagated back to the caller.
// This kills mutant 1 (removal of `return fmt.Errorf("stat socket...")`)
// which would silently ignore the stat error and fall through to dial.
func TestStaleSocketCleanup_StatErrorReturnsError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission tests are not meaningful when running as root")
	}

	// Create a directory with no-search permission. stat() on any path inside
	// it returns EACCES (permission denied), which is NOT os.IsNotExist.
	parentDir := t.TempDir()
	lockedDir := filepath.Join(parentDir, "locked")
	if err := os.Mkdir(lockedDir, 0o700); err != nil {
		t.Fatalf("mkdir locked dir: %v", err)
	}

	// Create a placeholder file inside the locked dir before sealing it.
	sockPath := filepath.Join(lockedDir, "test.sock")
	if err := os.WriteFile(sockPath, nil, 0o600); err != nil {
		t.Fatalf("create placeholder: %v", err)
	}

	// Seal the directory: no read, write, or execute bits.
	if err := os.Chmod(lockedDir, 0o000); err != nil {
		t.Fatalf("chmod locked dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(lockedDir, 0o700) }) //nolint:gosec // restoring dir permissions in cleanup

	// cleanStaleSocket should propagate the stat error, not return nil.
	err := cleanStaleSocket(sockPath)
	if err == nil {
		t.Fatal("expected error from stat on inaccessible path, got nil")
	}

	// The error must mention "stat socket" to confirm it comes from the stat
	// branch, not some later stage.
	if !containsSubstr(err.Error(), "stat socket") {
		t.Fatalf("expected error to mention 'stat socket', got: %v", err)
	}
}

// TestStaleSocketCleanup_RemoveErrorReturnsError tests that a failure in
// os.Remove is propagated back as an error.
// This kills mutant 3 (removal of `return fmt.Errorf("remove stale socket...")`)
// which would silently swallow the Remove error and return nil.
func TestStaleSocketCleanup_RemoveErrorReturnsError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission tests are not meaningful when running as root")
	}

	// Create the stale socket file inside a directory we will write-protect.
	parentDir := t.TempDir()
	sockPath := filepath.Join(parentDir, "stale.sock")
	if err := os.WriteFile(sockPath, nil, 0o600); err != nil {
		t.Fatalf("create stale socket: %v", err)
	}

	// Remove write permission from the parent directory so os.Remove(sockPath)
	// will fail with EACCES rather than succeeding.
	if err := os.Chmod(parentDir, 0o500); err != nil { //nolint:gosec // intentionally restricting dir write permission for test
		t.Fatalf("chmod parent dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parentDir, 0o700) }) //nolint:gosec // restoring dir permissions in cleanup

	// Nobody is listening on the socket, so the dial attempt will fail and
	// cleanStaleSocket will attempt os.Remove — which should fail.
	err := cleanStaleSocket(sockPath)
	if err == nil {
		t.Fatal("expected error when os.Remove fails, got nil")
	}

	// The error must mention "remove stale socket".
	if !containsSubstr(err.Error(), "remove stale socket") {
		t.Fatalf("expected error to mention 'remove stale socket', got: %v", err)
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
