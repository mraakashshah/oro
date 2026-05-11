package dispatcher

import "context"

// CommandRunner abstracts command execution for testability.
// Production implementation uses os/exec; tests provide a mock.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// InputCommandRunner extends CommandRunner for commands that need stdin.
type InputCommandRunner interface {
	CommandRunner
	RunWithInput(ctx context.Context, input, name string, args ...string) ([]byte, error)
}
