package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/worker"
)

func TestNewWorkerCmd_Flags(t *testing.T) {
	cmd := newWorkerCmd()

	if cmd.Use != "worker" {
		t.Fatalf("expected Use=worker, got %s", cmd.Use)
	}

	socketFlag := cmd.Flag("socket")
	if socketFlag == nil {
		t.Fatal("expected --socket flag")
	}

	idFlag := cmd.Flag("id")
	if idFlag == nil {
		t.Fatal("expected --id flag")
	}
}

func TestNewWorkerCmd_RequiresSocket(t *testing.T) {
	cmd := newWorkerCmd()
	cmd.SetArgs([]string{"--id=w-01"})
	// Should fail because --socket is required
	err := cmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error when --socket not provided")
	}
}

func TestNewWorkerCmd_RequiresID(t *testing.T) {
	cmd := newWorkerCmd()
	cmd.SetArgs([]string{"--socket=/tmp/test.sock"})
	// Should fail because --id is required
	err := cmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error when --id not provided")
	}
}

func TestNewWorkerCmd_RegisteredInRoot(t *testing.T) {
	root := newRootCmd()
	found := false
	for _, sub := range root.Commands() {
		if sub.Name() == "worker" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected 'worker' subcommand in root")
	}
}

func TestNewWorkerCmd_InvalidSocket(t *testing.T) {
	cmd := newWorkerCmd()
	cmd.SetArgs([]string{"--socket=/nonexistent/path/test.sock", "--id=w-01"})
	err := cmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error connecting to nonexistent socket")
	}
}

func TestWorkerSpawnerBuildsRuntimeRouter(t *testing.T) {
	claudeSpawner := &workerRouterTestSpawner{}
	codexSpawner := &workerRouterTestSpawner{}
	prevClaude := newClaudeWorkerSpawner
	prevCodex := newCodexWorkerSpawner
	newClaudeWorkerSpawner = func() worker.StreamingSpawner { return claudeSpawner }
	newCodexWorkerSpawner = func() worker.StreamingSpawner { return codexSpawner }
	defer func() {
		newClaudeWorkerSpawner = prevClaude
		newCodexWorkerSpawner = prevCodex
	}()

	got := workerSpawnerForRuntime()
	if _, _, _, _, err := got.Spawn(context.Background(), runtimeClaude, "sonnet", "", "prompt", t.TempDir()); err != nil {
		t.Fatalf("spawn claude through runtime router: %v", err)
	}
	if _, _, _, _, err := got.Spawn(context.Background(), runtimeCodex, "gpt-5.5", "high", "prompt", t.TempDir()); err != nil {
		t.Fatalf("spawn codex through runtime router: %v", err)
	}

	if claudeSpawner.calls != 1 {
		t.Fatalf("claude spawner calls = %d, want 1", claudeSpawner.calls)
	}
	if codexSpawner.calls != 1 {
		t.Fatalf("codex spawner calls = %d, want 1", codexSpawner.calls)
	}
}

func TestRunWorkerResolvesIdentityBeforeRuntimeSpawner(t *testing.T) {
	t.Setenv("ORO_HOME", filepath.Join(t.TempDir(), "oro-home"))
	t.Setenv("ORO_PROJECT", "")
	t.Chdir(currentRepoRoot())

	var observedHome, observedProject string
	previousClaude := newClaudeWorkerSpawner
	previousCodex := newCodexWorkerSpawner
	newClaudeWorkerSpawner = func() worker.StreamingSpawner {
		observedHome, observedProject = os.Getenv("ORO_HOME"), os.Getenv("ORO_PROJECT")
		return &workerRouterTestSpawner{}
	}
	newCodexWorkerSpawner = func() worker.StreamingSpawner { return &workerRouterTestSpawner{} }
	defer func() {
		newClaudeWorkerSpawner = previousClaude
		newCodexWorkerSpawner = previousCodex
	}()

	err := runWorker(context.Background(), filepath.Join(t.TempDir(), "missing.sock"), "w-01")
	if err == nil {
		t.Fatal("expected missing socket connection to fail")
	}
	if observedHome == "" || observedProject == "" {
		t.Fatalf("runtime spawner saw unresolved identity: home=%q project=%q", observedHome, observedProject)
	}
}

type workerRouterTestSpawner struct {
	calls int
}

func (s *workerRouterTestSpawner) Spawn(_ context.Context, _, _, _ string) (worker.Process, io.ReadCloser, io.WriteCloser, error) {
	s.calls++
	return &workerRouterTestProcess{}, nil, nil, nil
}

func (s *workerRouterTestSpawner) StreamFormat() worker.StreamFormat {
	return worker.StreamFormatClaudeJSON
}

type workerRouterTestProcess struct{}

func (p *workerRouterTestProcess) Wait() error { return nil }
func (p *workerRouterTestProcess) Kill() error { return nil }

func TestWorkerMemoryRetired(t *testing.T) {
	if store := openWorkerMemoryStore(nil); store != nil {
		t.Fatalf("openWorkerMemoryStore() = %#v, want nil after memory retirement", store)
	}
	services := newDispatcherMemoryServices(nil)
	if services.Store != nil {
		t.Fatalf("dispatcher memory store = %#v, want nil after memory retirement", services.Store)
	}
}

func TestWorkerCmdDoesNotImportMemory(t *testing.T) {
	src, err := os.ReadFile("cmd_worker.go")
	if err != nil {
		t.Fatalf("read cmd_worker.go: %v", err)
	}
	if strings.Contains(string(src), `"oro/pkg/memory"`) {
		t.Fatal("cmd_worker.go must not import oro/pkg/memory")
	}
}
