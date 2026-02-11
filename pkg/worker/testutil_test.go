package worker_test

import (
	"testing"
	"time"
)

// waitFor polls condition every tick until it returns true or timeout expires.
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

// justWait waits for the full duration without checking any condition.
// Use this when you need to wait for time to pass (e.g., for goroutines to start).
func justWait(timeout time.Duration) {
	time.Sleep(timeout)
}
