package dispatcher

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"oro/pkg/processenv"
)

// AcceptanceRunner executes an epic's acceptance test command and reports
// whether it passed.
type AcceptanceRunner interface {
	Run(ctx context.Context, cmd string) (output string, passed bool, err error)
}

// ShellAcceptanceRunner runs the acceptance command through sh -c and reports
// pass when the process exits with code 0.
type ShellAcceptanceRunner struct{}

// Run executes cmd via sh -c and returns the combined output. passed is true
// when the process exits with code 0.
func (r *ShellAcceptanceRunner) Run(ctx context.Context, cmd string) (output string, passed bool, err error) {
	c := exec.CommandContext(ctx, "sh", "-c", cmd) //nolint:gosec // cmd is a user-defined acceptance test string
	out, runErr := c.CombinedOutput()
	output = string(out)
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return output, false, nil
		}
		return output, false, fmt.Errorf("running acceptance test: %w", runErr)
	}
	return output, true, nil
}

// QGRunner executes the quality gate script in a worktree and reports
// whether it passed. skipMutation true means mutation testing is skipped;
// false means the caller explicitly opted into mutation testing.
type QGRunner interface {
	Run(ctx context.Context, worktree string, skipMutation bool, mutationBase string) (passed bool, output string, err error)
}

// ShellQGRunner runs quality_gate.sh inside the worktree via bash. It looks
// for scripts/quality_gate.sh first, then quality_gate.sh at the repo root.
// It returns (true, output, nil) on exit 0, (false, output, nil) on non-zero
// exit, and (false, "", err) if the script cannot be found or launched.
type ShellQGRunner struct{}

// Run implements QGRunner using the same logic as worker.RunQualityGate but
// self-contained in the dispatcher package to avoid an import cycle.
func (r *ShellQGRunner) Run(ctx context.Context, worktree string, skipMutation bool, mutationBase string) (passed bool, output string, err error) {
	candidates := []string{
		filepath.Join(worktree, "scripts", "quality_gate.sh"),
		filepath.Join(worktree, "quality_gate.sh"),
	}
	scriptPath := ""
	for _, p := range candidates {
		if _, statErr := os.Stat(p); statErr == nil {
			scriptPath = p
			break
		}
	}
	if scriptPath == "" {
		return false, "", fmt.Errorf("quality gate script not found in scripts/quality_gate.sh or quality_gate.sh")
	}
	if output, conflictErr := qualityGateConflictMarkerOutput(scriptPath); conflictErr != nil {
		return false, "", conflictErr
	} else if output != "" {
		return false, output, nil
	}

	args := []string{scriptPath}
	if !skipMutation {
		args = append(args, "--mutation-testing")
	}
	cmd := exec.CommandContext(ctx, "bash", args...) //nolint:gosec // script path constructed from worktree, not user input
	cmd.Dir = worktree
	cmd.Env = qgRunnerEnv(skipMutation, worktree, mutationBase)
	out, runErr := cmd.CombinedOutput()
	output = string(out)
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return false, output, nil
		}
		return false, output, fmt.Errorf("run quality gate: %w", runErr)
	}
	return true, output, nil
}

func qualityGateConflictMarkerOutput(scriptPath string) (string, error) {
	file, err := os.Open(scriptPath) //nolint:gosec // path is the selected quality gate script inside a validated worktree.
	if err != nil {
		return "", fmt.Errorf("open quality gate script: %w", err)
	}
	defer func() { _ = file.Close() }()

	var b strings.Builder
	scanner := bufio.NewScanner(file)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if strings.HasPrefix(line, "<<<<<<<") || strings.HasPrefix(line, "=======") || strings.HasPrefix(line, ">>>>>>>") {
			if b.Len() == 0 {
				b.WriteString("FAIL: quality_gate.sh contains unresolved git conflict markers\n")
			}
			b.WriteString(scriptPath)
			b.WriteByte(':')
			b.WriteString(strconv.Itoa(lineNo))
			b.WriteByte(':')
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan quality gate script: %w", err)
	}
	return b.String(), nil
}

func qgRunnerEnv(skipMutation bool, worktree, mutationBase string) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if processenv.StripQualityGateEnv(kv) {
			continue
		}
		env = append(env, kv)
	}
	if skipMutation {
		env = append(env, "ORO_SKIP_MUTATION=1")
	}
	if mutationBase != "" {
		env = append(env, "ORO_MUTATION_BASE="+mutationBase)
	}
	return processenv.ForWorkdir(env, worktree)
}
