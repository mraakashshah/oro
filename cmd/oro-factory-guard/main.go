// Command oro-factory-guard bounds the lifetime of a private Linux PID namespace.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"
)

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
}

func runCLI(args []string, ready, diagnostic io.Writer) int {
	deadline, err := parseDeadline(args)
	if err == nil && (runtime.GOOS != "linux" || os.Getpid() != 1 || os.Getuid() != 65534 || os.Getgid() != 65534) {
		err = errors.New("guard requires Linux PID1 with UID/GID 65534")
	}
	if err == nil {
		err = runGuard(context.Background(), deadline, ready)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		// Exiting PID1 makes the Linux kernel kill every remaining process in
		// this namespace, including orphaned processes in detached sessions.
		return 0
	}
	_, _ = fmt.Fprintf(diagnostic, "oro-factory-guard: %v\n", err)
	return 1
}

func runGuard(ctx context.Context, absoluteDeadline time.Time, ready io.Writer) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("guard startup: %w", err)
	}
	remaining := time.Until(absoluteDeadline)
	if absoluteDeadline.IsZero() || remaining <= 0 {
		return errors.New("guard deadline must be in the future")
	}
	if ready == nil {
		return errors.New("guard requires a readiness writer")
	}
	// Convert the admitted wall-clock deadline once. Go's timer subsequently
	// uses the monotonic clock; no signal, input, or clock change can refresh it.
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	written := make(chan error, 1)
	go func() {
		n, err := io.WriteString(ready, "READY\n")
		if err == nil && n != len("READY\n") {
			err = io.ErrShortWrite
		}
		written <- err
	}()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("guard canceled: %w", ctx.Err())
		case <-timer.C:
			return context.DeadlineExceeded
		case err := <-written:
			if err != nil {
				return fmt.Errorf("guard readiness: %w", err)
			}
			written = nil
		}
	}
}

func parseDeadline(args []string) (time.Time, error) {
	if len(args) != 2 || args[0] != "--deadline" {
		return time.Time{}, errors.New("usage: oro-factory-guard --deadline RFC3339Nano")
	}
	deadline, err := time.Parse(time.RFC3339Nano, args[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("parse guard deadline: %w", err)
	}
	return deadline, nil
}
