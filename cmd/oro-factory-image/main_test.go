package main //nolint:testpackage // Test the private bootstrap runner without native Docker effects.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestGuardImageBuild(t *testing.T) {
	if mode := os.Getenv("ORO_GUARD_BUILD_HELPER"); mode != "" {
		guardBuildProcessHelper(t, mode)
		return
	}
	t.Run("minimal private build and exact immutable receipt", testGuardBuildSuccess)
	t.Run("invalid inputs cannot launch or publish", testGuardBuildInputs)
	t.Run("failed inspection cannot publish", testGuardBuildFailures)
	t.Run("owned subprocess cancellation and overflow join", testGuardBuildProcesses)
	t.Run("retirement requires ESRCH after transient permission uncertainty", testGuardBuildRetirement)
	t.Run("CLI validation never accepts ambient inputs", testGuardBuildCLI)
}

func testGuardBuildRetirement(t *testing.T) {
	for _, first := range []error{nil, syscall.EPERM} {
		t.Run(strconv.FormatBool(first != nil), func(t *testing.T) {
			sequence := []error{first, syscall.EPERM, nil, syscall.ESRCH}
			calls := 0
			signal := func(group int, kind syscall.Signal) error {
				if group != -42 || (calls == 0 && kind != syscall.SIGKILL) || (calls > 0 && kind != 0) {
					t.Fatalf("unexpected signal %d %d", group, kind)
				}
				err := sequence[calls]
				calls++
				return err
			}
			if err := retireGuardGroupWithSignal(42, signal, time.Second); err != nil || calls != len(sequence) {
				t.Fatalf("transient permission denied before positive ESRCH: calls=%d err=%v", calls, err)
			}
		})
	}
	calls := 0
	start := time.Now()
	err := retireGuardGroupWithSignal(42, func(int, syscall.Signal) error { calls++; return syscall.EPERM }, 15*time.Millisecond)
	if !errors.Is(err, syscall.EPERM) || calls < 2 || time.Since(start) > time.Second {
		t.Fatalf("persistent uncertainty lost or unbounded: calls=%d elapsed=%v err=%v", calls, time.Since(start), err)
	}
}

func testGuardBuildSuccess(t *testing.T) {
	f := guardBuildFixture(t)
	t.Setenv("DOCKER_HOST", "tcp://foreign:2375")
	t.Setenv("DOCKER_CONFIG", "/foreign/credentials")
	var calls []guardImageCommand
	result, err := buildGuardImageWithRunner(t.Context(), f, guardBuildRunner(t, f, &calls, ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || result.Version != 1 || result.BaseReference != "golang@sha256:27f2d3b83cfc5803c8078f8e730af0f7c431bf067afaec8f60a36fb763187cba" || result.BaseID != "sha256:"+strings.Repeat("a", 64) || result.ImageID != "sha256:"+strings.Repeat("b", 64) || result.GuardSHA256 != f.GuardSHA256 || result.Platform != "linux/arm64" || len(result.BuildLabel) != 32 {
		t.Fatalf("wrong receipt %+v calls=%d", result, len(calls))
	}
	data, err := os.ReadFile(filepath.Join(f.Output, "receipt.json"))
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, canonical) || len(data) > 4096 {
		t.Fatal("receipt is not exact bounded JSON")
	}
	info, err := os.Stat(filepath.Join(f.Output, "receipt.json"))
	if err != nil || info.Mode().Perm() != 0o400 {
		t.Fatal("receipt is not immutable", err)
	}
	if _, err := buildGuardImageWithRunner(t.Context(), f, guardBuildRunner(t, f, &calls, "")); err == nil || len(calls) != 3 {
		t.Fatal("existing output was reused")
	}
}

func guardBuildRunner(t *testing.T, f guardImageOptions, calls *[]guardImageCommand, failure string) guardImageRunner {
	t.Helper()
	return func(ctx context.Context, call guardImageCommand) ([]byte, error) {
		*calls = append(*calls, call)
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 60*time.Second {
			t.Error("child lacks60s deadline")
		}
		if call.Path != f.Docker || call.Dir != f.Output || !reflect.DeepEqual(call.Env, []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "LANG=C", "HOME=" + filepath.Join(f.Output, "home")}) {
			t.Errorf("untrusted command environment %+v", call)
		}
		prefix := []string{"--config", filepath.Join(f.Output, "docker"), "--context", f.Context}
		if len(call.Args) < len(prefix) || !slices.Equal(call.Args[:len(prefix)], prefix) {
			t.Fatal("missing exact context/config")
		}
		if failure == "runner" {
			return nil, errors.New("injected process failure")
		}
		if len(*calls) == 1 {
			want := append(prefix, "image", "inspect", "--format", "{{json .}}", "golang@sha256:27f2d3b83cfc5803c8078f8e730af0f7c431bf067afaec8f60a36fb763187cba")
			if !slices.Equal(call.Args, want) {
				t.Errorf("base inspect %q", call.Args)
			}
			return []byte(`{"Id":"sha256:` + strings.Repeat("a", 64) + `","Os":"linux","Architecture":"arm64"}`), nil
		}
		if len(*calls) == 2 {
			guardBuildContext(t, f, call.Args)
			return []byte("build complete\n"), nil
		}
		label := strings.TrimPrefix((*calls)[1].Args[9], "dev.getoro.factory.guard-build=")
		if failure == "malformed" {
			return []byte("not-json"), nil
		}
		id, user, entry, arch := "sha256:"+strings.Repeat("b", 64), "65534:65534", "/usr/local/bin/oro-factory-guard", "arm64"
		switch failure {
		case "image":
			id = "mutable:tag"
		case "user":
			user = "0"
		case "entrypoint":
			entry = "/bin/sh"
		case "arch":
			arch = "amd64"
		case "label":
			label = "other"
		}
		data, err := json.Marshal(map[string]any{"Id": id, "Os": "linux", "Architecture": arch, "Config": map[string]any{"User": user, "Entrypoint": []string{entry}, "Labels": map[string]string{"dev.getoro.factory.guard-build": label}}})
		switch failure {
		case "case alias":
			data = bytes.Replace(data, []byte(`"Id":`), []byte(`"id":`), 1)
		case "duplicate":
			data = bytes.Replace(data, []byte(`"Id":`), []byte(`"Id":"wrong","Id":`), 1)
		case "invalid UTF8":
			data = append([]byte(`{"ignored":"`), append([]byte{255}, append([]byte(`",`), data[1:]...)...)...)
		case "trailing":
			data = append(data, []byte(" null")...)
		case "oversized":
			data = bytes.Repeat([]byte(" "), (1<<20)+1)
		case "receipt exists":
			guardBuildWrite(t, filepath.Join(f.Output, "receipt.pending"), []byte("previous"), 0o400)
		}
		return data, err
	}
}

func guardBuildContext(t *testing.T, f guardImageOptions, args []string) {
	t.Helper()
	if len(args) != 13 || !slices.Equal(args[4:9], []string{"build", "--network=none", "--pull=false", "--platform=linux/arm64", "--label"}) || args[10] != "--tag" {
		t.Fatalf("build options %q", args)
	}
	contextPath := args[len(args)-1]
	entries, err := os.ReadDir(contextPath)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if !slices.Equal(names, []string{"Dockerfile", "guard"}) {
		t.Fatalf("unsafe build context %v", names)
	}
	data, err := os.ReadFile(filepath.Join(contextPath, "guard"))
	if err != nil || guardBuildHash(data) != f.GuardSHA256 {
		t.Fatal("guard bytes changed", err)
	}
	recipe, err := os.ReadFile(filepath.Join(contextPath, "Dockerfile"))
	if err != nil || string(recipe) != "FROM golang@sha256:27f2d3b83cfc5803c8078f8e730af0f7c431bf067afaec8f60a36fb763187cba\nCOPY guard /usr/local/bin/oro-factory-guard\nUSER 65534:65534\nENTRYPOINT [\"/usr/local/bin/oro-factory-guard\"]\n" {
		t.Fatal("recipe is not fixed", err)
	}
	cli, err := os.ReadFile(filepath.Join(f.Output, "docker", "config.json"))
	if err != nil || string(cli) != "{}" {
		t.Fatal("ambient Docker configuration", err)
	}
	metadata, err := os.ReadFile(filepath.Join(f.Output, "docker", "contexts", "meta", guardBuildHash([]byte(f.Context)), "meta.json"))
	if err != nil || !bytes.Contains(metadata, []byte(f.Endpoint)) {
		t.Fatal("dedicated endpoint missing", err)
	}
}

func testGuardBuildInputs(t *testing.T) {
	for _, mode := range []string{"hash", "docker hash", "guard alias", "docker alias", "dynamic", "architecture", "oversize", "endpoint", "socket alias", "socket public", "fifo", "default", "relative", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			f := guardBuildFixture(t)
			ctx := t.Context()
			switch mode {
			case "hash":
				f.GuardSHA256 = strings.Repeat("a", 64)
			case "docker hash":
				f.DockerSHA256 = strings.Repeat("a", 64)
			case "guard alias":
				f.Guard = guardBuildAlias(t, f.Guard)
			case "docker alias":
				f.Docker = guardBuildAlias(t, f.Docker)
			case "dynamic", "architecture":
				data := guardBuildELF()
				if mode == "dynamic" {
					binary.LittleEndian.PutUint32(data[64:], 3)
				} else {
					binary.LittleEndian.PutUint16(data[18:], 62)
				}
				guardBuildWrite(t, f.Guard, data, 0o500)
				f.GuardSHA256 = guardBuildHash(data)
			case "oversize":
				if err := os.Chmod(f.Guard, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Truncate(f.Guard, (64<<20)+1); err != nil {
					t.Fatal(err)
				}
			case "endpoint":
				f.Endpoint = "unix://" + f.Guard
			case "socket alias":
				f.Endpoint = "unix://" + guardBuildAlias(t, strings.TrimPrefix(f.Endpoint, "unix://"))
			case "socket public":
				if err := os.Chmod(strings.TrimPrefix(f.Endpoint, "unix://"), 0o666); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				f.Guard = filepath.Join(filepath.Dir(f.Guard), "fifo")
				if err := syscall.Mkfifo(f.Guard, 0o500); err != nil {
					t.Fatal(err)
				}
			case "default":
				f.Context = "default"
			case "relative":
				f.Output = "relative"
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			calls := 0
			got, err := buildGuardImageWithRunner(ctx, f, func(context.Context, guardImageCommand) ([]byte, error) { calls++; return nil, nil })
			if err == nil || got != (guardImageReceipt{}) || calls != 0 {
				t.Fatalf("invalid input executed %+v %v calls%d", got, err, calls)
			}
			if _, err := os.Stat(f.Output); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("input rejection created output", err)
			}
		})
	}
}

func testGuardBuildFailures(t *testing.T) {
	for _, mode := range []string{"runner", "malformed", "image", "user", "entrypoint", "arch", "label", "case alias", "duplicate", "invalid UTF8", "trailing", "oversized", "receipt exists"} {
		t.Run(mode, func(t *testing.T) {
			f := guardBuildFixture(t)
			var calls []guardImageCommand
			got, err := buildGuardImageWithRunner(t.Context(), f, guardBuildRunner(t, f, &calls, mode))
			if err == nil || got != (guardImageReceipt{}) {
				t.Fatalf("uncertain image published %+v %v", got, err)
			}
			if _, err := os.Stat(filepath.Join(f.Output, "receipt.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failure published receipt", err)
			}
		})
	}
}

func guardBuildFixture(t *testing.T) guardImageOptions {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	guard := filepath.Join(root, "guard-input")
	data := guardBuildELF()
	guardBuildWrite(t, guard, data, 0o500)
	docker := filepath.Join(root, "docker")
	program := []byte("#!/bin/sh\nexit 0\n")
	guardBuildWrite(t, docker, program, 0o500)
	socketRoot, err := os.MkdirTemp("/tmp", "ogi-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketRoot) })
	socketRoot, err = filepath.EvalSymlinks(socketRoot)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(socketRoot, "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := os.Chmod(socket, 0o600); err != nil {
		t.Fatal(err)
	}
	return guardImageOptions{Guard: guard, GuardSHA256: guardBuildHash(data), Docker: docker, DockerSHA256: guardBuildHash(program), Context: "oro-factory", Endpoint: "unix://" + socket, Output: filepath.Join(root, "output")}
}

func guardBuildELF() []byte {
	data := make([]byte, 120)
	copy(data, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(data[16:], 2)
	binary.LittleEndian.PutUint16(data[18:], 183)
	binary.LittleEndian.PutUint32(data[20:], 1)
	binary.LittleEndian.PutUint64(data[24:], 0x400000)
	binary.LittleEndian.PutUint64(data[32:], 64)
	binary.LittleEndian.PutUint16(data[52:], 64)
	binary.LittleEndian.PutUint16(data[54:], 56)
	binary.LittleEndian.PutUint16(data[56:], 1)
	binary.LittleEndian.PutUint32(data[64:], 1)
	binary.LittleEndian.PutUint32(data[68:], 5)
	binary.LittleEndian.PutUint64(data[80:], 0x400000)
	binary.LittleEndian.PutUint64(data[96:], 120)
	binary.LittleEndian.PutUint64(data[104:], 120)
	return data
}

func guardBuildHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func guardBuildWrite(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		if err := os.Chmod(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func guardBuildAlias(t *testing.T, path string) string {
	t.Helper()
	alias := path + "-alias"
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	return alias
}

func testGuardBuildProcesses(t *testing.T) {
	for _, mode := range []string{"cancel", "overflow", "descendant", "nonzero", "success"} {
		t.Run(mode, func(t *testing.T) {
			path, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(t.TempDir(), "pid")
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			call := guardImageCommand{Path: path, Args: []string{"-test.run=^TestGuardImageBuild$"}, Env: []string{"ORO_GUARD_BUILD_HELPER=" + mode, "ORO_GUARD_BUILD_MARKER=" + marker, "GOCOVERDIR=" + filepath.Dir(marker)}, Dir: filepath.Dir(marker)}
			if mode == "cancel" || mode == "descendant" {
				go func() {
					for ctx.Err() == nil {
						if _, err := os.Stat(marker); err == nil {
							cancel()
							return
						}
						time.Sleep(time.Millisecond)
					}
				}()
			}
			data, err := runGuardCommand(ctx, call)
			runError := err
			if mode == "success" {
				if err != nil || string(data) != "ok" {
					t.Fatalf("successful subprocess %q %v", data, err)
				}
			} else if err == nil || len(data) > 1<<20 {
				t.Fatalf("unbounded process result %d %v", len(data), err)
			}
			pidData, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal("helper did not run", err)
			}
			pid, err := strconv.Atoi(string(pidData))
			if err != nil || pid <= 1 {
				t.Fatal("invalid helper PID")
			}
			if err := syscall.Kill(-pid, 0); !errors.Is(err, syscall.ESRCH) {
				t.Fatalf("owned process group %d remains after return: observe=%v runner=%v", pid, err, runError)
			}
			if mode == "descendant" {
				child, err := os.ReadFile(marker + ".child")
				if err != nil {
					t.Fatal(err)
				}
				childPID, err := strconv.Atoi(string(child))
				if err != nil || childPID <= 1 {
					t.Fatal("bad descendant evidence")
				}
				if err := syscall.Kill(childPID, 0); !errors.Is(err, syscall.ESRCH) {
					t.Fatal("descendant survived joined group", err)
				}
			}
		})
	}
}

func guardBuildProcessHelper(t *testing.T, mode string) {
	t.Helper()
	if mode == "descendant" {
		path, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		child := exec.Command(path, "-test.run=^TestGuardImageBuild$")
		child.Env = []string{"ORO_GUARD_BUILD_HELPER=child", "ORO_GUARD_BUILD_MARKER=" + os.Getenv("ORO_GUARD_BUILD_MARKER") + ".child", "GOCOVERDIR=" + os.Getenv("GOCOVERDIR")}
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		for {
			if _, err := os.Stat(os.Getenv("ORO_GUARD_BUILD_MARKER") + ".child"); err == nil {
				break
			}
			time.Sleep(time.Millisecond)
		}
	}
	if err := os.WriteFile(os.Getenv("ORO_GUARD_BUILD_MARKER"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	if mode == "overflow" {
		for {
			_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), 64<<10))
		}
	}
	if mode == "success" {
		_, _ = os.Stdout.Write([]byte("ok"))
		os.Exit(0)
	}
	if mode == "nonzero" {
		os.Exit(17)
	}
	for {
		time.Sleep(time.Second)
	}
}

func testGuardBuildCLI(t *testing.T) {
	var out, diagnostic bytes.Buffer
	if runCLI(t.Context(), nil, &out, &diagnostic) != 1 || diagnostic.Len() == 0 || out.Len() != 0 {
		t.Fatal("missing flags did not explain rejection")
	}
	if runCLI(t.Context(), []string{"--unknown"}, &out, &diagnostic) != 1 {
		t.Fatal("unknown flag accepted")
	}
	if runCLI(t.Context(), []string{"positional"}, &out, &diagnostic) != 1 {
		t.Fatal("positional argument accepted")
	}
	f := guardBuildFixture(t)
	// This private script is a test Docker protocol peer, never native Docker.
	program := []byte("#!/bin/sh\nif [ \"$5\" = build ]; then exit 0; fi\ncase \"$9\" in golang@*) printf '{\"Id\":\"sha256:" + strings.Repeat("a", 64) + "\",\"Os\":\"linux\",\"Architecture\":\"arm64\"}';; *) label=${9#oro-factory-guard:}; printf '{\"Id\":\"sha256:" + strings.Repeat("b", 64) + "\",\"Os\":\"linux\",\"Architecture\":\"arm64\",\"Config\":{\"User\":\"65534:65534\",\"Entrypoint\":[\"/usr/local/bin/oro-factory-guard\"],\"Labels\":{\"dev.getoro.factory.guard-build\":\"%s\"}}}' \"$label\";; esac\n")
	guardBuildWrite(t, f.Docker, program, 0o500)
	f.DockerSHA256 = guardBuildHash(program)
	args := []string{"--guard", f.Guard, "--guard-sha256", f.GuardSHA256, "--docker", f.Docker, "--docker-sha256", f.DockerSHA256, "--context", f.Context, "--endpoint", f.Endpoint, "--output", f.Output}
	out.Reset()
	diagnostic.Reset()
	if runCLI(t.Context(), args, &out, &diagnostic) != 0 {
		t.Fatalf("CLI private peer failed: %s", diagnostic.String())
	}
	var receipt guardImageReceipt
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil || receipt.ImageID != "sha256:"+strings.Repeat("b", 64) {
		t.Fatal("CLI did not return exact receipt", err)
	}
}
