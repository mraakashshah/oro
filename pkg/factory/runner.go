package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type commandInput struct {
	Objective    string `json:"objective"`
	Attempt      int    `json:"attempt"`
	Worktree     string `json:"worktree"`
	CandidateSHA string `json:"candidate_sha"`
	BaseSHA      string `json:"base_sha"`
	Feedback     string `json:"feedback"`
}

type commandResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Err      error
}

const commandOutputLimit = 1 << 20

// executeCommand bounds cooperative trusted adapters; it is not a hostile-code sandbox.
func executeCommand(ctx context.Context, command Command, input commandInput, privateDir string) commandResult {
	cmd, err := prepareCommand(ctx, command, input, privateDir)
	if err != nil {
		return commandResult{ExitCode: -1, Err: err}
	}
	return runProcess(ctx, cmd)
}

// runProcess applies the same cancellation, descendant and output bounds to
// admitted policy programs and Git commands created with exec.CommandContext.
func runProcess(ctx context.Context, cmd *exec.Cmd) commandResult {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	stop := func() error { return killCommandGroup(cmd) }
	stdout := &commandOutput{stream: "stdout", stop: stop}
	stderr := &commandOutput{stream: "stderr", stop: stop}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.Cancel = stop
	// A successfully exited parent may leave descendants holding its output pipes.
	cmd.WaitDelay = 200 * time.Millisecond
	err := cmd.Run()
	err = errors.Join(err, stop(), stdout.err, stderr.err, ctx.Err())
	result := commandResult{Stdout: stdout.buffer.Bytes(), Stderr: stderr.buffer.Bytes(), ExitCode: -1}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		result.Err = fmt.Errorf("execute process: %w", err)
	}
	return result
}

func prepareCommand(ctx context.Context, command Command, input commandInput, privateDir string) (*exec.Cmd, error) {
	if input.Worktree == "" || privateDir == "" {
		return nil, errors.New("worktree and private execution directory are required")
	}
	env, err := commandEnvironment(privateDir)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("encode command input: %w", err)
	}
	cmd := exec.CommandContext(ctx, command.Path, command.Args...) //nolint:gosec // Admitted policy path and literal argv are trusted inputs.
	cmd.Dir, cmd.Env = input.Worktree, env
	cmd.Stdin = bytes.NewReader(append(encoded, '\n'))
	return cmd, nil
}

func commandEnvironment(privateDir string) ([]string, error) {
	root, err := filepath.Abs(privateDir)
	if err != nil {
		return nil, fmt.Errorf("resolve private execution directory: %w", err)
	}
	overrides := map[string]string{
		"TMPDIR": filepath.Join(root, "tmp"), "TMP": filepath.Join(root, "tmp"), "TEMP": filepath.Join(root, "tmp"),
		"XDG_CACHE_HOME": filepath.Join(root, "cache"), "GOCACHE": filepath.Join(root, "cache", "go-build"),
		"GIT_TERMINAL_PROMPT": "0", "GIT_EDITOR": "true", "GIT_SEQUENCE_EDITOR": "true", "GIT_ASKPASS": "false", "SSH_ASKPASS": "false",
	}
	for _, name := range []string{"TMPDIR", "XDG_CACHE_HOME", "GOCACHE"} {
		if err := os.MkdirAll(overrides[name], 0o700); err != nil {
			return nil, fmt.Errorf("create private %s directory: %w", name, err)
		}
	}
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if name == "ORO" || strings.HasPrefix(name, "ORO_") || strings.HasPrefix(name, "GIT_") || overrides[name] != "" {
			continue
		}
		env = append(env, entry)
	}
	for name, value := range overrides {
		env = append(env, name+"="+value)
	}
	return env, nil
}

func killCommandGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill process group: %w", err)
	}
	return nil
}

// Each stream has one copying goroutine; Wait joins them before the fields are read.
type commandOutput struct {
	buffer bytes.Buffer
	stream string
	stop   func() error
	err    error
}

func (out *commandOutput) Write(data []byte) (int, error) {
	remaining := commandOutputLimit - out.buffer.Len()
	if len(data) <= remaining {
		n, err := out.buffer.Write(data)
		if err != nil {
			return n, fmt.Errorf("capture %s: %w", out.stream, err)
		}
		return n, nil
	}
	n, _ := out.buffer.Write(data[:remaining])
	out.err = errors.Join(fmt.Errorf("%s output limit exceeded (%d bytes)", out.stream, commandOutputLimit), out.stop())
	return n, out.err
}
