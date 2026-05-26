package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"oro/pkg/dbutil"
	"oro/pkg/dispatcher"
	"oro/pkg/protocol"
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

// TestOpenWorkerMemoryDB verifies that openWorkerMemoryDB opens a SQLite
// connection and creates a valid memory.Store. This ensures the worker memory
// wiring path works end-to-end.
func TestOpenWorkerMemoryDB(t *testing.T) {
	// Use a temp file for the DB so we can verify it opens correctly.
	dsn := fmt.Sprintf("file:worker_mem_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := dbutil.OpenDB(dsn)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	defer func() { _ = db.Close() }()

	store := openWorkerMemoryStore(db)
	if store == nil {
		t.Fatal("expected non-nil memory store from openWorkerMemoryStore")
	}
}

// TestWorkerMemoryStoreHasEmbedder verifies that openWorkerMemoryStore wires
// an Embedder so worker-inserted memories get TF-IDF embeddings.
func TestWorkerMemoryStoreHasEmbedder(t *testing.T) {
	dsn := fmt.Sprintf("file:worker_emb_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := dbutil.OpenDB(dsn)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	defer func() { _ = db.Close() }()

	store := openWorkerMemoryStore(db)
	if !store.HasEmbedder() {
		t.Fatal("expected non-nil embedder in worker memory store")
	}
}

func TestDispatcherMemoryServicesAdapter(t *testing.T) {
	dsn := fmt.Sprintf("file:dispatcher_mem_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := dbutil.OpenDB(dsn)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(context.Background(), protocol.SchemaDDL); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	services := newDispatcherMemoryServices(db)
	if services.Store == nil || !services.Store.HasEmbedder() {
		t.Fatal("expected memory-backed store with embedder")
	}

	ctx := context.Background()
	if _, err := services.Store.Insert(ctx, protocol.MemoryInsertParams{
		Content:    "adapter stores dispatcher memories",
		Type:       "lesson",
		Source:     "test",
		Confidence: 0.8,
	}); err != nil {
		t.Fatalf("insert memory through adapter: %v", err)
	}
	if err := services.InsertRejection(ctx, "bead-1", "worker-1", "needs more tests"); err != nil {
		t.Fatalf("insert rejection through adapter: %v", err)
	}
	rejections, err := services.GetRejections(ctx, "bead-1")
	if err != nil {
		t.Fatalf("get rejections through adapter: %v", err)
	}
	if len(rejections) != 1 || rejections[0].Feedback != "needs more tests" {
		t.Fatalf("unexpected rejections: %+v", rejections)
	}

	if err := services.ExecuteDream(ctx, []dispatcher.DreamAction{{
		Kind: "CREATE",
		Params: protocol.MemoryInsertParams{
			Content: "dream adapter creates memories",
			Type:    "pattern",
			Source:  "dreamer",
		},
	}}, func(msg string) { t.Log(msg) }); err != nil {
		t.Fatalf("execute dream through adapter: %v", err)
	}
	var dreamCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memories WHERE source = 'dreamer'`).Scan(&dreamCount); err != nil {
		t.Fatalf("count dream memories: %v", err)
	}
	if dreamCount != 1 {
		t.Fatalf("dream memory count = %d, want 1", dreamCount)
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
