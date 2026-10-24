package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestFactoryDockerReady(t *testing.T) {
	t.Run("hidden command is wired", func(t *testing.T) {
		command, remaining, err := newFactoryCmd().Find([]string{"_docker-start-ready"})
		if err != nil || len(remaining) != 0 || command.Name() != "_docker-start-ready" || !command.Hidden {
			t.Fatalf("trusted Docker readiness helper is unavailable: command=%s remaining=%v err=%v", command.Name(), remaining, err)
		}
	})
	t.Run("ready retires only its attaching client", testFactoryDockerReadySuccess)
	t.Run("untrusted inputs have no effects", testFactoryDockerReadyInputs)
	t.Run("container identity before attach", testFactoryDockerReadyInspection)
	t.Run("strict ready and complete pipe lifecycle", testFactoryDockerReadyLifecycle)
	t.Run("hidden CLI executes exact request", testFactoryDockerReadyCLI)
	t.Run("bounded inspection framing", testFactoryDockerReadyInspectionFraming)
	t.Run("canonical deadline syntax", testFactoryDockerReadyDeadlineSyntax)
	t.Run("receipt writer failure retains error", testFactoryDockerReadyReceiptFailure)
	t.Run("expired context cannot certify pending reap", testFactoryDockerReadyPendingReap)
}

func testFactoryDockerReadyInspectionFraming(t *testing.T) {
	for _, mode := range []string{"invalid JSON", "empty", "multiple", "trailing"} {
		t.Run(mode, func(t *testing.T) {
			request := factoryDockerReadyFixture(t, "ready")
			data := []byte("invalid")
			switch mode {
			case "empty":
				data = []byte("[]")
			case "multiple":
				var err error
				data, err = json.Marshal([]any{factoryDockerReadyInspectionFixture(request), factoryDockerReadyInspectionFixture(request)})
				if err != nil {
					t.Fatal(err)
				}
			case "trailing":
				var err error
				data, err = os.ReadFile(filepath.Join(request.DockerConfig, "inspect.json"))
				if err != nil {
					t.Fatal(err)
				}
				data = append(data, []byte("{}")...)
			}
			if err := os.WriteFile(filepath.Join(request.DockerConfig, "inspect.json"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := runFactoryDockerReady(t.Context(), request, io.Discard); err == nil {
				t.Fatal("unbound inspection accepted")
			}
			factoryHostExecAssertAbsent(t, filepath.Join(request.DockerConfig, "attach.pid"))
		})
	}
	t.Run("candidate namespace keeps its distinct finite quotas", func(t *testing.T) {
		request := factoryDockerReadyFixture(t, "ready")
		request.ScopeKind = "container"
		inspection := factoryDockerReadyInspectionFixture(request)
		config := inspection["HostConfig"].(map[string]any)
		config["Memory"], config["MemorySwap"], config["NanoCpus"], config["PidsLimit"] = 128<<20, 128<<20, 500000000, 32
		factoryDockerReadyWriteInspection(t, request, []any{inspection})
		if err := runFactoryDockerReady(t.Context(), request, io.Discard); err != nil {
			t.Fatal(err)
		}
		factoryDockerReadyAssertReaped(t, request)
	})
}

func testFactoryDockerReadyDeadlineSyntax(t *testing.T) {
	for _, value := range []string{"", "tomorrow", "2026-10-24T00:00:00+00:00", "2026-10-24T00:00:00.000Z"} {
		if _, err := parseFactoryDockerDeadline(value); err == nil {
			t.Fatalf("noncanonical deadline accepted: %q", value)
		}
	}
	value := "2026-10-24T00:00:00.123456789Z"
	got, err := parseFactoryDockerDeadline(value)
	if err != nil || got.Format(time.RFC3339Nano) != value {
		t.Fatalf("canonical deadline changed: %v %v", got, err)
	}
}

type factoryDockerReadyFailWriter struct{ short bool }

func (writer factoryDockerReadyFailWriter) Write(data []byte) (int, error) {
	if writer.short {
		return len(data) - 1, nil
	}
	return 0, io.ErrClosedPipe
}

func testFactoryDockerReadyReceiptFailure(t *testing.T) {
	for _, short := range []bool{true, false} {
		request := factoryDockerReadyFixture(t, "ready")
		err := runFactoryDockerReady(t.Context(), request, factoryDockerReadyFailWriter{short: short})
		want := io.ErrClosedPipe
		if short {
			want = io.ErrShortWrite
		}
		if !errors.Is(err, want) {
			t.Fatalf("readiness publication error lost: %v", err)
		}
		factoryDockerReadyAssertReaped(t, request)
	}
}

func testFactoryDockerReadySuccess(t *testing.T) {
	request := factoryDockerReadyFixture(t, "ready")
	var output bytes.Buffer
	if err := runFactoryDockerReady(t.Context(), request, &output); err != nil {
		t.Fatal(err)
	}
	want := "READY " + request.ContainerID + " " + request.GuardDeadline.UTC().Format(time.RFC3339Nano) + "\n"
	if output.String() != want || output.Len() > 256 {
		t.Fatalf("readiness receipt = %q, want %q", output.String(), want)
	}
	factoryDockerReadyAssertReaped(t, request)
	commands, err := os.ReadFile(filepath.Join(request.DockerConfig, "commands"))
	if err != nil {
		t.Fatal(err)
	}
	wantCommands := "inspect --type container " + request.ContainerID + "\nstart --attach " + request.ContainerID + "\n"
	if string(commands) != wantCommands {
		t.Fatalf("unexpected Docker authority: %q", commands)
	}
}

func testFactoryDockerReadyInputs(t *testing.T) {
	changes := map[string]func(*factoryDockerReadyRequest){
		"wrong hash":              func(r *factoryDockerReadyRequest) { r.DockerSHA256 = strings.Repeat("0", 64) },
		"relative executable":     func(r *factoryDockerReadyRequest) { r.DockerPath = "docker" },
		"context injection":       func(r *factoryDockerReadyRequest) { r.Context = "--host=evil" },
		"empty context":           func(r *factoryDockerReadyRequest) { r.Context = "" },
		"invalid container":       func(r *factoryDockerReadyRequest) { r.ContainerID = "short" },
		"invalid scope":           func(r *factoryDockerReadyRequest) { r.ScopeID = "../scope" },
		"invalid nonce":           func(r *factoryDockerReadyRequest) { r.ScopeNonce = strings.Repeat("A", 32) },
		"invalid kind":            func(r *factoryDockerReadyRequest) { r.ScopeKind = "host" },
		"zero generation":         func(r *factoryDockerReadyRequest) { r.Generation = 0 },
		"invalid objective":       func(r *factoryDockerReadyRequest) { r.ObjectiveHash = "abc" },
		"invalid policy":          func(r *factoryDockerReadyRequest) { r.PolicyHash = "abc" },
		"mutable image":           func(r *factoryDockerReadyRequest) { r.Image = "golang:latest" },
		"expired":                 func(r *factoryDockerReadyRequest) { r.Deadline = time.Now().Add(-time.Second) },
		"past guard":              func(r *factoryDockerReadyRequest) { r.GuardDeadline = time.Now().Add(-time.Second) },
		"invocation beyond guard": func(r *factoryDockerReadyRequest) { r.Deadline = r.GuardDeadline.Add(time.Second) },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			request := factoryDockerReadyFixture(t, "ready")
			change(&request)
			if err := runFactoryDockerReady(t.Context(), request, io.Discard); err == nil {
				t.Fatal("unsafe readiness request accepted")
			}
			factoryHostExecAssertAbsent(t, filepath.Join(request.DockerConfig, "commands"))
		})
	}
	for _, mode := range []string{"config permissions", "config alias", "program alias", "canceled", "nil output"} {
		t.Run(mode, func(t *testing.T) {
			request := factoryDockerReadyFixture(t, "ready")
			original := request.DockerConfig
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			output := io.Discard
			switch mode {
			case "config permissions":
				if err := os.Chmod(request.DockerConfig, 0o755); err != nil {
					t.Fatal(err)
				}
			case "config alias":
				request.DockerConfig += "-alias"
				if err := os.Symlink(original, request.DockerConfig); err != nil {
					t.Fatal(err)
				}
			case "program alias":
				request.DockerPath += "-alias"
				if err := os.Symlink(strings.TrimSuffix(request.DockerPath, "-alias"), request.DockerPath); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				cancel()
			case "nil output":
				output = nil
			}
			if err := runFactoryDockerReady(ctx, request, output); err == nil {
				t.Fatal("unsafe readiness request accepted")
			}
			factoryHostExecAssertAbsent(t, filepath.Join(original, "commands"))
		})
	}
}

func testFactoryDockerReadyInspection(t *testing.T) {
	changes := map[string]func(map[string]any){
		"foreign ID":    func(m map[string]any) { m["Id"] = strings.Repeat("f", 64) },
		"foreign name":  func(m map[string]any) { m["Name"] = "/foreign" },
		"foreign image": func(m map[string]any) { m["Image"] = "sha256:" + strings.Repeat("f", 64) },
		"foreign nonce": func(m map[string]any) {
			m["Config"].(map[string]any)["Labels"].(map[string]string)["dev.getoro.factory.nonce"] = strings.Repeat("f", 32)
		},
		"candidate user":   func(m map[string]any) { m["Config"].(map[string]any)["User"] = "1000:1000" },
		"wrong entrypoint": func(m map[string]any) { m["Config"].(map[string]any)["Entrypoint"] = []string{"/bin/sh"} },
		"wrong deadline": func(m map[string]any) {
			m["Config"].(map[string]any)["Cmd"] = []string{"--deadline", "2099-01-01T00:00:00Z"}
		},
		"tty":             func(m map[string]any) { m["Config"].(map[string]any)["Tty"] = true },
		"stdout detached": func(m map[string]any) { m["Config"].(map[string]any)["AttachStdout"] = false },
		"open stdin":      func(m map[string]any) { m["Config"].(map[string]any)["OpenStdin"] = true },
		"running":         func(m map[string]any) { m["State"].(map[string]any)["Running"] = true },
		"old start":       func(m map[string]any) { m["State"].(map[string]any)["StartedAt"] = "2026-01-01T00:00:00Z" },
		"exited":          func(m map[string]any) { m["State"].(map[string]any)["Status"] = "exited" },
		"live PID":        func(m map[string]any) { m["State"].(map[string]any)["Pid"] = 123 },
		"network":         func(m map[string]any) { m["HostConfig"].(map[string]any)["NetworkMode"] = "bridge" },
		"writable root":   func(m map[string]any) { m["HostConfig"].(map[string]any)["ReadonlyRootfs"] = false },
		"cap add":         func(m map[string]any) { m["HostConfig"].(map[string]any)["CapAdd"] = []string{"SYS_ADMIN"} },
		"cap drop":        func(m map[string]any) { m["HostConfig"].(map[string]any)["CapDrop"] = []string{} },
		"privileged":      func(m map[string]any) { m["HostConfig"].(map[string]any)["Privileged"] = true },
		"security option": func(m map[string]any) {
			m["HostConfig"].(map[string]any)["SecurityOpt"] = []string{"seccomp=unconfined"}
		},
		"host PID":    func(m map[string]any) { m["HostConfig"].(map[string]any)["PidMode"] = "host" },
		"host IPC":    func(m map[string]any) { m["HostConfig"].(map[string]any)["IpcMode"] = "host" },
		"host cgroup": func(m map[string]any) { m["HostConfig"].(map[string]any)["CgroupnsMode"] = "host" },
		"restart": func(m map[string]any) {
			m["HostConfig"].(map[string]any)["RestartPolicy"] = map[string]any{"Name": "always"}
		},
		"logging": func(m map[string]any) {
			m["HostConfig"].(map[string]any)["LogConfig"] = map[string]any{"Type": "json-file"}
		},
		"unbounded memory":   func(m map[string]any) { m["HostConfig"].(map[string]any)["Memory"] = 0 },
		"unbounded swap":     func(m map[string]any) { m["HostConfig"].(map[string]any)["MemorySwap"] = -1 },
		"unbounded CPU":      func(m map[string]any) { m["HostConfig"].(map[string]any)["NanoCpus"] = 0 },
		"unbounded PID":      func(m map[string]any) { m["HostConfig"].(map[string]any)["PidsLimit"] = -1 },
		"keeper quota drift": func(m map[string]any) { m["HostConfig"].(map[string]any)["ShmSize"] = 2 << 20 },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			request := factoryDockerReadyFixture(t, "ready")
			inspection := factoryDockerReadyInspectionFixture(request)
			change(inspection)
			factoryDockerReadyWriteInspection(t, request, []any{inspection})
			if err := runFactoryDockerReady(t.Context(), request, io.Discard); err == nil {
				t.Fatal("foreign or unsafe container started")
			}
			factoryHostExecAssertAbsent(t, filepath.Join(request.DockerConfig, "attach.pid"))
		})
	}
}

func testFactoryDockerReadyLifecycle(t *testing.T) {
	for _, mode := range []string{"missing", "partial", "wrong", "extra", "early exit", "stderr overflow", "retained pipe", "inspect failure", "inspect overflow"} {
		t.Run(mode, func(t *testing.T) {
			request := factoryDockerReadyFixture(t, mode)
			request.Deadline = time.Now().Add(300 * time.Millisecond)
			var output bytes.Buffer
			started := time.Now()
			runError := runFactoryDockerReady(t.Context(), request, &output)
			if runError == nil {
				t.Fatal("unproven readiness accepted")
			}
			if output.Len() != 0 {
				t.Fatalf("failure emitted readiness %q", output.String())
			}
			if time.Since(started) > 900*time.Millisecond {
				t.Fatal("client lifecycle exceeded fixed deadline")
			}
			if _, err := os.Stat(filepath.Join(request.DockerConfig, "attach.pid")); err == nil {
				factoryDockerReadyAssertFailedRetirement(t, request, runError)
			}
		})
	}
}

func testFactoryDockerReadyPendingReap(t *testing.T) {
	// Hold Wait ourselves so the uncertainty is deterministic, rather than
	// depending on the scheduler racing the helper's fixed invocation deadline.
	child := exec.CommandContext(t.Context(), "/bin/sleep", "10")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _, _ = child.Process.Wait() })
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	waited := make(chan error, 1)
	if err := reapFactoryDockerClient(ctx, waited); !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "reap uncertain") {
		t.Fatalf("pending child reap was not explicitly uncertain: %v", err)
	}
	// An expired helper is not allowed to claim ESRCH before Wait. Only actual
	// joined SIGKILL evidence permits the success path to certify retirement.
	if err := syscall.Kill(child.Process.Pid, 0); err != nil {
		t.Fatalf("fixture unexpectedly reaped before Wait: %v", err)
	}
	waited <- child.Wait()
	if err := reapFactoryDockerClient(t.Context(), waited); err != nil {
		t.Fatalf("actual joined SIGKILL was rejected: %v", err)
	}
	if err := syscall.Kill(child.Process.Pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("joined child remains observable: %v", err)
	}
}

func factoryDockerReadyAssertFailedRetirement(t *testing.T, r factoryDockerReadyRequest, runError error) {
	t.Helper()
	pid := factoryDockerReadyPID(t, r)
	if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		return
	}
	if (!errors.Is(runError, context.DeadlineExceeded) && !errors.Is(runError, context.Canceled)) || !strings.Contains(runError.Error(), "reap uncertain") {
		t.Fatalf("observable child has no explicit bounded retirement uncertainty: %v", runError)
	}
	// This is test-side observation of the already-failed helper, never extra
	// execution/publication authority. Production returns by its fixed deadline;
	// its outer authenticated scope owns retirement after this uncertainty.
	observation, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		select {
		case <-observation.Done():
			t.Fatalf("failed helper's owned client did not disappear: pid=%d error=%v", pid, runError)
		case <-ticker.C:
		}
	}
}

func testFactoryDockerReadyCLI(t *testing.T) {
	request := factoryDockerReadyFixture(t, "ready")
	command := newFactoryCmd()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(io.Discard)
	command.SetArgs([]string{
		"_docker-start-ready", "--docker", request.DockerPath, "--docker-sha256", request.DockerSHA256,
		"--docker-config", request.DockerConfig, "--context", request.Context, "--container", request.ContainerID,
		"--scope-id", request.ScopeID, "--scope-nonce", request.ScopeNonce, "--scope-generation", "1", "--scope-kind", request.ScopeKind,
		"--objective-hash", request.ObjectiveHash, "--policy-hash", request.PolicyHash, "--image", request.Image,
		"--guard-deadline", request.GuardDeadline.Format(time.RFC3339Nano), "--deadline", request.Deadline.Format(time.RFC3339Nano),
	})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(output.String(), "READY "+request.ContainerID+" ") {
		t.Fatalf("missing exact CLI receipt: %q", output.String())
	}
}

func factoryDockerReadyFixture(t *testing.T, mode string) factoryDockerReadyRequest {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "config")
	if err := os.Mkdir(config, 0o700); err != nil {
		t.Fatal(err)
	}
	program := `#!/bin/sh
set -eu
[ "$1" = "--config" ] && [ "$3" = "--context" ] && [ "$4" = "colima-oro-factory" ] || exit 80
[ "${DOCKER_HOST-unset}" = "unset" ] || exit 81
config=$2
shift 4
printf '%s\n' "$*" >> "$config/commands"
mode=$(/bin/cat "$config/mode")
case "$1" in
inspect)
  case "$mode" in
  'inspect failure') exit 7;;
  'inspect overflow') exec /usr/bin/yes x;;
  esac
  exec /bin/cat "$config/inspect.json";;
start)
  printf '%s\n' "$$" > "$config/attach.pid"
  case "$mode" in
  ready) printf 'READY\n';;
  missing) :;;
  partial) printf 'REA';;
  wrong) printf 'WRONG\n';;
  extra) printf 'READY\nEXTRA\n';;
  'early exit') exit 7;;
  'stderr overflow') exec /usr/bin/yes x >&2;;
  'retained pipe') /bin/sleep 1 & printf 'READY\n';;
  esac
  exec /bin/sleep 10;;
*) exit 82;;
esac
`
	path := filepath.Join(root, "docker")
	if err := os.WriteFile(path, []byte(program), 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "mode"), []byte(mode), 0o400); err != nil {
		t.Fatal(err)
	}
	request := factoryDockerReadyRequest{
		DockerPath: path, DockerSHA256: factoryHostDigest([]byte(program)), DockerConfig: config, Context: "colima-oro-factory",
		ContainerID: strings.Repeat("a", 64), ScopeID: strings.Repeat("b", 32), ScopeNonce: strings.Repeat("c", 32), ScopeKind: "keeper", ObjectiveHash: "0123456789abcdef",
		PolicyHash: strings.Repeat("d", 64), Image: "sha256:" + strings.Repeat("e", 64), Generation: 1, Deadline: time.Now().UTC().Add(3 * time.Second), GuardDeadline: time.Now().UTC().Add(time.Minute),
	}
	factoryDockerReadyWriteInspection(t, request, []any{factoryDockerReadyInspectionFixture(request)})
	return request
}

func factoryDockerReadyInspectionFixture(r factoryDockerReadyRequest) map[string]any {
	return map[string]any{
		"Id": r.ContainerID, "Name": "/oro-factory-" + r.ObjectiveHash + "-" + r.ScopeKind + "-" + r.ScopeID + "-" + r.ScopeNonce, "Image": r.Image,
		"Config": map[string]any{
			"User": "65534:65534", "Entrypoint": []string{"/usr/local/bin/oro-factory-guard"}, "Cmd": []string{"--deadline", r.GuardDeadline.UTC().Format(time.RFC3339Nano)},
			"Tty": false, "OpenStdin": false, "AttachStdout": true, "Labels": map[string]string{
				"dev.getoro.factory.objective": r.ObjectiveHash, "dev.getoro.factory.scope": r.ScopeID, "dev.getoro.factory.nonce": r.ScopeNonce,
				"dev.getoro.factory.generation": strconv.FormatUint(r.Generation, 10), "dev.getoro.factory.kind": r.ScopeKind, "dev.getoro.factory.policy": r.PolicyHash,
			},
		},
		"State": map[string]any{"Status": "created", "Running": false, "Pid": 0, "StartedAt": "0001-01-01T00:00:00Z", "FinishedAt": "0001-01-01T00:00:00Z"},
		"HostConfig": map[string]any{
			"NetworkMode": "none", "ReadonlyRootfs": true, "CapDrop": []string{"ALL"}, "CapAdd": []string{}, "Privileged": false,
			"SecurityOpt": []string{"no-new-privileges"}, "PidMode": "", "IpcMode": "private", "CgroupnsMode": "private", "RestartPolicy": map[string]any{"Name": "no", "MaximumRetryCount": 0},
			"LogConfig": map[string]any{"Type": "none", "Config": map[string]string{}}, "Memory": 64 << 20, "MemorySwap": 64 << 20, "NanoCpus": 250000000, "PidsLimit": 16, "ShmSize": 1 << 20,
		},
	}
}

func factoryDockerReadyWriteInspection(t *testing.T, r factoryDockerReadyRequest, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.DockerConfig, "inspect.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func factoryDockerReadyAssertReaped(t *testing.T, r factoryDockerReadyRequest) {
	t.Helper()
	pid := factoryDockerReadyPID(t, r)
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("attaching client was not reaped: probe=%v", err)
	}
}

func factoryDockerReadyPID(t *testing.T, r factoryDockerReadyRequest) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(r.DockerConfig, "attach.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		t.Fatalf("invalid owned child PID: %q", data)
	}
	return pid
}
