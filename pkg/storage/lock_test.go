package storage_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"oro/pkg/storage"
)

const maintenanceLockHolderEnv = "ORO_TEST_MAINTENANCE_LOCK_HOLDER"

func TestMaintenanceLockExcludesPeers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "maintenance.lock")

	stdin, stdout, holder := startMaintenanceLockHolder(t, path)
	t.Cleanup(func() {
		if holder.ProcessState != nil {
			return
		}
		_ = stdin.Close()
		waitForMaintenanceLockHolder(t, holder)
	})

	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatalf("holder did not report readiness: %v", scanner.Err())
	}
	if got := scanner.Text(); got != "ready" {
		t.Fatalf("holder readiness = %q, want ready", got)
	}

	if _, err := storage.AcquireMaintenanceLock(context.Background(), path); !errors.Is(err, storage.ErrMaintenanceBusy) {
		t.Fatalf("peer AcquireMaintenanceLock() error = %v, want ErrMaintenanceBusy", err)
	}

	if err := stdin.Close(); err != nil {
		t.Fatalf("close holder stdin: %v", err)
	}
	waitForMaintenanceLockHolder(t, holder)

	lock, err := storage.AcquireMaintenanceLock(context.Background(), path)
	if err != nil {
		t.Fatalf("AcquireMaintenanceLock() after holder exit: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("release acquired lock: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := storage.AcquireMaintenanceLock(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("AcquireMaintenanceLock() with canceled context error = %v, want context.Canceled", err)
	}
}

func TestMaintenanceLockHolderProcess(t *testing.T) {
	if os.Getenv(maintenanceLockHolderEnv) != "1" {
		return
	}
	path := os.Args[len(os.Args)-1]
	lock, err := storage.AcquireMaintenanceLock(context.Background(), path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer lock.Close()
	if _, err := fmt.Fprintln(os.Stdout, "ready"); err != nil {
		os.Exit(1)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func startMaintenanceLockHolder(t *testing.T, path string) (io.WriteCloser, io.ReadCloser, *exec.Cmd) {
	t.Helper()
	holder := exec.Command(os.Args[0], "-test.run=^TestMaintenanceLockHolderProcess$", "--", path)
	holder.Env = append(os.Environ(), maintenanceLockHolderEnv+"=1")
	stdin, err := holder.StdinPipe()
	if err != nil {
		t.Fatalf("create holder stdin: %v", err)
	}
	stdout, err := holder.StdoutPipe()
	if err != nil {
		t.Fatalf("create holder stdout: %v", err)
	}
	if err := holder.Start(); err != nil {
		t.Fatalf("start holder: %v", err)
	}
	return stdin, stdout, holder
}

func waitForMaintenanceLockHolder(t *testing.T, holder *exec.Cmd) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- holder.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("holder exit: %v", err)
		}
	case <-time.After(2 * time.Second):
		if err := holder.Process.Kill(); err != nil {
			t.Errorf("kill timed-out holder: %v", err)
		}
		<-done
		t.Fatal("holder did not exit")
	}
}
