package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"testing"
	"time"
)

// waitFor polls condition every tick until it returns true or timeout expires.
// This replaces time.Sleep in tests to provide proper synchronization.
func waitFor(t *testing.T, condition func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond) // short poll inside helper is OK
	}
	t.Fatalf("waitFor: condition not met within %v", timeout)
}
