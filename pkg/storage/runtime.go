package storage

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// RuntimeLeaseCatalog persists the lifecycle of a runtime lease.
//
//oro:testonly — production subprocess spawners adopt this contract in follow-up storage lifecycle work.
type RuntimeLeaseCatalog interface {
	AcquireLease(context.Context, LeaseRequest) (Lease, error)
	ReleaseLease(context.Context, LeaseID) error
}

// RuntimeRequest supplies the policy and lease metadata for one worktree runtime.
//
//oro:testonly — production subprocess spawners adopt this contract in follow-up storage lifecycle work.
type RuntimeRequest struct {
	Catalog RuntimeLeaseCatalog
	Lease   LeaseRequest
	Env     []string
	Workdir string
	Policy  StoragePolicy
}

// RuntimeHandle owns one lease-protected subprocess environment.
//
//oro:testonly — production subprocess spawners adopt this contract in follow-up storage lifecycle work.
type RuntimeHandle struct {
	Env        []string
	ScratchDir string

	catalog  RuntimeLeaseCatalog
	leaseID  LeaseID
	close    sync.Once
	closeErr error
}

// OpenRuntime resolves cache and scratch paths, then records an active lease.
// Callers must keep the returned handle open until their spawned process has
// completed, including cancellation and recovered-panic paths.
//
//oro:testonly — production subprocess spawners adopt this contract in follow-up storage lifecycle work.
func OpenRuntime(ctx context.Context, request RuntimeRequest) (*RuntimeHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("open runtime context: %w", err)
	}
	if request.Catalog == nil {
		return nil, fmt.Errorf("runtime catalog is nil")
	}
	if request.Workdir == "" {
		return nil, fmt.Errorf("runtime workdir is empty")
	}

	resolved, err := ResolveCacheEnv(request.Env, request.Workdir, request.Policy)
	if err != nil {
		return nil, fmt.Errorf("resolve runtime cache environment: %w", err)
	}
	scratchDir, err := runtimeScratchDir(resolved.Env, request.Workdir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(scratchDir, 0o750); err != nil {
		return nil, fmt.Errorf("create runtime scratch directory: %w", err)
	}

	env := withRuntimeScratch(resolved.Env, scratchDir)
	request.Lease.Namespace = filepath.Base(scratchDir)
	request.Lease.ScratchPath = scratchDir
	lease, err := request.Catalog.AcquireLease(ctx, request.Lease)
	if err != nil {
		return nil, fmt.Errorf("acquire runtime lease: %w", err)
	}
	return &RuntimeHandle{
		Env:        env,
		ScratchDir: scratchDir,
		catalog:    request.Catalog,
		leaseID:    lease.ID,
	}, nil
}

// Close releases the runtime lease exactly once. It deliberately uses a fresh
// background context so cancellation of the spawned process cannot leak its
// completed runtime lease.
func (handle *RuntimeHandle) Close() error {
	if handle == nil {
		return nil
	}
	handle.close.Do(func() {
		if err := handle.catalog.ReleaseLease(context.Background(), handle.leaseID); err != nil {
			handle.closeErr = fmt.Errorf("release runtime lease: %w", err)
		}
	})
	return handle.closeErr
}

func runtimeScratchDir(env []string, workdir string) (string, error) {
	canonical, err := canonicalCachePath(workdir)
	if err != nil {
		return "", fmt.Errorf("resolve runtime workdir %q: %w", workdir, err)
	}
	root := runtimeScratchRoot(env)
	return filepath.Join(root, runtimeScratchToken(canonical)), nil
}

func runtimeScratchRoot(env []string) string {
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && key == "ORO_SUBPROCESS_TMP_ROOT" && value != "" {
			return value
		}
	}
	root := os.TempDir()
	if runtime.GOOS == "darwin" {
		root = "/tmp"
	}
	return filepath.Join(root, "oro-subprocess")
}

func runtimeScratchToken(workdir string) string {
	sum := sha256.Sum256([]byte(workdir))
	return fmt.Sprintf("%x", sum[:8])
}

func withRuntimeScratch(env []string, scratchDir string) []string {
	result := make([]string, 0, len(env)+3)
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if ok && (key == "TMPDIR" || key == "TMP" || key == "TEMP") {
			continue
		}
		result = append(result, entry)
	}
	return append(result,
		"TMPDIR="+scratchDir,
		"TMP="+scratchDir,
		"TEMP="+scratchDir,
	)
}
