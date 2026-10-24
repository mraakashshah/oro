//go:build darwin

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func TestFactoryLaunchdCleanup(t *testing.T) {
	t.Run("exact cleanup passes wrapper layers", testFactoryCleanupWrapperLayers)
	t.Run("current authority precedes launch effects", testFactoryCleanupLaunchAuthority)
	t.Run("ordinary host and observer stay restricted", testFactoryCleanupOrdinaryKinds)
	t.Run("native late volume cleanup and retirement", testFactoryCleanupLaunchNative)
}

// This fixture seeds an already-admitted permit and its single C reservation.
// It proves the runtime boundary, not public CLI admission or supervisor wiring.
func newFactoryCleanupLaunchFixture(t *testing.T) factoryDockerCleanupFixture {
	t.Helper()
	base := newFactoryCleanupInvocationFixture(t)
	f := factoryDockerCleanupFixture{config: base.runtime, permit: base.permit}
	f.config.config.Deadline = time.Now().UTC().Add(-time.Second)
	// The journal requires the exact cleanup (60s) plus reconciliation (1s)
	// recovery allowance, rather than merely a later hard stop.
	f.config.config.HardStop = f.config.config.Deadline.Add(61 * time.Second)
	f.permit.Deadline = time.Now().UTC().Add(30 * time.Second)
	f.writeConfig(t)
	return f
}

func seedFactoryCleanupLaunchFixture(t *testing.T, f *factoryDockerCleanupFixture) {
	t.Helper()
	f.db = f.seedJournal(t)
	if _, err := f.db.Exec("UPDATE managed_scopes SET phase='reserved',pid=0,pgid=0 WHERE id=?", f.permit.Scope.ID); err != nil {
		t.Fatal(err)
	}
}

func factoryCleanupLaunchInvocation(t *testing.T, f factoryDockerCleanupFixture) (factoryLaunchdConfig, factory.HostInvocation) {
	t.Helper()
	c := f.config.config
	config := factoryLaunchdConfig{
		StateRoot: c.StateRoot, ObjectiveID: c.ObjectiveID, OroPath: c.OroPath, OroSHA256: c.OroSHA256,
		GitPath: c.GitPath, GitSHA256: c.GitSHA256, Repository: c.Repository, TargetRef: c.TargetRef,
		CleanupTimeout: time.Duration(c.CleanupTimeoutSeconds) * time.Second, HardStop: c.HardStop,
		RuntimeConfigPath: f.config.path, RuntimeConfigSHA256: factoryHostDigest(f.config.data),
	}
	data, err := json.Marshal(f.permit)
	if err != nil {
		t.Fatal(err)
	}
	return config, factory.HostInvocation{
		Scope: f.permit.Scope, Path: c.OroPath, Dir: c.StateRoot, Deadline: f.permit.Deadline, OutputLimit: 1 << 20,
		Args: []string{"factory", "_docker-cleanup", "--runtime-config", config.RuntimeConfigPath, "--runtime-config-sha256", config.RuntimeConfigSHA256},
		Env:  []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "LANG=C"}, Stdin: data,
	}
}

func testFactoryCleanupWrapperLayers(t *testing.T) {
	f := newFactoryCleanupLaunchFixture(t)
	seedFactoryCleanupLaunchFixture(t, &f)
	config, invocation := factoryCleanupLaunchInvocation(t, f)
	runtime, err := openFactoryLaunchdRuntime(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if err := runtime.validateInvocation(t.Context(), invocation); err != nil {
		t.Fatalf("exact reserved late cleanup rejected: %v", err)
	}
	dir := filepath.Join(config.StateRoot, config.ObjectiveID, "host-scopes", f.permit.Scope.ID+"."+f.permit.Scope.Nonce)
	request := factoryHostExecRequest{Invocation: invocation, ResultPath: filepath.Join(dir, "result.pending.json")}
	data, err := encodeFactoryHostExecRequest(request)
	if err != nil {
		t.Fatalf("cleanup request envelope rejected: %v", err)
	}
	wrapper := factory.HostInvocation{
		Scope: invocation.Scope, Path: config.OroPath, Dir: dir, Deadline: invocation.Deadline, OutputLimit: invocation.OutputLimit,
		Args: []string{"factory", "_host-exec", "--request", filepath.Join(dir, "request.json"), "--request-sha256", factoryHostDigest(data), "--socket", "/private/tmp/owned-cleanup.sock"},
	}
	if _, err := factoryJobPlist(wrapper); err != nil {
		t.Fatalf("cleanup wrapper renderer rejected: %v", err)
	}
	observation, err := runtime.authority.Observe(t.Context(), invocation.Scope)
	if err != nil || observation.Phase != "reserved" || observation.PID != 0 || observation.PGID != 0 {
		t.Fatalf("validation granted a scope: %+v %v", observation, err)
	}
	assertFactoryCleanupLaunchBudget(t, f)
}

func testFactoryCleanupLaunchAuthority(t *testing.T) {
	for _, mode := range []string{
		"absent permit", "changed permit", "stale generation", "revoked", "stopped", "foreign", "no C token",
		"prior host live", "prior cleanup live", "malformed kind", "malformed label", "arbitrary Docker", "arbitrary host",
		"consumed grant", "changed deadline", "past hard stop",
	} {
		t.Run(mode, func(t *testing.T) {
			f := newFactoryCleanupLaunchFixture(t)
			seedFactoryCleanupLaunchFixture(t, &f)
			config, invocation := factoryCleanupLaunchInvocation(t, f)
			mutateFactoryCleanupLaunch(t, &f, &invocation, mode)
			runtime, err := openFactoryLaunchdRuntime(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			calls := 0
			runtime.launchctl = func(context.Context, ...string) (int, error) {
				calls++
				return -1, errors.New("denied fixture unexpectedly reached launchctl")
			}
			if _, err := runtime.Invoke(t.Context(), invocation); err == nil || calls != 0 {
				t.Fatalf("%s reached launch effects: calls=%d err=%v", mode, calls, err)
			}
			if _, err := os.Lstat(filepath.Join(config.StateRoot, config.ObjectiveID, "host-scopes")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s created request state: %v", mode, err)
			}
		})
	}
}

func mutateFactoryCleanupLaunch(t *testing.T, f *factoryDockerCleanupFixture, invocation *factory.HostInvocation, mode string) {
	t.Helper()
	statement := ""
	switch mode {
	case "absent permit":
		statement = "DELETE FROM managed_cleanup_permits"
	case "changed permit":
		f.permit.Binding.Labels["changed"] = "after invocation was constructed"
		f.persistPermit(t)
	case "stale generation", "stopped":
		generation, desired := 2, "stopped"
		if mode == "stale generation" {
			generation, desired = 3, "stopping"
		}
		factoryCleanupExecJSON(t, f.db, "UPDATE managed_supervision SET body=?", map[string]any{"Revision": 1, "Generation": generation, "SupervisorToken": "owner", "DesiredState": desired})
	case "revoked", "consumed grant":
		phase := "revoked"
		if mode == "consumed grant" {
			phase = "granted"
		}
		if _, err := f.db.Exec("UPDATE managed_scopes SET phase=? WHERE id=?", phase, f.permit.Scope.ID); err != nil {
			t.Fatal(err)
		}
	case "foreign":
		invocation.Scope.Nonce = strings.Repeat("f", 32)
		invocation.Scope.Name = strings.Replace(invocation.Scope.Name, f.permit.Scope.Nonce, invocation.Scope.Nonce, 1)
	case "no C token":
		statement = "DELETE FROM managed_reservations"
	case "prior host live", "prior cleanup live":
		scope := f.permit.Scope
		scope.ID, scope.Nonce = strings.Repeat("f", 32), strings.Repeat("a", 32)
		if mode == "prior host live" {
			scope.Kind = "host"
		}
		scope.Name = "dev.getoro.factory." + f.permit.Binding.Labels["dev.getoro.factory.objective"] + "." + scope.Kind + "." + scope.ID + "." + scope.Nonce
		body, err := json.Marshal(struct {
			factory.ScopeHandle
			Deadline time.Time
		}{scope, f.permit.Deadline})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.Exec("INSERT INTO managed_scopes(id,nonce,backend,name,body,phase,pid,pgid) VALUES(?,?,?,?,?,'granted',42,42)", scope.ID, scope.Nonce, scope.Backend, scope.Name, body); err != nil {
			t.Fatal(err)
		}
	case "malformed kind":
		invocation.Scope.Kind = "worker"
		invocation.Scope.Name = strings.Replace(invocation.Scope.Name, ".cleanup.", ".worker.", 1)
	case "malformed label":
		invocation.Scope.Name += "-other"
	case "arbitrary Docker":
		invocation.Path, invocation.Args = f.config.config.DockerPath, []string{"volume", "prune"}
	case "arbitrary host":
		invocation.Path, invocation.Args = "/bin/echo", []string{"late arbitrary work"}
	case "changed deadline":
		invocation.Deadline = invocation.Deadline.Add(time.Nanosecond)
	case "past hard stop":
		invocation.Deadline = f.config.config.HardStop.Add(time.Nanosecond)
	}
	if statement != "" {
		if _, err := f.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func testFactoryCleanupLaunchNative(t *testing.T) {
	if os.Getenv("ORO_FACTORY_INTEGRATION") != "1" {
		t.Skip("native launchd cleanup requires ORO_FACTORY_INTEGRATION=1")
	}
	contextName, endpoint := os.Getenv("ORO_FACTORY_DOCKER_CONTEXT"), os.Getenv("ORO_FACTORY_DOCKER_ENDPOINT")
	if contextName != "colima-oro-factory" || endpoint == "" {
		t.Fatal("native cleanup requires explicit colima-oro-factory context and its dedicated Unix endpoint")
	}
	binaryDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(binaryDir, "oro")
	if output, err := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build frozen native Oro: %v %s", err, output)
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	docker, err = filepath.EvalSymlinks(docker)
	if err != nil {
		t.Fatal(err)
	}
	f := newFactoryCleanupLaunchFixture(t)
	configureFactoryCleanupLaunchNative(t, &f, binary, docker, contextName, endpoint)
	f.createNativeResource(t, f.permit.Target.Name)
	foreign := f.permit.Target.Name + "-foreign"
	f.createNativeResource(t, foreign)
	before := f.docker(t, "volume", "inspect", foreign)
	f.permit.Operation = factory.DockerCleanupRemove
	seedFactoryCleanupLaunchFixture(t, &f)
	config, invocation := factoryCleanupLaunchInvocation(t, f)
	runtime, err := openFactoryLaunchdRuntime(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Retire(context.Background(), invocation.Scope); err != nil {
			t.Errorf("retire owned native cleanup scope: %v", err)
		} else if err := os.RemoveAll("/private/tmp/of-" + factoryLaunchdObjectiveHash(config)); err != nil {
			t.Errorf("remove owned retired socket namespace: %v", err)
		}
		_ = runtime.Close()
	})
	assertFactoryCleanupNativeInvocation(t, runtime, f, invocation)
	if after := f.docker(t, "volume", "inspect", foreign); !bytes.Equal(before, after) {
		t.Fatal("foreign sentinel volume changed")
	}
	t.Logf("foreign sentinel unchanged=%s; teardown verifies exact fixture lifetime before removal", foreign)
}

func testFactoryCleanupOrdinaryKinds(t *testing.T) {
	for _, kind := range []string{"host", "observer"} {
		t.Run(kind, func(t *testing.T) {
			config := factoryScopeConfigFixture(t)
			db := factoryScopeSeedJournal(t, &config)
			deadline := time.Now().UTC().Add(time.Minute)
			scope := factoryScopeSeed(t, db, config, "a", deadline, "reserved", 0)
			scope.Kind = kind
			scope.Name = strings.Replace(scope.Name, ".host.", "."+kind+".", 1)
			body, err := json.Marshal(struct {
				factory.ScopeHandle
				Deadline time.Time
			}{scope, deadline})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("UPDATE managed_scopes SET name=?,body=? WHERE id=?", scope.Name, body, scope.ID); err != nil {
				t.Fatal(err)
			}
			runtime, err := openFactoryLaunchdRuntime(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			invocation := factory.HostInvocation{
				Scope: scope, Path: config.GitPath, Dir: config.Repository, Deadline: deadline, OutputLimit: 256,
				Args: factoryObserverArgs(config.TargetRef), Env: factoryObserverEnvironment(),
			}
			if err := runtime.validateInvocation(t.Context(), invocation); err != nil {
				t.Fatalf("ordinary %s requires an unrelated Docker artifact: %v", kind, err)
			}
			if kind == "observer" {
				invocation.Args = append(invocation.Args, "unadmitted-ref")
				if runtime.validateInvocation(t.Context(), invocation) == nil {
					t.Fatal("observer gained arbitrary command authority")
				}
			}
		})
	}
}

func configureFactoryCleanupLaunchNative(t *testing.T, f *factoryDockerCleanupFixture, binary, docker, contextName, endpoint string) {
	t.Helper()
	// Replace only this test's private metadata directory; never consult or write
	// the user's Docker config. The admitted original Docker executable is hashed.
	previous := filepath.Dir(f.config.metaPath)
	f.config.config.DockerContext, f.permit.Binding.Context = contextName, contextName
	f.config.metaPath = filepath.Join(filepath.Dir(previous), factoryHostDigest([]byte(contextName)), "meta.json")
	if err := os.Rename(previous, filepath.Dir(f.config.metaPath)); err != nil {
		t.Fatal(err)
	}
	f.configureNative(t, binary, docker, endpoint)
	if _, err := readFactoryRuntimeConfig(t.Context(), f.config.path, factoryHostDigest(f.config.data)); err != nil {
		t.Fatalf("private native runtime snapshot: %v", err)
	}
	// Requiring a successful exact-context daemon request makes an enabled but
	// unavailable integration backend fail, rather than silently skipping proof.
	f.docker(t, "version", "--format", "{{.Server.Os}}/{{.Server.Arch}}")
}

func assertFactoryCleanupNativeInvocation(t *testing.T, runtime *factoryLaunchdRuntime, f factoryDockerCleanupFixture, invocation factory.HostInvocation) {
	t.Helper()
	if !time.Now().After(f.config.config.Deadline) {
		t.Fatal("native fixture did not pass the original work deadline")
	}
	result, err := runtime.Invoke(t.Context(), invocation)
	if err != nil || result.ExitCode != 0 || len(result.Stderr) != 0 {
		t.Fatalf("native cleanup exit=%d stderr=%s stdout=%s error=%v", result.ExitCode, result.Stderr, result.Stdout, err)
	}
	receipt, err := factory.DecodeDockerCleanupResult(result.Stdout, f.permit)
	if err != nil || receipt.Observation != "observed_absent" || !receipt.MutationAcknowledged {
		t.Fatalf("native cleanup receipt=%+v error=%v", receipt, err)
	}
	observation, err := runtime.authority.Observe(t.Context(), invocation.Scope)
	if err != nil || observation.Phase != "quiescent" || observation.PID <= 1 || observation.PID != observation.PGID {
		t.Fatalf("cleanup scope did not positively retire: %+v %v", observation, err)
	}
	if err := syscall.Kill(-observation.PGID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("authenticated cleanup PGID survived Invoke: %v", err)
	}
	assertFactoryCleanupLaunchBudget(t, f)
	calls := 0
	original := runtime.launchctl
	runtime.launchctl = func(ctx context.Context, args ...string) (int, error) { calls++; return original(ctx, args...) }
	if _, err := runtime.Invoke(t.Context(), invocation); err == nil || calls != 0 {
		t.Fatalf("consumed cleanup scope replay reached launch: calls=%d error=%v", calls, err)
	}
	t.Logf("real HELLO/GO/PUBLISHED completed; exact gui/%s/%s retired PID=PGID=%d with ESRCH; C remains 1; work deadline=%s; canonical receipt=%s", strconv.Itoa(os.Getuid()), invocation.Scope.Name, observation.PGID, f.config.config.Deadline, result.Stdout)
}

func assertFactoryCleanupLaunchBudget(t *testing.T, f factoryDockerCleanupFixture) {
	t.Helper()
	var count int
	if err := f.db.QueryRow("SELECT count(*) FROM managed_reservations WHERE kind='cleanup'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("runtime changed the once-budgeted C reservation: count=%d error=%v", count, err)
	}
}
