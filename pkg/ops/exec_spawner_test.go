package ops //nolint:testpackage // internal test needs access to unexported opsProcess

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
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

func TestOpsProcessWaitNilSuccess(t *testing.T) {
	// "true" exits 0 — cmd.Wait() returns nil.
	cmd := exec.Command("true")
	var buf strings.Builder
	cmd.Stdout = &buf
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	p := &opsProcess{cmd: cmd, output: &buf}

	err := p.Wait()
	if err != nil {
		t.Errorf("Wait() = %v, want nil for successful process", err)
	}
}

func TestOpsProcessWaitErrorWrapped(t *testing.T) {
	// "false" exits 1 — cmd.Wait() returns a non-nil error.
	cmd := exec.Command("false")
	var buf strings.Builder
	cmd.Stdout = &buf
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	p := &opsProcess{cmd: cmd, output: &buf}

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
	var buf strings.Builder
	cmd.Stdout = &buf
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	p := &opsProcess{cmd: cmd, output: &buf}

	err := p.Kill()
	if err != nil {
		t.Errorf("Kill() = %v, want nil for successful kill", err)
	}
	// Clean up: wait for the killed process to avoid zombies.
	_ = cmd.Wait()
}
