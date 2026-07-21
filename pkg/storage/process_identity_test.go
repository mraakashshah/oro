package storage_test

import (
	"errors"
	"os"
	"os/exec"
	"testing"

	"oro/pkg/storage"
)

func TestProcessIdentityRejectsPIDReuse(t *testing.T) {
	live, err := storage.InspectProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatalf("inspect live process: %v", err)
	}
	if live.PID != os.Getpid() || live.StartMarker == "" || live.Executable == "" || live.ProcessGroup <= 0 {
		t.Fatalf("incomplete live identity: %+v", live)
	}

	cmd := exec.Command("sleep", "30") //nolint:gosec // test child supplies deterministic identity data
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState != nil || cmd.Process == nil {
			return
		}
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("kill cleanup child: %v", err)
		}
		if err := waitForKilledProcess(cmd); err != nil {
			t.Errorf("wait cleanup child: %v", err)
		}
	})

	owner, err := storage.InspectProcessIdentity(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("inspect child process: %v", err)
	}
	if owner.PID != cmd.Process.Pid || owner.StartMarker == "" || owner.Executable == "" || owner.ProcessGroup <= 0 {
		t.Fatalf("incomplete child identity: %+v", owner)
	}
	if !owner.Matches(owner) {
		t.Fatal("identity must match its live owner")
	}

	reused := owner
	reused.StartMarker += "-reused"
	if reused.Matches(owner) {
		t.Fatal("identity with a reused PID start marker must not match")
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill child: %v", err)
	}
	if err := waitForKilledProcess(cmd); err != nil {
		t.Fatalf("wait for child: %v", err)
	}

	afterExit, err := storage.InspectProcessIdentity(owner.PID)
	if err == nil && owner.Matches(afterExit) {
		t.Fatalf("exited or reused pid %d still matched original identity: %+v", owner.PID, afterExit)
	}
}

func waitForKilledProcess(cmd *exec.Cmd) error {
	err := cmd.Wait()
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil
	}
	return err
}
