package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"oro/pkg/factory"

	"golang.org/x/sys/unix"
)

func TestFactoryDockerCleanup(t *testing.T) {
	t.Run("hidden command is registered", func(t *testing.T) {
		command, _, err := newRootCmd().Find([]string{"factory", "_docker-cleanup"})
		if err != nil || command.Name() != "_docker-cleanup" || !command.Hidden {
			t.Fatalf("hidden cleanup helper missing: command=%s error=%v", command.Name(), err)
		}
	})
	t.Run("owned inspection uses durable authority", testFactoryDockerCleanupInspect)
	t.Run("partial pipe is canceled without readers", testFactoryDockerCleanupInputCancel)
	t.Run("bounded mutation and successful result table", testFactoryDockerCleanupMutations)
	t.Run("already stopped still acknowledges fixed stop", testFactoryDockerCleanupAlreadyStopped)
	t.Run("revoked authority prevents every next child", testFactoryDockerCleanupRevocation)
	t.Run("exact inherited process group required", testFactoryDockerCleanupGroup)
	t.Run("canonical bounded pipe and descriptor cleanup", testFactoryDockerCleanupInputBounds)
	t.Run("denied or uncertain results retain authority", testFactoryDockerCleanupFailures)
	t.Run("actual CLI minimal environment and held stdin", testFactoryDockerCleanupCLI)
	t.Run("absence discovery and publication failures", testFactoryDockerCleanupResultEdges)
	t.Run("actual inherited stdin cancellation", testFactoryDockerCleanupInputProcess)
}

type factoryDockerCleanupFixture struct {
	config factoryRuntimeConfigFixture
	permit factory.DockerCleanupPermit
	db     *sql.DB
}

func newFactoryDockerCleanupFixture(t *testing.T, kind string) factoryDockerCleanupFixture {
	t.Helper()
	f := newFactoryRuntimeConfigFixture(t)
	f.config.Deadline = time.Now().UTC().Add(-time.Second)
	f.config.HardStop = f.config.Deadline.Add(61 * time.Second)
	f.config.CleanupTimeoutSeconds = 60
	p := factoryDockerCleanupPlanFixture(kind)
	objective := factoryHostDigest([]byte(f.config.StateRoot + "\x00" + f.config.ObjectiveID))[:16]
	p.Target.Name = "oro-factory-" + objective + "-" + kind + "-" + p.Target.ID + "-" + p.Target.Nonce
	p.Scope.Name = "dev.getoro.factory." + objective + ".cleanup." + p.Scope.ID + "." + p.Scope.Nonce
	p.Binding.Name, p.Binding.Context = p.Target.Name, f.config.DockerContext
	p.Binding.Labels["dev.getoro.factory.objective"] = objective
	p.Binding.Labels["dev.getoro.factory.policy"] = f.config.PolicyHash
	p.Deadline = time.Now().UTC().Add(30 * time.Second)
	result := factoryDockerCleanupFixture{config: f, permit: p}
	result.setProgram(t, "#!/bin/sh\ncat <<'CLEANUP_INSPECTION'\n"+string(factoryDockerCleanupInspectionResult(t, factoryDockerCleanupObject(p)).Stdout)+"\nCLEANUP_INSPECTION\n")
	result.db = result.seedJournal(t)
	return result
}

func (f *factoryDockerCleanupFixture) setProgram(t *testing.T, script string) {
	t.Helper()
	if err := os.Chmod(f.config.config.DockerPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.config.config.DockerPath, []byte(script), 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.config.config.DockerPath, 0o500); err != nil {
		t.Fatal(err)
	}
	digest := factoryHostDigest([]byte(script))
	f.config.config.OroSHA256, f.config.config.GitSHA256, f.config.config.DockerSHA256 = digest, digest, digest
	f.writeConfig(t)
}

func (f *factoryDockerCleanupFixture) writeConfig(t *testing.T) {
	t.Helper()
	var err error
	f.config.data, err = canonicalFactoryRuntimeConfig(f.config.config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.config.path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.config.path, f.config.data, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.config.path, 0o400); err != nil {
		t.Fatal(err)
	}
}

// This test-owned fixture represents already-admitted authority. Production
// supervisor allocation and launchd cleanup admission are separate tasks.
func (f *factoryDockerCleanupFixture) seedJournal(t *testing.T) *sql.DB {
	t.Helper()
	c := f.config.config
	dir := filepath.Join(c.StateRoot, c.ObjectiveID)
	namespace := filepath.Join(c.StateRoot, ".managed")
	if err := os.Mkdir(namespace, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := []byte("oro-managed-state-v1;page=4096;data=67108864;allowance=268435456\n")
	for _, path := range []string{filepath.Join(namespace, c.ObjectiveID+".state.init"), filepath.Join(dir, "state.init")} {
		if err := os.WriteFile(path, marker, 0o600); err != nil {
			t.Fatal(err)
		}
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
CREATE TABLE managed_reservations(kind TEXT NOT NULL,token TEXT NOT NULL,generation INTEGER NOT NULL,PRIMARY KEY(kind,token));
CREATE TABLE managed_cleanup_permits(scope_id TEXT PRIMARY KEY,body BLOB NOT NULL);
CREATE TABLE managed_docker_candidates (
 workspace_id TEXT PRIMARY KEY REFERENCES managed_workspaces(id),
 container_id TEXT NOT NULL UNIQUE REFERENCES managed_scopes(id),
 create_host_id TEXT NOT NULL UNIQUE REFERENCES managed_scopes(id),
 body BLOB NOT NULL CHECK(length(body)<=16384)
 );`)
	if err != nil {
		t.Fatal(err)
	}
	resources := factory.RuntimePolicy{MemoryBytes: 1, SourceBytes: 1, ScratchBytes: 1, HostRequiredBytes: 1, HostReserveBytes: 1, CPUs: 1, PIDs: 1, MaxFiles: 1, SourceInodes: 1, ScratchInodes: 1, MaxModelCalls: 1, MaxModelTurns: 1, MaxToolCalls: 1, MaxHostInvocations: 100, DockerContext: c.DockerContext}
	recovery := factory.RecoveryPolicy{MaxControllerStarts: 5, MaxSupervisorStarts: 5, MaxReconciliationInvocations: 5, MaxCleanupInvocations: 5, RestartDelaySeconds: 1, CleanupTimeoutSeconds: 60, ReconciliationSeconds: 1}
	admission := map[string]any{"ObjectiveID": c.ObjectiveID, "ManifestHash": c.PolicyHash, "Deadline": c.Deadline, "HardStop": c.HardStop, "Recovery": recovery, "Runtime": resources, "Reservation": factory.HostReservation{ID: "cleanup-proof", StateRoot: c.StateRoot, ObjectiveID: c.ObjectiveID, Bytes: 1, SpecSHA256: strings.Repeat("a", 64)}}
	factoryCleanupExecJSON(t, db, "INSERT INTO managed_admissions VALUES(1,?)", admission)
	factoryCleanupExecJSON(t, db, "INSERT INTO managed_supervision VALUES(1,1,?)", map[string]any{"Revision": 1, "Generation": 2, "SupervisorToken": "owner", "DesiredState": "stopping"})
	for _, scope := range []factory.ScopeHandle{f.permit.Target, f.permit.Scope} {
		body, err := json.Marshal(struct {
			factory.ScopeHandle
			Deadline time.Time
		}{scope, f.permit.Deadline})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("INSERT INTO managed_scopes(id,nonce,backend,name,body,phase,pid,pgid) VALUES(?,?,?,?,?,'granted',?,?)", scope.ID, scope.Nonce, scope.Backend, scope.Name, body, syscall.Getpgrp(), syscall.Getpgrp()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("INSERT INTO managed_reservations VALUES('cleanup',?,?)", f.permit.Scope.ID, f.permit.Scope.Generation); err != nil {
		t.Fatal(err)
	}
	factoryCleanupExecJSON(t, db, "INSERT INTO managed_cleanup_permits VALUES('"+f.permit.Scope.ID+"',?)", f.permit)
	return db
}

func factoryCleanupExecJSON(t *testing.T, db *sql.DB, statement string, value any) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(statement, body); err != nil {
		t.Fatal(err)
	}
}

func (f factoryDockerCleanupFixture) command(ctx context.Context, t *testing.T, input *os.File, output io.Writer) error {
	t.Helper()
	cmd := newRootCmd()
	cmd.SetArgs([]string{"factory", "_docker-cleanup", "--runtime-config", f.config.path, "--runtime-config-sha256", factoryHostDigest(f.config.data)})
	cmd.SetIn(input)
	cmd.SetOut(output)
	return cmd.ExecuteContext(ctx)
}

func factoryCleanupInput(t *testing.T, data []byte) *os.File {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = read.Close() })
	done := make(chan error, 1)
	go func() {
		_, err := write.Write(data)
		done <- err
		_ = write.Close()
	}()
	t.Cleanup(func() {
		_ = read.Close()
		if err := <-done; err != nil && !strings.Contains(err.Error(), "broken pipe") {
			t.Error(err)
		}
	})
	return read
}

func testFactoryDockerCleanupInspect(t *testing.T) {
	f := newFactoryDockerCleanupFixture(t, "container")
	data, err := json.Marshal(f.permit)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := f.command(t.Context(), t, factoryCleanupInput(t, data), &output); err != nil {
		t.Fatal(err)
	}
	result, err := factory.DecodeDockerCleanupResult(output.Bytes(), f.permit)
	if err != nil || result.Observation != "present" || result.MutationAcknowledged {
		t.Fatalf("result=%+v error=%v output=%s", result, err, output.String())
	}
}

func testFactoryDockerCleanupInputCancel(t *testing.T) {
	f := newFactoryDockerCleanupFixture(t, "container")
	calls := f.responses(t, []factory.HostResult{{}}, "")
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	if _, err := fmt.Fprint(write, `{"scope":`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	timer := time.AfterFunc(80*time.Millisecond, cancel)
	defer timer.Stop()
	defer cancel()
	started := time.Now()
	var output bytes.Buffer
	err = f.command(ctx, t, read, &output)
	if err == nil || ctx.Err() == nil || time.Since(started) > time.Second || output.Len() != 0 {
		t.Fatalf("partial pipe failed cancellation: error=%v elapsed=%s output=%s", err, time.Since(started), output.String())
	}
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatalf("partial input spawned Docker: %v", err)
	}
}

func (f *factoryDockerCleanupFixture) persistPermit(t *testing.T) {
	t.Helper()
	factoryCleanupExecJSON(t, f.db, "UPDATE managed_cleanup_permits SET body=?", f.permit)
}

func (f *factoryDockerCleanupFixture) responses(t *testing.T, responses []factory.HostResult, afterFirst string) string {
	t.Helper()
	root := f.config.config.StateRoot
	calls := filepath.Join(root, "calls")
	script := "#!/bin/sh\nset -eu\nprintf 'call\\n' >> '" + calls + "'\nn=$(( $(/usr/bin/wc -l < '" + calls + "') ))\ncase $n in\n"
	for index, result := range responses {
		stdout := filepath.Join(root, fmt.Sprintf("response-%d-stdout", index))
		stderr := filepath.Join(root, fmt.Sprintf("response-%d-stderr", index))
		for path, data := range map[string][]byte{stdout: result.Stdout, stderr: result.Stderr} {
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		script += fmt.Sprintf("%d)\n", index+1)
		script += afterFirst + "\n"
		script += fmt.Sprintf("/bin/cat '%s'\n/bin/cat '%s' >&2\nexit %d;;\n", stdout, stderr, result.ExitCode)
	}
	script += "*) exit 91;;\nesac\n"
	f.setProgram(t, script)
	return calls
}

func factoryCleanupAbsent(p factory.DockerCleanupPermit) factory.HostResult {
	identity := p.Binding.ContainerID
	if identity == "" {
		identity = p.Binding.Name
	}
	message := "Error response from daemon: No such container: " + identity + "\n"
	if p.Binding.Kind == "volume" {
		message = "Error response from daemon: get " + p.Binding.Name + ": no such volume\n"
	}
	return factory.HostResult{Stdout: []byte("[]\n"), Stderr: []byte(message), ExitCode: 1}
}

func (f factoryDockerCleanupFixture) invoke(t *testing.T) (factory.DockerCleanupResult, error) {
	t.Helper()
	data, err := json.Marshal(f.permit)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = f.command(t.Context(), t, factoryCleanupInput(t, data), &output)
	if err != nil {
		if output.Len() != 0 {
			t.Fatalf("failed helper emitted success bytes: %s", output.String())
		}
		return factory.DockerCleanupResult{}, err
	}
	return factory.DecodeDockerCleanupResult(output.Bytes(), f.permit)
}

func testFactoryDockerCleanupMutations(t *testing.T) {
	for _, kind := range []string{"container", "volume"} {
		for _, operation := range []factory.DockerCleanupOp{factory.DockerCleanupStop, factory.DockerCleanupRemove} {
			if kind == "volume" && operation == factory.DockerCleanupStop {
				continue
			}
			t.Run(kind+"-"+string(operation), func(t *testing.T) {
				f := newFactoryDockerCleanupFixture(t, kind)
				f.permit.Operation = operation
				f.persistPermit(t)
				object := factoryDockerCleanupObject(f.permit)
				first := factoryDockerCleanupInspectionResult(t, object)
				last, want := factoryCleanupAbsent(f.permit), "observed_absent"
				if operation == factory.DockerCleanupStop {
					object["State"] = map[string]any{"Status": "exited", "Running": false, "Pid": 0, "Paused": false, "Restarting": false, "Dead": false}
					last, want = factoryDockerCleanupInspectionResult(t, object), "stopped"
				}
				calls := f.responses(t, []factory.HostResult{first, {Stdout: []byte("ack\n")}, last}, "")
				got, err := f.invoke(t)
				if err != nil || got.Observation != want || !got.MutationAcknowledged {
					t.Fatalf("result=%+v error=%v", got, err)
				}
				data, err := os.ReadFile(calls)
				if err != nil || string(data) != "call\ncall\ncall\n" {
					t.Fatalf("fixed child count: %q %v", data, err)
				}
			})
		}
	}
}

func testFactoryDockerCleanupAlreadyStopped(t *testing.T) {
	f := newFactoryDockerCleanupFixture(t, "container")
	f.permit.Operation = factory.DockerCleanupStop
	f.persistPermit(t)
	object := factoryDockerCleanupObject(f.permit)
	object["State"] = map[string]any{"Status": "exited", "Running": false, "Pid": 0, "Paused": false, "Restarting": false, "Dead": false}
	stopped := factoryDockerCleanupInspectionResult(t, object)
	calls := f.responses(t, []factory.HostResult{stopped, {}, stopped}, "")
	result, err := f.invoke(t)
	if err != nil || result.Observation != "stopped" || !result.MutationAcknowledged {
		t.Fatalf("already stopped result=%+v error=%v", result, err)
	}
	data, err := os.ReadFile(calls)
	if err != nil || string(data) != "call\ncall\ncall\n" {
		t.Fatalf("already-stopped target skipped fixed stop: %q %v", data, err)
	}
}

func testFactoryDockerCleanupRevocation(t *testing.T) {
	f := newFactoryDockerCleanupFixture(t, "container")
	f.permit.Operation = factory.DockerCleanupRemove
	f.persistPermit(t)
	database := filepath.Join(f.config.config.StateRoot, f.config.config.ObjectiveID, "state.db")
	revoke := "/usr/bin/sqlite3 '" + database + "' \"UPDATE managed_scopes SET phase='revoked' WHERE id='" + f.permit.Scope.ID + "';\""
	calls := f.responses(t, []factory.HostResult{factoryDockerCleanupInspectionResult(t, factoryDockerCleanupObject(f.permit)), {}}, revoke)
	if _, err := f.invoke(t); err == nil {
		t.Fatal("revoked cleanup ran another child")
	}
	data, err := os.ReadFile(calls)
	if err != nil || string(data) != "call\n" {
		t.Fatalf("revocation child count: %q %v", data, err)
	}
}

func testFactoryDockerCleanupGroup(t *testing.T) {
	f := newFactoryDockerCleanupFixture(t, "container")
	if _, err := f.db.Exec("UPDATE managed_scopes SET pid=99,pgid=99 WHERE id=?", f.permit.Scope.ID); err != nil {
		t.Fatal(err)
	}
	calls := f.responses(t, []factory.HostResult{factoryDockerCleanupInspectionResult(t, factoryDockerCleanupObject(f.permit))}, "")
	if _, err := f.invoke(t); err == nil {
		t.Fatal("direct replay outside inherited owner group succeeded")
	}
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatalf("denied group spawned Docker: %v", err)
	}
}

func testFactoryDockerCleanupInputBounds(t *testing.T) {
	p := factoryDockerCleanupPlanFixture("container")
	valid, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"valid": valid, "trailing": append(append([]byte{}, valid...), '\n'),
		"excess": bytes.Repeat([]byte("x"), factory.ManagedCleanupPermitBytes+1), "invalid": []byte(`{"scope":`),
		"invalid permit": []byte(`{}`),
		"unknown":        append([]byte(`{"unknown":1,`), valid[1:]...), "duplicate": append([]byte(`{"deadline":"2000-01-01T00:00:00Z",`), valid[1:]...),
	} {
		t.Run(name, func(t *testing.T) {
			input := factoryCleanupInput(t, data)
			before := factoryCleanupFDFlags(t, input)
			got, err := readFactoryDockerCleanupPermit(t.Context(), input, time.Now().Add(time.Second))
			if (err == nil) != (name == "valid") || (err == nil && got.Scope != p.Scope) {
				t.Fatalf("decode=%+v error=%v", got, err)
			}
			if after := factoryCleanupFDFlags(t, input); after != before {
				t.Fatalf("original descriptor flags changed: before=%d after=%d", before, after)
			}
		})
	}
	input := factoryCleanupInput(t, valid)
	owned, err := openFactoryCleanupInput(input)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := owned.file.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var descriptor uintptr
	if err := raw.Control(func(fd uintptr) {
		descriptor = fd
		flags, err := unix.FcntlInt(fd, unix.F_GETFD, 0)
		if err != nil || flags&unix.FD_CLOEXEC == 0 {
			t.Errorf("duplicate can leak into children: flags=%d error=%v", flags, err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := owned.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := unix.FcntlInt(descriptor, unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
		t.Fatalf("duplicate still open: %v", err)
	}
	regular, err := os.CreateTemp(t.TempDir(), "not-a-pipe")
	if err != nil {
		t.Fatal(err)
	}
	defer regular.Close()
	if _, err := readFactoryDockerCleanupPermit(t.Context(), regular, time.Now().Add(time.Second)); err == nil {
		t.Fatal("stdin without a pollable pipe accepted")
	}
	if _, err := readFactoryDockerCleanupPermit(t.Context(), input, time.Now().Add(-time.Second)); err == nil {
		t.Fatal("expired hard stop accepted")
	}
}

func testFactoryDockerCleanupResultEdges(t *testing.T) {
	for _, mode := range []string{"absent inspect", "absent stop", "absent remove", "unknown container", "unknown volume", "short writer", "failed writer", "nil writer", "late writer", "canceled", "wrong config", "missing authority"} {
		t.Run(mode, func(t *testing.T) {
			kind := "container"
			if mode == "unknown volume" {
				kind = "volume"
			}
			f := newFactoryDockerCleanupFixture(t, kind)
			first := factoryDockerCleanupInspectionResult(t, factoryDockerCleanupObject(f.permit))
			switch mode {
			case "absent inspect", "absent stop", "absent remove":
				f.permit.Operation = factory.DockerCleanupOp(strings.TrimPrefix(mode, "absent "))
				first = factoryCleanupAbsent(f.permit)
			case "unknown container", "unknown volume":
				f.permit.Binding.ContainerID, f.permit.Binding.CreatedAt = "", ""
			}
			f.persistPermit(t)
			f.responses(t, []factory.HostResult{first}, "")
			config, err := readFactoryRuntimeConfig(t.Context(), f.config.path, factoryHostDigest(f.config.data))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var output bytes.Buffer
			var writer io.Writer = &output
			if mode == "short writer" || mode == "failed writer" {
				writer = factoryDockerReadyFailWriter{short: mode == "short writer"}
			}
			if mode == "nil writer" {
				writer = nil
			}
			if mode == "late writer" {
				writer = factoryCleanupCancelWriter{cancel: cancel}
			}
			if mode == "canceled" {
				cancel()
			}
			if mode == "wrong config" {
				config.DockerEndpoint = "unix:///wrong"
			}
			if mode == "missing authority" {
				if err := f.db.Close(); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(config.StateRoot, config.ObjectiveID, "state.db")
				if err := os.Rename(path, path+".missing"); err != nil {
					t.Fatal(err)
				}
			}
			err = runFactoryDockerCleanup(ctx, config, f.permit, writer)
			if strings.Contains(mode, "writer") || mode == "canceled" || mode == "wrong config" || mode == "missing authority" {
				if err == nil {
					t.Fatal("failed output or authorization returned success")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			result, err := factory.DecodeDockerCleanupResult(output.Bytes(), f.permit)
			if err != nil || result.MutationAcknowledged {
				t.Fatalf("observation=%+v error=%v", result, err)
			}
		})
	}
}

type factoryCleanupCancelWriter struct{ cancel context.CancelFunc }

func (writer factoryCleanupCancelWriter) Write(data []byte) (int, error) {
	writer.cancel()
	return len(data), nil
}

func testFactoryDockerCleanupInputProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFactoryDockerCleanupInputProcess$", "--", "cleanup-stdin-cancel")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(input, `{"scope":`); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("actual inherited stdin cancellation: %v %s", err, output.String())
	}
}

func TestFactoryDockerCleanupInputProcess(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != "cleanup-stdin-cancel" {
		return
	}
	before := factoryCleanupFDFlags(t, os.Stdin)
	ctx, cancel := context.WithCancel(t.Context())
	timer := time.AfterFunc(80*time.Millisecond, cancel)
	defer timer.Stop()
	defer cancel()
	_, err := readFactoryDockerCleanupPermit(ctx, os.Stdin, time.Now().Add(time.Minute))
	if !errors.Is(err, context.Canceled) || factoryCleanupFDFlags(t, os.Stdin) != before {
		t.Fatalf("actual fd0 cancellation/restore: %v", err)
	}
	stack := make([]byte, 64<<10)
	n := runtime.Stack(stack, true)
	if bytes.Contains(stack[:n], []byte(".readFactoryDockerCleanupPermit(")) {
		t.Fatalf("cleanup stdin left a reader goroutine:\n%s", stack[:n])
	}
}

func factoryCleanupFDFlags(t *testing.T, file *os.File) int {
	t.Helper()
	raw, err := file.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var flags int
	if err := raw.Control(func(fd uintptr) { flags, err = unix.FcntlInt(fd, unix.F_GETFL, 0) }); err != nil {
		t.Fatal(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	return flags
}

func testFactoryDockerCleanupFailures(t *testing.T) {
	for _, mode := range []string{"reserved", "revoked", "stale", "wrong permit", "foreign object", "mutation failed", "postcondition failed", "revoked before verify", "config swapped"} {
		t.Run(mode, func(t *testing.T) {
			f := newFactoryDockerCleanupFixture(t, "container")
			f.permit.Operation = factory.DockerCleanupRemove
			f.persistPermit(t)
			object := factoryDockerCleanupObject(f.permit)
			results := []factory.HostResult{factoryDockerCleanupInspectionResult(t, object), {}, factoryCleanupAbsent(f.permit)}
			after := ""
			switch mode {
			case "reserved", "revoked":
				if _, err := f.db.Exec("UPDATE managed_scopes SET phase=? WHERE id=?", mode, f.permit.Scope.ID); err != nil {
					t.Fatal(err)
				}
			case "stale":
				factoryCleanupExecJSON(t, f.db, "UPDATE managed_supervision SET body=?", map[string]any{"Revision": 1, "Generation": 3, "DesiredState": "stopping"})
			case "wrong permit":
				f.permit.Operation = factory.DockerCleanupStop
			case "foreign object":
				object["Id"] = strings.Repeat("0", 64)
				results[0] = factoryDockerCleanupInspectionResult(t, object)
			case "mutation failed":
				results[1].ExitCode = 7
			case "postcondition failed":
				results[2] = results[0]
			case "revoked before verify":
				database := filepath.Join(f.config.config.StateRoot, f.config.config.ObjectiveID, "state.db")
				after = "if [ \"$n\" = 2 ]; then /usr/bin/sqlite3 '" + database + "' \"UPDATE managed_scopes SET phase='revoked' WHERE id='" + f.permit.Scope.ID + "';\"; fi"
			case "config swapped":
				after = "/bin/chmod 600 '" + f.config.path + "'\nprintf '{}' > '" + f.config.path + "'\n/bin/chmod 400 '" + f.config.path + "'"
			}
			f.responses(t, results, after)
			if _, err := f.invoke(t); err == nil {
				t.Fatal("uncertain or unauthorized cleanup returned success")
			}
			var phase string
			if err := f.db.QueryRow("SELECT phase FROM managed_scopes WHERE id=?", f.permit.Target.ID).Scan(&phase); err != nil || phase != "granted" {
				t.Fatalf("helper fabricated target retirement: phase=%s error=%v", phase, err)
			}
		})
	}
}

func testFactoryDockerCleanupCLI(t *testing.T) {
	binaryDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(binaryDir, "oro")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build actual cleanup CLI: %v %s", err, output)
	}
	for _, mode := range []string{"valid", "partial", "denied"} {
		t.Run(mode, func(t *testing.T) {
			f := newFactoryDockerCleanupFixture(t, "container")
			var calls string
			if mode == "partial" {
				calls = f.responses(t, []factory.HostResult{{}}, "")
			}
			f.config.config.OroPath = binary
			program, err := os.ReadFile(binary)
			if err != nil {
				t.Fatal(err)
			}
			f.config.config.OroSHA256 = factoryHostDigest(program)
			if mode == "partial" {
				f.config.config.HardStop = time.Now().UTC().Add(time.Second)
				f.config.config.Deadline = f.config.config.HardStop.Add(-61 * time.Second)
			}
			f.writeConfig(t)
			stdout, stderr, err := f.native(t, binary, mode)
			if mode != "valid" {
				assertFactoryCleanupNativeDenial(t, mode, calls, stdout, stderr, err)
				return
			}
			if err != nil {
				t.Fatalf("actual cleanup CLI failed: %v %s", err, stderr)
			}
			result, err := factory.DecodeDockerCleanupResult(stdout, f.permit)
			if err != nil || result.Observation != "present" || len(stderr) != 0 {
				t.Fatalf("actual CLI result=%+v stderr=%s error=%v", result, stderr, err)
			}
			t.Logf("actual frozen Oro CLI passed with exactly PATH/LC_ALL/LANG; no HOME/ORO_HOME; PGID-bound fixture scope=%s", f.permit.Scope.ID)
		})
	}
	t.Run("native Docker owned resources", func(t *testing.T) {
		if os.Getenv("ORO_FACTORY_INTEGRATION") != "1" {
			t.Skip("native Docker cleanup requires ORO_FACTORY_INTEGRATION=1 and ORO_FACTORY_DOCKER_ENDPOINT")
		}
		testFactoryDockerCleanupNative(t, binary)
	})
}

func assertFactoryCleanupNativeDenial(t *testing.T, mode, calls string, stdout, stderr []byte, err error) {
	t.Helper()
	if err == nil || len(stdout) != 0 {
		t.Fatalf("native %s returned success: %s %s %v", mode, stdout, stderr, err)
	}
	if mode != "partial" {
		return
	}
	if !bytes.Contains(stderr, []byte("read bounded factory cleanup stdin")) {
		t.Fatalf("actual fd0 did not end at immutable bound: %s", stderr)
	}
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatalf("partial native stdin spawned Docker: %v", err)
	}
}

func (f factoryDockerCleanupFixture) native(t *testing.T, binary, mode string) ([]byte, []byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "factory", "_docker-cleanup", "--runtime-config", f.config.path, "--runtime-config-sha256", factoryHostDigest(f.config.data))
	cmd.Env, cmd.Dir = []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "LANG=C"}, f.config.config.StateRoot
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	group := cmd.Process.Pid
	if mode == "denied" {
		group++
	}
	if _, err := f.db.Exec("UPDATE managed_scopes SET pid=?,pgid=? WHERE id=?", group, group, f.permit.Scope.ID); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(f.permit)
	if err != nil {
		t.Fatal(err)
	}
	if mode == "partial" {
		data = []byte(`{"scope":`)
	}
	if _, err := input.Write(data); err != nil {
		t.Fatal(err)
	}
	if mode != "partial" {
		_ = input.Close()
	}
	err = cmd.Wait()
	return stdout.Bytes(), stderr.Bytes(), err
}

func testFactoryDockerCleanupNative(t *testing.T, binary string) {
	t.Helper()
	endpoint := os.Getenv("ORO_FACTORY_DOCKER_ENDPOINT")
	if endpoint == "" {
		t.Fatal("explicit run-scoped Colima Unix endpoint is required")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	docker, err = filepath.EvalSymlinks(docker)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"container-stop", "container-remove", "volume-remove"} {
		t.Run(mode, func(t *testing.T) {
			kind := strings.Split(mode, "-")[0]
			f := newFactoryDockerCleanupFixture(t, kind)
			f.configureNative(t, binary, docker, endpoint)
			foreign := f.permit.Target.Name + "-foreign"
			f.createNativeResource(t, f.permit.Target.Name)
			f.createNativeResource(t, foreign)
			before := f.docker(t, kind, "inspect", foreign)
			if mode == "container-stop" {
				f.permit.Operation = factory.DockerCleanupStop
			} else {
				f.permit.Operation = factory.DockerCleanupRemove
			}
			f.persistPermit(t)
			t.Logf("exact daemon creation identity kind=%s CreatedAt=%q", kind, f.permit.Binding.CreatedAt)
			stdout, stderr, err := f.native(t, binary, "valid")
			if err != nil {
				t.Fatalf("actual Docker helper: %v %s", err, stderr)
			}
			result, err := factory.DecodeDockerCleanupResult(stdout, f.permit)
			if err != nil || !result.MutationAcknowledged || len(stderr) != 0 {
				t.Fatalf("native receipt=%s stderr=%s error=%v", stdout, stderr, err)
			}
			if after := f.docker(t, kind, "inspect", foreign); !bytes.Equal(before, after) {
				t.Fatal("foreign resource changed during exact cleanup")
			}
			t.Logf("native cleanup receipt=%s; foreign unchanged=%s; work deadline=%s", stdout, foreign, f.config.config.Deadline)
		})
	}
}

func (f *factoryDockerCleanupFixture) configureNative(t *testing.T, binary, docker, endpoint string) {
	t.Helper()
	f.config.config.OroPath, f.config.config.DockerPath, f.config.config.DockerEndpoint = binary, docker, endpoint
	for path, digest := range map[string]*string{binary: &f.config.config.OroSHA256, docker: &f.config.config.DockerSHA256} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		*digest = factoryHostDigest(data)
	}
	f.writeConfig(t)
	_, metadata, err := factoryDockerContextSnapshot(f.config.config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.config.metaPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.config.metaPath, metadata, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.config.metaPath, 0o400); err != nil {
		t.Fatal(err)
	}
}

func (f factoryDockerCleanupFixture) docker(t *testing.T, args ...string) []byte {
	t.Helper()
	result, err := f.dockerResult(t, args...)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("owned Docker fixture %v: %v exit=%d %s", args, err, result.ExitCode, result.Stderr)
	}
	return result.Stdout
}

func (f factoryDockerCleanupFixture) dockerResult(t *testing.T, args ...string) (factory.HostResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	prefix := []string{"--config", filepath.Join(filepath.Dir(f.config.path), "docker"), "--context", f.config.config.DockerContext}
	cmd := exec.CommandContext(ctx, f.config.config.DockerPath, append(prefix, args...)...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "LANG=C"}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	return factory.HostResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: code}, err
}

func (f *factoryDockerCleanupFixture) createNativeResource(t *testing.T, name string) {
	t.Helper()
	kind := f.permit.Binding.Kind
	args := []string{"volume", "create", "--driver", "local"}
	if kind == "container" {
		args = []string{"create", "--name", name, "--pull=never", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--memory", "32m", "--memory-swap", "32m", "--cpus", "0.25", "--pids-limit", "8", "--log-driver", "none", "--user", "1000:1000", "--entrypoint", "/bin/sleep"}
	} else {
		for key, value := range f.permit.Binding.VolumeOptions {
			args = append(args, "--opt", key+"="+value)
		}
	}
	for key, value := range f.permit.Binding.Labels {
		args = append(args, "--label", key+"="+value)
	}
	if kind == "container" {
		args = append(args, "golang@sha256:27f2d3b83cfc5803c8078f8e730af0f7c431bf067afaec8f60a36fb763187cba", "60")
	} else {
		args = append(args, name)
	}
	f.docker(t, args...)
	if kind == "container" {
		f.docker(t, "start", name)
	}
	var objects []map[string]json.RawMessage
	if err := json.Unmarshal(f.docker(t, kind, "inspect", name), &objects); err != nil || len(objects) != 1 {
		t.Fatalf("inspect fixture resource: %v", err)
	}
	if name == f.permit.Target.Name {
		f.bindNativeLifetime(t, objects[0])
	}
	identity := name
	if kind == "container" {
		if err := json.Unmarshal(objects[0]["Id"], &identity); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { f.removeNativeResource(t, kind, identity, objects[0]) })
}

func (f *factoryDockerCleanupFixture) bindNativeLifetime(t *testing.T, object map[string]json.RawMessage) {
	t.Helper()
	created := "CreatedAt"
	if f.permit.Binding.Kind == "container" {
		created = "Created"
		if err := json.Unmarshal(object["Id"], &f.permit.Binding.ContainerID); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(object["Image"], &f.permit.Binding.ImageID); err != nil {
			t.Fatal(err)
		}
	}
	if err := json.Unmarshal(object[created], &f.permit.Binding.CreatedAt); err != nil {
		t.Fatal(err)
	}
	t.Logf("raw daemon CreatedAt=%q", f.permit.Binding.CreatedAt)
	canonical, err := factory.CanonicalDockerCreatedAt(f.permit.Binding.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	// Fixture ingestion mirrors the trusted storage producer: persist the
	// canonical instant, while keeping the raw daemon snapshot for teardown.
	f.permit.Binding.CreatedAt = canonical
}

func (f factoryDockerCleanupFixture) removeNativeResource(t *testing.T, kind, identity string, original map[string]json.RawMessage) {
	t.Helper()
	observed, _ := f.dockerResult(t, kind, "inspect", identity)
	if factoryDockerCleanupAbsent(kind, identity, observed) {
		return
	}
	var objects []map[string]json.RawMessage
	if observed.ExitCode != 0 || json.Unmarshal(observed.Stdout, &objects) != nil || len(objects) != 1 {
		t.Errorf("cannot verify owned cleanup fixture %s: %s", identity, observed.Stderr)
		return
	}
	key := "CreatedAt"
	if kind == "container" {
		key = "Created"
	}
	if !bytes.Equal(objects[0][key], original[key]) || !bytes.Equal(objects[0]["Name"], original["Name"]) {
		t.Errorf("cleanup fixture lifetime changed: %s", identity)
		return
	}
	if kind == "volume" {
		f.docker(t, "volume", "rm", identity)
		return
	}
	f.docker(t, "rm", "--force", identity)
}
