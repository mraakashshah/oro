package ops //nolint:testpackage // internal test needs access to unexported opsProcess

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestOpsSpawnerUsesRuntime(t *testing.T) {
	t.Setenv("PWD", "/wrong/root")
	t.Setenv("GIT_DIR", "/wrong/root/.git")
	workdir := t.TempDir()
	spawner := NewExecSpawner(RuntimeSpec{
		Command: "sh",
		BuildArgs: func(model, prompt string) []string {
			return []string{"-c", "printf '%s|%s|%s|%s' \"$1\" \"$2\" \"$PWD\" \"${GIT_DIR-unset}\"", "sh", model, prompt}
		},
	})

	proc, err := spawner.Spawn(context.Background(), "balanced", "review this", workdir)
	if err != nil {
		t.Fatalf("Spawn() error = %v", err)
	}
	if err := proc.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	output, err := proc.Output()
	if err != nil {
		t.Fatalf("Output() error = %v", err)
	}
	if output != "balanced|review this|"+workdir+"|unset" {
		t.Fatalf("output = %q, want runtime-built args to include model and prompt", output)
	}
}

func TestOpsSpawnerSetsPWDWhenBuildEnvProvided(t *testing.T) {
	t.Setenv("PWD", "/wrong/root")
	t.Setenv("GIT_WORK_TREE", "/wrong/root")
	workdir := t.TempDir()
	spawner := NewExecSpawner(RuntimeSpec{
		Command: "sh",
		BuildArgs: func(_, _ string) []string {
			return []string{"-c", "printf '%s|%s' \"$PWD\" \"${GIT_WORK_TREE-unset}\""}
		},
		BuildEnv: func() []string {
			return append(os.Environ(), "CUSTOM_ENV=1")
		},
	})

	proc, err := spawner.Spawn(context.Background(), "balanced", "review this", workdir)
	if err != nil {
		t.Fatalf("Spawn() error = %v", err)
	}
	if err := proc.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	output, err := proc.Output()
	if err != nil {
		t.Fatalf("Output() error = %v", err)
	}
	if output != workdir+"|unset" {
		t.Fatalf("env report = %q, want %q", output, workdir+"|unset")
	}
}

func TestOpsProcessLastOutputAt(t *testing.T) {
	workdir := t.TempDir()
	spawner := NewExecSpawner(RuntimeSpec{
		Command: "sh",
		BuildArgs: func(_, _ string) []string {
			return []string{"-c", "sleep 0.1; printf first; sleep 0.05; printf second"}
		},
	})

	proc, err := spawner.Spawn(context.Background(), "balanced", "review this", workdir)
	if err != nil {
		t.Fatalf("Spawn() error = %v", err)
	}
	if got := proc.LastOutputAt(); !got.IsZero() {
		t.Fatalf("LastOutputAt() before output = %v, want zero", got)
	}

	beforeWait := time.Now()
	if err := proc.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	afterWait := time.Now()
	got := proc.LastOutputAt()
	if got.IsZero() {
		t.Fatal("LastOutputAt() after output = zero, want last-output time")
	}
	if got.Before(beforeWait) || got.After(afterWait) {
		t.Fatalf("LastOutputAt() = %v, want between %v and %v", got, beforeWait, afterWait)
	}
	output, err := proc.Output()
	if err != nil {
		t.Fatalf("Output() error = %v", err)
	}
	if output != "firstsecond" {
		t.Fatalf("Output() = %q, want firstsecond", output)
	}
}

func TestExecSpawnerStreamsIncrementally(t *testing.T) {
	workdir := t.TempDir()
	spawner := NewExecSpawner(RuntimeSpec{
		Command: "sh",
		BuildArgs: func(_, _ string) []string {
			return []string{"-c", "printf partial; sleep 0.2; printf '\\n'; sleep 0.2; printf second\\\\n; sleep 0.2"}
		},
	})

	proc, err := spawner.Spawn(context.Background(), "balanced", "review this", workdir)
	if err != nil {
		t.Fatalf("Spawn() error = %v", err)
	}
	waited := false
	t.Cleanup(func() {
		if waited {
			return
		}
		if err := proc.Kill(); err != nil {
			t.Logf("cleanup Kill() error: %v", err)
		}
		_ = proc.Wait()
	})

	time.Sleep(50 * time.Millisecond)
	if got := proc.LastOutputAt(); !got.IsZero() {
		t.Fatalf("LastOutputAt() after partial stdout = %v, want zero until a line is complete", got)
	}

	firstLineAt := waitForLastOutputAt(t, proc, time.Time{})
	secondLineAt := waitForLastOutputAt(t, proc, firstLineAt)
	if !secondLineAt.After(firstLineAt) {
		t.Fatalf("second line LastOutputAt() = %v, want after first line time %v", secondLineAt, firstLineAt)
	}

	if err := proc.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	waited = true
	afterWait := time.Now()
	lastOutputAt := proc.LastOutputAt()
	if lastOutputAt.After(afterWait) {
		t.Fatalf("LastOutputAt() = %v, want final line timestamp before Wait() returned at %v", lastOutputAt, afterWait)
	}

	output, err := proc.Output()
	if err != nil {
		t.Fatalf("Output() error = %v", err)
	}
	if output != "partial\nsecond\n" {
		t.Fatalf("Output() = %q, want full stdout", output)
	}
}

func TestExecSpawnerCapturesLargeRecord(t *testing.T) {
	workdir := t.TempDir()
	record := strings.Repeat("x", 256*1024)
	spawner := NewExecSpawner(RuntimeSpec{
		Command: "sh",
		BuildArgs: func(_, _ string) []string {
			return []string{"-c", "head -c 262144 /dev/zero | tr '\\000' x; printf '\\n'"}
		},
	})

	proc, err := spawner.Spawn(context.Background(), "balanced", record, workdir)
	if err != nil {
		t.Fatalf("Spawn() error = %v", err)
	}
	if err := proc.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	output, err := proc.Output()
	if err != nil {
		t.Fatalf("Output() error = %v", err)
	}
	if output != record+"\n" {
		t.Fatalf("Output() length = %d, want %d", len(output), len(record)+1)
	}
}

func waitForLastOutputAt(t *testing.T, proc Process, after time.Time) time.Time {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		got := proc.LastOutputAt()
		if got.After(after) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("LastOutputAt() did not advance after %v", after)
	return time.Time{}
}

func TestOpsProcessWaitNilSuccess(t *testing.T) {
	// "true" exits 0 — cmd.Wait() returns nil.
	cmd := exec.Command("true")
	p := &opsProcess{cmd: cmd}
	cmd.Stdout = p
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	err := p.Wait()
	if err != nil {
		t.Errorf("Wait() = %v, want nil for successful process", err)
	}
}

func TestOpsProcessWaitErrorWrapped(t *testing.T) {
	// "false" exits 1 — cmd.Wait() returns a non-nil error.
	cmd := exec.Command("false")
	p := &opsProcess{cmd: cmd}
	cmd.Stdout = p
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	err := p.Wait()
	if err == nil {
		t.Fatal("Wait() = nil, want non-nil error for failed process")
	}
	if !strings.Contains(err.Error(), "wait:") {
		t.Errorf("Wait() error = %q, want it to contain 'wait:'", err.Error())
	}
}

func TestOpsProcessKillNilSuccess(t *testing.T) {
	// Start a long-running process so Kill() has a live process to kill.
	cmd := exec.Command("sleep", "60")
	p := &opsProcess{cmd: cmd}
	cmd.Stdout = p
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	err := p.Kill()
	if err != nil {
		t.Errorf("Kill() = %v, want nil for successful kill", err)
	}
	// Clean up: wait for the killed process to avoid zombies.
	_ = cmd.Wait()
}

func TestKillTerminatesProcessGroup(t *testing.T) {
	workdir := t.TempDir()
	spawner := NewExecSpawner(RuntimeSpec{
		Command: "sh",
		BuildArgs: func(_, _ string) []string {
			return []string{"-c", "trap '' TERM; sleep 3600 & echo $! > child.pid; wait"}
		},
	})

	proc, err := spawner.Spawn(context.Background(), "balanced", "review this", workdir)
	if err != nil {
		t.Fatalf("Spawn() error = %v", err)
	}
	parentPID := proc.(*opsProcess).cmd.Process.Pid
	waited := false
	t.Cleanup(func() {
		if waited {
			return
		}
		_ = proc.Kill()
		_ = proc.Wait()
	})
	waitForGrandchildInProcessGroup(t, workdir, parentPID)

	if err := proc.Kill(); err != nil {
		t.Fatalf("Kill() error = %v", err)
	}
	timely, waitErr := waitForProcessExit(proc, parentPID)
	waited = true
	if !timely {
		t.Fatal("Wait() did not return after Kill()")
	}
	if waitErr == nil {
		t.Fatal("Wait() error = nil, want killed process error")
	}
	waitForProcessGroupGone(t, parentPID)
}

func TestContextCancellationTerminatesProcessGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	workdir := t.TempDir()
	spawner := NewExecSpawner(RuntimeSpec{
		Command: "sh",
		BuildArgs: func(_, _ string) []string {
			return []string{"-c", "trap '' TERM; sleep 3600 & echo $! > child.pid; wait"}
		},
	})

	proc, err := spawner.Spawn(ctx, "balanced", "review this", workdir)
	if err != nil {
		t.Fatalf("Spawn() error = %v", err)
	}
	parentPID := proc.(*opsProcess).cmd.Process.Pid
	waited := false
	t.Cleanup(func() {
		if waited {
			return
		}
		cancel()
		_ = proc.Kill()
		_ = proc.Wait()
	})
	waitForGrandchildInProcessGroup(t, workdir, parentPID)

	cancel()
	timely, waitErr := waitForProcessExit(proc, parentPID)
	waited = true
	if !timely {
		t.Fatal("Wait() did not return after context cancellation")
	}
	if waitErr == nil {
		t.Fatal("Wait() error = nil, want canceled process error")
	}
	waitForProcessGroupGone(t, parentPID)
}

func waitForGrandchildInProcessGroup(t *testing.T, workdir string, parentPID int) {
	t.Helper()
	waitForProcessCondition(t, func() bool {
		data, err := os.ReadFile(filepath.Join(workdir, "child.pid"))
		if err != nil {
			return false
		}
		childPID, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil || childPID <= 0 {
			return false
		}
		childPGID, err := syscall.Getpgid(childPID)
		return err == nil && childPGID == parentPID
	})
}

func waitForProcessExit(proc Process, pgid int) (bool, error) {
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- proc.Wait()
	}()

	select {
	case waitErr := <-waitDone:
		return true, waitErr
	case <-time.After(2 * time.Second):
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return false, <-waitDone
	}
}

func waitForProcessGroupGone(t *testing.T, pgid int) {
	t.Helper()
	waitForProcessCondition(t, func() bool {
		return errors.Is(syscall.Kill(-pgid, syscall.Signal(0)), syscall.ESRCH)
	})
}

func waitForProcessCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("process condition was not met before timeout")
}
