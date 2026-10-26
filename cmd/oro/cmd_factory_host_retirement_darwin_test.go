//go:build darwin

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"oro/pkg/factory"
)

type blockingHostAuthority struct {
	factory.ScopeAuthority
	entered chan struct{}
	release chan struct{}
}

func (a *blockingHostAuthority) RecordHello(ctx context.Context, scope factory.ScopeHandle, pid, pgid int) error {
	close(a.entered)
	<-a.release
	return a.ScopeAuthority.RecordHello(ctx, scope, pid, pgid)
}

func TestFactoryHostRetirementBound(t *testing.T) {
	t.Run("caller deadline", func(t *testing.T) {
		cfg := factoryScopeConfigFixture(t)
		db := factoryScopeSeedJournal(t, &cfg)
		scope := factoryScopeSeed(t, db, cfg, "a", time.Now().Add(time.Minute), "reserved", 0)
		runtime, err := openFactoryLaunchdRuntime(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer runtime.Close()
		calls := 0
		runtime.launchctl = func(ctx context.Context, _ ...string) (int, error) { calls++; <-ctx.Done(); return -1, ctx.Err() }
		ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
		defer cancel()
		start := time.Now()
		err = runtime.Retire(ctx, scope)
		if err == nil || time.Since(start) > time.Second || calls != 1 {
			t.Fatalf("retirement exceeded caller deadline: %v, %s, calls=%d", err, time.Since(start), calls)
		}
	})
	t.Run("expired and canceled callers", func(t *testing.T) {
		cfg := factoryScopeConfigFixture(t)
		db := factoryScopeSeedJournal(t, &cfg)
		scope := factoryScopeSeed(t, db, cfg, "e", time.Now().Add(time.Minute), "reserved", 0)
		runtime, err := openFactoryLaunchdRuntime(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer runtime.Close()
		calls := 0
		runtime.launchctl = func(context.Context, ...string) (int, error) { calls++; return 0, nil }
		expired, cancelExpired := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
		defer cancelExpired()
		if err := runtime.Retire(expired, scope); !errors.Is(err, context.DeadlineExceeded) || calls != 0 {
			t.Fatalf("expired cleanup acted: %v calls=%d", err, calls)
		}
		future, cancelFuture := context.WithDeadline(t.Context(), time.Now().Add(time.Second))
		cancelFuture()
		if err := runtime.Retire(future, scope); err != nil || calls != 1 {
			t.Fatalf("canceled cleanup failed: %v calls=%d", err, calls)
		}
	})
	t.Run("grant serialization", func(t *testing.T) {
		cfg := factoryScopeConfigFixture(t)
		db := factoryScopeSeedJournal(t, &cfg)
		deadline := time.Now().Add(time.Minute)
		scope := factoryScopeSeed(t, db, cfg, "b", deadline, "reserved", 0)
		runtime, err := openFactoryLaunchdRuntime(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer runtime.Close()
		blocked := &blockingHostAuthority{ScopeAuthority: runtime.authority, entered: make(chan struct{}), release: make(chan struct{})}
		runtime.authority = blocked
		call := &factoryLaunchdCall{cancel: func() {}, done: make(chan struct{})}
		if err := runtime.register(scope, call); err != nil {
			t.Fatal(err)
		}
		grant := make(chan error, 1)
		go func() {
			grant <- runtime.recordGrant(t.Context(), factory.HostInvocation{Scope: scope, Deadline: deadline}, call, 42, 42)
		}()
		<-blocked.entered
		ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
		defer cancel()
		retired := make(chan error, 1)
		go func() { retired <- runtime.Retire(ctx, scope) }()
		select {
		case err := <-retired:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("expected uncertainty on deadline: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("retirement blocked behind grant")
		}
		close(blocked.release)
		<-grant
		close(call.done)
		runtime.unregister(scope)
		observation, err := runtime.authority.Observe(t.Context(), scope)
		if err != nil || observation.Phase == "quiescent" {
			t.Fatalf("false quiescence: %+v %v", observation, err)
		}
		runtime.launchctl = func(context.Context, ...string) (int, error) { return 0, nil }
		runtime.probe = func(int) error { return syscall.ESRCH }
		if err := runtime.Retire(t.Context(), scope); err != nil {
			t.Fatalf("cleanup after release: %v", err)
		}
	})
}

func TestFactoryHostRetirementRegistration(t *testing.T) {
	cfg := factoryScopeConfigFixture(t)
	db := factoryScopeSeedJournal(t, &cfg)
	scope := factoryScopeSeed(t, db, cfg, "c", time.Now().Add(time.Minute), "reserved", 0)
	runtime, err := openFactoryLaunchdRuntime(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	started, release := make(chan struct{}), make(chan struct{})
	runtime.launchctl = func(context.Context, ...string) (int, error) { close(started); <-release; return 0, nil }
	retired := make(chan error, 1)
	go func() { retired <- runtime.Retire(t.Context(), scope) }()
	<-started
	registered := make(chan error, 1)
	go func() {
		registered <- runtime.register(scope, &factoryLaunchdCall{cancel: func() {}, done: make(chan struct{})})
	}()
	select {
	case err := <-registered:
		t.Fatalf("registration escaped retirement: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-retired; err != nil {
		t.Fatal(err)
	}
	if err := <-registered; err == nil {
		t.Fatal("registered retired scope")
	}
}

func TestFactoryHostRetirementPeer(t *testing.T) {
	if socket := os.Getenv("FACTORY_RETIREMENT_SOCKET"); socket != "" {
		conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		var hello factoryHostExecHello
		if err := json.Unmarshal([]byte(os.Getenv("FACTORY_RETIREMENT_HELLO")), &hello); err != nil {
			t.Fatal(err)
		}
		hello.PID, hello.PGID = os.Getpid(), syscall.Getpgrp()
		if err := json.NewEncoder(conn).Encode(hello); err != nil {
			t.Fatal(err)
		}
		var response factoryHostExecGo
		_ = json.NewDecoder(conn).Decode(&response)
		return
	}
	cfg := factoryScopeConfigFixture(t)
	db := factoryScopeSeedJournal(t, &cfg)
	deadline := time.Now().Add(10 * time.Second)
	scope := factoryScopeSeed(t, db, cfg, "d", deadline, "reserved", 0)
	runtime, err := openFactoryLaunchdRuntime(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	blocked := &blockingHostAuthority{ScopeAuthority: runtime.authority, entered: make(chan struct{}), release: make(chan struct{})}
	runtime.authority = blocked
	dir, err := os.MkdirTemp("/private/tmp", "fhr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "socket")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := listener.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	hello, err := json.Marshal(factoryHostExecHello{Version: 1, Operation: "HELLO", RequestSHA256: "digest", Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(t.Context(), executable, "-test.run=^TestFactoryHostRetirementPeer$")
	child.Env = []string{"FACTORY_RETIREMENT_SOCKET=" + socket, "FACTORY_RETIREMENT_HELLO=" + string(hello), "GORACE=atexit_sleep_ms=0"}
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	conn, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	call := &factoryLaunchdCall{cancel: func() {}, done: make(chan struct{})}
	if err := runtime.register(scope, call); err != nil {
		t.Fatal(err)
	}
	prepared := &factoryLaunchdPrepared{request: factoryHostExecRequest{Invocation: factory.HostInvocation{Scope: scope, Deadline: deadline}}, digest: "digest"}
	grant := make(chan error, 1)
	go func() { _, err := runtime.grant(t.Context(), conn, prepared, call); grant <- err }()
	select {
	case <-blocked.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("authenticated HELLO did not reach authority")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if err := runtime.Retire(ctx, scope); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("retirement: %v", err)
	}
	close(blocked.release)
	if err := <-grant; err != nil {
		t.Fatal(err)
	}
	close(call.done)
	runtime.unregister(scope)
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	runtime.launchctl = func(context.Context, ...string) (int, error) { return 0, nil }
	runtime.probe = func(int) error { return syscall.ESRCH }
	if err := runtime.Retire(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
}

func TestFactoryHostInvokeCleanupDeadline(t *testing.T) {
	cfg := factoryScopeConfigFixture(t)
	db := factoryScopeSeedJournal(t, &cfg)
	workDeadline := time.Now().Add(200 * time.Millisecond)
	callerDeadline := workDeadline.Add(400 * time.Millisecond)
	scope := factoryScopeSeed(t, db, cfg, "f", workDeadline, "reserved", 0)
	runtime, err := openFactoryLaunchdRuntime(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	t.Cleanup(func() { _ = os.RemoveAll("/private/tmp/of-" + factoryLaunchdObjectiveHash(cfg)) })
	var gotDeadline time.Time
	runtime.launchctl = func(ctx context.Context, args ...string) (int, error) {
		if args[0] == "bootstrap" {
			<-ctx.Done()
			return -1, ctx.Err()
		}
		gotDeadline, _ = ctx.Deadline()
		return int(syscall.ESRCH), nil
	}
	ctx, cancel := context.WithDeadline(t.Context(), callerDeadline)
	defer cancel()
	invocation := factory.HostInvocation{Scope: scope, Path: cfg.GitPath, Dir: cfg.Repository, Deadline: workDeadline, OutputLimit: 256}
	_, err = runtime.Invoke(ctx, invocation)
	if err == nil {
		t.Fatal("expected expired execution")
	}
	if !gotDeadline.Equal(callerDeadline) {
		t.Fatalf("cleanup deadline %v; want caller deadline %v", gotDeadline, callerDeadline)
	}
	observation, err := runtime.authority.Observe(t.Context(), scope)
	if err != nil || observation.Phase != "quiescent" {
		t.Fatalf("legal cleanup did not finish: %+v %v", observation, err)
	}
}

func TestFactoryHostRetirementActiveUnregister(t *testing.T) {
	cfg := factoryScopeConfigFixture(t)
	db := factoryScopeSeedJournal(t, &cfg)
	scope := factoryScopeSeed(t, db, cfg, "9", time.Now().Add(time.Minute), "reserved", 0)
	runtime, err := openFactoryLaunchdRuntime(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	canceled, done := make(chan struct{}), make(chan struct{})
	call := &factoryLaunchdCall{cancel: func() { close(canceled) }, done: done}
	if err := runtime.register(scope, call); err != nil {
		t.Fatal(err)
	}
	bootout, release := make(chan struct{}), make(chan struct{})
	runtime.launchctl = func(context.Context, ...string) (int, error) { close(bootout); <-release; return 0, nil }
	retired := make(chan error, 1)
	go func() { retired <- runtime.Retire(t.Context(), scope) }()
	<-canceled
	close(done)
	<-bootout
	runtime.unregister(scope)
	registered := make(chan error, 1)
	go func() {
		registered <- runtime.register(scope, &factoryLaunchdCall{cancel: func() {}, done: make(chan struct{})})
	}()
	select {
	case err := <-registered:
		if err == nil {
			t.Error("registered while active retirement in progress")
		}
		close(release)
		if err := <-retired; err != nil {
			t.Fatal(err)
		}
		return
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-retired; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-registered:
		if err == nil {
			t.Error("registered retired scope")
		}
	case <-time.After(time.Second):
		t.Fatal("registration stuck")
	}
}
