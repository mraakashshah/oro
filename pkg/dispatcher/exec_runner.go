package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ExecCommandRunner implements CommandRunner using os/exec.
type ExecCommandRunner struct{}

// Run executes a command and returns its stdout as bytes.
func (r *ExecCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if ok := errors.As(err, &exitErr); ok {
			return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, exitErr.Stderr)
		}
		return nil, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return out, nil
}
