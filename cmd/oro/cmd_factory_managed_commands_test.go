//go:build darwin

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"oro/pkg/factory"
)

func TestFactoryManagedCommands(t *testing.T) {
	for _, name := range []string{"_controller", "_supervise"} {
		cmd, _, err := newFactoryCmd().Find([]string{name})
		if err != nil || cmd.Name() != name || !cmd.Hidden {
			t.Errorf("hidden command %s is not registered", name)
		}
	}
	t.Run("parser rejects extras", func(t *testing.T) {
		for _, args := range [][]string{{"_controller", "--unknown"}, {"_controller", "extra"}, {"_supervise", "--unknown"}, {"_supervise", "extra"}} {
			command := newFactoryCmd()
			command.SetErr(io.Discard)
			command.SetOut(io.Discard)
			command.SetArgs(args)
			if err := command.Execute(); err == nil {
				t.Fatalf("accepted %v", args)
			}
		}
	})
	t.Run("bounded diagnostics", func(t *testing.T) { testFactoryManagedCommandsCountedSupervisor(t, "diagnostic") })
	t.Run("counted supervisor", func(t *testing.T) { testFactoryManagedCommandsCountedSupervisor(t, "failure") })
	t.Run("controller core", func(t *testing.T) { testFactoryManagedCommandsCountedSupervisor(t, "core") })
	t.Run("revoked after GO", func(t *testing.T) { testFactoryManagedCommandsCountedSupervisor(t, "revoked") })
	t.Run("expired supervisor", testFactoryManagedCommandsInvalidSupervisor)
	t.Run("no GO", func(t *testing.T) { testFactoryManagedCommandsHandshake(t, "none") })
	t.Run("GO with missing admission", func(t *testing.T) { testFactoryManagedCommandsHandshake(t, "valid") })
	t.Run("forged GO", func(t *testing.T) { testFactoryManagedCommandsHandshake(t, "forged") })
	t.Run("changed deadline", func(t *testing.T) { testFactoryManagedCommandsHandshake(t, "deadline") })
	t.Run("cancel before GO", func(t *testing.T) { testFactoryManagedCommandsHandshake(t, "cancel") })
	t.Run("not group leader", func(t *testing.T) { testFactoryManagedCommandsHandshake(t, "not-leader") })
}

func testFactoryManagedCommandsInvalidSupervisor(t *testing.T) {
	t.Helper()
	var previous string
	for _, stop := range []string{"2000-01-01T00:00:00Z", time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano)} {
		var opened int
		cmd := buildFactorySuperviseCmd(func(context.Context, string, string, factory.ManagedRuntimeReference) (*factorySupervisorBackend, error) {
			opened++
			return nil, errors.New("unexpected open")
		})
		var diagnostics bytes.Buffer
		cmd.SetErr(&diagnostics)
		cmd.SetArgs([]string{"--state-dir", "/missing", "--id", "test", "--runtime-config", "/missing/runtime.json", "--runtime-config-sha256", strings.Repeat("a", 64), "--hard-stop", stop})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("handled invalid authority: %v", err)
		}
		if previous != "" && previous == diagnostics.String() {
			t.Fatal("distinct failures yielded identical diagnostic")
		}
		previous = diagnostics.String()
		if strings.Contains(diagnostics.String(), "Usage:") {
			t.Fatal("service failure printed usage")
		}
		if opened != 0 || diagnostics.Len() == 0 || diagnostics.Len() > 256 {
			t.Fatalf("opened %d, diagnostic length %d", opened, diagnostics.Len())
		}
	}
}

func TestFactoryManagedCommandsChild(t *testing.T) {
	if os.Getenv("TEST_MANAGED_CHILD") != "1" {
		return
	}
	cmd := newFactoryCmd()
	cmd.SetArgs([]string{"_controller", "--request", os.Getenv("TEST_MANAGED_REQUEST"), "--request-sha256", os.Getenv("TEST_MANAGED_DIGEST"), "--socket", os.Getenv("TEST_MANAGED_SOCKET")})
	ctx := context.Background()
	if os.Getenv("TEST_MANAGED_CANCEL") == "1" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
	}
	if err := cmd.ExecuteContext(ctx); err == nil {
		t.Fatal("invalid handshake accepted")
	}
}

func testFactoryManagedCommandsHandshake(t *testing.T, grant string) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin peer authentication")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	scope := factory.ScopeHandle{Kind: "controller", Backend: "launchd", Generation: 1, ID: strings.Repeat("a", 32), Nonce: strings.Repeat("b", 32)}
	config := factoryLaunchdConfig{StateRoot: root, ObjectiveID: "test"}
	scope.Name = "dev.getoro.factory." + factoryLaunchdObjectiveHash(config) + ".controller." + scope.ID + "." + scope.Nonce
	request := factoryControllerRequest{Version: 1, Scope: scope, StateRoot: root, ObjectiveID: "test", Runtime: factory.ManagedRuntimeReference{Path: filepath.Join(root, "test", "runtime", "runtime.json"), SHA256: strings.Repeat("c", 64)}, Deadline: time.Now().UTC().Add(20 * time.Second)}
	dir := filepath.Join(root, "test", "host-scopes", scope.ID+"."+scope.Nonce)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := encodeFactoryControllerRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "request.json")
	if err := os.WriteFile(path, data, 0o400); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "link")
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := readFactoryControllerRequest(path, factoryHostDigest(data)); err == nil {
		t.Fatal("hard-linked request accepted")
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if _, err := readFactoryControllerRequest(path, strings.Repeat("d", 64)); err == nil {
		t.Fatal("changed digest accepted")
	}
	if _, err := readFactoryControllerRequest(filepath.Join(dir, ".", "alias.json"), factoryHostDigest(data)); err == nil {
		t.Fatal("alias path accepted")
	}
	socketParent := "/private/tmp/of-" + factoryLaunchdObjectiveHash(config)
	socketDir := filepath.Join(socketParent, scope.ID+scope.Nonce)
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketParent) })
	socket := filepath.Join(socketDir, "s")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := os.Chmod(socket, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = listener.SetDeadline(time.Now().Add(10 * time.Second))
	for _, p := range []string{root, filepath.Join(root, "test"), filepath.Join(root, "test", "host-scopes"), dir, socketParent, socketDir} {
		if err := factoryHostPrivateDirectory(p); err != nil {
			t.Fatalf("directory %s: %v", p, err)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	childCtx, childCancel := context.WithTimeout(context.Background(), 25*time.Second)
	child := exec.CommandContext(childCtx, exe, "-test.run=^TestFactoryManagedCommandsChild$")
	t.Cleanup(func() {
		childCancel()
		if child.Process != nil && child.ProcessState == nil {
			_ = syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
			_ = child.Wait()
		}
	})
	child.Env = append(os.Environ(), "TEST_MANAGED_CHILD=1", "TEST_MANAGED_REQUEST="+path, "TEST_MANAGED_DIGEST="+factoryHostDigest(data), "TEST_MANAGED_SOCKET="+socket, "TEST_MANAGED_CANCEL="+map[bool]string{true: "1", false: "0"}[grant == "cancel"])
	var childOutput bytes.Buffer
	child.Stdout = &childOutput
	child.Stderr = &childOutput
	if grant != "not-leader" {
		child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	if grant == "not-leader" {
		if err := child.Wait(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(childOutput.String(), "process group leader") {
			t.Fatalf("nonleader accepted: %s", childOutput.String())
		}
		return
	}
	conn, err := listener.AcceptUnix()
	if err != nil {
		_ = syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
		_ = child.Wait()
		t.Fatalf("accept: %v child: %s", err, childOutput.String())
	}
	var hello factoryHostExecHello
	if err := readFactoryRuntimeFrame(conn, &hello); err != nil {
		t.Fatal(err)
	}
	if hello.PID != child.Process.Pid || hello.PGID != child.Process.Pid || hello.Scope != scope {
		t.Fatal("invalid actual child HELLO")
	}
	if grant != "none" && grant != "cancel" {
		response := factoryHostExecGo{Version: 1, Operation: "GO", RequestSHA256: factoryHostDigest(data), Scope: scope, PID: child.Process.Pid, PGID: child.Process.Pid, Deadline: request.Deadline}
		if grant == "forged" {
			response.RequestSHA256 = strings.Repeat("d", 64)
		}
		if grant == "deadline" {
			response.Deadline = response.Deadline.Add(time.Second)
		}
		if err := json.NewEncoder(conn).Encode(response); err != nil {
			t.Fatal(err)
		}
	}
	if grant == "cancel" {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		var b [1]byte
		if _, err := conn.Read(b[:]); err == nil {
			t.Fatal("canceled handshake remained open")
		}
	}
	_ = conn.Close()
	if err := child.Wait(); err != nil {
		t.Fatalf("child: %v", err)
	}
	if grant == "valid" && !strings.Contains(childOutput.String(), "admission") {
		t.Fatalf("GO did not reach admission: %s", childOutput.String())
	}
	if (grant == "forged" || grant == "deadline") && !strings.Contains(childOutput.String(), "GO mismatch") {
		t.Fatalf("forged GO accepted: %s", childOutput.String())
	}
	if _, err := os.Stat(filepath.Join(root, "test", "runtime")); !os.IsNotExist(err) {
		t.Fatal("runtime touched before GO")
	}
}

func testFactoryManagedCommandsCountedSupervisor(t *testing.T, mode string) {
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
		Runtime:  factory.RuntimePolicy{DockerContext: "fixture", ImageDigest: "sha256:" + strings.Repeat("a", 64), MemoryBytes: 256 << 20, ScratchBytes: 1 << 20, SourceBytes: 1 << 20, HostRequiredBytes: 1, HostReserveBytes: 1 << 20, CPUs: 1, PIDs: 16, MaxFiles: 16, SourceInodes: 16, ScratchInodes: 16, MaxModelCalls: 1, MaxModelTurns: 1, MaxToolCalls: 1, MaxHostInvocations: 20, Model: "fixture", ModelExecutable: model, GitExecutable: git, MaxModelInvocationUSD: "0.01"}, Acceptance: []factory.Check{{ID: "check", Args: []string{"/bin/true"}}}, ReviewInstructions: "fixture",
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
	t.Cleanup(func() { _ = listener.Close() })
	if err := os.Chmod(endpoint, 0o600); err != nil {
		t.Fatal(err)
	}
	oro := filepath.Join(base, "oro")
	docker := filepath.Join(base, "docker")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fixtureState := filepath.Join(base, "docker-state.json")
	helperCoverage := filepath.Join(base, "helper-coverage")
	if err := os.Mkdir(helperCoverage, 0o700); err != nil {
		t.Fatal(err)
	}
	oroProgram := []byte(fmt.Sprintf("#!/bin/sh\nexport GORACE=atexit_sleep_ms=0\nexport GOCOVERDIR=%s\nexec %s -test.run=^TestFactoryManagedReadyProgram$ -- \"$@\"\n", strconv.Quote(helperCoverage), strconv.Quote(exe)))
	dockerProgram := []byte(fmt.Sprintf("#!/bin/sh\nexport GORACE=atexit_sleep_ms=0\nexport GOCOVERDIR=%s\nexec %s -test.run=^TestFactoryManagedDockerProgram$ -- %s \"$@\"\n", strconv.Quote(helperCoverage), strconv.Quote(exe), strconv.Quote(fixtureState)))
	for path, program := range map[string][]byte{oro: oroProgram, docker: dockerProgram} {
		if err := os.WriteFile(path, program, 0o500); err != nil {
			t.Fatal(err)
		}
	}
	if mode == "core" {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, docker, "--config", "/tmp/config", "--context", "fixture", "volume", "create", "--driver", "local", "fixture-volume")
		command.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "LANG=C"}
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		if err := command.Run(); err != nil || stdout.String() != "fixture-volume\n" || stderr.Len() != 0 {
			t.Fatalf("fixture protocol: exit=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
		}
	}
	config := factoryRuntimeConfig{Version: 1, StateRoot: root, ObjectiveID: spec.ID, PolicyHash: view.PolicyHash, OroPath: oro, OroSHA256: factoryHostDigest(oroProgram), GitPath: view.GitPath, GitSHA256: view.GitSHA256, Repository: repo, TargetRef: view.TargetRef, Deadline: view.Deadline, HardStop: view.HardStop, CleanupTimeoutSeconds: view.CleanupTimeoutSeconds, DockerPath: docker, DockerSHA256: factoryHostDigest(dockerProgram), DockerContext: view.DockerContext, DockerEndpoint: "unix://" + endpoint}
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
	var bootstraps int
	var retired bool
	var child *exec.Cmd
	childCtx, childCancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(func() {
		childCancel()
		if child != nil && child.Process != nil {
			_ = syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
			_ = child.Wait()
			child = nil
		}
	})
	open := func(ctx context.Context, root, id string, ref factory.ManagedRuntimeReference) (*factorySupervisorBackend, error) {
		if mode == "diagnostic" {
			return nil, errors.New("distinct\x00\n\x1b" + strings.Repeat("x", 8000))
		}
		backend, err := openFactorySupervisorBackend(ctx, root, id, ref)
		if err != nil {
			return nil, err
		}
		backend.host.launchctl = func(_ context.Context, args ...string) (int, error) {
			if args[0] == "bootstrap" {
				bootstraps++
				if mode == "failure" {
					return -1, errors.New("recorded bootstrap failure")
				}
				exe, err := os.Executable()
				if err != nil {
					return -1, err
				}
				child = exec.CommandContext(childCtx, exe, "-test.run=^TestFactoryManagedCommandsCoreChild$")
				managedForwardCoverage(child)
				child.Env = append(os.Environ(), "TEST_MANAGED_PLIST="+args[2], "TEST_MANAGED_REVOKE="+mode, "TEST_MANAGED_MARKER="+filepath.Join(base, "revoked"), "GORACE=atexit_sleep_ms=0")
				child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
				ownedChild := child
				child.Cancel = func() error { return syscall.Kill(-ownedChild.Process.Pid, syscall.SIGKILL) }
				child.WaitDelay = 3 * time.Second
				if err := child.Start(); err != nil {
					return -1, err
				}
				return 0, nil
			}
			if child != nil {
				err := child.Wait()
				child = nil
				if err != nil {
					return -1, fmt.Errorf("controller child retirement: %w", err)
				}
			}
			retired = true
			return 0, nil
		}
		return backend, nil
	}
	cmd := buildFactorySuperviseCmd(open)
	var diagnostics bytes.Buffer
	cmd.SetErr(&diagnostics)
	cmd.SetArgs([]string{"--state-dir", root, "--id", spec.ID, "--runtime-config", ref.Path, "--runtime-config-sha256", ref.SHA256, "--hard-stop", view.HardStop.Format(time.RFC3339Nano)})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if mode == "diagnostic" {
		out := diagnostics.String()
		if !strings.Contains(out, "distinct") || len(out) != 4096 || strings.Count(out, "\n") != 1 || strings.ContainsAny(out, "\x00\x1b") || strings.Contains(out, "Usage:") {
			t.Fatalf("unsafe diagnostic length=%d", len(out))
		}
		return
	}
	if mode != "failure" && !retired {
		t.Fatal("controller was not retired by the supervisor")
	}
	if bootstraps != 1 {
		t.Fatalf("recorded %d bootstraps", bootstraps)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, spec.ID, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if mode != "failure" {
		var phase string
		if err := db.QueryRowContext(t.Context(), "SELECT phase FROM managed_scopes WHERE name LIKE ?", "%controller%").Scan(&phase); err != nil || phase != "quiescent" {
			t.Fatalf("controller retirement phase %q: %v", phase, err)
		}
	}
	//nolint:nestif // Verify the durable result of the actual controller child.
	if mode == "core" {
		var state factory.Status
		state, err = factory.ReadStatus(t.Context(), root, spec.ID)
		if err != nil {
			t.Fatal(err)
		}
		if state.Attempts != 1 || state.CandidateSHA != "" {
			t.Fatalf("core did not preserve one attempt without candidate: %+v", state)
		}
		result, err := os.ReadFile(filepath.Join(base, "revoked-result"))
		if err != nil {
			t.Fatalf("controller result: %v", err)
		}
		if !strings.Contains(string(result), "managed candidate interrupted: engineer-adapter-unavailable") {
			var supervision []byte
			_ = db.QueryRowContext(t.Context(), "SELECT body FROM managed_supervision").Scan(&supervision)
			hosts, _ := os.ReadFile(filepath.Join(base, "revoked-hosts"))
			t.Fatalf("unexpected controller result: %s; supervision %s; host calls %d", result, supervision, bytes.Count(hosts, []byte("\n")))
		}
		var candidateBody []byte
		var candidates int
		if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_docker_candidates").Scan(&candidates); err != nil || candidates != 1 {
			t.Fatalf("candidate count %d: %v", candidates, err)
		}
		if err := db.QueryRowContext(t.Context(), "SELECT body FROM managed_docker_candidates").Scan(&candidateBody); err != nil {
			t.Fatal(err)
		}
		var candidate map[string]any
		if err := json.Unmarshal(candidateBody, &candidate); err != nil {
			t.Fatal(err)
		}
		if candidate["phase"] != "retired" {
			t.Fatalf("candidate not retired: %s", candidateBody)
		}
		fixtureData, err := os.ReadFile(fixtureState)
		if err != nil {
			t.Fatal(err)
		}
		var dockerState managedFakeDockerState
		if err := json.Unmarshal(fixtureData, &dockerState); err != nil {
			t.Fatal(err)
		}
		wantDocker := []string{"volume-create", "volume-inspect", "keeper-create", "keeper-inspect", "keeper-ready-inspect", "keeper-start", "keeper-inspect", "source-seed", "keeper-inspect", "keeper-inspect", "candidate-create", "candidate-inspect", "candidate-ready-inspect", "candidate-start", "candidate-inspect", "keeper-inspect", "candidate-ready-inspect", "candidate-stop", "candidate-ready-inspect"}
		if !slices.Equal(dockerState.Operations, wantDocker) {
			t.Fatalf("Docker operations = %v, want %v", dockerState.Operations, wantDocker)
		}
		if dockerState.KeeperLabels["dev.fixture.image"] != "inherited" || dockerState.CandidateLabels["dev.fixture.image"] != "inherited" {
			t.Fatal("image label was not inherited by both containers")
		}
		if dockerState.Name == dockerState.CandidateName || !dockerState.Running || dockerState.CandidateRunning {
			t.Fatalf("keeper/candidate identity or state: %+v", dockerState)
		}
		if candidate["guard_deadline"] != candidate["execution_deadline"] || candidate["guard_deadline"] != dockerState.CandidateDeadline || dockerState.Deadline != view.HardStop.Format(time.RFC3339Nano) {
			t.Fatalf("keeper/candidate deadline mismatch: %s %+v", candidateBody, dockerState)
		}
		hostData, err := os.ReadFile(filepath.Join(base, "revoked-hosts"))
		if err != nil {
			t.Fatal(err)
		}
		decoder := json.NewDecoder(bytes.NewReader(hostData))
		var calls []factory.HostInvocation
		for decoder.More() {
			var call factory.HostInvocation
			if err := decoder.Decode(&call); err != nil {
				t.Fatal(err)
			}
			calls = append(calls, call)
		}
		wantPaths := []string{view.GitPath, view.GitPath, view.GitPath, view.GitPath, view.GitPath, view.GitPath, docker, docker, docker, docker, oro, docker, docker, docker, docker, docker, docker, oro, docker, docker, oro}
		if len(calls) != len(wantPaths) {
			t.Fatalf("host calls = %d, want %d", len(calls), len(wantPaths))
		}
		workerDeadline, err := time.Parse(time.RFC3339Nano, dockerState.CandidateDeadline)
		if err != nil {
			t.Fatal(err)
		}
		started, err := time.Parse(time.RFC3339Nano, candidate["execution_started_at"].(string))
		if err != nil || !workerDeadline.Equal(started.Add(time.Duration(spec.StepTimeoutSeconds)*time.Second)) || workerDeadline.After(view.Deadline) {
			t.Fatalf("worker deadline changed: %s, %v", candidateBody, err)
		}
		for index, call := range calls {
			if call.Path != wantPaths[index] {
				t.Fatalf("host call %d path = %s, want %s", index, call.Path, wantPaths[index])
			}
			if index >= 14 && index < 20 && !call.Deadline.Equal(workerDeadline) {
				t.Fatalf("candidate call %d deadline = %s, want %s", index, call.Deadline, workerDeadline)
			}
		}
		var permit factory.DockerCleanupPermit
		if err := json.Unmarshal(calls[20].Stdin, &permit); err != nil || permit.Operation != factory.DockerCleanupStop || permit.Target.Kind != "container" || permit.Binding.ContainerID != strings.Repeat("e", 64) || permit.Binding.Labels["dev.fixture.image"] != "inherited" || permit.Scope != calls[20].Scope || !permit.Deadline.Equal(calls[20].Deadline) || permit.Deadline.After(view.HardStop) || !slices.Equal(calls[20].Args, []string{"factory", "_docker-cleanup", "--runtime-config", ref.Path, "--runtime-config-sha256", ref.SHA256}) {
			t.Fatalf("cleanup call/permit differs: %+v, %v", permit, err)
		}
		var targetPhase, cleanupPhase string
		if err := db.QueryRowContext(t.Context(), "SELECT (SELECT phase FROM managed_scopes WHERE id=?), (SELECT phase FROM managed_scopes WHERE id=?)", permit.Target.ID, permit.Scope.ID).Scan(&targetPhase, &cleanupPhase); err != nil || targetPhase != "quiescent" || cleanupPhase != "quiescent" {
			t.Fatalf("retirement phases = %q/%q: %v", targetPhase, cleanupPhase, err)
		}
		var hostCount, unretired int
		if err := db.QueryRowContext(t.Context(), "SELECT (SELECT count(*) FROM managed_reservations WHERE kind='host'), (SELECT count(*) FROM managed_scopes WHERE name LIKE '%host%' AND phase != 'quiescent')").Scan(&hostCount, &unretired); err != nil || hostCount != 20 || unretired != 0 {
			t.Fatalf("host reservations %d, unretired %d: %v", hostCount, unretired, err)
		}
		if candidate["container_started_at"] != "2026-10-24T06:00:00Z" {
			t.Fatalf("candidate lifetime missing: %s", candidateBody)
		}
		afterView, err := factory.ReadManagedAdmission(t.Context(), root, spec.ID)
		if err != nil || !afterView.Deadline.Equal(view.Deadline) {
			t.Fatalf("objective deadline changed: %v", err)
		}

		var models int
		if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_reservations WHERE kind='model'").Scan(&models); err != nil || models != 0 {
			t.Fatalf("model reservations %d: %v", models, err)
		}
		var workspace, stage string
		var workspaceBody []byte
		var attempt int
		if err := db.QueryRowContext(t.Context(), "SELECT id, stage, attempt, body FROM managed_workspaces").Scan(&workspace, &stage, &attempt, &workspaceBody); err != nil {
			t.Fatal(err)
		}
		var workspaceData map[string]any
		if err := json.Unmarshal(workspaceBody, &workspaceData); err != nil {
			t.Fatal(err)
		}
		seed, err := os.ReadFile(fixtureState + "-seed")
		if err != nil || factoryHostDigest(seed) != workspaceData["seed_sha256"] || float64(len(seed)) != workspaceData["seed_bytes"] {
			t.Fatalf("seed does not match ready workspace: %v", err)
		}
		if candidate["workspace_id"] != workspace {
			t.Fatalf("candidate workspace differs: %s", candidateBody)
		}
		binding, ok := candidate["binding"].(map[string]any)
		if !ok || binding["container_id"] != strings.Repeat("e", 64) || binding["image_id"] != spec.Runtime.ImageDigest {
			t.Fatalf("candidate binding differs: %s", candidateBody)
		}
		if workspaceData["phase"] != "ready" {
			t.Fatalf("workspace not ready: %s", workspaceBody)
		}
		if !factoryJobLowerHex(workspace, 32) || stage != "worker" || attempt != 1 {
			t.Fatalf("unexpected worker workspace %q %q %d", workspace, stage, attempt)
		}
		var scopes int
		if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_scopes").Scan(&scopes); err != nil || scopes < 2 {
			t.Fatalf("host wrapper scopes %d: %v", scopes, err)
		}
	}
	//nolint:nestif // Check each post-GO fence and its absence of side effects.
	if mode == "revoked" {
		marker, e := os.ReadFile(filepath.Join(base, "revoked"))
		if e != nil || string(marker) != "revoked" {
			t.Fatalf("revocation marker: %q %v", marker, e)
		}
		result, e := os.ReadFile(filepath.Join(base, "revoked-result"))
		if e != nil {
			t.Fatalf("child result: %v", e)
		}
		if !strings.Contains(string(result), "managed owner requires granted controller") {
			t.Fatalf("unexpected revocation result: %s", result)
		}
		var workspaces, hosts int
		if e := db.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_workspaces").Scan(&workspaces); e != nil {
			t.Fatal(e)
		}
		if e := db.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_scopes WHERE name NOT LIKE ?", "%controller%").Scan(&hosts); e != nil {
			t.Fatal(e)
		}
		if workspaces != 0 || hosts != 0 {
			t.Fatalf("unexpected workspaces %d host scopes %d", workspaces, hosts)
		}
		status, err := factory.ReadStatus(t.Context(), root, spec.ID)
		if err != nil || status.Attempts != 0 {
			t.Fatalf("revoked controller started work: %+v %v", status, err)
		}
	}
	for _, kind := range []string{"supervisor", "controller"} {
		var count int
		if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_reservations WHERE kind=?", kind).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s counted %d: %v", kind, count, err)
		}
	}
}

func TestFactoryManagedCommandsCoreChild(t *testing.T) {
	path := os.Getenv("TEST_MANAGED_PLIST")
	if path == "" {
		return
	}
	timer := time.AfterFunc(105*time.Second, func() { os.Exit(2) })
	defer timer.Stop()
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
		t.Fatal("missing controller argv")
	}
	open := func(ctx context.Context, config factoryLaunchdConfig) (*factoryLaunchdRuntime, error) {
		host, err := openFactoryLaunchdRuntime(ctx, config)
		//nolint:nestif // Verify the exact post-GO grant before mutating it.
		if err == nil && os.Getenv("TEST_MANAGED_REVOKE") == "revoked" {
			db, e := sql.Open("sqlite", filepath.Join(config.StateRoot, config.ObjectiveID, "state.db"))
			if e != nil {
				return nil, e
			}
			result, e := db.ExecContext(ctx, "UPDATE managed_scopes SET phase='revoked' WHERE phase='granted' AND name LIKE ?", "%controller%")
			_ = db.Close()
			if e != nil {
				return nil, e
			}
			n, e := result.RowsAffected()
			if e != nil || n != 1 {
				return nil, errors.New("controller grant not revoked")
			}
			if e := os.WriteFile(os.Getenv("TEST_MANAGED_MARKER"), []byte("revoked"), 0o600); e != nil {
				return nil, e
			}
		}
		if err != nil {
			return nil, err
		}
		var child *exec.Cmd
		childCtx, childCancel := context.WithTimeout(context.Background(), 45*time.Second)
		t.Cleanup(func() {
			childCancel()
			if child != nil && child.Process != nil {
				_ = syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
				_ = child.Wait()
				child = nil
			}
		})
		host.launchctl = func(_ context.Context, args ...string) (int, error) {
			if args[0] == "bootout" {
				if child != nil {
					err := child.Wait()
					child = nil
					if err != nil {
						return -1, fmt.Errorf("host child retirement: %w", err)
					}
				}
				return 0, nil
			}
			if err := managedRecordHostInvocation(args[2], os.Getenv("TEST_MANAGED_MARKER")+"-hosts"); err != nil {
				return -1, err
			}
			exe, err := os.Executable()
			if err != nil {
				return -1, err
			}
			child = exec.CommandContext(childCtx, exe, "-test.run=^TestFactoryManagedHostPeer$")
			managedForwardCoverage(child)
			child.Env = append(os.Environ(), "FACTORY_SUPERVISOR_HOST_PLIST="+args[2], "GORACE=atexit_sleep_ms=0")
			child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			ownedChild := child
			child.Cancel = func() error { return syscall.Kill(-ownedChild.Process.Pid, syscall.SIGKILL) }
			child.WaitDelay = 3 * time.Second
			if err := child.Start(); err != nil {
				return -1, err
			}
			return 0, nil
		}
		return host, nil
	}
	cmd := buildFactoryControllerCmd(open)
	cmd.SetArgs(args[3:])
	err = cmd.ExecuteContext(t.Context())
	if err != nil {
		_ = os.WriteFile(os.Getenv("TEST_MANAGED_MARKER")+"-result", []byte(err.Error()), 0o600)
	}
}

func managedForwardCoverage(cmd *exec.Cmd) {
	if f := flag.Lookup("test.gocoverdir"); f != nil && f.Value.String() != "" {
		cmd.Args = append(cmd.Args, "-test.gocoverdir="+f.Value.String())
	}
}

func managedRecordHostInvocation(plist, output string) error {
	data, err := os.ReadFile(plist)
	if err != nil {
		return err
	}
	var doc factoryJobXMLValue
	if err := xml.Unmarshal(data, &doc); err != nil {
		return err
	}
	var args []string
	var walk func(factoryJobXMLValue)
	walk = func(node factoryJobXMLValue) {
		if node.XMLName.Local == "array" {
			for _, child := range node.Children {
				args = append(args, child.Text)
			}
			return
		}
		for _, child := range node.Children {
			walk(child)
		}
	}
	walk(doc)
	index := slices.Index(args, "--request")
	if index < 0 || index+1 >= len(args) {
		return errors.New("missing host request")
	}
	data, err = os.ReadFile(args[index+1])
	if err != nil {
		return err
	}
	var request factoryHostExecRequest
	if err := json.Unmarshal(data, &request); err != nil {
		return err
	}
	f, err := os.OpenFile(output, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(request.Invocation)
}

type managedFakeDockerState struct {
	Volume, Name, Image, Deadline                                   string
	Labels, Options, KeeperLabels                                   map[string]string
	Running                                                         bool
	CandidateName, CandidateImage, CandidateDeadline                string
	CandidateLabels                                                 map[string]string
	CandidateRunning                                                bool
	CandidateStopped                                                bool
	CandidateMemory, CandidateCPUs, CandidatePIDs, CandidateScratch string
	Operations                                                      []string
}

func TestFactoryManagedReadyProgram(t *testing.T) {
	args := managedFixtureArgs()
	if len(args) == 0 {
		return
	}
	cmd := newFactoryCmd()
	cmd.SetArgs(args[1:])
	if err := cmd.Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func managedFixtureArgs() []string {
	for i, arg := range os.Args {
		if arg == "--" {
			return os.Args[i+1:]
		}
	}
	return nil
}

func TestFactoryManagedDockerProgram(t *testing.T) {
	args := managedFixtureArgs()
	if len(args) == 0 {
		return
	}
	if len(args) < 7 || args[1] != "--config" || args[3] != "--context" || args[4] != "fixture" {
		os.Exit(2)
	}
	path := args[0]
	args = args[5:]
	var state managedFakeDockerState
	if data, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(data, &state) != nil {
			os.Exit(2)
		}
	}
	save := func() {
		data, err := json.Marshal(state)
		if err != nil || os.WriteFile(path, data, 0o600) != nil {
			os.Exit(2)
		}
	}
	emit := func(value any) {
		if json.NewEncoder(os.Stdout).Encode(value) != nil {
			os.Exit(2)
		}
	}
	//nolint:nestif // Capture the real volume invocation operands.
	if len(args) >= 3 && args[0] == "volume" && args[1] == "create" {
		if args[len(args)-1] != "fixture-volume" {
			state.Operations = nil
		}
		state.Operations = append(state.Operations, "volume-create")
		state.Labels = map[string]string{}
		state.Options = map[string]string{}
		state.Volume = args[len(args)-1]
		for i := 2; i < len(args)-1; i++ {
			if args[i] == "--label" || args[i] == "--opt" {
				key, value, ok := strings.Cut(args[i+1], "=")
				if !ok {
					os.Exit(2)
				}
				if args[i] == "--label" {
					state.Labels[key] = value
				} else {
					state.Options[key] = value
				}
				i++
			}
		}
		save()
		fmt.Println(state.Volume)
		os.Exit(0)
	}
	if len(args) == 3 && args[0] == "volume" && args[1] == "inspect" && args[2] == state.Volume {
		state.Operations = append(state.Operations, "volume-inspect")
		save()
		emit([]any{map[string]any{"Name": state.Volume, "CreatedAt": "2026-10-24T05:49:04Z", "Driver": "local", "Labels": state.Labels, "Options": state.Options, "Mountpoint": "/var/lib/docker/volumes/owned/_data", "Scope": "local"}})
		os.Exit(0)
	}
	if len(args) >= 6 && args[0] == "container" && args[1] == "create" {
		candidate := slices.Contains(args, "--tmpfs")
		if err := managedFakeCreate(&state, args, candidate); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		id := strings.Repeat("d", 64)
		if candidate {
			id = strings.Repeat("e", 64)
			state.Operations = append(state.Operations, "candidate-create")
		} else {
			state.Operations = append(state.Operations, "keeper-create")
		}
		save()
		fmt.Println(id)
		os.Exit(0)
	}
	if len(args) >= 3 && ((args[0] == "container" && args[1] == "inspect") || (args[0] == "inspect" && args[1] == "--type")) {
		value, err := managedFakeInspection(&state, args)
		if err != nil {
			os.Exit(2)
		}
		save()
		emit([]any{value})
		os.Exit(0)
	}
	if slices.Equal(args, []string{"stop", "--signal", "SIGKILL", "--timeout", "0", strings.Repeat("e", 64)}) && state.CandidateRunning {
		state.CandidateRunning = false
		state.CandidateStopped = true
		state.Operations = append(state.Operations, "candidate-stop")
		save()
		fmt.Println(strings.Repeat("e", 64))
		os.Exit(0)
	}
	if len(args) == 3 && args[0] == "start" && args[1] == "--attach" {
		switch args[2] {
		case strings.Repeat("d", 64):
			state.Running = true
			state.Operations = append(state.Operations, "keeper-start")
		case strings.Repeat("e", 64):
			state.CandidateRunning = true
			state.Operations = append(state.Operations, "candidate-start")
		default:
			os.Exit(2)
		}
		save()
		fmt.Println("READY")
		timer := time.NewTimer(45 * time.Second)
		defer timer.Stop()
		<-timer.C
		os.Exit(2)
	}

	if len(args) == 3 && args[0] == "cp" && args[1] == "-" {
		state.Operations = append(state.Operations, "source-seed")
		save()
		seed, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
		if err != nil || os.WriteFile(path+"-seed", seed, 0o600) != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	_, _ = fmt.Fprintln(os.Stderr, "unexpected fake Docker arguments", args)
	os.Exit(2)
}

func managedFakeInspection(state *managedFakeDockerState, args []string) (map[string]any, error) {
	id := args[len(args)-1]
	candidate := id == strings.Repeat("e", 64)
	if !candidate && id != strings.Repeat("d", 64) {
		return nil, errors.New("unknown container")
	}
	kind := "keeper"
	if candidate {
		kind = "candidate"
	}
	if args[0] == "inspect" {
		kind += "-ready-inspect"
	} else {
		kind += "-inspect"
	}
	state.Operations = append(state.Operations, kind)
	return managedFakeKeeper(*state, candidate), nil
}

func managedFakeCreate(s *managedFakeDockerState, args []string, candidate bool) error {
	if len(args) < 5 || args[len(args)-2] != "--deadline" {
		return errors.New("invalid create tail")
	}
	flags := map[string]string{}
	labels := map[string]string{}
	for i := 2; i < len(args)-3; i++ {
		key := args[i]
		if key == "--pull=never" || key == "--init=false" || key == "--read-only" {
			if _, ok := flags[key]; ok {
				return errors.New("duplicate create flag")
			}
			flags[key] = "true"
			continue
		}
		if i+1 >= len(args)-3 {
			return errors.New("missing create argument")
		}
		i++
		if key == "--label" {
			k, v, ok := strings.Cut(args[i], "=")
			if !ok || labels[k] != "" {
				return errors.New("invalid label")
			}
			labels[k] = v
			continue
		}
		if _, ok := flags[key]; ok {
			return errors.New("duplicate create argument")
		}
		flags[key] = args[i]
	}
	if len(labels) != 6 {
		return errors.New("incorrect ownership labels")
	}
	expected := map[string]string{"--pull=never": "true", "--read-only": "true", "--network": "none", "--cap-drop": "ALL", "--security-opt": "no-new-privileges", "--ipc": "private", "--cgroupns": "private", "--restart": "no", "--log-driver": "none", "--pids-limit": "16", "--shm-size": "1048576", "--user": "65534:65534", "--entrypoint": "/usr/local/bin/oro-factory-guard", "--mount": "type=volume,source=" + s.Volume + ",target=/source,volume-nocopy"}
	expected["--init=false"] = "true"
	if candidate {
		expected["--memory"] = "268435456"
		expected["--memory-swap"] = "268435456"
		expected["--cpus"] = "1.000000000"
		expected["--tmpfs"] = "/scratch:rw,size=1048576,nr_inodes=16,uid=1000,gid=1000,mode=0700"
	} else {
		expected["--memory"] = "67108864"
		expected["--memory-swap"] = "67108864"
		expected["--cpus"] = "0.25"
	}
	name := flags["--name"]
	delete(flags, "--name")
	if name == "" || !maps.Equal(flags, expected) {
		return fmt.Errorf("unexpected create arguments: %v", flags)
	}
	labels["dev.fixture.image"] = "inherited"
	image, deadline := args[len(args)-3], args[len(args)-1]
	if image != "sha256:"+strings.Repeat("a", 64) {
		return errors.New("unexpected image")
	}
	if _, err := time.Parse(time.RFC3339Nano, deadline); err != nil {
		return err
	}
	if candidate {
		s.CandidateName, s.CandidateImage, s.CandidateDeadline, s.CandidateLabels = name, image, deadline, labels
		s.CandidateMemory, s.CandidateCPUs, s.CandidatePIDs, s.CandidateScratch = flags["--memory"], "1000000000", flags["--pids-limit"], flags["--tmpfs"]
	} else {
		s.Name, s.Image, s.Deadline, s.KeeperLabels = name, image, deadline, labels
	}
	return nil
}

func managedFakeKeeper(s managedFakeDockerState, candidate bool) map[string]any {
	id := strings.Repeat("d", 64)
	if candidate {
		id = strings.Repeat("e", 64)
		s.Name, s.Image, s.Deadline, s.KeeperLabels, s.Running = s.CandidateName, s.CandidateImage, s.CandidateDeadline, s.CandidateLabels, s.CandidateRunning
	}
	status := map[string]any{"Status": "created", "Running": false, "Paused": false, "Restarting": false, "Dead": false, "Pid": 0, "ExitCode": 0, "Error": "", "StartedAt": "0001-01-01T00:00:00Z", "FinishedAt": "0001-01-01T00:00:00Z"}
	if s.Running {
		status["Status"] = "running"
		status["Running"] = true
		status["Pid"] = 203
		status["StartedAt"] = "2026-10-24T06:00:00Z"
	}
	if candidate && s.CandidateStopped {
		status["Status"] = "exited"
		status["StartedAt"] = "2026-10-24T06:00:00Z"
	}
	object := map[string]any{
		"Id": id, "Name": "/" + s.Name, "Image": s.Image, "Created": "2026-10-24T05:49:04Z",
		"Config": map[string]any{"User": "65534:65534", "Entrypoint": []string{"/usr/local/bin/oro-factory-guard"}, "Cmd": []string{"--deadline", s.Deadline}, "Labels": s.KeeperLabels, "Tty": false, "OpenStdin": false, "AttachStdout": true}, "State": status,
		"HostConfig": map[string]any{"NetworkMode": "none", "PidMode": "", "IpcMode": "private", "CgroupnsMode": "private", "ReadonlyRootfs": true, "Privileged": false, "CapAdd": nil, "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges"}, "Memory": int64(64 << 20), "MemorySwap": int64(64 << 20), "NanoCpus": int64(250000000), "PidsLimit": int64(16), "ShmSize": int64(1 << 20), "RestartPolicy": map[string]any{"Name": "no", "MaximumRetryCount": 0}, "LogConfig": map[string]any{"Type": "none", "Config": map[string]string{}}, "Binds": nil, "VolumesFrom": nil, "Tmpfs": nil, "Devices": nil, "DeviceRequests": nil, "Mounts": []any{map[string]any{"Type": "volume", "Source": s.Volume, "Target": "/source", "ReadOnly": false, "VolumeOptions": map[string]any{"NoCopy": true}}}},
		"Mounts":     []any{map[string]any{"Type": "volume", "Name": s.Volume, "Source": "/var/lib/docker/volumes/owned/_data", "Destination": "/source", "Driver": "local", "RW": true, "Propagation": ""}},
	}
	host := object["HostConfig"].(map[string]any)
	host["UTSMode"], host["Init"] = "", false
	if candidate {
		host["Memory"], _ = strconv.ParseInt(s.CandidateMemory, 10, 64)
		host["MemorySwap"] = host["Memory"]
		host["NanoCpus"], _ = strconv.ParseInt(s.CandidateCPUs, 10, 64)
		host["PidsLimit"], _ = strconv.ParseInt(s.CandidatePIDs, 10, 64)
		host["Tmpfs"] = map[string]string{"/scratch": strings.TrimPrefix(s.CandidateScratch, "/scratch:")}
	}
	return object
}

func TestFactoryManagedHostPeer(t *testing.T) {
	if os.Getenv("FACTORY_SUPERVISOR_HOST_PLIST") == "" {
		return
	}
	timer := time.AfterFunc(40*time.Second, func() { os.Exit(2) })
	defer timer.Stop()
	TestFactorySupervisorHostPeer(t)
}
