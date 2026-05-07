package data

import (
	"context"
	"os/exec"
	"time"
)

// Timeout tiers for external command execution.
const (
	// timeoutShort is for quick local probes such as git config.
	timeoutShort = 5 * time.Second
)

// runWithTimeout executes a command with a context timeout and returns its stdout.
func runWithTimeout(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Output()
}
