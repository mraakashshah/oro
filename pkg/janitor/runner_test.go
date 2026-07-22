package janitor //nolint:testpackage // verifies the package-private leased subprocess runner lifecycle.

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"oro/pkg/storage"
)

func TestJanitorCommandsUseRuntimeLease(t *testing.T) {
	assertDetectorSubprocessesUseRunner(t)

	worktree := t.TempDir()
	catalogPath := filepath.Join(t.TempDir(), "catalog.db")
	catalog, err := storage.OpenCatalog(t.Context(), catalogPath)
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })

	started := filepath.Join(worktree, "started")
	release := filepath.Join(worktree, "release")
	acquired := filepath.Join(worktree, "acquired")
	scriptPath := filepath.Join(worktree, detectScriptPath)
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o750); err != nil {
		t.Fatalf("create detector script directory: %v", err)
	}
	script := "#!/bin/sh\n" +
		"test -f \"$JANITOR_ACQUIRED\" || exit 90\n" +
		"touch \"$JANITOR_STARTED\"\n" +
		"while [ ! -e \"$JANITOR_RELEASE\" ]; do sleep 0.01; done\n" +
		"printf '%s\\n' '{\"detector\":\"test\",\"file\":\"runner.go\",\"title\":\"leased\",\"detail\":\"leased\",\"line\":1}'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o750); err != nil {
		t.Fatalf("write detector script: %v", err)
	}
	now := time.Now().UTC()
	leaseID := storage.LeaseID("janitor-command")
	runtime := storage.RuntimeRequest{
		Catalog: &orderingLeaseCatalog{RuntimeLeaseCatalog: catalog, acquired: acquired},
		Lease: storage.LeaseRequest{
			ID:           leaseID,
			ControllerID: "janitor-controller",
			OwnerID:      "janitor-owner",
			PID:          os.Getpid(),
			ProcessStart: now,
			AcquiredAt:   now,
			HeartbeatAt:  now,
		},
		Env: []string{
			"ORO_SUBPROCESS_TMP_ROOT=" + filepath.Join(t.TempDir(), "scratch"),
			"JANITOR_ACQUIRED=" + acquired,
			"JANITOR_STARTED=" + started,
			"JANITOR_RELEASE=" + release,
		},
		Workdir: worktree,
	}

	type detectResult struct {
		candidates []Candidate
		found      bool
		err        error
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan detectResult, 1)
	go func() {
		candidates, _, found, runErr := RunDetectScript(ctx, worktree, WithRuntime(runtime))
		done <- detectResult{candidates: candidates, found: found, err: runErr}
	}()
	waitForFile(t, started)

	lease, err := catalog.Lease(t.Context(), leaseID)
	if err != nil {
		t.Fatalf("load active lease: %v", err)
	}
	if lease.ReleasedAt != nil {
		t.Fatal("janitor command lease released before command exit")
	}
	plan := storage.PlanCleanup(storage.Snapshot{CatalogHealthy: true, Candidates: []storage.Candidate{{
		Path:        lease.ScratchPath,
		Scope:       storage.ScopeRuntime,
		Allowlisted: true,
		Owned:       true,
		LeaseActive: lease.ReleasedAt == nil,
	}}}, storage.StoragePolicy{DeletionAuthorized: true}, storage.ScopeRuntime)
	if len(plan.Decisions) != 1 || plan.Decisions[0].PreserveReason != storage.PreserveActive {
		t.Fatalf("active janitor lease cleanup decision = %#v, want active-lease preservation", plan.Decisions)
	}

	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatalf("release command: %v", err)
	}
	var result detectResult
	select {
	case result = <-done:
	case <-ctx.Done():
		t.Fatalf("wait for leased janitor command: %v", ctx.Err())
	}
	if result.err != nil || !result.found || len(result.candidates) != 1 {
		t.Fatalf("RunDetectScript() = candidates:%v found:%t err:%v", result.candidates, result.found, result.err)
	}
	lease, err = catalog.Lease(t.Context(), leaseID)
	if err != nil {
		t.Fatalf("load released lease: %v", err)
	}
	if lease.ReleasedAt == nil {
		t.Fatal("janitor command lease remained active after command exit")
	}

	assertAnalyzerFindingsSurviveNonzeroExit(t, catalog, runtime, worktree)
}

type orderingLeaseCatalog struct {
	storage.RuntimeLeaseCatalog
	acquired string
}

func (catalog *orderingLeaseCatalog) AcquireLease(ctx context.Context, request storage.LeaseRequest) (storage.Lease, error) {
	lease, err := catalog.RuntimeLeaseCatalog.AcquireLease(ctx, request)
	if err != nil {
		return storage.Lease{}, fmt.Errorf("acquire wrapped lease: %w", err)
	}
	if err := os.WriteFile(catalog.acquired, nil, 0o600); err != nil {
		return storage.Lease{}, fmt.Errorf("write acquired marker: %w", err)
	}
	return lease, nil
}

func assertAnalyzerFindingsSurviveNonzeroExit(t *testing.T, catalog *storage.Catalog, runtime storage.RuntimeRequest, worktree string) {
	t.Helper()
	binDir := t.TempDir()
	binary := filepath.Join(binDir, "golangci-lint")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' 'main.go:1: detector finding'\nexit 1\n"), 0o750); err != nil {
		t.Fatalf("write analyzer: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "main.go"), []byte("package test\n"), 0o600); err != nil {
		t.Fatalf("write analyzer target: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	runtime.Catalog = catalog
	runtime.Lease.ID = "janitor-analyzer"
	runtime.Env = append(os.Environ(), "ORO_SUBPROCESS_TMP_ROOT="+filepath.Join(t.TempDir(), "analyzer-scratch"))
	candidates, err := RunBuiltin(t.Context(), worktree, "", "golangci-lint", WithRuntime(runtime))
	if err != nil {
		t.Fatalf("RunBuiltin() with finding exit: %v", err)
	}
	if len(candidates) != 1 || candidates[0].File != "main.go" || candidates[0].Line != 1 {
		t.Fatalf("RunBuiltin() candidates = %#v, want analyzer stdout finding", candidates)
	}
}

func assertDetectorSubprocessesUseRunner(t *testing.T) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "detect.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse detector imports: %v", err)
	}
	execAliases := make(map[string]struct{})
	for _, imported := range file.Imports {
		path, unquoteErr := strconv.Unquote(imported.Path.Value)
		if unquoteErr != nil {
			t.Fatalf("parse detector import path: %v", unquoteErr)
		}
		if path != "os/exec" {
			continue
		}
		name := "exec"
		if imported.Name != nil {
			name = imported.Name.Name
		}
		execAliases[name] = struct{}{}
	}

	file, err = parser.ParseFile(token.NewFileSet(), "detect.go", nil, 0)
	if err != nil {
		t.Fatalf("parse detector source: %v", err)
	}
	directCalls := 0
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "CommandContext" {
			return true
		}
		identifier, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}
		if _, ok := execAliases[identifier.Name]; ok {
			directCalls++
		}
		return true
	})
	if directCalls != 0 {
		t.Fatalf("detect.go has %d direct os/exec.CommandContext call(s); route every subprocess through runner", directCalls)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
