package dispatcher

import "context"

// CommandRunner abstracts command execution for testability.
// Production implementation uses os/exec; tests provide a mock.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}
