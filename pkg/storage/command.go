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

func runLeasedCommandWithFactory(ctx context.Context, request CommandRequest, factory leasedCommandFactory) (result CommandResult, err error) {
	handle, err := OpenRuntime(ctx, request.Runtime)
	if err != nil {
		return CommandResult{}, fmt.Errorf("open command runtime: %w", err)
	}
	defer func() {
		err = errors.Join(err, handle.Close())
	}()

	command := factory(ctx, request, handle.Env)
	if startErr := command.start(); startErr != nil {
		return CommandResult{}, fmt.Errorf("start leased command %q: %w", request.Path, startErr)
	}
	if waitErr := command.wait(); waitErr != nil {
		result.ExitCode = command.exitCode()
		return result, fmt.Errorf("wait leased command %q: %w", request.Path, waitErr)
	}
	result.ExitCode = command.exitCode()
	return result, nil
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
