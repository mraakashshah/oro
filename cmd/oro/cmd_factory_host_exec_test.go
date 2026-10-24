package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"oro/pkg/factory"
)

func TestFactoryHostExec(t *testing.T) {
	t.Run("handshake socket has no cleanup permit authority", func(t *testing.T) {
		authority := &factoryHostSocketAuthority{}
		permit, err := authority.CleanupPermit(t.Context(), factory.ScopeHandle{})
		if err == nil || !reflect.DeepEqual(permit, factory.DockerCleanupPermit{}) {
			t.Fatalf("handshake socket supplied cleanup authority: %+v %v", permit, err)
		}
	})
	t.Run("hidden command is registered", func(t *testing.T) {
		command, _, err := newFactoryCmd().Find([]string{"_host-exec"})
		if err != nil || command.Name() != "_host-exec" || !command.Hidden {
			t.Fatalf("bounded hidden host wrapper is unavailable: err=%v", err)
		}
	})
	t.Run("no GO causes no effects", testFactoryHostExecNoGrant)
	t.Run("literal argv cwd stdin environment and inherited PGID", testFactoryHostExecLiteral)
	t.Run("deadline and output bound both streams", testFactoryHostExecBounds)
	t.Run("parent exit bounds inherited pipes", testFactoryHostExecPipes)
	t.Run("invalid request fails before authority", testFactoryHostExecInvalid)
	t.Run("private explicit result and replay exclusion", testFactoryHostExecReplay)
	t.Run("immutable canonical CLI envelope", testFactoryHostExecCLI)
	t.Run("socket GO is scope digest deadline and one shot bound", testFactoryHostExecSocket)
	t.Run("complete hidden CLI uses authenticated socket", testFactoryHostExecCLIAllowed)
	t.Run("escaped failure serialization stays reserved", testFactoryHostExecSerialization)
	t.Run("silent authority is deadline bounded", testFactoryHostExecSilentAuthority)
	t.Run("inflight barrier survives publication", testFactoryHostExecPermanentBarrier)
	t.Run("final sync cannot outlive deadline", testFactoryHostExecFinalSync)
	t.Run("ordinary nonzero exit is a result", testFactoryHostExecExpectedExit)
	t.Run("pending hash enforces file and byte bounds", testFactoryHostExecPendingHash)
	t.Run("publication confirmation is one shot after GO", testFactoryHostExecOnePublished)
	t.Run("stdin early close and retained pipe differ", testFactoryHostExecInputPipes)
	t.Run("initial bulk source fits complete request", func(t *testing.T) {
		request := factoryHostExecFixtureRequest(t, "literal")
		request.Invocation.Stdin = make([]byte, 16<<20)
		data, err := encodeFactoryHostExecRequest(request)
		if err != nil || len(data) > factoryHostExecRequestLimit {
			t.Fatalf("initial source transfer cannot fit request: bytes=%d err=%v", len(data), err)
		}
	})
}

func testFactoryHostExecNoGrant(t *testing.T) {
	for _, kind := range []string{"denied", "hello error", "observation mismatch", "deadline elapsed during GO"} {
		t.Run(kind, func(t *testing.T) {
			request := factoryHostExecFixtureRequest(t, "literal")
			authority := &factoryHostExecTestAuthority{deadline: request.Invocation.Deadline, grant: true}
			switch kind {
			case "denied":
				authority.grant = false
			case "hello error":
				authority.helloError = errors.New("unauthenticated peer")
			case "observation mismatch":
				authority.deadline = authority.deadline.Add(time.Second)
			case "deadline elapsed during GO":
				request.Invocation.Deadline = time.Now().Add(40 * time.Millisecond)
				authority.deadline = request.Invocation.Deadline
				authority.grantDelay = 70 * time.Millisecond
			}
			if err := runFactoryHostExec(t.Context(), request, authority); err == nil {
				t.Fatal("ungranted or expired execution succeeded")
			}
			factoryHostExecAssertAbsent(t, filepath.Join(request.Invocation.Dir, "child-started"))
			factoryHostExecAssertAbsent(t, request.ResultPath)
			entries, err := os.ReadDir(filepath.Dir(request.ResultPath))
			if err != nil || len(entries) != 0 {
				t.Fatalf("pre-GO mutable work: entries=%v err=%v", entries, err)
			}
		})
	}
}

func testFactoryHostExecLiteral(t *testing.T) {
	request := factoryHostExecFixtureRequest(t, "literal")
	literal := []string{"$(touch forbidden)", "a b", "&<>*", "--option=literal"}
	request.Invocation.Args = append(request.Invocation.Args, literal...)
	request.Invocation.Stdin = []byte("literal input\x00\n")
	request.Invocation.Env = append(request.Invocation.Env, "FACTORY_LITERAL=exact value")
	authority := &factoryHostExecTestAuthority{deadline: request.Invocation.Deadline, grant: true}
	if err := runFactoryHostExec(t.Context(), request, authority); err != nil {
		t.Fatal(err)
	}
	pending := readFactoryHostExecPending(t, request.ResultPath)
	var got struct {
		Args            []string
		Dir, Stdin, Env string
		PGID            int
	}
	if err := json.Unmarshal(pending.Result.Stdout, &got); err != nil {
		t.Fatalf("decode literal output %q: %v", pending.Result.Stdout, err)
	}
	pgid, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Args, literal) || got.Dir != request.Invocation.Dir || got.Stdin != string(request.Invocation.Stdin) || got.Env != "exact value" || got.PGID != pgid {
		t.Fatalf("invocation changed: %+v (parent pgid %d)", got, pgid)
	}
	if pending.Scope != request.Invocation.Scope || pending.RequestSHA256 != factoryHostExecTestDigest(t, request) || pending.Result.ExitCode != 0 || pending.Error != "" || authority.quiescentCalls != 0 {
		t.Fatalf("pending receipt lost binding or claimed retirement: %+v authority=%+v", pending, authority)
	}
	factoryHostExecAssertAbsent(t, filepath.Join(request.Invocation.Dir, "result.pending.json"))
}

func testFactoryHostExecBounds(t *testing.T) {
	for _, mode := range []string{"stdout", "stderr", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			request := factoryHostExecFixtureRequest(t, mode)
			request.Invocation.OutputLimit = 256
			if mode == "timeout" {
				request.Invocation.Deadline = time.Now().Add(180 * time.Millisecond)
			}
			authority := &factoryHostExecTestAuthority{deadline: request.Invocation.Deadline, grant: true}
			started := time.Now()
			err := runFactoryHostExec(t.Context(), request, authority)
			if err == nil || time.Since(started) > 3*time.Second {
				t.Fatalf("unbounded or successful failure: elapsed=%s err=%v", time.Since(started), err)
			}
			if mode == "timeout" {
				factoryHostExecAssertAbsent(t, request.ResultPath)
				return
			}
			pending := readFactoryHostExecPending(t, request.ResultPath)
			if len(pending.Result.Stdout) > 256 || len(pending.Result.Stderr) > 256 || pending.Error == "" {
				t.Fatalf("unbounded or unmarked failed result: %+v", pending)
			}
			stat, statErr := os.Stat(request.ResultPath)
			if statErr != nil || stat.Size() > 4*request.Invocation.OutputLimit+factoryHostExecMetadataLimit {
				t.Fatalf("serialized result is unbounded: stat=%v err=%v", stat, statErr)
			}
		})
	}
}

func testFactoryHostExecPipes(t *testing.T) {
	for _, mode := range []string{"pipes", "pipes7", "stdout-pipe7", "stderr-pipe7"} {
		t.Run(mode, func(t *testing.T) {
			request := factoryHostExecFixtureRequest(t, mode)
			authority := &factoryHostExecTestAuthority{deadline: request.Invocation.Deadline, grant: true}
			started := time.Now()
			if err := runFactoryHostExec(t.Context(), request, authority); err == nil {
				t.Fatal("retained output pipe was reported as complete")
			}
			if time.Since(started) > 900*time.Millisecond {
				t.Fatal("descendant retained the wrapper beyond the fixed pipe wait bound")
			}
			pending := readFactoryHostExecPending(t, request.ResultPath)
			if pending.Error == "" || authority.quiescentCalls != 0 {
				t.Fatal("wrapper accepted result or certified descendant retirement")
			}
			if strings.HasSuffix(mode, "7") && pending.Result.ExitCode != 7 {
				t.Fatalf("exit7 lost while reporting capture failure: %+v", pending)
			}
		})
	}
}

func testFactoryHostExecInvalid(t *testing.T) {
	for _, change := range []func(*factoryHostExecRequest){
		func(r *factoryHostExecRequest) { r.Invocation.OutputLimit = 0 },
		func(r *factoryHostExecRequest) { r.Invocation.Path = "relative" },
		func(r *factoryHostExecRequest) { r.Invocation.Args = []string{"nul\x00"} },
		func(r *factoryHostExecRequest) { r.Invocation.Env = []string{"INVALID"} },
		func(r *factoryHostExecRequest) { r.Invocation.Deadline = time.Now().Add(-time.Second) },
		func(r *factoryHostExecRequest) { r.ResultPath = filepath.Join(r.Invocation.Dir, "other.json") },
		func(r *factoryHostExecRequest) { r.Invocation.Scope.Nonce = "wrong" },
		func(r *factoryHostExecRequest) { r.Invocation.Stdin = make([]byte, factoryHostExecRequestLimit) },
	} {
		request := factoryHostExecFixtureRequest(t, "literal")
		change(&request)
		authority := &factoryHostExecTestAuthority{deadline: request.Invocation.Deadline, grant: true}
		if err := runFactoryHostExec(t.Context(), request, authority); err == nil || authority.helloCalls != 0 {
			t.Fatalf("invalid input reached authority: err=%v calls=%d", err, authority.helloCalls)
		}
		factoryHostExecAssertAbsent(t, filepath.Join(request.Invocation.Dir, "child-started"))
	}
}

func testFactoryHostExecReplay(t *testing.T) {
	request := factoryHostExecFixtureRequest(t, "literal")
	authority := &factoryHostExecTestAuthority{deadline: request.Invocation.Deadline, grant: true}
	if err := runFactoryHostExec(t.Context(), request, authority); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(request.ResultPath)
	if err := os.Remove(filepath.Join(request.Invocation.Dir, "child-started")); err != nil {
		t.Fatal(err)
	}
	if err := runFactoryHostExec(t.Context(), request, authority); err == nil {
		t.Fatal("replayed scope executed")
	}
	factoryHostExecAssertAbsent(t, filepath.Join(request.Invocation.Dir, "child-started"))
	after, _ := os.ReadFile(request.ResultPath)
	if !bytes.Equal(before, after) {
		t.Fatal("existing pending result overwritten")
	}
	t.Run("incomplete invocation is not replayable", func(t *testing.T) {
		r := factoryHostExecFixtureRequest(t, "literal")
		if err := os.WriteFile(r.ResultPath+".inflight", nil, 0o600); err != nil {
			t.Fatal(err)
		}
		a := &factoryHostExecTestAuthority{deadline: r.Invocation.Deadline, grant: true}
		if err := runFactoryHostExec(t.Context(), r, a); err == nil {
			t.Fatal("inflight scope replayed")
		}
		factoryHostExecAssertAbsent(t, filepath.Join(r.Invocation.Dir, "child-started"))
	})
}

func testFactoryHostExecCLI(t *testing.T) {
	for _, kind := range []string{"digest", "noncanonical", "unknown", "duplicate", "oversized", "destination", "deadline", "output", "symlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			request := factoryHostExecFixtureRequest(t, "literal")
			path := filepath.Join(filepath.Dir(request.ResultPath), "request.json")
			if kind == "destination" {
				request.ResultPath = filepath.Join(request.Invocation.Dir, "result.pending.json")
			}
			data, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "noncanonical":
				data = append(data, '\n')
			case "unknown":
				data = append([]byte(`{"unknown":1,`), data[1:]...)
			case "duplicate":
				data = append([]byte(`{"result_path":"discarded",`), data[1:]...)
			case "oversized":
				data = append(data, bytes.Repeat([]byte(" "), factoryHostExecRequestLimit)...)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if kind == "symlink" {
				other := path + ".real"
				if err := os.Rename(path, other); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, path); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "fifo" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			hash := sha256.Sum256(data)
			digest := hex.EncodeToString(hash[:])
			deadline := request.Invocation.Deadline.UTC().Format(time.RFC3339Nano)
			limit := strconv.FormatInt(request.Invocation.OutputLimit, 10)
			if kind == "digest" {
				digest = strings.Repeat("0", 64)
			}
			if kind == "deadline" {
				deadline = request.Invocation.Deadline.Add(time.Second).UTC().Format(time.RFC3339Nano)
			}
			if kind == "output" {
				limit = "9999"
			}
			command := newFactoryCmd()
			command.SetArgs([]string{"_host-exec", "--request", path, "--request-sha256", digest, "--socket", "/private/tmp/absent-factory.sock", "--deadline", deadline, "--output-limit", limit})
			command.SetOut(io.Discard)
			command.SetErr(io.Discard)
			if err := command.ExecuteContext(t.Context()); err == nil || strings.Contains(err.Error(), "connect factory authority") {
				t.Fatalf("invalid envelope reached socket or succeeded: %v", err)
			}
			factoryHostExecAssertAbsent(t, filepath.Join(request.Invocation.Dir, "child-started"))
			factoryHostExecAssertAbsent(t, request.ResultPath)
		})
	}
}

func testFactoryHostExecSocket(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("production peer credentials are qualified on Darwin")
	}
	for _, kind := range []string{"valid", "deny", "scope", "digest", "deadline", "peer", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			request := factoryHostExecFixtureRequest(t, "literal")
			digest := factoryHostExecTestDigest(t, request)
			conn, server := factoryHostExecSocketPair(t)
			done := make(chan error, 1)
			go func() {
				var hello factoryHostExecHello
				if err := json.NewDecoder(server).Decode(&hello); err != nil {
					done <- err
					return
				}
				response := factoryHostExecGo{Version: 1, Operation: "GO", RequestSHA256: digest, Scope: request.Invocation.Scope, PID: hello.PID, PGID: hello.PGID, Deadline: request.Invocation.Deadline}
				switch kind {
				case "deny":
					response.Operation = "DENY"
				case "scope":
					response.Scope.Generation++
				case "digest":
					response.RequestSHA256 = strings.Repeat("0", 64)
				case "deadline":
					response.Deadline = response.Deadline.Add(time.Second)
				case "peer":
					response.PID++
				case "oversized":
					response.Operation = strings.Repeat("X", factoryHostExecMetadataLimit)
				}
				done <- json.NewEncoder(server).Encode(response)
			}()
			authority := &factoryHostSocketAuthority{conn: conn, request: request, digest: digest}
			err := runFactoryHostExec(t.Context(), request, authority)
			_ = conn.Close()
			if serverErr := <-done; serverErr != nil && err == nil {
				t.Fatalf("server handshake: %v", serverErr)
			}
			if err := authority.MarkQuiescent(t.Context(), request.Invocation.Scope); err == nil {
				t.Fatal("wrapper can certify retirement")
			}
			if kind != "valid" {
				if err == nil {
					t.Fatal("invalid GO executed")
				}
				factoryHostExecAssertAbsent(t, request.ResultPath)
				factoryHostExecAssertAbsent(t, filepath.Join(request.Invocation.Dir, "child-started"))
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if granted, grantErr := authority.Granted(t.Context(), request.Invocation.Scope); granted || grantErr == nil {
				t.Fatal("GO replayed")
			}
			readFactoryHostExecPending(t, request.ResultPath)
		})
	}
}

func factoryHostExecSocketPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	dir, err := os.MkdirTemp("/private/tmp", "fhe-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "socket"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	client, err := net.DialUnix("unix", nil, listener.Addr().(*net.UnixAddr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	server, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return client, server
}

type factoryHostExecTestAuthority struct {
	deadline                   time.Time
	grant                      bool
	helloError                 error
	grantDelay                 time.Duration
	helloCalls, quiescentCalls int
	pid, pgid                  int
}

func (a *factoryHostExecTestAuthority) RecordHello(_ context.Context, _ factory.ScopeHandle, pid, pgid int) error {
	a.helloCalls++
	a.pid, a.pgid = pid, pgid
	return a.helloError
}

func (a *factoryHostExecTestAuthority) Granted(context.Context, factory.ScopeHandle) (bool, error) {
	time.Sleep(a.grantDelay)
	return a.grant, nil
}

func (*factoryHostExecTestAuthority) CleanupPermit(context.Context, factory.ScopeHandle) (factory.DockerCleanupPermit, error) {
	return factory.DockerCleanupPermit{}, errors.New("test handshake authority does not supply cleanup permits")
}

func (a *factoryHostExecTestAuthority) Observe(context.Context, factory.ScopeHandle) (factory.ScopeObservation, error) {
	return factory.ScopeObservation{Phase: "granted", PID: a.pid, PGID: a.pgid, Deadline: a.deadline}, nil
}

func (a *factoryHostExecTestAuthority) MarkQuiescent(context.Context, factory.ScopeHandle) error {
	a.quiescentCalls++
	return nil
}
func (a *factoryHostExecTestAuthority) Close() error { return nil }

func factoryHostExecFixtureRequest(t *testing.T, mode string) factoryHostExecRequest {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	coverageDir := filepath.Join(dir, "coverage")
	if err := os.Mkdir(coverageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	privateDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(privateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	id, nonce := strings.Repeat("a", 32), strings.Repeat("b", 32)
	return factoryHostExecRequest{ResultPath: filepath.Join(privateDir, "result.pending.json"), Invocation: factory.HostInvocation{
		Scope: factory.ScopeHandle{ID: id, Nonce: nonce, Kind: "host", Backend: "launchd", Generation: 1, Name: "dev.getoro.factory.0123456789abcdef.host." + id + "." + nonce},
		Path:  executable, Args: []string{"-test.run=^TestFactoryHostExecFixture$", "--"},
		Env: []string{"FACTORY_HOST_EXEC_FIXTURE=" + mode, "GORACE=atexit_sleep_ms=0", "TMPDIR=" + dir, "GOCOVERDIR=" + coverageDir}, Dir: dir,
		Deadline: time.Now().Add(5 * time.Second).UTC(), OutputLimit: 4096,
	}}
}

func factoryHostExecTestDigest(t *testing.T, request factoryHostExecRequest) string {
	t.Helper()
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func readFactoryHostExecPending(t *testing.T, path string) factoryHostExecPending {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var pending factoryHostExecPending
	if err := json.Unmarshal(data, &pending); err != nil {
		t.Fatalf("non-atomic pending JSON: %v", err)
	}
	return pending
}

func factoryHostExecAssertAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected effect at %s: %v", path, err)
	}
}

// Each fixture owns only its temporary cwd; descendants exit on a fixed timer.
func TestFactoryHostExecFixture(t *testing.T) {
	mode := os.Getenv("FACTORY_HOST_EXEC_FIXTURE")
	if mode == "" {
		return
	}
	if err := os.WriteFile("child-started", []byte("started"), 0o600); err != nil {
		os.Exit(90)
	}
	switch mode {
	case "literal":
		input, _ := io.ReadAll(os.Stdin)
		cwd, _ := os.Getwd()
		pgid, _ := syscall.Getpgid(os.Getpid())
		args := os.Args[3:]
		_ = json.NewEncoder(os.Stdout).Encode(struct {
			Args            []string
			Dir, Stdin, Env string
			PGID            int
		}{args, cwd, string(input), os.Getenv("FACTORY_LITERAL"), pgid})
	case "stdout", "stderr":
		output := os.Stdout
		if mode == "stderr" {
			output = os.Stderr
		}
		_, _ = fmt.Fprint(output, strings.Repeat("x", 8192))
		time.Sleep(5 * time.Second)
	case "timeout":
		time.Sleep(5 * time.Second)
	case "pipes", "pipes7", "stdout-pipe7", "stderr-pipe7", "stdin-pipe7":
		child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestFactoryHostExecFixture$", "--")
		child.Env = []string{"FACTORY_HOST_EXEC_FIXTURE=hold", "GORACE=atexit_sleep_ms=0", "TMPDIR=" + os.Getenv("TMPDIR"), "GOCOVERDIR=" + os.Getenv("GOCOVERDIR")}
		if mode != "stderr-pipe7" && mode != "stdin-pipe7" {
			child.Stdout = os.Stdout
		}
		if mode != "stdout-pipe7" && mode != "stdin-pipe7" {
			child.Stderr = os.Stderr
		}
		if mode == "stdin-pipe7" {
			child.Stdin = os.Stdin
		}
		if err := child.Start(); err != nil {
			os.Exit(91)
		}
		if strings.HasSuffix(mode, "7") {
			_, _ = fmt.Fprint(os.Stdout, "expected stdout")
			_, _ = fmt.Fprint(os.Stderr, "expected stderr")
			os.Exit(7)
		}
	case "hold":
		time.Sleep(time.Second)
	case "exit":
		_, _ = fmt.Fprint(os.Stdout, "expected stdout")
		_, _ = fmt.Fprint(os.Stderr, "expected stderr")
		os.Exit(7)
	case "signal":
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		time.Sleep(time.Second)
	default:
		os.Exit(92)
	}
	os.Exit(0)
}

func testFactoryHostExecSerialization(t *testing.T) {
	request := factoryHostExecFixtureRequest(t, "exit")
	request.Invocation.OutputLimit = 1
	file, err := reserveFactoryHostResult(t.Context(), request.ResultPath, request.Invocation.Deadline)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	pending := factoryHostExecPending{
		Version: 1, RequestSHA256: factoryHostExecTestDigest(t, request), Scope: request.Invocation.Scope,
		Deadline: request.Invocation.Deadline, OutputLimit: request.Invocation.OutputLimit,
		Result: factory.HostResult{Stdout: []byte{0}, Stderr: []byte{255}, ExitCode: -1},
		Error:  strings.Repeat("\x00", factoryHostExecErrorLimit),
	}
	if err := publishFactoryHostResult(t.Context(), file, request.ResultPath, pending); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(request.ResultPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 6*factoryHostExecErrorLimit || int64(len(data)) > 4*pending.OutputLimit+factoryHostExecMetadataLimit {
		t.Fatalf("escaping lost content or exceeded reservation: %d bytes", len(data))
	}
	if got := readFactoryHostExecPending(t, request.ResultPath); got.Error != pending.Error {
		t.Fatal("escaped error changed")
	}
}

func testFactoryHostExecPermanentBarrier(t *testing.T) {
	request := factoryHostExecFixtureRequest(t, "literal")
	authority := &factoryHostExecTestAuthority{deadline: request.Invocation.Deadline, grant: true}
	if err := runFactoryHostExec(t.Context(), request, authority); err != nil {
		t.Fatal(err)
	}
	inflight, err := os.Stat(request.ResultPath + ".inflight")
	if err != nil {
		t.Fatalf("publication removed the exclusive execution barrier: %v", err)
	}
	pending, err := os.Stat(request.ResultPath)
	if err != nil || !os.SameFile(inflight, pending) {
		t.Fatalf("permanent barrier must share the published inode: %v", err)
	}
	// A delayed reservation may have observed the pending name before Link.
	// The exclusive inflight name must still deny it after publication finishes.
	if err := os.Remove(request.ResultPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(request.Invocation.Dir, "child-started")); err != nil {
		t.Fatal(err)
	}
	if err := runFactoryHostExec(t.Context(), request, authority); err == nil {
		t.Fatal("a stale missing-pending observation replayed the scope")
	}
	factoryHostExecAssertAbsent(t, filepath.Join(request.Invocation.Dir, "child-started"))
}

func testFactoryHostExecFinalSync(t *testing.T) {
	for _, mode := range []string{"cancellation", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			request := factoryHostExecFixtureRequest(t, "literal")
			if mode == "deadline" {
				request.Invocation.Deadline = time.Now().Add(100 * time.Millisecond)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			file, err := reserveFactoryHostResult(ctx, request.ResultPath, request.Invocation.Deadline)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			pending := factoryHostExecPending{
				Version: 1, RequestSHA256: factoryHostExecTestDigest(t, request), Scope: request.Invocation.Scope,
				Deadline: request.Invocation.Deadline, OutputLimit: request.Invocation.OutputLimit,
				Result: factory.HostResult{ExitCode: 0},
			}
			synced := false
			err = publishFactoryHostResultWithSync(ctx, file, request.ResultPath, pending, func(path string) error {
				if err := syncFactoryHostDirectory(path); err != nil {
					return err
				}
				synced = true
				if mode == "cancellation" {
					cancel()
				} else {
					time.Sleep(time.Until(request.Invocation.Deadline) + time.Millisecond)
				}
				return nil
			})
			if err == nil || !synced {
				t.Fatalf("publication reported success after final sync crossed its authority: synced=%v err=%v", synced, err)
			}
		})
	}
}

func testFactoryHostExecSilentAuthority(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("production peer credentials are qualified on Darwin")
	}
	request := factoryHostExecFixtureRequest(t, "literal")
	request.Invocation.Deadline = time.Now().Add(150 * time.Millisecond)
	client, _ := factoryHostExecSocketPair(t)
	authority := &factoryHostSocketAuthority{conn: client, request: request, digest: factoryHostExecTestDigest(t, request)}
	started := time.Now()
	if err := runFactoryHostExec(t.Context(), request, authority); err == nil {
		t.Fatal("silent authority granted work")
	}
	if time.Since(started) > time.Second {
		t.Fatal("silent authority exceeded deadline")
	}
	factoryHostExecAssertAbsent(t, request.ResultPath)
	factoryHostExecAssertAbsent(t, filepath.Join(request.Invocation.Dir, "child-started"))
}

func testFactoryHostExecCLIAllowed(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("production peer credentials are qualified on Darwin")
	}
	for _, mode := range []string{"literal", "exit", "signal", "stdout", "timeout", "lost publication", "pipes7", "stdout-pipe7", "stderr-pipe7", "stdin-pipe7"} {
		t.Run(mode, func(t *testing.T) { factoryHostExecCLIPublication(t, mode) })
	}
}

func factoryHostExecCLIPublication(t *testing.T, mode string) {
	t.Helper()
	fixtureMode := mode
	if mode == "lost publication" {
		fixtureMode = "literal"
	}
	request := factoryHostExecFixtureRequest(t, fixtureMode)
	if mode == "exit" || mode == "stdin-pipe7" {
		request.Invocation.Stdin = bytes.Repeat([]byte("x"), 1<<20)
	}
	if mode == "stdout" {
		request.Invocation.OutputLimit = 256
	}
	if mode == "timeout" {
		request.Invocation.Deadline = time.Now().Add(150 * time.Millisecond)
	}
	request.Invocation.Scope.Kind = "observer"
	request.Invocation.Scope.Name = strings.Replace(request.Invocation.Scope.Name, ".host.", ".observer.", 1)
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(request.ResultPath), "request.json")
	if err := os.WriteFile(path, data, 0o400); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("/private/tmp", "fhc-")
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
	if err := listener.SetDeadline(request.Invocation.Deadline); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- serveFactoryHostExecTestHello(listener, request, factoryHostDigest(data), mode) }()
	command := newFactoryCmd()
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{
		"_host-exec", "--request", path, "--request-sha256", factoryHostDigest(data), "--socket", socket,
		"--deadline", request.Invocation.Deadline.UTC().Format(time.RFC3339Nano), "--output-limit", strconv.FormatInt(request.Invocation.OutputLimit, 10),
	})
	wantSuccess := mode == "literal" || mode == "exit"
	if err := command.ExecuteContext(t.Context()); (err == nil) != wantSuccess {
		t.Fatalf("CLI success=%v want=%v: %v", err == nil, wantSuccess, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !wantSuccess {
		return
	}
	wantExit := 0
	if mode == "exit" {
		wantExit = 7
	}
	pending := readFactoryHostExecPending(t, request.ResultPath)
	if pending.RequestSHA256 != factoryHostDigest(data) || pending.Result.ExitCode != wantExit || pending.Error != "" || pending.Scope.Kind != "observer" {
		t.Fatalf("unbound CLI result: %+v", pending)
	}
}

func serveFactoryHostExecTestHello(listener *net.UnixListener, request factoryHostExecRequest, digest, mode string) error {
	connection, err := listener.AcceptUnix()
	if err != nil {
		return err
	}
	defer connection.Close()
	if err := connection.SetDeadline(request.Invocation.Deadline); err != nil {
		return err
	}
	var hello factoryHostExecHello
	if err := json.NewDecoder(connection).Decode(&hello); err != nil {
		return err
	}
	pid, uid, err := factoryHostPeer(connection)
	if err != nil {
		return err
	}
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		return err
	}
	if hello.PID != pid || uid != os.Getuid() || hello.PGID != pgid || hello.Scope != request.Invocation.Scope || hello.RequestSHA256 != digest {
		return errors.New("HELLO did not match kernel or frozen request")
	}
	if _, err := os.Lstat(request.ResultPath); !errors.Is(err, os.ErrNotExist) {
		return errors.New("pending result exists before GO")
	}
	if _, err := os.Lstat(filepath.Join(request.Invocation.Dir, "child-started")); !errors.Is(err, os.ErrNotExist) {
		return errors.New("child exists before GO")
	}
	if err := json.NewEncoder(connection).Encode(factoryHostExecGo{Version: 1, Operation: "GO", RequestSHA256: digest, Scope: request.Invocation.Scope, PID: pid, PGID: pgid, Deadline: request.Invocation.Deadline}); err != nil {
		return err
	}
	if mode == "lost publication" {
		return nil
	}
	if mode != "literal" && mode != "exit" {
		data, _ := io.ReadAll(io.LimitReader(connection, factoryHostExecWireLimit+1))
		if len(data) != 0 {
			return fmt.Errorf("failed invocation emitted completion bytes: %q", data)
		}
		return nil
	}
	return readFactoryHostExecTestPublished(connection, request, digest, pid, pgid)
}

func testFactoryHostExecExpectedExit(t *testing.T) {
	request := factoryHostExecFixtureRequest(t, "exit")
	authority := &factoryHostExecTestAuthority{deadline: request.Invocation.Deadline, grant: true}
	if err := runFactoryHostExec(t.Context(), request, authority); err != nil {
		t.Fatalf("ordinary exit7 was rejected as infrastructure failure: %v", err)
	}
	pending := readFactoryHostExecPending(t, request.ResultPath)
	if pending.Result.ExitCode != 7 || pending.Error != "" || string(pending.Result.Stdout) != "expected stdout" || string(pending.Result.Stderr) != "expected stderr" {
		t.Fatalf("normal nonzero result changed: %+v", pending)
	}
}

func readFactoryHostExecTestPublished(connection *net.UnixConn, request factoryHostExecRequest, digest string, pid, pgid int) error {
	var published struct {
		Version       int                 `json:"version"`
		Operation     string              `json:"operation"`
		RequestSHA256 string              `json:"request_sha256"`
		Scope         factory.ScopeHandle `json:"scope"`
		PID           int                 `json:"pid"`
		PGID          int                 `json:"pgid"`
		Deadline      time.Time           `json:"deadline"`
		PendingSHA256 string              `json:"pending_sha256"`
	}
	if err := json.NewDecoder(io.LimitReader(connection, factoryHostExecWireLimit)).Decode(&published); err != nil {
		return fmt.Errorf("missing bounded PUBLISHED message: %w", err)
	}
	if !time.Now().Before(request.Invocation.Deadline) {
		return errors.New("PUBLISHED arrived after immutable deadline")
	}
	data, err := os.ReadFile(request.ResultPath)
	if err != nil {
		return err
	}
	if published.Version != 1 || published.Operation != "PUBLISHED" || published.RequestSHA256 != digest || published.Scope != request.Invocation.Scope || published.PID != pid || published.PGID != pgid || !published.Deadline.Equal(request.Invocation.Deadline) || published.PendingSHA256 != factoryHostDigest(data) {
		return fmt.Errorf("PUBLISHED lost scope, identity, deadline or byte binding: %+v", published)
	}
	return nil
}

func testFactoryHostExecPendingHash(t *testing.T) {
	for _, mode := range []string{"valid", "oversized", "permissions", "symlink", "fifo", "canceled", "expired"} {
		t.Run(mode, func(t *testing.T) {
			request := factoryHostExecFixtureRequest(t, "literal")
			request.Invocation.OutputLimit = 1
			data := []byte("pending bytes")
			if mode == "oversized" {
				data = bytes.Repeat([]byte("x"), factoryHostExecMetadataLimit+5)
			}
			if err := os.WriteFile(request.ResultPath, data, 0o600); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "permissions":
				if err := os.Chmod(request.ResultPath, 0o644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(request.ResultPath, request.ResultPath+".real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(request.ResultPath+".real", request.ResultPath); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := os.Remove(request.ResultPath); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Mkfifo(request.ResultPath, 0o600); err != nil {
					t.Fatal(err)
				}
			case "expired":
				request.Invocation.Deadline = time.Now().Add(-time.Second)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			got, err := factoryHostPendingDigest(ctx, request)
			if mode == "valid" {
				if err != nil || got != factoryHostDigest(data) {
					t.Fatalf("pending hash = %s err=%v", got, err)
				}
				return
			}
			if err == nil || got != "" {
				t.Fatalf("unsafe pending file produced digest=%s err=%v", got, err)
			}
		})
	}
}

func testFactoryHostExecOnePublished(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("production peer credentials are qualified on Darwin")
	}
	request := factoryHostExecFixtureRequest(t, "literal")
	digest := factoryHostExecTestDigest(t, request)
	client, server := factoryHostExecSocketPair(t)
	if err := server.SetDeadline(request.Invocation.Deadline); err != nil {
		t.Fatal(err)
	}
	authority := &factoryHostSocketAuthority{conn: client, request: request, digest: digest}
	if err := authority.Published(t.Context()); err == nil {
		t.Fatal("publication confirmed before GO")
	}
	done := make(chan error, 1)
	go func() {
		var hello factoryHostExecHello
		if err := json.NewDecoder(server).Decode(&hello); err != nil {
			done <- err
			return
		}
		response := factoryHostExecGo{Version: 1, Operation: "GO", RequestSHA256: digest, Scope: request.Invocation.Scope, PID: hello.PID, PGID: hello.PGID, Deadline: request.Invocation.Deadline}
		if err := json.NewEncoder(server).Encode(response); err != nil {
			done <- err
			return
		}
		if err := readFactoryHostExecTestPublished(server, request, digest, hello.PID, hello.PGID); err != nil {
			done <- err
			return
		}
		extra, err := io.ReadAll(io.LimitReader(server, factoryHostExecWireLimit))
		if err == nil && len(extra) != 0 {
			err = errors.New("more than one PUBLISHED frame arrived")
		}
		done <- err
	}()
	if err := runFactoryHostExec(t.Context(), request, authority); err != nil {
		t.Fatal(err)
	}
	if err := authority.Published(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := authority.Published(t.Context()); err == nil {
		t.Fatal("PUBLISHED was replayable")
	}
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func testFactoryHostExecInputPipes(t *testing.T) {
	for _, mode := range []string{"exit", "stdin-pipe7"} {
		t.Run(mode, func(t *testing.T) {
			request := factoryHostExecFixtureRequest(t, mode)
			request.Invocation.Stdin = bytes.Repeat([]byte("x"), 1<<20)
			authority := &factoryHostExecTestAuthority{deadline: request.Invocation.Deadline, grant: true}
			started := time.Now()
			err := runFactoryHostExec(t.Context(), request, authority)
			if (err == nil) != (mode == "exit") {
				t.Fatalf("early exit versus retained stdin has wrong outcome: %v", err)
			}
			if time.Since(started) > 900*time.Millisecond {
				t.Fatal("retained stdin exceeded the fixed I/O drain bound")
			}
			pending := readFactoryHostExecPending(t, request.ResultPath)
			if pending.Result.ExitCode != 7 || (pending.Error == "") != (mode == "exit") {
				t.Fatalf("stdin completion is not explicit in pending result: %+v", pending)
			}
		})
	}
}
