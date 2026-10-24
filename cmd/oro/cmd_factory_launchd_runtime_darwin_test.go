//go:build darwin

package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"oro/pkg/factory"
)

func testFactoryHostScopeJournal(t *testing.T) {
	cfg := factoryScopeConfigFixture(t)
	if runtime, err := openFactoryLaunchdRuntime(t.Context(), cfg); err == nil {
		_ = runtime.Close()
		t.Fatal("runtime created authority for unadmitted objective")
	}
	entries, err := os.ReadDir(cfg.StateRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("constructor initialized missing state: %v %v", entries, err)
	}
	db := factoryScopeSeedJournal(t, &cfg)
	scope := factoryScopeSeed(t, db, cfg, "a", time.Now().Add(time.Minute), "granted", 42)
	runtime, err := openFactoryLaunchdRuntime(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	calls := 0
	runtime.launchctl = func(_ context.Context, args ...string) (int, error) {
		calls++
		if len(args) != 2 || args[0] != "bootout" || args[1] != "gui/"+strconv.Itoa(os.Getuid())+"/"+scope.Name {
			t.Fatalf("wrong retirement target %v", args)
		}
		return 3, nil
	}
	probes := 0
	runtime.probe = func(group int) error {
		if group != 42 {
			t.Fatal("wrong authenticated group")
		}
		probes++
		if probes == 1 {
			return syscall.EPERM
		}
		return syscall.ESRCH
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := runtime.Retire(canceled, scope); err != nil {
		t.Fatal(err)
	}
	observation, err := runtime.authority.Observe(t.Context(), scope)
	if err != nil || observation.Phase != "quiescent" || calls != 1 || probes != 2 {
		t.Fatalf("unproven retirement %+v %v calls=%d probes=%d", observation, err, calls, probes)
	}
	unknown := scope
	unknown.Nonce = strings.Repeat("b", 32)
	unknown.Name = strings.ReplaceAll(unknown.Name, "."+scope.Nonce, "."+unknown.Nonce)
	if err := runtime.Retire(t.Context(), unknown); err == nil || calls != 1 {
		t.Fatal("unknown scope reached launchctl")
	}
	unknownInvocation := factory.HostInvocation{Scope: unknown, Path: cfg.GitPath, Dir: cfg.Repository, Deadline: time.Now().Add(time.Minute), OutputLimit: 256}
	if _, err := runtime.Invoke(t.Context(), unknownInvocation); err == nil || calls != 1 {
		t.Fatal("unknown invocation reached backend effects")
	}
	if _, err := os.Lstat(filepath.Join(cfg.StateRoot, cfg.ObjectiveID, "host-scopes")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unknown invocation created request state")
	}
	uncertain := factoryScopeSeed(t, db, cfg, "c", time.Now().Add(time.Minute), "granted", 43)
	runtime.config.CleanupTimeout = 30 * time.Millisecond
	runtime.launchctl = func(context.Context, ...string) (int, error) { return 0, nil }
	runtime.probe = func(int) error { return syscall.EPERM }
	if err := runtime.Retire(t.Context(), uncertain); err == nil {
		t.Fatal("uncertain group became retired")
	}
	observation, err = runtime.authority.Observe(t.Context(), uncertain)
	if err != nil || observation.Phase != "granted" {
		t.Fatalf("uncertain ownership changed %+v %v", observation, err)
	}
	runtime.launchctl = func(context.Context, ...string) (int, error) { return 5, errors.New("launchd failed") }
	runtime.probe = func(int) error { return syscall.ESRCH }
	if err := runtime.Retire(t.Context(), uncertain); err == nil {
		t.Fatal("failed bootout was ignored")
	}
}

func testFactoryHostScopePlatform(t *testing.T) {
	t.Helper()
	t.Run("objective-bound journal retirement", testFactoryHostScopeJournal)
	t.Run("cleanup respects immutable hard stop", testFactoryScopeCleanupHardStop)
	t.Run("retirement requires exact active handle", testFactoryScopeRetirementHandle)
	t.Run("retirement waits for in-flight bootstrap", testFactoryScopeBootstrapRetirement)
	t.Run("native launchd invocations", testFactoryHostScopeNative)
}

func testFactoryScopeCleanupHardStop(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprintf("expired=%t", expired), func(t *testing.T) {
			cfg := factoryScopeConfigFixture(t)
			db := factoryScopeSeedJournal(t, &cfg)
			scope := factoryScopeSeed(t, db, cfg, "a", time.Now().Add(time.Minute), "granted", 42)
			runtime, err := openFactoryLaunchdRuntime(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			runtime.config.HardStop = time.Now().Add(100 * time.Millisecond)
			if expired {
				runtime.config.HardStop = time.Now().Add(-time.Millisecond)
			}
			calls, probes := 0, 0
			var cleanupDeadline time.Time
			runtime.launchctl = func(ctx context.Context, _ ...string) (int, error) {
				calls++
				cleanupDeadline, _ = ctx.Deadline()
				return int(syscall.ESRCH), nil
			}
			runtime.probe = func(int) error { probes++; return syscall.ESRCH }
			canceled, cancel := context.WithCancel(t.Context())
			cancel()
			retireErr := runtime.Retire(canceled, scope)
			observation, err := runtime.authority.Observe(t.Context(), scope)
			if err != nil {
				t.Fatal(err)
			}
			if expired {
				if retireErr == nil || calls != 0 || probes != 0 || observation.Phase != "granted" {
					t.Fatalf("expired cleanup acted or certified quiescence: err=%v calls=%d probes=%d phase=%s", retireErr, calls, probes, observation.Phase)
				}
				return
			}
			if retireErr != nil || calls != 1 || probes != 1 || observation.Phase != "quiescent" {
				t.Fatalf("live cleanup failed independently of canceled invocation: err=%v calls=%d probes=%d phase=%s", retireErr, calls, probes, observation.Phase)
			}
			if !cleanupDeadline.Equal(runtime.config.HardStop) {
				t.Fatalf("cleanup escaped immutable hard stop: cleanup=%s hard-stop=%s", cleanupDeadline, runtime.config.HardStop)
			}
		})
	}
}

func testFactoryScopeBootstrapRetirement(t *testing.T) {
	cfg := factoryScopeConfigFixture(t)
	db := factoryScopeSeedJournal(t, &cfg)
	scope := factoryScopeSeed(t, db, cfg, "d", time.Now().Add(time.Minute), "reserved", 0)
	runtime, err := openFactoryLaunchdRuntime(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	canceled := make(chan struct{})
	done := make(chan struct{})
	launched := make(chan struct{}, 1)
	if err := runtime.register(scope, &factoryLaunchdCall{cancel: func() { close(canceled) }, done: done}); err != nil {
		t.Fatal(err)
	}
	defer runtime.unregister(scope)
	runtime.launchctl = func(context.Context, ...string) (int, error) { launched <- struct{}{}; return 0, nil }
	retired := make(chan error, 1)
	go func() { retired <- runtime.Retire(t.Context(), scope) }()
	<-canceled
	select {
	case <-launched:
		t.Error("bootout raced an unfinished bootstrap command")
	case <-time.After(30 * time.Millisecond):
	}
	close(done)
	if err := <-retired; err != nil {
		t.Fatal(err)
	}
}

func testFactoryScopeRetirementHandle(t *testing.T) {
	for _, field := range []string{"nonce", "generation"} {
		t.Run(field, func(t *testing.T) {
			cfg := factoryScopeConfigFixture(t)
			db := factoryScopeSeedJournal(t, &cfg)
			scope := factoryScopeSeed(t, db, cfg, "c", time.Now().Add(time.Minute), "reserved", 0)
			runtime, err := openFactoryLaunchdRuntime(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			active, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan struct{})
			close(done)
			call := &factoryLaunchdCall{cancel: cancel, done: done}
			if err := runtime.register(scope, call); err != nil {
				t.Fatal(err)
			}
			defer runtime.unregister(scope)
			calls := 0
			runtime.launchctl = func(context.Context, ...string) (int, error) { calls++; return int(syscall.ESRCH), nil }
			forged := scope
			if field == "nonce" {
				forged.Nonce = strings.Repeat("d", 32)
				forged.Name = "dev.getoro.factory." + factoryLaunchdObjectiveHash(cfg) + "." + forged.Kind + "." + forged.ID + "." + forged.Nonce
			} else {
				forged.Generation++
			}
			retireErr := runtime.Retire(t.Context(), forged)
			observation, err := runtime.authority.Observe(t.Context(), scope)
			if err != nil {
				t.Fatal(err)
			}
			if retireErr == nil || active.Err() != nil || call.retiring || calls != 0 || observation.Phase != "reserved" {
				t.Fatalf("altered %s affected genuine invocation: err=%v canceled=%v retiring=%t calls=%d phase=%s", field, retireErr, active.Err(), call.retiring, calls, observation.Phase)
			}
			if err := runtime.Retire(t.Context(), scope); err != nil || active.Err() == nil || !call.retiring || calls != 1 {
				t.Fatalf("genuine retirement failed after rejected handle: err=%v canceled=%v retiring=%t calls=%d", err, active.Err(), call.retiring, calls)
			}
		})
	}
}

func testFactoryHostScopeNative(t *testing.T) {
	if os.Getenv("ORO_FACTORY_INTEGRATION") != "1" {
		t.Skip("native factory scope integration requires ORO_FACTORY_INTEGRATION=1")
	}
	buildDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(buildDir, "oro")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build native wrapper: %v %s", err, output)
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	for index, mode := range []string{"normal", "exit7", "timeout", "lost-bootstrap-ack", "revoked", "retire-active", "tamper-pending"} {
		t.Run(mode, func(t *testing.T) {
			cfg := factoryScopeConfigFixture(t)
			cfg.OroPath = binary
			cfg.OroSHA256 = digest
			cfg.CleanupTimeout = 7 * time.Second
			db := factoryScopeSeedJournal(t, &cfg)
			deadline := time.Now().UTC().Add(12 * time.Second)
			if mode == "timeout" {
				deadline = time.Now().UTC().Add(2 * time.Second)
			}
			scope := factoryScopeSeed(t, db, cfg, fmt.Sprintf("%x", index+1), deadline, "reserved", 0)
			runtime, err := openFactoryLaunchdRuntime(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := runtime.Retire(context.Background(), scope); err != nil {
					t.Errorf("owned fixture retirement: %v", err)
				} else if err := os.RemoveAll("/private/tmp/of-" + factoryLaunchdObjectiveHash(cfg)); err != nil {
					t.Errorf("remove owned fixture socket namespace: %v", err)
				}
				_ = runtime.Close()
			})
			original := runtime.launchctl
			runtime.launchctl = func(ctx context.Context, args ...string) (int, error) {
				code, err := original(ctx, args...)
				if args[0] == "bootstrap" && code == 0 && err == nil {
					if mode == "lost-bootstrap-ack" {
						return -1, errors.New("owned injected lost bootstrap acknowledgment")
					}
					if mode == "revoked" {
						_, err = db.Exec("UPDATE managed_scopes SET phase='revoked' WHERE id=?", scope.ID)
					}
				}
				return code, err
			}
			if mode == "tamper-pending" {
				probe := runtime.probe
				runtime.probe = func(group int) error {
					err := probe(group)
					if errors.Is(err, syscall.ESRCH) {
						path := filepath.Join(cfg.StateRoot, cfg.ObjectiveID, "host-scopes", scope.ID+"."+scope.Nonce, "result.pending.json")
						if err := os.WriteFile(path, []byte("tampered after quiescence"), 0o600); err != nil {
							t.Error(err)
						}
					}
					return err
				}
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			inv := factory.HostInvocation{Scope: scope, Path: executable, Args: []string{"-test.run=^TestFactoryHostScopePayload$"}, Env: []string{"FACTORY_SCOPE_PAYLOAD=" + mode, "GORACE=atexit_sleep_ms=0"}, Dir: cfg.Repository, Deadline: deadline, OutputLimit: 4096}
			coverageDir := filepath.Join(cfg.Repository, "coverage")
			if err := os.Mkdir(coverageDir, 0o700); err != nil {
				t.Fatal(err)
			}
			inv.Env = append(inv.Env, "GOCOVERDIR="+coverageDir)
			result, invokeErr := invokeFactoryScopeFixture(t, runtime, inv, mode)
			wantSuccess := mode == "normal" || mode == "exit7"
			if (invokeErr == nil) != wantSuccess {
				t.Fatalf("native result=%+v error=%v", result, invokeErr)
			}
			observation, err := runtime.authority.Observe(t.Context(), scope)
			if err != nil || observation.Phase != "quiescent" {
				t.Fatalf("scope not retired before return: %+v %v", observation, err)
			}
			if observation.PGID > 1 && !errors.Is(syscall.Kill(-observation.PGID, 0), syscall.ESRCH) {
				t.Fatal("authenticated process group survived Invoke")
			}
			t.Logf("retired exact gui/%d/%s phase=%s peer PID=%d PGID=%d; authenticated groups require ESRCH", os.Getuid(), scope.Name, observation.Phase, observation.PID, observation.PGID)
			marker := filepath.Join(cfg.Repository, "payload-started.json")
			if mode == "lost-bootstrap-ack" || mode == "revoked" {
				if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("ungranted payload executed")
				}
				return
			}
			if observation.PID <= 1 || observation.PGID != observation.PID {
				t.Fatalf("wrapper was not authenticated group leader: %+v", observation)
			}
			identityBytes, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal(err)
			}
			var identity struct{ PID, PGID, ChildPID, ChildPGID int }
			if err := json.Unmarshal(identityBytes, &identity); err != nil {
				t.Fatal(err)
			}
			if identity.PGID != observation.PGID || identity.ChildPGID != observation.PGID {
				t.Fatalf("descendants escaped job group: %+v", identity)
			}
			if wantSuccess {
				wantExit := 0
				if mode == "exit7" {
					wantExit = 7
				}
				if result.ExitCode != wantExit || string(result.Stdout) != string(identityBytes) {
					t.Fatalf("pending result lost exact output/status: %+v", result)
				}
				if _, err := runtime.Invoke(t.Context(), inv); err == nil {
					t.Fatal("quiescent scope replayed")
				}
			}
		})
	}
}

func invokeFactoryScopeFixture(t *testing.T, runtime *factoryLaunchdRuntime, invocation factory.HostInvocation, mode string) (factory.HostResult, error) {
	t.Helper()
	if mode != "retire-active" {
		return runtime.Invoke(t.Context(), invocation)
	}
	type outcome struct {
		result factory.HostResult
		err    error
	}
	finished := make(chan outcome, 1)
	go func() { result, err := runtime.Invoke(t.Context(), invocation); finished <- outcome{result, err} }()
	until := time.Now().Add(3 * time.Second)
	for {
		data, _ := os.ReadFile(filepath.Join(invocation.Dir, "payload-started.json"))
		if json.Valid(data) {
			break
		}
		if !time.Now().Before(until) {
			t.Fatal("native payload never reached retirement barrier")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := runtime.Retire(t.Context(), invocation.Scope); err != nil {
		t.Fatal(err)
	}
	got := <-finished
	return got.result, got.err
}

func TestFactoryHostScopePayload(t *testing.T) {
	mode := os.Getenv("FACTORY_SCOPE_PAYLOAD")
	if mode == "" {
		return
	}
	child := exec.Command("/bin/sleep", "20")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	childGroup, err := syscall.Getpgid(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(struct{ PID, PGID, ChildPID, ChildPGID int }{os.Getpid(), syscall.Getpgrp(), child.Process.Pid, childGroup})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("payload-started.json", data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := child.Process.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stdout.Write(data); err != nil {
		t.Fatal(err)
	}
	if mode == "timeout" || mode == "retire-active" {
		time.Sleep(20 * time.Second)
	}
	code := 0
	if mode == "exit7" {
		code = 7
	}
	os.Exit(code)
}

// Only test setup writes this admitted journal fixture. Production accesses it
// exclusively through objective-bound ScopeAuthority.
func factoryScopeSeedJournal(t *testing.T, cfg *factoryLaunchdConfig) *sql.DB {
	t.Helper()
	dir := filepath.Join(cfg.StateRoot, cfg.ObjectiveID)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	namespace := filepath.Join(cfg.StateRoot, ".managed")
	if err := os.Mkdir(namespace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(namespace, cfg.ObjectiveID+".state.init"), []byte("oro-managed-state-v1;page=4096;data=67108864;allowance=268435456\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.init"), []byte("oro-managed-state-v1;page=4096;data=67108864;allowance=268435456\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA page_size=4096; PRAGMA journal_mode=DELETE; PRAGMA busy_timeout=1000;
CREATE TABLE managed_admissions(id INTEGER PRIMARY KEY,body BLOB NOT NULL);
CREATE TABLE managed_supervision(id INTEGER PRIMARY KEY,revision INTEGER NOT NULL,body BLOB NOT NULL);
CREATE TABLE managed_scopes(id TEXT PRIMARY KEY,nonce TEXT NOT NULL UNIQUE,backend TEXT NOT NULL,name TEXT NOT NULL,body BLOB NOT NULL,phase TEXT NOT NULL,pid INTEGER NOT NULL DEFAULT 0,pgid INTEGER NOT NULL DEFAULT 0,cleanup TEXT NOT NULL DEFAULT '',UNIQUE(backend,name));
CREATE TABLE managed_events(revision INTEGER PRIMARY KEY,kind TEXT NOT NULL,ref TEXT NOT NULL,body BLOB NOT NULL);
CREATE TABLE managed_reservations(kind TEXT NOT NULL,token TEXT NOT NULL,generation INTEGER NOT NULL,PRIMARY KEY(kind,token));`)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().UTC().Add(time.Hour)
	cfg.HardStop = deadline.Add(2 * time.Second)
	resources := factory.RuntimePolicy{MemoryBytes: 1, SourceBytes: 1, ScratchBytes: 1, HostRequiredBytes: 1, HostReserveBytes: 1, CPUs: 1, PIDs: 1, MaxFiles: 1, SourceInodes: 1, ScratchInodes: 1, MaxModelCalls: 1, MaxModelTurns: 1, MaxToolCalls: 1, MaxHostInvocations: 100}
	recovery := factory.RecoveryPolicy{MaxControllerStarts: 5, MaxSupervisorStarts: 5, MaxReconciliationInvocations: 5, RestartDelaySeconds: 1, CleanupTimeoutSeconds: 1, ReconciliationSeconds: 1}
	admission := map[string]any{"ObjectiveID": cfg.ObjectiveID, "ManifestHash": strings.Repeat("a", 64), "Deadline": deadline, "HardStop": cfg.HardStop, "Recovery": recovery, "Runtime": resources, "Reservation": factory.HostReservation{ID: "scope-proof", StateRoot: cfg.StateRoot, ObjectiveID: cfg.ObjectiveID, Bytes: 1}}
	body, err := json.Marshal(admission)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO managed_admissions VALUES(1,?)", body); err != nil {
		t.Fatal(err)
	}
	state := []byte(`{"Revision":1,"Generation":1,"SupervisorToken":"owner","DesiredState":"running","Reason":"","NextStart":"0001-01-01T00:00:00Z"}`)
	if _, err = db.Exec("INSERT INTO managed_supervision VALUES(1,1,?)", state); err != nil {
		t.Fatal(err)
	}
	return db
}

func factoryScopeSeed(t *testing.T, db *sql.DB, cfg factoryLaunchdConfig, letter string, deadline time.Time, phase string, pid int) factory.ScopeHandle {
	t.Helper()
	scope := factory.ScopeHandle{ID: strings.Repeat(letter, 32), Nonce: strings.Repeat(letter, 32), Kind: "host", Backend: "launchd", Generation: 1}
	scope.Name = fmt.Sprintf("dev.getoro.factory.%s.host.%s.%s", factoryLaunchdObjectiveHash(cfg), scope.ID, scope.Nonce)
	body, err := json.Marshal(struct {
		factory.ScopeHandle
		Deadline time.Time
	}{scope, deadline.UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO managed_scopes(id,nonce,backend,name,body,phase,pid,pgid) VALUES(?,?,?,?,?,?,?,?)", scope.ID, scope.Nonce, scope.Backend, scope.Name, body, phase, pid, pid); err != nil {
		t.Fatal(err)
	}
	return scope
}
