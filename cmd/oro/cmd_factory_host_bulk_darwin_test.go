//go:build darwin

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1" // Git's default object format, not an authority digest.
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"oro/pkg/factory"
	"oro/pkg/storage"
)

// This fixture allocates real persistent capacity but seeds private admitted
// journal rows. It proves the transport, not public start or core admission.
// T6c may use runtime's existing test seams, reserveScope, invocation and retire;
// there is no production fault environment or replacement launchctl here.
type factoryHostBulkFixture struct {
	base        string
	config      factoryLaunchdConfig
	spec        factory.ManagedSpec
	limits      factory.HostRetentionLimits
	reservation factory.HostReservation
	deadline    time.Time
	db          *sql.DB
	runtime     *factoryLaunchdRuntime
	scopes      []factory.ScopeHandle
	receipt     factoryHostBulkReceipt
}

type factoryHostBulkReceipt struct {
	Config                 factoryLaunchdConfig
	Spec                   factory.ManagedSpec
	Limits                 factory.HostRetentionLimits
	Reservation            factory.HostReservation
	Capacity               storage.DiskUsage
	FreshDecision          storage.BudgetDecision
	Deadline               time.Time
	Calls                  []factoryHostBulkCall
	ElapsedSeconds         float64
	SelfHighWaterRSSBytes  int64
	RetainedLogicalBytes   int64
	RetainedAllocatedBytes int64
	Released               bool
}

type factoryHostBulkCall struct {
	Scope                    factory.ScopeHandle
	Observation              factory.ScopeObservation
	Deadline                 time.Time
	RequestBytes             int64
	RequestSHA256            string
	PendingBytes             int64
	PendingSHA256            string
	TranscriptEncodedBytes   int64
	StdinBytes               int
	StdinSHA256              string
	StdoutBytes, StderrBytes int
	StdoutSHA256             string
	SampledWrapperRSSBytes   int64
	RSSSampleIntervalMS      int
	ElapsedSeconds           float64
	AuthenticatedPublished   bool
}

func TestFactoryHostBulkNative(t *testing.T) {
	if os.Getenv("ORO_FACTORY_INTEGRATION") != "1" {
		t.Skip("native host bulk requires ORO_FACTORY_INTEGRATION=1")
	}
	f := newFactoryHostBulkFixture(t)
	started := time.Now()
	data := make([]byte, 32<<20)
	// Deterministic binary, including NUL and invalid UTF-8, with modest entropy
	// across blocks so the test is independent of text and shell encodings.
	for offset := 0; offset < len(data); offset += sha256.Size {
		sum := sha256.Sum256([]byte(strconv.Itoa(offset)))
		copy(data[offset:], sum[:])
	}
	objectHash := sha1.New() //nolint:gosec // Exact SHA-1 Git blob identity; byte integrity also uses SHA-256.
	_, _ = fmt.Fprintf(objectHash, "blob %d\x00", len(data))
	_, _ = objectHash.Write(data)
	wantOID := hex.EncodeToString(objectHash.Sum(nil))
	inputScope := f.reserveScope(t, f.deadline)
	inward := f.invoke(t, f.invocation(inputScope, []string{"hash-object", "-w", "--stdin", "--no-filters"}, data, 256))
	if string(inward.Stdout) != wantOID+"\n" {
		t.Fatalf("inward Git identity differs: %q", inward.Stdout)
	}
	outputScope := f.reserveScope(t, f.deadline)
	outward := f.invoke(t, f.invocation(outputScope, []string{"cat-file", "blob", wantOID}, nil, int64(len(data))))
	if len(outward.Stdout) != len(data) || sha256.Sum256(outward.Stdout) != sha256.Sum256(data) || !bytes.Equal(outward.Stdout, data) {
		t.Fatal("actual binary round-trip changed bytes")
	}
	f.receipt.ElapsedSeconds = time.Since(started).Seconds()
	f.finish(t)
}

func newFactoryHostBulkFixture(t *testing.T) *factoryHostBulkFixture {
	t.Helper()
	parent := os.Getenv("ORO_FACTORY_EVIDENCE_DIR")
	if parent == "" {
		t.Fatal("native bulk requires persistent ORO_FACTORY_EVIDENCE_DIR; uncertain authority must survive test failure")
	}
	base, err := os.MkdirTemp(parent, "host-bulk-")
	if err != nil {
		t.Fatal(err)
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	f := &factoryHostBulkFixture{base: base}
	f.preparePrograms(t)
	f.prepareBudget(t)
	f.seedJournal(t)
	f.runtime, err = openFactoryLaunchdRuntime(t.Context(), f.config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.close(t) })
	f.receipt.Config, f.receipt.Spec, f.receipt.Limits = f.config, f.spec, f.limits
	f.receipt.Reservation, f.receipt.Deadline = f.reservation, f.deadline
	if err := releaseFactoryHostReservation(t.Context(), f.config.StateRoot, f.reservation.ID); err == nil {
		t.Fatal("running objective released capacity")
	}
	return f
}

func (f *factoryHostBulkFixture) preparePrograms(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	gitOutput, err := exec.CommandContext(ctx, "/usr/bin/xcrun", "--find", "git").Output()
	if err != nil {
		t.Fatalf("resolve actual Git: %v", err)
	}
	git, err := filepath.EvalSymlinks(strings.TrimSpace(string(gitOutput)))
	if err != nil || git == "/usr/bin/git" {
		t.Fatalf("Git must be the actual developer-tool executable: %q %v", git, err)
	}
	binary := filepath.Join(f.base, "oro")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, ".")
	if output, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("build private frozen Oro: %v %s", buildErr, output)
	}
	if err := os.Chmod(binary, 0o500); err != nil {
		t.Fatal(err)
	}
	f.config = factoryLaunchdConfig{
		StateRoot: filepath.Join(f.base, "state"), ObjectiveID: "bulk-native",
		OroPath: binary, OroSHA256: factoryHostBulkFileHash(t, binary, 512<<20),
		GitPath: git, GitSHA256: factoryHostBulkFileHash(t, git, 512<<20),
		Repository: filepath.Join(f.base, "repository"), TargetRef: "refs/heads/bulk-native", CleanupTimeout: 15 * time.Second,
	}
	if err := os.Mkdir(f.config.Repository, 0o700); err != nil {
		t.Fatal(err)
	}
	init := exec.CommandContext(ctx, git, "-c", "core.hooksPath=/dev/null", "init", "--bare", "--object-format=sha1", f.config.Repository)
	init.Env = factoryObserverEnvironment()
	if output, initErr := init.CombinedOutput(); initErr != nil {
		t.Fatalf("initialize private bare Git fixture: %v %s", initErr, output)
	}
}

func (f *factoryHostBulkFixture) prepareBudget(t *testing.T) {
	t.Helper()
	f.spec = factory.ManagedSpec{
		ID: f.config.ObjectiveID, Objective: "prove exact native binary transport", Repository: f.config.Repository,
		TargetBranch: "bulk-native", RequiredChecks: []string{"exact"}, MaxAttempts: 1, TimeoutSeconds: 90, StepTimeoutSeconds: 60,
		Recovery: factory.RecoveryPolicy{
			MaxControllerStarts: 1, MaxSupervisorStarts: 1, RestartDelaySeconds: 1,
			CleanupTimeoutSeconds: 15, ReconciliationSeconds: 5, MaxReconciliationInvocations: 1, MaxCleanupInvocations: 1,
		},
		Runtime: factory.RuntimePolicy{
			DockerContext: "colima-oro-factory", ImageDigest: "sha256:" + strings.Repeat("a", 64),
			MemoryBytes: 256 << 20, ScratchBytes: 1 << 20, SourceBytes: 32 << 20, HostRequiredBytes: 1, HostReserveBytes: 1 << 30,
			CPUs: 1, PIDs: 16, MaxFiles: 16, SourceInodes: 16, ScratchInodes: 16, MaxModelCalls: 1, MaxModelTurns: 1,
			MaxToolCalls: 1, MaxHostInvocations: 8, Model: "unused-fixture", ModelExecutable: f.config.OroPath,
			GitExecutable: f.config.GitPath, MaxModelInvocationUSD: "0.01",
		},
		Acceptance: []factory.Check{{ID: "exact", Args: []string{"/bin/true"}}}, ReviewInstructions: "fixture only",
	}
	var err error
	f.limits, err = factory.ManagedHostRetention(f.spec)
	if err != nil {
		t.Fatal(err)
	}
	f.spec.Runtime.HostRequiredBytes = f.limits.MinimumBytes
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	probe := func(ctx context.Context, path string) (storage.DiskUsage, error) {
		usage, probeErr := factoryHostBulkDiskProbe(ctx, path)
		f.receipt.Capacity = usage
		return usage, probeErr
	}
	f.receipt.FreshDecision, err = storage.CheckRuntimeBudget(ctx, probe, storage.RuntimeBudgetRequest{
		Root: f.config.StateRoot, RequiredBytes: f.spec.Runtime.HostRequiredBytes, MinFreeBytes: f.spec.Runtime.HostReserveBytes, Fresh: true,
	})
	if err != nil || f.receipt.FreshDecision.Mode != storage.BudgetFresh {
		t.Fatalf("real capacity unavailable: %+v %v", f.receipt.FreshDecision, err)
	}
	f.reservation, err = admitFactoryHostBudget(ctx, f.config.StateRoot, f.spec, probe)
	if err != nil {
		t.Fatalf("real serialized persistent allocation: %v", err)
	}
	if f.reservation.Bytes != f.limits.MinimumBytes || f.reservation.StateRoot != f.config.StateRoot || f.reservation.ObjectiveID != f.spec.ID {
		t.Fatal("allocator returned a different reservation tuple")
	}
	f.deadline = time.Now().UTC().Add(90 * time.Second)
	f.config.HardStop = f.deadline.Add(20 * time.Second)
}

func factoryHostBulkDiskProbe(ctx context.Context, root string) (storage.DiskUsage, error) {
	if err := ctx.Err(); err != nil {
		return storage.DiskUsage{}, err
	}
	// Fresh admission probes the existing canonical parent without creating it.
	var stat syscall.Statfs_t
	if err := syscall.Statfs(filepath.Dir(root), &stat); err != nil {
		return storage.DiskUsage{}, fmt.Errorf("native Statfs: %w", err)
	}
	if stat.Bsize <= 0 || stat.Blocks > math.MaxInt64/uint64(stat.Bsize) || stat.Bavail > stat.Blocks {
		return storage.DiskUsage{}, errors.New("invalid or overflowing native filesystem capacity")
	}
	return storage.DiskUsage{CapacityBytes: stat.Blocks * uint64(stat.Bsize), FreeBytes: stat.Bavail * uint64(stat.Bsize)}, ctx.Err()
}

func (f *factoryHostBulkFixture) seedJournal(t *testing.T) {
	t.Helper()
	dir := filepath.Join(f.config.StateRoot, f.spec.ID)
	namespace := filepath.Join(f.config.StateRoot, ".managed")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// The persistent allocator owns this already-created namespace. Never
	// replace or recreate its authority while adding the private journal fixture.
	if err := factoryHostPrivateDirectory(namespace); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(namespace, f.spec.ID+".state.init"), filepath.Join(dir, "state.init")} {
		if err := os.WriteFile(path, []byte("oro-managed-state-v1;page=4096;data=67108864;allowance=268435456\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	f.db, err = sql.Open("sqlite", filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Close() })
	f.db.SetMaxOpenConns(1)
	_, err = f.db.ExecContext(t.Context(), `PRAGMA page_size=4096; PRAGMA journal_mode=DELETE; PRAGMA synchronous=FULL; PRAGMA busy_timeout=1000;
CREATE TABLE managed_admissions(id INTEGER PRIMARY KEY,body BLOB NOT NULL);
CREATE TABLE managed_supervision(id INTEGER PRIMARY KEY,revision INTEGER NOT NULL,body BLOB NOT NULL);
CREATE TABLE managed_scopes(id TEXT PRIMARY KEY,nonce TEXT NOT NULL UNIQUE,backend TEXT NOT NULL,name TEXT NOT NULL,body BLOB NOT NULL,phase TEXT NOT NULL,pid INTEGER NOT NULL DEFAULT 0,pgid INTEGER NOT NULL DEFAULT 0,cleanup TEXT NOT NULL DEFAULT '',UNIQUE(backend,name));
CREATE TABLE managed_events(revision INTEGER PRIMARY KEY,kind TEXT NOT NULL,ref TEXT NOT NULL,body BLOB NOT NULL);
CREATE TABLE managed_reservations(kind TEXT NOT NULL,token TEXT NOT NULL,generation INTEGER NOT NULL,PRIMARY KEY(kind,token));`)
	if err != nil {
		t.Fatal(err)
	}
	admission := map[string]any{
		"ObjectiveID": f.spec.ID, "ManifestHash": factoryHostBulkJSONHash(t, f.spec), "Deadline": f.deadline,
		"HardStop": f.config.HardStop, "Recovery": f.spec.Recovery, "Runtime": f.spec.Runtime, "Reservation": f.reservation,
	}
	body, err := json.Marshal(admission)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ExecContext(t.Context(), "INSERT INTO managed_admissions VALUES(1,?)", body); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ExecContext(t.Context(), "INSERT INTO managed_supervision VALUES(1,1,?)", []byte(`{"Revision":1,"Generation":1,"SupervisorToken":"fixture","DesiredState":"running","Reason":"","NextStart":"0001-01-01T00:00:00Z"}`)); err != nil {
		t.Fatal(err)
	}
}

func (f *factoryHostBulkFixture) reserveScope(t *testing.T, deadline time.Time) factory.ScopeHandle {
	t.Helper()
	if len(f.scopes) >= f.spec.Runtime.MaxHostInvocations || !time.Now().Before(deadline) || deadline.After(f.deadline) {
		t.Fatal("fixture scope exceeds its original finite allowance")
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	scope := factory.ScopeHandle{ID: hex.EncodeToString(random[:16]), Nonce: hex.EncodeToString(random[16:]), Kind: "host", Backend: "launchd", Generation: 1}
	scope.Name = "dev.getoro.factory." + factoryLaunchdObjectiveHash(f.config) + ".host." + scope.ID + "." + scope.Nonce
	body, err := json.Marshal(struct {
		factory.ScopeHandle
		Deadline time.Time
	}{scope, deadline})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck // A successful commit has already closed the transaction.
	var desired string
	if err := tx.QueryRowContext(t.Context(), "SELECT json_extract(body,'$.DesiredState') FROM managed_supervision").Scan(&desired); err != nil || desired != "running" {
		t.Fatalf("fixture authority sealed: %s %v", desired, err)
	}
	if _, err := tx.ExecContext(t.Context(), "INSERT INTO managed_reservations VALUES('host',?,1)", scope.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), "INSERT INTO managed_scopes(id,nonce,backend,name,body,phase) VALUES(?,?,?,?,?,'reserved')", scope.ID, scope.Nonce, scope.Backend, scope.Name, body); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	f.scopes = append(f.scopes, scope)
	return scope
}

func (f *factoryHostBulkFixture) invocation(scope factory.ScopeHandle, args []string, input []byte, outputLimit int64) factory.HostInvocation {
	return factory.HostInvocation{
		Scope: scope, Path: f.config.GitPath,
		Args: append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "--no-optional-locks", "--no-replace-objects"}, args...),
		Env:  factoryObserverEnvironment(), Dir: f.config.Repository, Stdin: input, Deadline: f.deadline, OutputLimit: outputLimit,
	}
}

func (f *factoryHostBulkFixture) invoke(t *testing.T, invocation factory.HostInvocation) factory.HostResult {
	t.Helper()
	started := time.Now()
	if int64(len(invocation.Stdin)) > f.limits.ArchiveBytes || invocation.OutputLimit <= 0 || invocation.OutputLimit > f.limits.HostOutputBytes || invocation.Deadline.After(f.deadline) {
		t.Fatal("fixture invocation exceeds original admitted transport bounds")
	}
	ctx, cancel := context.WithDeadline(t.Context(), invocation.Deadline)
	defer cancel()
	stopSamples := f.sampleWrapper(ctx, invocation.Scope)
	result, err := f.runtime.Invoke(ctx, invocation)
	peak := stopSamples()
	if err != nil || result.ExitCode != 0 || len(result.Stderr) != 0 {
		t.Fatalf("native literal Git invocation: exit=%d stderr=%q error=%v", result.ExitCode, result.Stderr, err)
	}
	observation := f.retire(t, invocation.Scope)
	if !observation.Deadline.Equal(invocation.Deadline) {
		t.Fatal("retired scope changed its original deadline")
	}
	if peak <= 0 {
		t.Fatal("no authenticated wrapper RSS sample was observed")
	}
	f.recordCall(t, invocation, result, observation, peak, time.Since(started))
	return result
}

func (f *factoryHostBulkFixture) retire(t *testing.T, scope factory.ScopeHandle) factory.ScopeObservation {
	t.Helper()
	if err := f.runtime.Retire(context.Background(), scope); err != nil {
		t.Fatalf("retire exact owned scope: %v", err)
	}
	observation, err := f.runtime.authority.Observe(t.Context(), scope)
	if err != nil || observation.Phase != "quiescent" || observation.PID <= 1 || observation.PGID != observation.PID {
		t.Fatalf("scope has no authenticated positive retirement: %+v %v", observation, err)
	}
	if !errors.Is(syscall.Kill(-observation.PGID, 0), syscall.ESRCH) {
		t.Fatal("authenticated scope process group remains live or uncertain")
	}
	return observation
}

func (f *factoryHostBulkFixture) sampleWrapper(ctx context.Context, scope factory.ScopeHandle) func() int64 {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan int64, 1)
	go func() {
		var peak int64
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		defer func() { done <- peak }()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				peak = max(peak, f.wrapperRSS(ctx, scope))
			}
		}
	}()
	return func() int64 { cancel(); return <-done }
}

func (f *factoryHostBulkFixture) wrapperRSS(ctx context.Context, scope factory.ScopeHandle) int64 {
	observation, err := f.runtime.authority.Observe(ctx, scope)
	if err != nil || observation.PID <= 1 || observation.PID != observation.PGID || observation.Phase != "granted" {
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	deadline, _ := ctx.Deadline()
	result, err := executeFactoryHostCommand(ctx, factory.HostInvocation{Path: "/bin/ps", Args: []string{"-o", "pid=,pgid=,rss=", "-p", strconv.Itoa(observation.PID)}, Env: []string{"LC_ALL=C"}, Dir: f.base, Deadline: deadline, OutputLimit: 256})
	if err != nil || result.ExitCode != 0 {
		return 0
	}
	var pid, pgid int
	var kib int64
	if n, err := fmt.Sscanf(string(result.Stdout), "%d %d %d", &pid, &pgid, &kib); err != nil || n != 3 || pid != observation.PID || pgid != observation.PGID || kib <= 0 || kib > math.MaxInt64/1024 {
		return 0
	}
	return kib * 1024
}

func (f *factoryHostBulkFixture) recordCall(t *testing.T, invocation factory.HostInvocation, result factory.HostResult, observation factory.ScopeObservation, peak int64, elapsed time.Duration) {
	t.Helper()
	dir := filepath.Join(f.config.StateRoot, f.spec.ID, "host-scopes", invocation.Scope.ID+"."+invocation.Scope.Nonce)
	sizes := make(map[string]int64)
	for name, limit := range map[string]int64{"request.json": f.limits.RequestBytes, "result.pending.json": f.limits.PendingResultBytes, "job.plist": f.limits.ScopeMetadataBytes} {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
			t.Fatalf("retained artifact %s exceeds its bound: %v %v", name, info, err)
		}
		sizes[name] = info.Size()
	}
	transcript, err := json.Marshal(result)
	if err != nil || int64(len(transcript)) > f.limits.TranscriptBytes || int64(len(result.Stdout)) > invocation.OutputLimit || int64(len(result.Stderr)) > invocation.OutputLimit {
		t.Fatalf("result/transcript exceeds retention: %v", err)
	}
	f.receipt.Calls = append(f.receipt.Calls, factoryHostBulkCall{
		Scope: invocation.Scope, Observation: observation, Deadline: invocation.Deadline,
		RequestBytes: sizes["request.json"], PendingBytes: sizes["result.pending.json"], TranscriptEncodedBytes: int64(len(transcript)),
		RequestSHA256: factoryHostBulkFileHash(t, filepath.Join(dir, "request.json"), f.limits.RequestBytes),
		PendingSHA256: factoryHostBulkFileHash(t, filepath.Join(dir, "result.pending.json"), f.limits.PendingResultBytes),
		StdinBytes:    len(invocation.Stdin), StdinSHA256: fmt.Sprintf("%x", sha256.Sum256(invocation.Stdin)),
		StdoutBytes: len(result.Stdout), StderrBytes: len(result.Stderr), StdoutSHA256: fmt.Sprintf("%x", sha256.Sum256(result.Stdout)),
		SampledWrapperRSSBytes: peak, RSSSampleIntervalMS: 20, ElapsedSeconds: elapsed.Seconds(), AuthenticatedPublished: true,
	})
	// Invoke can only return nil error after authenticated PUBLISHED and exact
	// retirement. A file read here is size evidence, never publication authority.
	f.assertSpent(t)
}

func (f *factoryHostBulkFixture) assertSpent(t *testing.T) {
	t.Helper()
	rows, err := f.db.QueryContext(t.Context(), "SELECT token,generation FROM managed_reservations WHERE kind='host'")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	spent := make(map[string]uint64)
	for rows.Next() {
		var token string
		var generation uint64
		if err := rows.Scan(&token, &generation); err != nil {
			t.Fatal(err)
		}
		spent[token] = generation
	}
	if err := rows.Err(); err != nil || len(spent) != len(f.scopes) {
		t.Fatalf("durable host spend changed: %v %v", spent, err)
	}
	for _, scope := range f.scopes {
		if spent[scope.ID] != scope.Generation {
			t.Fatalf("host invocation refunded or reassigned: %+v %v", scope, spent)
		}
	}
}

func (f *factoryHostBulkFixture) finish(t *testing.T) {
	t.Helper()
	for _, scope := range f.scopes {
		f.retire(t, scope)
	}
	if err := releaseFactoryHostReservation(t.Context(), f.config.StateRoot, f.reservation.ID); err == nil {
		t.Fatal("quiescent but running fixture released capacity")
	}
	f.sealStopped(t)
	exact, eligible, err := factory.ReleasableHostReservation(t.Context(), f.config.StateRoot, f.spec.ID)
	if err != nil || !eligible || exact != f.reservation {
		t.Fatalf("release predicate differs from original tuple: %+v %v %v", exact, eligible, err)
	}
	if err := releaseFactoryHostReservation(t.Context(), f.config.StateRoot, f.reservation.ID); err != nil {
		t.Fatal(err)
	}
	f.assertSpent(t)
	f.assertReleased(t)
	f.receipt.Released = true
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	f.receipt.SelfHighWaterRSSBytes = usage.Maxrss // Darwin reports bytes, not KiB.
	f.measureRetained(t)
	data, err := json.MarshalIndent(f.receipt, "", "  ")
	if err != nil || len(data) > 1<<20 {
		t.Fatalf("receipt encoding exceeds metadata bound: %v", err)
	}
	if err := os.WriteFile(filepath.Join(f.base, "receipt.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("native bulk receipt=%s; real reservation=%d; retained=%d; elapsed=%.3fs; self highwater RSS=%d; wrapper samples are observed maxima at 20ms, not OS highwater or a hard RSS bound", filepath.Join(f.base, "receipt.json"), f.reservation.Bytes, f.receipt.RetainedLogicalBytes, f.receipt.ElapsedSeconds, usage.Maxrss)
}

func (f *factoryHostBulkFixture) assertReleased(t *testing.T) {
	t.Helper()
	access, err := lockFactoryBudget(t.Context(), f.config.StateRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer access.close()
	ledger, initialized, err := access.load(t.Context())
	if err != nil || !initialized {
		t.Fatalf("reopen persistent release: initialized=%v %v", initialized, err)
	}
	entry, found := ledger.objective(f.spec.ID)
	if !found || !entry.Released || entry.reservation(f.config.StateRoot) != f.reservation {
		t.Fatalf("release changed original reservation tuple: %+v", entry)
	}
}

func (f *factoryHostBulkFixture) sealStopped(t *testing.T) {
	t.Helper()
	// Test-only lifecycle setup, after actual observations/ESRCH above. Scope
	// phases and counters are never fabricated/reset to make release eligible.
	previous := "running"
	for _, desired := range []string{"stopping", "stopped"} {
		result, err := f.db.ExecContext(t.Context(), `UPDATE managed_supervision SET revision=revision+1,
body=json_set(body,'$.Revision',revision+1,'$.DesiredState',?)
WHERE id=1 AND json_extract(body,'$.DesiredState')=? AND NOT EXISTS(SELECT 1 FROM managed_scopes WHERE phase!='quiescent')`, desired, previous)
		if err != nil {
			t.Fatal(err)
		}
		if count, err := result.RowsAffected(); err != nil || count != 1 {
			t.Fatalf("fixture final seal lacks all-scope retirement: %d %v", count, err)
		}
		previous = desired
	}
}

func (f *factoryHostBulkFixture) measureRetained(t *testing.T) {
	t.Helper()
	err := filepath.WalkDir(f.base, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f.receipt.RetainedLogicalBytes += info.Size()
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			f.receipt.RetainedAllocatedBytes += stat.Blocks * 512
		}
		return nil
	})
	if err != nil || f.receipt.RetainedLogicalBytes+(1<<20) > f.reservation.Bytes || f.receipt.RetainedAllocatedBytes+(1<<20) > f.reservation.Bytes {
		t.Fatalf("actual retained files exceed reserved capacity: %+v %v", f.receipt, err)
	}
}

func (f *factoryHostBulkFixture) close(t *testing.T) {
	t.Helper()
	for _, scope := range f.scopes {
		if err := f.runtime.Retire(context.Background(), scope); err != nil {
			t.Errorf("uncertain owned scope retained with capacity: %s %v", scope.Name, err)
			return
		}
	}
	if err := f.runtime.Close(); err != nil {
		t.Error(err)
	}
	// Only this fixture's exact private socket namespace, after retirement.
	if err := os.RemoveAll("/private/tmp/of-" + factoryLaunchdObjectiveHash(f.config)); err != nil {
		t.Error(err)
	}
}

func factoryHostBulkFileHash(t *testing.T, path string, limit int64) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, limit+1))
	if err != nil || n > limit {
		t.Fatalf("hash bounded fixture artifact: %v", err)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func factoryHostBulkJSONHash(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
