package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
)

// CommandRequest supplies one command and the runtime lease it must hold.
//
//oro:testonly — direct command callers adopt this contract in follow-up storage lifecycle work.
type CommandRequest struct {
	Runtime RuntimeRequest
	Path    string
	Args    []string
	Dir     string
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
}

// CommandResult records the completed command's exit code.
//
//oro:testonly — direct command callers adopt this contract in follow-up storage lifecycle work.
type CommandResult struct {
	ExitCode int
}

// StartedCommand is a running command that owns its runtime lease until Wait
// returns. Callers must call Wait after a successful StartLeasedCommand.
type StartedCommand struct {
	command leasedCommand
	handle  *RuntimeHandle
}

type leasedCommand interface {
	start() error
	wait() error
	exitCode() int
}

type leasedCommandFactory func(context.Context, CommandRequest, []string) leasedCommand

// RunLeasedCommand resolves the command environment, holds its runtime lease
// for the complete child lifecycle, and owns the child's process group.
//
//oro:testonly — direct command callers adopt this contract in follow-up storage lifecycle work.
func RunLeasedCommand(ctx context.Context, request CommandRequest) (CommandResult, error) {
	return runLeasedCommandWithFactory(ctx, request, newExecLeasedCommand)
}

// StartLeasedCommand starts a command after acquiring its runtime lease. The
// returned command releases the lease when Wait completes, including when the
// child exits unsuccessfully or is cancelled.
//
// Its production callers (pkg/worker/spawner.go, pkg/ops/exec_spawner.go) were
// dropped by the same -s ours merge and return with the remaining
// runtime-storage files under oro-33h1.
//
//oro:testonly — production callers land with the remaining runtime-storage restoration.
func StartLeasedCommand(ctx context.Context, request CommandRequest) (*StartedCommand, error) {
	return startLeasedCommandWithFactory(ctx, request, newExecLeasedCommand)
}

func runLeasedCommandWithFactory(ctx context.Context, request CommandRequest, factory leasedCommandFactory) (result CommandResult, err error) {
	command, err := startLeasedCommandWithFactory(ctx, request, factory)
	if err != nil {
		return CommandResult{}, err
	}
	if waitErr := command.Wait(); waitErr != nil {
		return CommandResult{ExitCode: command.ExitCode()}, fmt.Errorf("wait leased command %q: %w", request.Path, waitErr)
	}
	return CommandResult{ExitCode: command.ExitCode()}, nil
}

func startLeasedCommandWithFactory(ctx context.Context, request CommandRequest, factory leasedCommandFactory) (_ *StartedCommand, err error) {
	handle, err := OpenRuntime(ctx, request.Runtime)
	if err != nil {
		return nil, fmt.Errorf("open command runtime: %w", err)
	}

	command := factory(ctx, request, handle.Env)
	if startErr := command.start(); startErr != nil {
		return nil, errors.Join(
			fmt.Errorf("start leased command %q: %w", request.Path, startErr),
			handle.Close(),
		)
	}
	return &StartedCommand{command: command, handle: handle}, nil
}

// Wait waits for the child and releases the runtime lease exactly once.
func (command *StartedCommand) Wait() error {
	if command == nil {
		return nil
	}
	return errors.Join(command.command.wait(), command.handle.Close())
}

// ExitCode returns the child exit code after it exits.
func (command *StartedCommand) ExitCode() int {
	if command == nil {
		return -1
	}
	return command.command.exitCode()
}

// Kill cancels an exec-backed leased child process. Callers still must Wait to
// reap the process and release the lease.
func (command *StartedCommand) Kill() error {
	execCommand, ok := command.command.(execCommand)
	if !ok || execCommand.command.Cancel == nil {
		return fmt.Errorf("kill leased command: unsupported command type")
	}
	if err := execCommand.command.Cancel(); err != nil {
		return fmt.Errorf("cancel leased command: %w", err)
	}
	return nil
}

func newExecLeasedCommand(ctx context.Context, request CommandRequest, env []string) leasedCommand {
	//nolint:gosec // CommandRequest intentionally accepts the caller-approved executable and argv.
	command := exec.CommandContext(ctx, request.Path, request.Args...)
	command.Dir = request.Dir
	if command.Dir == "" {
		command.Dir = request.Runtime.Workdir
	}
	command.Env = env
	command.Stdin = request.Stdin
	command.Stdout = request.Stdout
	command.Stderr = request.Stderr
	configureLeasedCommandProcessGroup(command)
	return execCommand{command: command}
}

type execCommand struct {
	command *exec.Cmd
}

func (command execCommand) start() error {
	if err := command.command.Start(); err != nil {
		return fmt.Errorf("start command: %w", err)
	}
	return nil
}

func (command execCommand) wait() error {
	if err := command.command.Wait(); err != nil {
		return fmt.Errorf("wait command: %w", err)
	}
	return nil
}

func (command execCommand) exitCode() int {
	if command.command.ProcessState == nil {
		return -1
	}
	return command.command.ProcessState.ExitCode()
}
