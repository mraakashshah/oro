package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"oro/pkg/processenv"
)

// ExecCommandRunner implements CommandRunner using os/exec.
// Dir, if non-empty, is set as cmd.Dir so the command runs from that directory.
type ExecCommandRunner struct {
	Dir string
}

// Run executes a command and returns its stdout as bytes.
func (r *ExecCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if r.Dir != "" {
		cmd.Dir = r.Dir
	}
	cmd.Env = processenv.ForWorkdir(os.Environ(), r.Dir)
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
