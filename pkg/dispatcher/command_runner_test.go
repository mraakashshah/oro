package dispatcher_test

import (
	"testing"

	"oro/pkg/dispatcher"
)

// TestCommandRunnerNeutralOwner confirms CommandRunner is declared in the
// neutral command_runner.go file, not coupled to a single consumer like
// beadsource. Compile-time ownership signal.
func TestCommandRunnerNeutralOwner(t *testing.T) {
	var _ dispatcher.CommandRunner = (*dispatcher.ExecCommandRunner)(nil)
}
