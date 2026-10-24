package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestGuardDeadline(t *testing.T) {
	deadline := time.Now().Add(80 * time.Millisecond)
	var ready bytes.Buffer
	err := runGuard(t.Context(), deadline, &ready)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runGuard = %v, want deadline expiry", err)
	}
	if time.Now().Before(deadline) {
		t.Fatal("guard returned before its admitted deadline")
	}
	if ready.String() != "READY\n" {
		t.Fatalf("readiness = %q, want READY\\n", ready.String())
	}
}

func TestGuardRejectsInvalidStartup(t *testing.T) {
	for _, deadline := range []time.Time{{}, time.Now().Add(-time.Second)} {
		var ready bytes.Buffer
		if err := runGuard(t.Context(), deadline, &ready); err == nil {
			t.Fatal("expired or zero deadline accepted")
		}
		if ready.Len() != 0 {
			t.Fatal("invalid deadline announced readiness")
		}
	}
	if err := runGuard(t.Context(), time.Now().Add(time.Second), nil); err == nil {
		t.Fatal("nil readiness writer accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var ready bytes.Buffer
	if err := runGuard(ctx, time.Now().Add(time.Second), &ready); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled guard = %v", err)
	}
	if ready.Len() != 0 {
		t.Fatal("canceled guard announced readiness")
	}
}

type blockedReadiness struct {
	started chan struct{}
	release chan struct{}
	done    chan struct{}
}

func (w blockedReadiness) Write(p []byte) (int, error) {
	close(w.started)
	<-w.release
	close(w.done)
	return len(p), nil
}

func TestGuardDeadlineSurvivesBlockedReadiness(t *testing.T) {
	w := blockedReadiness{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	defer close(w.release)
	result := make(chan error, 1)
	deadline := time.Now().Add(80 * time.Millisecond)
	go func() { result <- runGuard(t.Context(), deadline, w) }()
	select {
	case <-w.started:
	case <-time.After(time.Second):
		t.Fatal("readiness write never started")
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("blocked readiness = %v, want deadline expiry", err)
		}
		if time.Now().Before(deadline) {
			t.Fatal("blocked readiness caused early expiry")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked readiness extended the immutable deadline")
	}
}

type failedReadiness struct{ err error }

func (w failedReadiness) Write([]byte) (int, error) { return 0, w.err }

func TestGuardReadinessFailure(t *testing.T) {
	want := errors.New("closed readiness pipe")
	if err := runGuard(t.Context(), time.Now().Add(time.Second), failedReadiness{want}); !errors.Is(err, want) {
		t.Fatalf("write failure = %v, want %v", err, want)
	}
	if err := runGuard(t.Context(), time.Now().Add(time.Second), failedReadiness{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write = %v, want %v", err, io.ErrShortWrite)
	}
}

func TestParseDeadline(t *testing.T) {
	value := "2030-10-23T12:00:00.123456789Z"
	want, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseDeadline([]string{"--deadline", value})
	if err != nil || !got.Equal(want) {
		t.Fatalf("parseDeadline = %v, %v; want %v", got, err, want)
	}
	for _, args := range [][]string{
		nil,
		{"--deadline"},
		{"--deadline", "1h"},
		{"--deadline", ""},
		{"--deadline", value, "extra"},
		{"--deadline", value, "--deadline", value},
		{"--extend", value},
		{"--deadline=" + value},
		{"--deadline", "2030-10-23"},
	} {
		if _, err := parseDeadline(args); err == nil {
			t.Errorf("accepted invalid arguments %q", args)
		}
	}
}

func TestGuardCLIRejectsInvalidInput(t *testing.T) {
	var ready, diagnostic bytes.Buffer
	if code := runCLI([]string{"--extend", "1h"}, &ready, &diagnostic); code == 0 {
		t.Fatal("invalid CLI accepted")
	}
	if ready.Len() != 0 || !strings.Contains(diagnostic.String(), "usage:") {
		t.Fatalf("invalid CLI output: ready=%q diagnostic=%q", ready.String(), diagnostic.String())
	}
}

func TestGuardCLIRefusesNonNamespaceIdentity(t *testing.T) {
	if runtime.GOOS == "linux" && os.Getpid() == 1 && os.Getuid() == 65534 && os.Getgid() == 65534 {
		t.Skip("unit test itself has the required isolated guard identity")
	}
	var ready, diagnostic bytes.Buffer
	args := []string{"--deadline", time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}
	if code := runCLI(args, &ready, &diagnostic); code == 0 {
		t.Fatal("guard accepted execution outside its unprivileged PID1 identity")
	}
	if ready.Len() != 0 || diagnostic.Len() == 0 {
		t.Fatalf("identity refusal output: ready=%q diagnostic=%q", ready.String(), diagnostic.String())
	}
}

// TestNamespaceDeadline requires the dedicated VM and never falls back to the
// active Docker context. Once enabled, missing prerequisites are test failures.
func TestNamespaceDeadline(t *testing.T) {
	if os.Getenv("ORO_FACTORY_INTEGRATION") != "1" {
		t.Skip("set ORO_FACTORY_INTEGRATION=1 to require the real namespace test")
	}
	dockerContext := os.Getenv("ORO_FACTORY_DOCKER_CONTEXT")
	if dockerContext != "colima-oro-factory" {
		t.Fatal("ORO_FACTORY_DOCKER_CONTEXT must name the dedicated colima-oro-factory context")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("required Docker backend unavailable: %v", err)
	}
	if got := dockerOutput(t, dockerContext, "info", "--format", "{{.OSType}}/{{.Architecture}}"); got != "linux/aarch64" {
		t.Fatalf("required Linux arm64 backend unavailable: %q", got)
	}
	imageID, nonce := buildGuardImage(t, dockerContext)
	deadline := time.Now().Add(12 * time.Second)
	name := "oro-factory-guard-test-" + nonce
	args := []string{
		"create", "--name", name, "--label", "oro.factory.guard-test=" + nonce,
		"--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--cgroupns", "private", "--ipc", "private", "--memory", "64m", "--memory-swap", "64m",
		"--cpus", "0.5", "--pids-limit", "64", "--log-driver", "none", "--shm-size", "1m",
		"--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=4194304,nr_inodes=1024,mode=1777",
		"--user", "65534:65534", imageID, "--deadline", deadline.UTC().Format(time.RFC3339Nano),
	}
	containerID := dockerOutput(t, dockerContext, args...)
	t.Cleanup(func() { cleanupGuardResource(t, dockerContext, "container", containerID, nonce) })
	assertGuardIsolation(t, dockerContext, containerID, imageID, nonce)
	startGuardAndDropHostClient(t, dockerContext, containerID)
	assertCandidateCannotControlGuard(t, dockerContext, containerID)
	startDetachedWriter(t, dockerContext, containerID)
	// There is no controller or SQLite store in this fixture. The attaching
	// host process has been SIGKILLed. No Docker command or cleanup runs while
	// the independent guard reaches the original admitted deadline.
	remaining := time.Until(deadline.Add(400 * time.Millisecond))
	if remaining <= 0 {
		t.Fatal("attack fixture consumed its deadline before autonomous observation")
	}
	time.Sleep(remaining)
	assertNamespaceExpired(t, dockerContext, containerID, deadline)
	t.Logf("namespace=%s original_deadline=%s: UID1000 attacks denied; detached session writer killed after host client death", containerID, deadline.UTC().Format(time.RFC3339Nano))
}

const guardBaseImage = "golang@sha256:27f2d3b83cfc5803c8078f8e730af0f7c431bf067afaec8f60a36fb763187cba"

func buildGuardImage(t *testing.T, dockerContext string) (string, string) {
	t.Helper()
	dockerOutput(t, dockerContext, "image", "inspect", guardBaseImage)
	dir := t.TempDir()
	binary := filepath.Join(dir, "guard")
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-trimpath", "-buildvcs=false", "-o", binary, ".")
	build.Env = append(os.Environ(), "GOTOOLCHAIN=go1.26.10", "CGO_ENABLED=0", "GOOS=linux", "GOARCH=arm64")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build static Linux guard: %v\n%s", err, output)
	}
	assertStaticGuard(t, binary)
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(binary, 0o555); err != nil {
		t.Fatal(err)
	}
	var nonceBytes [8]byte
	if _, err := rand.Read(nonceBytes[:]); err != nil {
		t.Fatal(err)
	}
	nonce := hex.EncodeToString(nonceBytes[:])
	tag := "oro-factory-guard-test:" + nonce
	// The complete build context is exactly this recipe and the static guard.
	// Neither the repository nor host credentials enter the Docker build.
	recipe := "FROM " + guardBaseImage + "\nCOPY guard /usr/local/bin/oro-factory-guard\nUSER 65534:65534\nENTRYPOINT [\"/usr/local/bin/oro-factory-guard\"]\n"
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(recipe), 0o600); err != nil {
		t.Fatal(err)
	}
	dockerOutput(t, dockerContext, "build", "--network", "none", "--pull=false", "--label", "oro.factory.guard-test="+nonce, "--tag", tag, dir)
	imageID := dockerOutput(t, dockerContext, "image", "inspect", "--format", "{{.Id}}", tag)
	if !strings.HasPrefix(imageID, "sha256:") || len(imageID) != 71 {
		t.Fatalf("derived image identity is not immutable: %q", imageID)
	}
	t.Cleanup(func() { cleanupGuardResource(t, dockerContext, "image", imageID, nonce) })
	t.Logf("base=%s source_sha256=%x static_linux_arm64_sha256=%x image=%s", guardBaseImage, sha256.Sum256(source), sha256.Sum256(artifact), imageID)
	return imageID, nonce
}

func assertStaticGuard(t *testing.T, path string) {
	t.Helper()
	binary, err := elf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer binary.Close()
	if binary.Machine != elf.EM_AARCH64 {
		t.Fatalf("guard ELF architecture = %v", binary.Machine)
	}
	for _, program := range binary.Progs {
		if program.Type == elf.PT_INTERP || program.Type == elf.PT_DYNAMIC {
			t.Fatal("guard is dynamically linked")
		}
	}
}

func dockerOutput(t *testing.T, dockerContext string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", append([]string{"--context", dockerContext}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("docker %q: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func cleanupGuardResource(t *testing.T, dockerContext, kind, id, nonce string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	args := []string{"--context", dockerContext, kind, "inspect", "--format", "{{index .Config.Labels \"oro.factory.guard-test\"}}", id}
	output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != nonce {
		t.Errorf("refuse cleanup without exact ownership proof for %s %s: %v %s", kind, id, err, output)
		return
	}
	args = []string{"--context", dockerContext, kind, "rm", id}
	if kind == "container" {
		args = append(args, "--force")
	}
	if output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		t.Errorf("cleanup owned %s %s: %v %s", kind, id, err, output)
	}
}

func assertGuardIsolation(t *testing.T, dockerContext, id, imageID, nonce string) {
	t.Helper()
	var objects []struct {
		Image  string
		Config struct {
			User   string
			Labels map[string]string
		}
		HostConfig struct {
			ReadonlyRootfs bool
			Privileged     bool
			NetworkMode    string
			PidMode        string
			IpcMode        string
			CgroupnsMode   string
			Memory         int64
			MemorySwap     int64
			NanoCpus       int64
			PidsLimit      int64
			Binds          []string
			CapAdd         []string
			CapDrop        []string
			SecurityOpt    []string
			Tmpfs          map[string]string
			LogConfig      struct{ Type string }
		}
		Mounts []struct{ Type, Source string }
	}
	if err := json.Unmarshal([]byte(dockerOutput(t, dockerContext, "inspect", id)), &objects); err != nil || len(objects) != 1 {
		t.Fatalf("inspect namespace: %v", err)
	}
	got := objects[0]
	host := got.HostConfig
	if got.Image != imageID || got.Config.User != "65534:65534" || got.Config.Labels["oro.factory.guard-test"] != nonce {
		t.Fatalf("unexpected guard identity: %+v", got.Config)
	}
	if !host.ReadonlyRootfs || host.Privileged || host.NetworkMode != "none" || host.PidMode != "" || host.IpcMode != "private" || host.CgroupnsMode != "private" {
		t.Fatalf("namespace isolation mismatch: %+v", host)
	}
	if host.Memory != 64<<20 || host.MemorySwap != host.Memory || host.NanoCpus != 500000000 || host.PidsLimit != 64 || host.LogConfig.Type != "none" {
		t.Fatalf("resource bounds mismatch: %+v", host)
	}
	if len(host.Binds) != 0 || len(host.CapAdd) != 0 || strings.Join(host.CapDrop, ",") != "ALL" || strings.Join(host.SecurityOpt, ",") != "no-new-privileges" {
		t.Fatalf("authority isolation mismatch: %+v", host)
	}
	if !strings.Contains(host.Tmpfs["/tmp"], "size=4194304,nr_inodes=1024") {
		t.Fatalf("scratch bounds missing: %v", host.Tmpfs)
	}
	for _, mount := range got.Mounts {
		if mount.Type != "tmpfs" {
			t.Fatalf("unexpected host/volume mount: %+v", mount)
		}
	}
}

func startGuardAndDropHostClient(t *testing.T, dockerContext, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", "--context", dockerContext, "start", "--attach", id)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	readiness := make([]byte, len("READY\n"))
	_, readErr := io.ReadFull(stdout, readiness)
	killErr := command.Process.Kill()
	waitErr := command.Wait()
	if readErr != nil || string(readiness) != "READY\n" {
		t.Fatalf("guard readiness absent: %q, read=%v wait=%v stderr=%s", readiness, readErr, waitErr, stderr.String())
	}
	if killErr != nil || waitErr == nil {
		t.Fatalf("could not kill attaching host client: kill=%v wait=%v", killErr, waitErr)
	}
	t.Log("guard READY received; attaching host client SIGKILLed and reaped")
}

func assertCandidateCannotControlGuard(t *testing.T, dockerContext, id string) {
	t.Helper()
	script := `set -eu
test "$(id -u)" = 1000
test "$(id -g)" = 1000
test "$(awk '/^Uid:/ {print $2}' /proc/1/status)" = 65534
test "$(awk '/^CapEff:/ {print $2}' /proc/self/status)" = 0000000000000000
test "$(awk '/^NoNewPrivs:/ {print $2}' /proc/self/status)" = 1
for signal in STOP KILL TERM; do
  if kill -"$signal" 1 2>/dev/null; then echo "candidate signaled PID1: $signal"; exit 1; fi
done
if (printf replace > /usr/local/bin/oro-factory-guard) 2>/dev/null; then exit 1; fi
if (printf extend > /proc/1/fd/0) 2>/dev/null; then exit 1; fi
if (printf extend > /proc/1/mem) 2>/dev/null; then exit 1; fi
if /usr/local/bin/oro-factory-guard --deadline 2099-01-01T00:00:00Z >/tmp/replacement 2>&1; then exit 1; fi
printf ATTACKS_DENIED`
	if got := dockerOutput(t, dockerContext, "exec", "--user", "1000:1000", id, "/bin/sh", "-c", script); got != "ATTACKS_DENIED" {
		t.Fatalf("candidate authority check = %q", got)
	}
}

func startDetachedWriter(t *testing.T, dockerContext, id string) {
	t.Helper()
	writer := `echo $$ > /tmp/writer.pid; while :; do echo tick >> /tmp/heartbeat; sleep 0.05; done`
	// Two forked sessions ensure the writer is detached from the Docker exec
	// process and adopted by PID1, rather than merely a foreground child.
	inner := "setsid /bin/sh -c " + shellLiteral(writer) + " </dev/null >/dev/null 2>/tmp/writer.log &"
	outer := "setsid /bin/sh -c " + shellLiteral(inner) + " </dev/null >/dev/null 2>/tmp/detach.log &"
	dockerOutput(t, dockerContext, "exec", "--user", "1000:1000", id, "/bin/sh", "-c", outer)
	script := `set -eu
for n in 1 2 3 4 5 6 7 8 9 10; do
  if test -s /tmp/writer.pid && test -s /tmp/heartbeat; then break; fi
  sleep 0.05
done
cat /tmp/detach.log /tmp/writer.log
pid=$(cat /tmp/writer.pid)
test "$(awk '/^PPid:/ {print $2}' /proc/$pid/status)" = 1
test "$(awk '{print $6}' /proc/$pid/stat)" = "$pid"
before=$(wc -c < /tmp/heartbeat)
sleep 0.15
after=$(wc -c < /tmp/heartbeat)
test "$after" -gt "$before"
printf 'writer_pid=%s heartbeat_before=%s heartbeat_after=%s' "$pid" "$before" "$after"`
	t.Log(dockerOutput(t, dockerContext, "exec", "--user", "1000:1000", id, "/bin/sh", "-c", script))
}

func shellLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func assertNamespaceExpired(t *testing.T, dockerContext, id string, deadline time.Time) {
	t.Helper()
	var objects []struct {
		State struct {
			Running, OOMKilled bool
			Pid, ExitCode      int
			FinishedAt         time.Time
		}
	}
	if err := json.Unmarshal([]byte(dockerOutput(t, dockerContext, "inspect", id)), &objects); err != nil || len(objects) != 1 {
		t.Fatalf("inspect expired namespace: %v", err)
	}
	state := objects[0].State
	if state.Running || state.Pid != 0 || state.OOMKilled || state.ExitCode != 0 {
		t.Fatalf("namespace not retired by normal watchdog expiry: %+v", state)
	}
	if state.FinishedAt.Before(deadline) || state.FinishedAt.After(deadline.Add(2*time.Second)) {
		t.Fatalf("namespace finished at %s, outside original deadline %s + 2s", state.FinishedAt, deadline)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "docker", "--context", dockerContext, "top", id).CombinedOutput()
	if err == nil || !strings.Contains(string(output), "not running") {
		t.Fatalf("namespace still exposes live descendants: %v %s", err, output)
	}
	t.Logf("kernel namespace retirement: pid=%d exit=%d finished=%s", state.Pid, state.ExitCode, state.FinishedAt.UTC().Format(time.RFC3339Nano))
}
