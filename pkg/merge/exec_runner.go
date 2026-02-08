package merge

import (
	"context"
	"os/exec"
	"strings"
)

// ExecGitRunner implements GitRunner using os/exec.
type ExecGitRunner struct{}

// Run executes a git command in the given directory and returns stdout and stderr.
func (r *ExecGitRunner) Run(ctx context.Context, dir string, args ...string) (stdout, stderr string, err error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir

	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err = cmd.Run()
	return stdoutBuf.String(), stderrBuf.String(), err
}
