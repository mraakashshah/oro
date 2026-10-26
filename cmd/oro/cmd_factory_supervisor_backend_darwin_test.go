//go:build darwin

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"oro/pkg/factory"
)

func TestFactorySupervisorBackend(t *testing.T) {
	for _, mode := range []string{"valid", "forged", "lost-bootstrap", "bootstrap-race", "handshake-retire", "recovered", "lost-go", "blocked-grant", "revoked-grant"} {
		t.Run("real supervisor "+mode, func(t *testing.T) { testFactorySupervisorRealLaunch(t, mode) })
	}
	t.Run("registration", testFactorySupervisorBackendRegistration)
	t.Run("retirement gate deadline", testFactoryControllerGateDeadline)
	t.Run("invalid scope", testFactorySupervisorBackendRejectsInvalidScope)
	t.Run("ordinary host invocation", testFactorySupervisorHostInvocation)
	t.Run("canonical request", func(t *testing.T) {
		root := "/private/tmp/factory"
		scope := factory.ScopeHandle{ID: strings.Repeat("a", 32), Nonce: strings.Repeat("b", 32), Kind: "controller", Backend: "launchd", Generation: 1}
		scope.Name = "dev.getoro.factory." + factoryLaunchdObjectiveHash(factoryLaunchdConfig{StateRoot: root, ObjectiveID: "objective"}) + ".controller." + scope.ID + "." + scope.Nonce
		request := factoryControllerRequest{Version: 1, Scope: scope, StateRoot: root, ObjectiveID: "objective", Runtime: factory.ManagedRuntimeReference{Path: root + "/objective/runtime/runtime.json", SHA256: strings.Repeat("c", 64)}, Deadline: time.Now().UTC().Add(time.Minute)}
		data, err := encodeFactoryControllerRequest(request)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := decodeFactoryControllerRequest(data)
		if err != nil || decoded != request {
			t.Fatalf("round trip: %+v, %v", decoded, err)
		}
		for _, bad := range [][]byte{append([]byte(" "), data...), append(bytes.Clone(data), '\n'), bytes.Repeat([]byte("x"), 16<<10+1)} {
			if _, err := decodeFactoryControllerRequest(bad); err == nil {
				t.Fatal("accepted noncanonical or oversized request")
			}
		}
		request.Scope.Name += ".other"
		if _, err := encodeFactoryControllerRequest(request); err == nil {
			t.Fatal("accepted mismatched scope label")
		}
	})
}

func testFactorySupervisorBackendRegistration(t *testing.T) {
	backend := &factorySupervisorBackend{controllers: make(map[string]*factoryControllerCall), gate: make(chan struct{}, 1)}
	scope := factory.ScopeHandle{ID: "id", Nonce: "nonce"}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	call := &factoryControllerCall{scope: scope, cancel: cancel, done: make(chan struct{})}
	if err := backend.registerController(t.Context(), call); err != nil {
		t.Fatal(err)
	}
	if err := backend.registerController(t.Context(), call); err == nil {
		t.Fatal("duplicate startup accepted")
	}
	if err := backend.stopController(ctx, scope); err == nil {
		t.Fatal("retirement returned before startup ended")
	}
	close(call.done)
	if err := backend.stopController(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	if err := backend.registerController(t.Context(), call); err == nil {
		t.Fatal("late startup accepted after retirement")
	}
}

func testFactorySupervisorBackendRejectsInvalidScope(t *testing.T) {
	backend := &factorySupervisorBackend{controllers: make(map[string]*factoryControllerCall), gate: make(chan struct{}, 1)}
	if err := backend.StartController(t.Context(), factory.ScopeHandle{}); err == nil {
		t.Fatal("invalid controller was started")
	}
}

func testFactorySupervisorRealLaunch(t *testing.T, mode string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(base, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	gitOutput, err := exec.Command("/usr/bin/xcrun", "--find", "git").Output()
	if err != nil {
		t.Fatal(err)
	}
	git, err := filepath.EvalSymlinks(strings.TrimSpace(string(gitOutput)))
	if err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) {
		t.Helper()
		command := exec.Command(git, append([]string{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgSign=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
		command.Dir = repo
		command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + base, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
		if out, e := command.CombinedOutput(); e != nil {
			t.Fatalf("git: %v %s", e, out)
		}
	}
	runGit("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "source"), []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "source")
	runGit("commit", "-m", "fixture")
	runGit("branch", "delivery")
	model := filepath.Join(base, "model")
	if err := os.WriteFile(model, []byte("#!/bin/sh\nexit 1\n"), 0o500); err != nil {
		t.Fatal(err)
	}
	spec := factory.ManagedSpec{
		ID: "controller-fixture", Objective: "test controller launch", Repository: repo, TargetBranch: "delivery", RequiredChecks: []string{"check"}, MaxAttempts: 1, TimeoutSeconds: 90, StepTimeoutSeconds: 30,
		Recovery: factory.RecoveryPolicy{MaxControllerStarts: 1, MaxSupervisorStarts: 1, RestartDelaySeconds: 1, CleanupTimeoutSeconds: 10, ReconciliationSeconds: 5, MaxReconciliationInvocations: 1, MaxCleanupInvocations: 1},
		Runtime:  factory.RuntimePolicy{DockerContext: "fixture", ImageDigest: "sha256:" + strings.Repeat("a", 64), MemoryBytes: 256 << 20, ScratchBytes: 1 << 20, SourceBytes: 1 << 20, HostRequiredBytes: 1, HostReserveBytes: 1 << 20, CPUs: 1, PIDs: 16, MaxFiles: 16, SourceInodes: 16, ScratchInodes: 16, MaxModelCalls: 1, MaxModelTurns: 1, MaxToolCalls: 1, MaxHostInvocations: 8, Model: "fixture", ModelExecutable: model, GitExecutable: git, MaxModelInvocationUSD: "0.01"}, Acceptance: []factory.Check{{ID: "check", Args: []string{"/bin/true"}}}, ReviewInstructions: "fixture",
	}
	limits, err := factory.ManagedHostRetention(spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Runtime.HostRequiredBytes = limits.MinimumBytes
	root := filepath.Join(base, "state")
	_, digest, err := factory.PrepareManagedSpecIdentity(spec)
	if err != nil {
		t.Fatal(err)
	}
	reservation := factory.HostReservation{ID: "fixture-reservation", StateRoot: root, ObjectiveID: spec.ID, Bytes: spec.Runtime.HostRequiredBytes, SpecSHA256: digest}
	if _, err := factory.AdmitManaged(t.Context(), root, spec, reservation); err != nil {
		t.Fatal(err)
	}
	view, err := factory.ReadManagedAdmission(t.Context(), root, spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	socketDir, err := os.MkdirTemp("/private/tmp", "fcs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	endpoint := filepath.Join(socketDir, "docker.sock")
	listener, err := net.Listen("unix", endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(endpoint, 0o600); err != nil {
		t.Fatal(err)
	}
	oro := filepath.Join(base, "oro")
	docker := filepath.Join(base, "docker")
	program := []byte("#!/bin/sh\nexit 1\n")
	for _, path := range []string{oro, docker} {
		if err := os.WriteFile(path, program, 0o500); err != nil {
			t.Fatal(err)
		}
	}
	config := factoryRuntimeConfig{Version: 1, StateRoot: root, ObjectiveID: spec.ID, PolicyHash: view.PolicyHash, OroPath: oro, OroSHA256: factoryHostDigest(program), GitPath: view.GitPath, GitSHA256: view.GitSHA256, Repository: repo, TargetRef: view.TargetRef, Deadline: view.Deadline, HardStop: view.HardStop, CleanupTimeoutSeconds: view.CleanupTimeoutSeconds, DockerPath: docker, DockerSHA256: factoryHostDigest(program), DockerContext: view.DockerContext, DockerEndpoint: "unix://" + endpoint}
	data, err := canonicalFactoryRuntimeConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	configJSON, meta, err := factoryDockerContextSnapshot(config)
	if err != nil {
		t.Fatal(err)
	}
	path := factoryRuntimeConfigPath(config)
	metaPath := filepath.Join(filepath.Dir(path), "docker", "contexts", "meta", factoryHostDigest([]byte(config.DockerContext)), "meta.json")
	if err := os.MkdirAll(filepath.Dir(metaPath), 0o700); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string][]byte{path: data, metaPath: meta, filepath.Join(filepath.Dir(path), "docker", "config.json"): configJSON} {
		if err := os.WriteFile(path, content, 0o400); err != nil {
			t.Fatal(err)
		}
	}
	ref := factory.ManagedRuntimeReference{Path: path, SHA256: factoryHostDigest(data)}
	if err := factory.ActivateManaged(t.Context(), root, spec.ID, ref); err != nil {
		t.Fatal(err)
	}
	backend, err := openFactorySupervisorBackend(t.Context(), root, spec.ID, ref)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	entered := make(chan struct{})
	release := make(chan struct{})
	if mode == "blocked-grant" || mode == "revoked-grant" {
		backend.host.authority = &factoryControllerAuthorityBarrier{ScopeAuthority: backend.host.authority, entered: entered, release: release}
	}
	var child *exec.Cmd
	childCtx, childCancel := context.WithCancel(context.Background())
	var target string
	var childMu sync.Mutex
	launched := make(chan factoryControllerRequest, 1)
	bootstrapRelease := make(chan struct{})
	var releaseOnce, bootstrapOnce sync.Once
	releaseBarrier := func() { releaseOnce.Do(func() { close(release) }) }
	releaseBootstrap := func() { bootstrapOnce.Do(func() { close(bootstrapRelease) }) }
	bootouts := make(chan struct{}, 4)
	backend.host.launchctl = func(ctx context.Context, args ...string) (int, error) {
		if args[0] == "bootout" {
			bootouts <- struct{}{}
			if len(args) != 2 || args[1] != target {
				return -1, errors.New("unexpected bootout target")
			}
			childMu.Lock()
			if child != nil && child.Process != nil {
				_ = syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
				_ = child.Wait()
				child = nil
			}
			childMu.Unlock()
			return 0, nil
		}
		plist, e := os.ReadFile(args[2])
		if e != nil {
			return -1, e
		}
		var doc factoryJobXMLValue
		if e = xml.Unmarshal(plist, &doc); e != nil {
			return -1, e
		}
		var stringsList []string
		var collect func(factoryJobXMLValue)
		collect = func(node factoryJobXMLValue) {
			if node.XMLName.Local == "string" {
				stringsList = append(stringsList, node.Text)
			}
			for _, c := range node.Children {
				collect(c)
			}
		}
		collect(doc)
		var reqPath, digest, socket string
		for i, s := range stringsList {
			if i+1 < len(stringsList) {
				switch s {
				case "--request":
					reqPath = stringsList[i+1]
				case "--request-sha256":
					digest = stringsList[i+1]
				case "--socket":
					socket = stringsList[i+1]
				}
			}
		}
		requestData, e := os.ReadFile(reqPath)
		if e != nil {
			return -1, e
		}
		request, e := decodeFactoryControllerRequest(requestData)
		if e != nil {
			return -1, e
		}
		target = backend.host.serviceTarget(request.Scope)
		if mode == "bootstrap-race" {
			launched <- request
			<-bootstrapRelease
			return -1, errors.New("late bootstrap completion")
		}
		exe, e := os.Executable()
		if e != nil {
			return -1, e
		}
		childMu.Lock()
		child = exec.CommandContext(childCtx, exe, "-test.run=^TestFactorySupervisorControllerPeer$")
		child.Env = []string{"FACTORY_CONTROLLER_SOCKET=" + socket, "FACTORY_CONTROLLER_REQUEST=" + reqPath, "FACTORY_CONTROLLER_DIGEST=" + digest, "FACTORY_CONTROLLER_GO=" + filepath.Join(base, "go"), "FACTORY_CONTROLLER_MODE=" + mode, "GORACE=atexit_sleep_ms=0"}
		child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if e = child.Start(); e != nil {
			childMu.Unlock()
			return -1, e
		}
		childMu.Unlock()
		launched <- request
		if mode == "lost-bootstrap" {
			return -1, errors.New("lost bootstrap acknowledgment")
		}
		return 0, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	supervisorDone := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		childCancel()
		releaseBarrier()
		releaseBootstrap()
		childMu.Lock()
		if child != nil && child.Process != nil {
			_ = child.Process.Kill()
			_ = child.Wait()
			child = nil
		}
		childMu.Unlock()
		<-supervisorDone
	})
	go func() {
		defer close(supervisorDone)
		_, e := factory.Supervise(ctx, root, spec.ID, backend)
		result <- e
	}()
	var request factoryControllerRequest
	select {
	case request = <-launched:
	case <-time.After(15 * time.Second):
		t.Fatal("controller was not launched")
	}
	deadline := time.Now().Add(10 * time.Second)
	runBootstrapRace := func() {
		replay, e := openFactorySupervisorBackend(t.Context(), root, spec.ID, ref)
		if e != nil {
			t.Fatal(e)
		}
		var bootstrapCount int
		replay.host.launchctl = func(context.Context, ...string) (int, error) { bootstrapCount++; return 0, nil }
		if e := replay.StartController(t.Context(), request.Scope); e == nil || bootstrapCount != 0 {
			t.Fatal("uncertain scope bootstrapped across instances")
		}
		_ = replay.Close()
		retired := make(chan error, 1)
		go func() { retired <- backend.Retire(t.Context(), request.Scope) }()
		select {
		case <-bootouts:
			t.Fatal("bootout raced ahead of pending bootstrap")
		case <-time.After(30 * time.Millisecond):
		}
		releaseBootstrap()
		select {
		case e := <-retired:
			if e != nil {
				t.Fatal(e)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("retirement did not wait for bootstrap")
		}
		select {
		case <-result:
		case <-time.After(15 * time.Second):
			t.Fatal("supervisor did not finish")
		}
		if err := backend.StartController(t.Context(), request.Scope); err == nil {
			t.Fatal("late bootstrap restarted retired scope")
		}
		cancel()
	}
	if mode == "bootstrap-race" {
		runBootstrapRace()
		return
	}
	runBlockedGrant := func() {
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("HELLO was not recorded")
		}
		if mode == "blocked-grant" {
			expired, expireCancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
			if e := backend.Retire(expired, request.Scope); e == nil {
				t.Fatal("expired cleanup deadline accepted")
			}
			expireCancel()
			select {
			case <-bootouts:
				t.Fatal("expired cleanup reached bootout")
			default:
			}
			short, c := context.WithTimeout(t.Context(), 50*time.Millisecond)
			defer c()
			started := time.Now()
			e := backend.Retire(short, request.Scope)
			if e == nil || time.Since(started) > time.Second {
				t.Fatalf("blocked retirement did not honor deadline: %v", e)
			}
			select {
			case <-bootouts:
				t.Fatal("bootout bypassed blocked authority")
			default:
			}
			observation, e := backend.host.authority.Observe(t.Context(), request.Scope)
			if e != nil || observation.Phase != "granted" {
				t.Fatalf("blocked authority changed scope: %+v %v", observation, e)
			}
		}
		if mode == "revoked-grant" {
			db, e := sql.Open("sqlite", filepath.Join(root, spec.ID, "state.db"))
			if e != nil {
				t.Fatal(e)
			}
			if _, e = db.ExecContext(t.Context(), "UPDATE managed_scopes SET phase='revoked' WHERE id=?", request.Scope.ID); e != nil {
				t.Fatal(e)
			}
			if e = db.Close(); e != nil {
				t.Fatal(e)
			}
		}
		releaseBarrier()
		if mode == "blocked-grant" {
			if e := backend.Retire(t.Context(), request.Scope); e != nil {
				t.Fatal(e)
			}
		}
		select {
		case <-result:
		case <-time.After(15 * time.Second):
			t.Fatal("supervisor did not retire after authority release")
		}
		observation, e := backend.host.authority.Observe(t.Context(), request.Scope)
		if e != nil || observation.Phase != "quiescent" {
			t.Fatalf("authority barrier retirement: %+v %v", observation, e)
		}
		if mode == "revoked-grant" {
			if _, e := os.Stat(filepath.Join(base, "go")); !errors.Is(e, os.ErrNotExist) {
				t.Fatal("revoked grant received GO")
			}
		}
		cancel()
	}
	if mode == "blocked-grant" || mode == "revoked-grant" {
		runBlockedGrant()
		return
	}
	if mode == "handshake-retire" {
		waitFactoryControllerFile(t, filepath.Join(base, "connected"), deadline)
		if e := backend.Retire(t.Context(), request.Scope); e != nil {
			t.Fatal(e)
		}
		select {
		case <-result:
		case <-time.After(15 * time.Second):
			t.Fatal("supervisor did not finish")
		}
		if _, e := os.Stat(filepath.Join(base, "go")); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("retired handshake received GO")
		}
		cancel()
		return
	}
	if mode == "lost-go" {
		waitFactoryControllerOwned(t, backend, request.Scope, deadline)
		if e := os.WriteFile(filepath.Join(base, "close"), []byte("close"), 0o600); e != nil {
			t.Fatal(e)
		}
		cancel()
		select {
		case <-result:
		case <-time.After(15 * time.Second):
			t.Fatal("lost GO did not retire")
		}
		observation, e := backend.host.authority.Observe(t.Context(), request.Scope)
		if e != nil || observation.Phase != "quiescent" || observation.PID <= 1 || observation.PGID != observation.PID {
			t.Fatalf("lost GO did not retain charged ownership: %+v %v", observation, e)
		}
		return
	}
	if mode != "valid" && mode != "recovered" {
		select {
		case e := <-result:
			if e == nil {
				t.Fatal("uncertain start succeeded")
			}
		case <-time.After(15 * time.Second):
			t.Fatal("uncertain start did not retire")
		}
		observation, e := backend.host.authority.Observe(t.Context(), request.Scope)
		if e != nil || observation.Phase != "quiescent" {
			t.Fatalf("uncertain start retirement: %+v %v", observation, e)
		}
		if _, e := os.Stat(filepath.Join(base, "go")); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("unauthorized peer received GO")
		}
		cancel()
		return
	}
	for {
		observation, e := backend.host.authority.Observe(t.Context(), request.Scope)
		if e != nil {
			t.Fatal(e)
		}
		if observation.Phase == "granted" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("grant not recorded: %+v", observation)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for {
		if _, e := os.Stat(filepath.Join(base, "go")); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("peer did not receive GO")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond)
	observer, e := openFactorySupervisorBackend(t.Context(), root, spec.ID, ref)
	if e != nil {
		t.Fatal(e)
	}
	defer observer.Close()
	bootstrapCalls := 0
	observer.host.launchctl = func(context.Context, ...string) (int, error) {
		bootstrapCalls++
		return -1, errors.New("unexpected launch")
	}
	if e := observer.StartController(t.Context(), request.Scope); e == nil || bootstrapCalls != 0 {
		t.Fatal("cross-instance consumed scope bootstrapped")
	}
	observer.host.launchctl = func(context.Context, ...string) (int, error) { return -1, errors.New("unconfirmed bootout") }
	if e := observer.Retire(t.Context(), request.Scope); e == nil {
		t.Fatal("failed bootout accepted")
	}
	observation, e := observer.host.authority.Observe(t.Context(), request.Scope)
	if e != nil || observation.Phase == "quiescent" {
		t.Fatal("failed bootout marked quiescent")
	}
	observer.host.launchctl = func(context.Context, ...string) (int, error) { return 0, nil }
	observer.host.probe = func(int) error { return syscall.EPERM }
	if quiet, e := observer.Quiescent(t.Context(), request.Scope); e == nil || quiet {
		t.Fatal("uncertain probe reported quiescence")
	}
	short, c := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer c()
	started := time.Now()
	if e := observer.Retire(short, request.Scope); e == nil || time.Since(started) > time.Second {
		t.Fatal("probe uncertainty escaped cleanup deadline")
	}
	observation, e = observer.host.authority.Observe(t.Context(), request.Scope)
	if e != nil || observation.Phase == "quiescent" {
		t.Fatal("uncertain probe marked quiescent")
	}

	if mode == "recovered" {
		recovered, e := openFactorySupervisorBackend(t.Context(), root, spec.ID, ref)
		if e != nil {
			t.Fatal(e)
		}
		recovered.host.launchctl = backend.host.launchctl
		defer recovered.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(t.Context(), 5*time.Second)
		cleanupCancel()
		if e := recovered.Retire(cleanupCtx, request.Scope); e != nil {
			t.Fatal(e)
		}
		select {
		case <-result:
		case <-time.After(15 * time.Second):
			t.Fatal("supervisor did not observe recovered retirement")
		}
		observation, e := recovered.host.authority.Observe(t.Context(), request.Scope)
		if e != nil || observation.Phase != "quiescent" {
			t.Fatalf("recovered retirement: %+v %v", observation, e)
		}
		cancel()
		return
	}
	if err := backend.StartController(t.Context(), request.Scope); err == nil {
		t.Fatal("duplicate start accepted")
	}
	if _, e := backend.Invoke(t.Context(), factory.HostInvocation{Scope: request.Scope, Path: "/bin/true", Dir: repo, Deadline: request.Deadline, OutputLimit: 256}); e == nil {
		t.Fatal("controller accepted through host Invoke")
	}

	if quiet, e := backend.Quiescent(t.Context(), request.Scope); e != nil || quiet {
		t.Fatalf("controller was not retained: %v %v", quiet, e)
	}
	cancel()
	select {
	case e := <-result:
		t.Logf("supervisor result: %v", e)
	case <-time.After(15 * time.Second):
		t.Fatal("supervisor did not retire")
	}
	observation, err = backend.host.authority.Observe(t.Context(), request.Scope)
	if err != nil || observation.Phase != "quiescent" {
		t.Fatalf("retirement: %+v %v", observation, err)
	}
	if err := backend.StartController(t.Context(), request.Scope); err == nil {
		t.Fatal("late controller startup accepted")
	}
	backend.host.config.HardStop = time.Now().Add(-time.Second)
	if err := backend.Retire(t.Context(), request.Scope); err == nil {
		t.Fatal("retirement exceeded hard stop")
	}
}

func TestFactorySupervisorControllerPeer(t *testing.T) {
	socket := os.Getenv("FACTORY_CONTROLLER_SOCKET")
	if socket == "" {
		return
	}
	data, err := os.ReadFile(os.Getenv("FACTORY_CONTROLLER_REQUEST"))
	if err != nil {
		t.Fatal(err)
	}
	request, err := decodeFactoryControllerRequest(data)
	if err != nil {
		t.Fatal(err)
	}
	timer := time.AfterFunc(time.Until(request.Deadline), func() { os.Exit(0) })
	defer timer.Stop()
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if os.Getenv("FACTORY_CONTROLLER_MODE") == "handshake-retire" {
		if err := os.WriteFile(filepath.Join(filepath.Dir(os.Getenv("FACTORY_CONTROLLER_GO")), "connected"), []byte("connected"), 0o600); err != nil {
			t.Fatal(err)
		}
		for {
			time.Sleep(time.Second)
		}
	}
	pid := os.Getpid()
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatal(err)
	}
	digest := os.Getenv("FACTORY_CONTROLLER_DIGEST")
	hello := factoryHostExecHello{Version: 1, Operation: "HELLO", RequestSHA256: digest, Scope: request.Scope, PID: pid, PGID: pgid}
	if os.Getenv("FACTORY_CONTROLLER_MODE") == "forged" {
		hello.PID++
	}
	if err := json.NewEncoder(conn).Encode(hello); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("FACTORY_CONTROLLER_MODE") == "lost-go" {
		for {
			if _, e := os.Stat(filepath.Join(filepath.Dir(os.Getenv("FACTORY_CONTROLLER_GO")), "close")); e == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	var goMessage factoryHostExecGo
	if err := json.NewDecoder(conn).Decode(&goMessage); err != nil {
		t.Fatal(err)
	}
	if !factoryHostGoMatches(goMessage, hello, request.Deadline) {
		t.Fatal("invalid GO")
	}
	if err := os.WriteFile(os.Getenv("FACTORY_CONTROLLER_GO"), []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Second)
	}
}

func waitFactoryControllerFile(t *testing.T, path string, deadline time.Time) {
	t.Helper()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("controller file not observed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitFactoryControllerOwned(t *testing.T, backend *factorySupervisorBackend, scope factory.ScopeHandle, deadline time.Time) {
	t.Helper()
	for {
		observation, err := backend.host.authority.Observe(t.Context(), scope)
		if err != nil {
			t.Fatal(err)
		}
		if observation.PID > 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("controller ownership not recorded")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func testFactoryControllerGateDeadline(t *testing.T) {
	t.Helper()
	backend := &factorySupervisorBackend{controllers: make(map[string]*factoryControllerCall), gate: make(chan struct{}, 1)}
	scope := factory.ScopeHandle{ID: "blocked"}
	backend.gate <- struct{}{}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- backend.stopController(ctx, scope) }()
	select {
	case err := <-done:
		<-backend.gate
		if err == nil {
			t.Fatal("blocked retirement succeeded")
		}
	case <-time.After(200 * time.Millisecond):
		<-backend.gate
		<-done
		t.Fatal("retirement exceeded cleanup bound while waiting for gate")
	}
}

type factoryControllerAuthorityBarrier struct {
	factory.ScopeAuthority
	entered, release chan struct{}
}

func (a *factoryControllerAuthorityBarrier) RecordHello(ctx context.Context, scope factory.ScopeHandle, pid, pgid int) error {
	if err := a.ScopeAuthority.RecordHello(ctx, scope, pid, pgid); err != nil {
		return err
	}
	close(a.entered)
	<-a.release
	return nil
}

func testFactorySupervisorHostInvocation(t *testing.T) {
	t.Helper()
	cfg := factoryScopeConfigFixture(t)
	db := factoryScopeSeedJournal(t, &cfg)
	deadline := time.Now().UTC().Add(15 * time.Second)
	scope := factoryScopeSeed(t, db, cfg, "e", deadline, "reserved", 0)
	host, err := openFactoryLaunchdRuntime(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	backend := &factorySupervisorBackend{host: host, controllers: make(map[string]*factoryControllerCall), gate: make(chan struct{}, 1)}
	var child *exec.Cmd
	childCtx, childCancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		childCancel()
		if child != nil && child.Process != nil {
			_ = child.Process.Kill()
			_ = child.Wait()
			child = nil
		}
	})
	bootouts := 0
	host.launchctl = func(ctx context.Context, args ...string) (int, error) {
		if args[0] == "bootout" {
			bootouts++
			if child != nil {
				_ = child.Wait()
				child = nil
			}
			return 0, nil
		}
		exe, e := os.Executable()
		if e != nil {
			return -1, e
		}
		child = exec.CommandContext(childCtx, exe, "-test.run=^TestFactorySupervisorHostPeer$")
		child.Env = []string{"FACTORY_SUPERVISOR_HOST_PLIST=" + args[2], "GORACE=atexit_sleep_ms=0"}
		child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if e := child.Start(); e != nil {
			return -1, e
		}
		return 0, nil
	}
	invocation := factory.HostInvocation{Scope: scope, Path: "/bin/echo", Args: []string{"host result"}, Dir: cfg.Repository, Deadline: deadline, OutputLimit: 256}
	result, err := backend.Invoke(t.Context(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Stdout) != "host result\n" || result.ExitCode != 0 || bootouts != 1 {
		t.Fatalf("host invocation changed: %+v bootouts=%d", result, bootouts)
	}
	observation, err := host.authority.Observe(t.Context(), scope)
	if err != nil || observation.Phase != "quiescent" {
		t.Fatalf("host lifecycle did not retire: %+v %v", observation, err)
	}
}

func TestFactorySupervisorHostPeer(t *testing.T) {
	path := os.Getenv("FACTORY_SUPERVISOR_HOST_PLIST")
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc factoryJobXMLValue
	if err := xml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	var args []string
	var walk func(factoryJobXMLValue)
	walk = func(node factoryJobXMLValue) {
		if node.XMLName.Local == "array" {
			for _, c := range node.Children {
				args = append(args, c.Text)
			}
			return
		}
		for _, c := range node.Children {
			walk(c)
		}
	}
	walk(doc)
	if len(args) < 4 {
		t.Fatal("missing host wrapper argv")
	}
	command := newFactoryHostExecCmd()
	command.SetArgs(args[3:])
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestFactorySupervisorPeerLifetime(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("/private/tmp", "fpl-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	scope := factory.ScopeHandle{ID: strings.Repeat("a", 32), Nonce: strings.Repeat("b", 32), Kind: "controller", Backend: "launchd", Generation: 1}
	scope.Name = "dev.getoro.factory." + factoryLaunchdObjectiveHash(factoryLaunchdConfig{StateRoot: root, ObjectiveID: "lifetime"}) + ".controller." + scope.ID + "." + scope.Nonce
	request := factoryControllerRequest{Version: 1, Scope: scope, StateRoot: root, ObjectiveID: "lifetime", Runtime: factory.ManagedRuntimeReference{Path: filepath.Join(root, "lifetime", "runtime", "runtime.json"), SHA256: strings.Repeat("c", 64)}, Deadline: time.Now().UTC().Add(1500 * time.Millisecond)}
	data, err := encodeFactoryControllerRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "request.json")
	if err := os.WriteFile(path, data, 0o400); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(executable, "-test.run=^TestFactorySupervisorControllerPeer$")
	child.Env = []string{"FACTORY_CONTROLLER_SOCKET=" + socket, "FACTORY_CONTROLLER_REQUEST=" + path, "FACTORY_CONTROLLER_DIGEST=" + factoryHostDigest(data), "FACTORY_CONTROLLER_GO=" + filepath.Join(dir, "go"), "FACTORY_CONTROLLER_MODE=handshake-retire", "GORACE=atexit_sleep_ms=0"}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = child.Process.Kill()
			<-done
		}
	})
	waitFactoryControllerFile(t, filepath.Join(dir, "connected"), time.Now().Add(time.Second))
	select {
	case err := <-done:
		waited = true
		if err != nil {
			t.Fatalf("peer did not exit naturally: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("peer outlived its request deadline")
	}
}
