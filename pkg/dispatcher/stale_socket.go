package dispatcher

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"
)

// cleanStaleSocket checks whether a UDS socket file at socketPath is stale
// (left over from a previous crash) or actively in use by another dispatcher.
//
// Behavior:
//   - If the file does not exist, returns nil (nothing to clean).
//   - If the file exists and a connection to it succeeds, another dispatcher is
//     running — returns an error so the caller does NOT clobber it.
//   - If the file exists and a connection fails (timeout or refused), the
//     socket is stale — removes the file and returns nil.
func cleanStaleSocket(socketPath string) error {
	_, err := os.Stat(socketPath)
	if os.IsNotExist(err) {
		return nil // No file — nothing to clean.
	}
	if err != nil {
		return fmt.Errorf("stat socket %s: %w", socketPath, err)
	}

	// Try connecting with a short timeout. If it succeeds, someone is
	// listening — another dispatcher is running.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	dialer := net.Dialer{}
	conn, dialErr := dialer.DialContext(ctx, "unix", socketPath)
	if dialErr == nil {
		// Connection succeeded — active dispatcher.
		_ = conn.Close()
		return fmt.Errorf("another dispatcher is already running on %s", socketPath)
	}

	// Connection failed — socket is stale. Remove it.
	if err := os.Remove(socketPath); err != nil {
		return fmt.Errorf("remove stale socket %s: %w", socketPath, err)
	}
	return nil
}
