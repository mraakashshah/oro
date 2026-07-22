package worker

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os/exec"

	"oro/pkg/storage"
)

type runtimeLeaseProvider interface {
	RuntimeRequest() storage.RuntimeRequest
}

func runtimeRequestForChild(template storage.RuntimeRequest, workdir string, env []string, owner string) storage.RuntimeRequest {
	request := template
	request.Workdir = workdir
	request.Env = env
	baseID := request.Lease.ID
	if baseID == "" {
		baseID = storage.LeaseID(owner)
	}
	request.Lease.ID = storage.LeaseID(string(baseID) + "-" + rand.Text())
	return request
}

func runLeasedQualityGate(
	ctx context.Context,
	worktree string,
	args []string,
	env []string,
	runtime storage.RuntimeRequest,
) (passed bool, output string, err error) {
	var out bytes.Buffer
	_, runErr := storage.RunLeasedCommand(ctx, storage.CommandRequest{
		Runtime: runtimeRequestForChild(runtime, worktree, env, "quality-gate"),
		Path:    "bash",
		Args:    args,
		Dir:     worktree,
		Stdout:  &out,
		Stderr:  &out,
	})
	output = out.String()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, output, fmt.Errorf("run quality gate canceled: %w", ctxErr)
	}
	if runErr == nil {
		return true, output, nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return false, output, nil
	}
	return false, output, fmt.Errorf("run quality gate: %w", runErr)
}

func startLeasedWorkerProcess(
	ctx context.Context,
	runtime storage.RuntimeRequest,
	path string,
	args []string,
	workdir string,
	stdin io.Reader,
	stderr io.Writer,
	runtimeName string,
	stderrTail *LineTailBuffer,
) (Process, io.ReadCloser, error) {
	stdoutReader, stdoutWriter := io.Pipe()
	command, err := storage.StartLeasedCommand(ctx, storage.CommandRequest{
		Runtime: runtime,
		Path:    path,
		Args:    args,
		Dir:     workdir,
		Stdin:   stdin,
		Stdout:  stdoutWriter,
		Stderr:  stderr,
	})
	if err != nil {
		_ = stdoutWriter.Close()
		_ = stdoutReader.Close()
		return nil, nil, fmt.Errorf("start leased worker command: %w", err)
	}
	return &CmdProcess{
		Leased:       command,
		Runtime:      runtimeName,
		Stderr:       stderrTail,
		stdoutCloser: stdoutWriter,
	}, stdoutReader, nil
}

func startQualityGateProcess(
	ctx context.Context,
	worktree string,
	args []string,
	env []string,
	runtime storage.RuntimeRequest,
) (*commandProcess, *bytes.Buffer, error) {
	output := &bytes.Buffer{}
	if runtime.Catalog != nil {
		command, err := storage.StartLeasedCommand(ctx, storage.CommandRequest{
			Runtime: runtimeRequestForChild(runtime, worktree, env, "quality-gate"),
			Path:    "bash",
			Args:    args,
			Dir:     worktree,
			Stdout:  output,
			Stderr:  output,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("run quality gate: %w", err)
		}
		return &commandProcess{leased: command}, output, nil
	}

	cmd := exec.CommandContext(ctx, "bash", args...) //nolint:gosec // script path constructed from worktree, not user input
	cmd.Dir = worktree
	cmd.Env = env
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("run quality gate: %w", err)
	}
	return &commandProcess{cmd: cmd}, output, nil
}
