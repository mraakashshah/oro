package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"syscall"

	"oro/pkg/factory"
)

// The helper owns only the newly spawned attaching client. The daemon's exact
// container is reconciled separately; a lost start acknowledgement is not proof
// that the namespace stopped or that starting it again is safe.
func attachFactoryDockerReady(ctx context.Context, invocation factory.HostInvocation) error {
	cmd := exec.CommandContext(ctx, invocation.Path, invocation.Args...) //nolint:gosec // Verified platform executable; fixed exact-container argv inside an authenticated host scope.
	cmd.Dir, cmd.Env = invocation.Dir, append([]string{}, invocation.Env...)
	invocation.OutputLimit = 4096
	capture, err := newFactoryHostIO(cmd, invocation)
	if err != nil {
		return err
	}
	defer capture.close()
	ready := make(chan error, 1)
	frame := &factoryDockerReadyFrame{ready: ready, stop: func() error { return stopFactoryHostChild(cmd) }}
	capture.pipes[0].transfer = func() error {
		_, err := io.Copy(frame, capture.pipes[0].parent)
		if err == nil && frame.buffer.Len() != len("READY\n") {
			err = errors.New("factory Docker client ended before complete READY")
		}
		if err != nil {
			frame.notify(err)
		}
		return err
	}
	if err := factoryHostDeadline(ctx, invocation.Deadline); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start owned factory Docker attaching client: %w", err)
	}
	startError := capture.start()
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	readyError := awaitFactoryDockerReady(ctx, ready, waited, startError)
	killError := cmd.Process.Kill()
	// Kill and reap must both be positively observed, even when READY won a race
	// with ordinary child exit. Exit0 cannot substitute for owned-client retirement.
	waitError := reapFactoryDockerClient(ctx, waited)
	drainError := capture.wait(ctx)
	return errors.Join(readyError, killError, waitError, drainError, factoryHostDeadline(ctx, invocation.Deadline))
}

func awaitFactoryDockerReady(ctx context.Context, ready <-chan error, waited chan error, startError error) error {
	if startError != nil {
		return startError
	}
	select {
	case err := <-ready:
		return err
	case err := <-waited:
		// Preserve completion for the mandatory reap stage, without an additional
		// Wait call. This path is handled by the caller through the buffered result.
		waited <- err
		return errors.Join(errors.New("factory Docker attaching client exited before retirement"), err)
	case <-ctx.Done():
		return fmt.Errorf("factory Docker readiness deadline: %w", ctx.Err())
	}
}

func reapFactoryDockerClient(ctx context.Context, waited <-chan error) error {
	select {
	case err := <-waited:
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			status, ok := exitError.Sys().(syscall.WaitStatus)
			if ok && status.Signaled() && status.Signal() == syscall.SIGKILL {
				return nil
			}
		}
		return errors.Join(errors.New("factory Docker attaching client lacks exact SIGKILL retirement"), err)
	case <-ctx.Done():
		return fmt.Errorf("factory Docker attaching client reap uncertain: %w", ctx.Err())
	}
}

type factoryDockerReadyFrame struct {
	buffer bytes.Buffer
	ready  chan error
	stop   func() error
}

func (frame *factoryDockerReadyFrame) notify(err error) {
	select {
	case frame.ready <- err:
	default:
	}
}

func (frame *factoryDockerReadyFrame) Write(data []byte) (int, error) {
	remaining := len("READY\n") - frame.buffer.Len()
	if len(data) > remaining {
		err := errors.Join(errors.New("factory Docker READY contains extra bytes"), frame.stop())
		frame.notify(err)
		return 0, err
	}
	n, _ := frame.buffer.Write(data)
	if !bytes.Equal(frame.buffer.Bytes(), []byte("READY\n")[:frame.buffer.Len()]) {
		err := errors.Join(errors.New("factory Docker READY has invalid framing"), frame.stop())
		frame.notify(err)
		return n, err
	}
	if frame.buffer.Len() == len("READY\n") {
		frame.notify(nil)
	}
	return n, nil
}
