package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"oro/pkg/factory"
)

func TestFactoryHostScope(t *testing.T) {
	t.Run("trusted config and exact observer command", testFactoryScopeConfig)
	t.Run("complete timely canonical publication", testFactoryScopePublication)
	t.Run("pending bytes remain bound after retirement", testFactoryScopePending)
	t.Run("retirement requires positive ESRCH", testFactoryScopeProbe)
	testFactoryHostScopePlatform(t)
}

func testFactoryScopePending(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	request := factoryHostExecRequest{Invocation: factory.HostInvocation{Scope: factory.ScopeHandle{ID: "scope"}, Deadline: time.Now().UTC().Add(time.Minute), OutputLimit: 32}, ResultPath: filepath.Join(dir, "result.pending.json")}
	publication := factoryHostExecPublished{RequestSHA256: strings.Repeat("a", 64)}
	pending := factoryHostExecPending{Version: 1, RequestSHA256: publication.RequestSHA256, Scope: request.Invocation.Scope, Deadline: request.Invocation.Deadline, OutputLimit: 32, Result: factory.HostResult{Stdout: []byte("exact\x00output"), ExitCode: 7}}
	write := func(value factoryHostExecPending) {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		publication.PendingSHA256 = factoryHostDigest(data)
		if err := os.WriteFile(request.ResultPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(pending)
	result, err := readFactoryRetiredResult(request, publication)
	if err != nil || !reflect.DeepEqual(result, pending.Result) {
		t.Fatalf("retired exact bytes changed: %+v %v", result, err)
	}
	for name, change := range map[string]func(*factoryHostExecPending){
		"request digest": func(p *factoryHostExecPending) { p.RequestSHA256 = strings.Repeat("c", 64) },
		"scope":          func(p *factoryHostExecPending) { p.Scope.Nonce = "different" },
		"deadline":       func(p *factoryHostExecPending) { p.Deadline = p.Deadline.Add(time.Second) },
		"output bound":   func(p *factoryHostExecPending) { p.OutputLimit++ },
		"failure":        func(p *factoryHostExecPending) { p.Error = "failed" },
		"stdout":         func(p *factoryHostExecPending) { p.Result.Stdout = bytes.Repeat([]byte("x"), 33) },
		"signal":         func(p *factoryHostExecPending) { p.Result.ExitCode = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := pending
			change(&changed)
			write(changed)
			if _, err := readFactoryRetiredResult(request, publication); err == nil {
				t.Fatal("mismatched pending result accepted")
			}
		})
	}
	write(pending)
	publication.PendingSHA256 = strings.Repeat("f", 64)
	if _, err := readFactoryRetiredResult(request, publication); err == nil {
		t.Fatal("pending bytes bypassed authenticated hash")
	}
	if err := os.Chmod(request.ResultPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readFactoryRetiredResult(request, publication); err == nil {
		t.Fatal("public pending result accepted")
	}
	originalPath := request.ResultPath
	request.ResultPath = filepath.Join(dir, "result-link.json")
	if err := os.Symlink(originalPath, request.ResultPath); err != nil {
		t.Fatal(err)
	}
	if _, err := readFactoryRetiredResult(request, publication); err == nil {
		t.Fatal("symlink result accepted")
	}
	request.ResultPath = filepath.Join(dir, "result-fifo.json")
	if err := syscall.Mkfifo(request.ResultPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readFactoryRetiredResult(request, publication); err == nil {
		t.Fatal("FIFO result accepted")
	}
}

func factoryScopeConfigFixture(t *testing.T) factoryLaunchdConfig {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	program, err := filepath.EvalSymlinks("/bin/echo")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(program)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	return factoryLaunchdConfig{
		StateRoot: root, ObjectiveID: "scope-fixture", OroPath: program,
		OroSHA256: hex.EncodeToString(digest[:]), GitPath: program, GitSHA256: hex.EncodeToString(digest[:]),
		Repository: root, TargetRef: "refs/heads/factory-proof", CleanupTimeout: time.Second, HardStop: time.Now().Add(time.Hour),
	}
}

func testFactoryScopeConfig(t *testing.T) {
	cfg := factoryScopeConfigFixture(t)
	if err := validateFactoryLaunchdConfig(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256([]byte(cfg.StateRoot + "\x00" + cfg.ObjectiveID))
	if factoryLaunchdObjectiveHash(cfg) != hex.EncodeToString(expected[:8]) {
		t.Fatal("namespace is not bound to canonical root and objective")
	}
	for name, change := range map[string]func(*factoryLaunchdConfig){
		"root escape":      func(c *factoryLaunchdConfig) { c.StateRoot += "/../elsewhere" },
		"objective escape": func(c *factoryLaunchdConfig) { c.ObjectiveID = "../other" },
		"wrong binary":     func(c *factoryLaunchdConfig) { c.OroSHA256 = strings.Repeat("0", 64) },
		"wrong git":        func(c *factoryLaunchdConfig) { c.GitSHA256 = strings.Repeat("0", 64) },
		"unsafe target":    func(c *factoryLaunchdConfig) { c.TargetRef = "refs/heads/../other" },
		"no cleanup":       func(c *factoryLaunchdConfig) { c.CleanupTimeout = 0 },
		"no hard stop":     func(c *factoryLaunchdConfig) { c.HardStop = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			changed := cfg
			change(&changed)
			if validateFactoryLaunchdConfig(t.Context(), changed) == nil {
				t.Fatal("unsafe config accepted")
			}
		})
	}
	inv := factory.HostInvocation{Path: cfg.GitPath, Dir: cfg.Repository, Args: factoryObserverArgs(cfg.TargetRef), Env: factoryObserverEnvironment(), OutputLimit: 256}
	if err := validateFactoryObserver(t.Context(), cfg, inv); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*factory.HostInvocation){
		func(i *factory.HostInvocation) { i.Args = append(i.Args, "other-ref") },
		func(i *factory.HostInvocation) { i.Args[len(i.Args)-1] = "refs/heads/other" },
		func(i *factory.HostInvocation) { i.Env = append(i.Env, "GIT_CONFIG_COUNT=1") },
		func(i *factory.HostInvocation) { i.Stdin = []byte("input") },
		func(i *factory.HostInvocation) { i.Dir = filepath.Dir(cfg.Repository) },
		func(i *factory.HostInvocation) { i.OutputLimit++ },
	} {
		changed := inv
		changed.Args = append([]string{}, inv.Args...)
		changed.Env = append([]string{}, inv.Env...)
		change(&changed)
		if validateFactoryObserver(t.Context(), cfg, changed) == nil {
			t.Fatal("observer gained arbitrary command authority")
		}
	}
	for _, entry := range inv.Env {
		if strings.HasPrefix(entry, "HOME=") || strings.HasPrefix(entry, "CODEX_HOME=") {
			t.Fatal("observer repurposed a home variable")
		}
	}
}

func testFactoryScopePublication(t *testing.T) {
	want := factoryHostExecPublished{Version: 1, Operation: "PUBLISHED", RequestSHA256: strings.Repeat("a", 64), Scope: factory.ScopeHandle{ID: "scope"}, PID: 42, PGID: 42, Deadline: time.Now().UTC().Add(time.Minute), PendingSHA256: strings.Repeat("b", 64)}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := receiveFactoryPublication(t.Context(), bytes.NewReader(append(data, '\n')), want)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("exact publication = %+v, %v", got, err)
	}
	for name, data := range map[string][]byte{
		"missing": nil, "partial": data, "oversize": []byte(strings.Repeat(" ", factoryHostExecWireLimit) + "\n"),
		"noncanonical": append([]byte(" "), append(data, '\n')...),
		"mismatch":     bytes.ReplaceAll(append(data, '\n'), []byte("PUBLISHED"), []byte("GO")),
		"duplicate":    append(append([]byte(`{"version":1,`), data[1:]...), '\n'),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := receiveFactoryPublication(t.Context(), bytes.NewReader(data), want); err == nil {
				t.Fatal("invalid publication accepted")
			}
		})
	}
	late := want
	late.Deadline = time.Now().Add(10 * time.Millisecond)
	lateData, _ := json.Marshal(late)
	reader := &factoryScopeDelayedReader{Reader: bytes.NewReader(append(lateData, '\n')), delay: 25 * time.Millisecond}
	if _, err := receiveFactoryPublication(t.Context(), reader, late); err == nil {
		t.Fatal("publication completed after immutable deadline")
	}
}

type factoryScopeDelayedReader struct {
	io.Reader
	delay time.Duration
}

func (r *factoryScopeDelayedReader) Read(p []byte) (int, error) {
	if r.delay > 0 {
		time.Sleep(r.delay)
		r.delay = 0
	}
	return r.Reader.Read(p)
}

func testFactoryScopeProbe(t *testing.T) {
	sequence := []error{syscall.EPERM, nil, syscall.ESRCH}
	calls := 0
	probe := func(group int) error {
		if group != 42 {
			t.Fatal("wrong group")
		}
		err := sequence[calls]
		calls++
		return err
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := waitFactoryGroupEmpty(ctx, 42, probe); err != nil || calls != 3 {
		t.Fatalf("transient group uncertainty = %v, calls=%d", err, calls)
	}
	ctx, cancel = context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if err := waitFactoryGroupEmpty(ctx, 42, func(int) error { return syscall.EPERM }); err == nil {
		t.Fatal("permission uncertainty certified quiescence")
	}
	if err := waitFactoryGroupEmpty(t.Context(), 0, probe); err == nil {
		t.Fatal("invalid group probed")
	}
}
