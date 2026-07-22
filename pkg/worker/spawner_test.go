package worker_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"oro/pkg/storage"
	"oro/pkg/worker"
)

func TestWorkerSpawnerUsesRuntimeLease(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.Mkdir(binDir, 0o750); err != nil {
		t.Fatalf("create fake binary directory: %v", err)
	}
	workerProbe := filepath.Join(root, "worker.env")
	workerRelease := filepath.Join(root, "worker.release")
	writeBlockingProbe(t, filepath.Join(binDir, "claude"), workerProbe, workerRelease)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ORO_SUBPROCESS_TMP_ROOT", filepath.Join(root, "scratch"))

	sharedCache := filepath.Join(root, "shared-cache")
	workerDir := filepath.Join(root, "worker")
	qgDir := filepath.Join(root, "quality-gate")
	for _, dir := range []string{workerDir, qgDir} {
		if err := os.Mkdir(dir, 0o750); err != nil {
			t.Fatalf("create workdir %q: %v", dir, err)
		}
	}

	workerCatalog := &recordingWorkerLeaseCatalog{}
	spawner := &worker.ClaudeSpawner{Runtime: workerRuntimeRequest(t, workerCatalog, workerDir, sharedCache, "worker")}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	process, stdout, _, err := spawner.Spawn(ctx, "balanced", "test prompt", workerDir)
	if err != nil {
		t.Fatalf("worker Spawn() error = %v", err)
	}
	defer func() { _ = stdout.Close() }()
	waitForWorkerProbe(t, workerProbe)
	if !workerCatalog.Active() {
		t.Fatal("worker lease was not active while child was running")
	}
	workerCache, workerScratch := readWorkerProbe(t, workerProbe)
	if err := os.WriteFile(workerRelease, nil, 0o600); err != nil {
		t.Fatalf("release worker child: %v", err)
	}
	if err := process.Wait(); err != nil {
		t.Fatalf("worker Wait() error = %v", err)
	}
	if workerCatalog.Active() || workerCatalog.Releases() != 1 {
		t.Fatalf("worker lease after Wait = active:%t releases:%d, want inactive after one release", workerCatalog.Active(), workerCatalog.Releases())
	}

	qgProbe := filepath.Join(root, "qg.env")
	qgRelease := filepath.Join(root, "qg.release")
	writeBlockingProbe(t, filepath.Join(qgDir, "quality_gate.sh"), qgProbe, qgRelease)
	qgCatalog := &recordingWorkerLeaseCatalog{}
	qgRuntime := workerRuntimeRequest(t, qgCatalog, qgDir, sharedCache, "quality-gate")
	type qgResult struct {
		passed bool
		output string
		err    error
	}
	qgDone := make(chan qgResult, 1)
	go func() {
		passed, output, runErr := worker.RunQualityGateWithRuntime(ctx, qgDir, true, qgRuntime)
		qgDone <- qgResult{passed: passed, output: output, err: runErr}
	}()

	waitForWorkerProbe(t, qgProbe)
	if !qgCatalog.Active() {
		t.Fatal("quality-gate lease was not active while child was running")
	}
	qgCache, qgScratch := readWorkerProbe(t, qgProbe)
	if err := os.WriteFile(qgRelease, nil, 0o600); err != nil {
		t.Fatalf("release quality-gate child: %v", err)
	}
	result := <-qgDone
	if result.err != nil || !result.passed {
		t.Fatalf("RunQualityGateWithRuntime() = passed:%t output:%q err:%v", result.passed, result.output, result.err)
	}
	if qgCatalog.Active() || qgCatalog.Releases() != 1 {
		t.Fatalf("quality-gate lease after Wait = active:%t releases:%d, want inactive after one release", qgCatalog.Active(), qgCatalog.Releases())
	}

	if workerCache != sharedCache || qgCache != sharedCache {
		t.Fatalf("shared cache paths = worker:%q qg:%q, want %q", workerCache, qgCache, sharedCache)
	}
	if workerScratch == "" || qgScratch == "" || workerScratch == qgScratch {
		t.Fatalf("scratch paths = worker:%q qg:%q, want non-empty unique paths", workerScratch, qgScratch)
	}
}

type recordingWorkerLeaseCatalog struct {
	mu       sync.Mutex
	active   bool
	releases int
}

func (catalog *recordingWorkerLeaseCatalog) AcquireLease(_ context.Context, request storage.LeaseRequest) (storage.Lease, error) {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	catalog.active = true
	return storage.Lease{LeaseRequest: request}, nil
}

func (catalog *recordingWorkerLeaseCatalog) ReleaseLease(_ context.Context, _ storage.LeaseID) error {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	catalog.active = false
	catalog.releases++
	return nil
}

func (catalog *recordingWorkerLeaseCatalog) Active() bool {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	return catalog.active
}

func (catalog *recordingWorkerLeaseCatalog) Releases() int {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	return catalog.releases
}

func workerRuntimeRequest(t *testing.T, catalog storage.RuntimeLeaseCatalog, workdir, sharedCache, owner string) storage.RuntimeRequest {
	t.Helper()
	now := time.Now().UTC()
	return storage.RuntimeRequest{
		Catalog: catalog,
		Lease: storage.LeaseRequest{
			ID:           storage.LeaseID(t.Name() + "-" + owner),
			ControllerID: "worker-test-controller",
			OwnerID:      owner,
			PID:          os.Getpid(),
			ProcessStart: now,
			AcquiredAt:   now,
			HeartbeatAt:  now,
		},
		Env:     os.Environ(),
		Workdir: workdir,
		Policy: storage.StoragePolicy{Providers: []storage.CacheProvider{{
			ID:          "worker-shared-cache",
			Variables:   []string{"ORO_TEST_SHARED_CACHE"},
			Scope:       storage.UserScope,
			DefaultPath: func() string { return sharedCache },
			Concurrency: storage.Concurrent,
			Ownership:   storage.OroManaged,
		}}},
	}
}

func writeBlockingProbe(t *testing.T, path, probe, release string) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"printf '%s\\n%s\\n' \"$ORO_TEST_SHARED_CACHE\" \"$TMPDIR\" > \"" + probe + "\"\n" +
		"while [ ! -f \"" + release + "\" ]; do sleep 0.01; done\n"
	if err := os.WriteFile(path, []byte(script), 0o750); err != nil {
		t.Fatalf("write blocking probe %q: %v", path, err)
	}
}

func waitForWorkerProbe(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for child probe %q", path)
}

func readWorkerProbe(t *testing.T, path string) (cache, scratch string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read child probe %q: %v", path, err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("child probe %q = %q, want cache and scratch lines", path, data)
	}
	return lines[0], lines[1]
}
