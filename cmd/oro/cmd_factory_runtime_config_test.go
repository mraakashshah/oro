package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestFactoryRuntimeConfig(t *testing.T) {
	t.Run("frozen private bundle after work deadline", testFactoryRuntimeConfigRead)
	t.Run("canonical identity and bounds", testFactoryRuntimeConfigInvalid)
	t.Run("exact bytes bind policy and deadlines", testFactoryRuntimeConfigBytes)
	t.Run("hostile snapshot and filesystem", testFactoryRuntimeConfigFiles)
	t.Run("read only and canceled", testFactoryRuntimeConfigReadOnly)
	t.Run("directory identity remains bound", testFactoryRuntimeConfigDirectorySwap)
	t.Run("owned unaliased file metadata", testFactoryRuntimeConfigOwnership)
	t.Run("bounded context metadata", testFactoryRuntimeConfigMetadataBound)
}

type factoryRuntimeConfigFixture struct {
	config   factoryRuntimeConfig
	path     string
	data     []byte
	metaPath string
}

func newFactoryRuntimeConfigFixture(t *testing.T) factoryRuntimeConfigFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	socketDir, err := os.MkdirTemp("/tmp", "oro-runtime-config-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketDir, err = filepath.EvalSymlinks(socketDir)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(socketDir, "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := os.Chmod(socket, 0o600); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(root, "admitted-program")
	programBytes := []byte("#!/bin/sh\nexit 73\n")
	if err := os.WriteFile(program, programBytes, 0o500); err != nil {
		t.Fatal(err)
	}
	deadline := time.Date(2025, 1, 2, 3, 4, 5, 123, time.UTC)
	config := factoryRuntimeConfig{
		Version: 1, StateRoot: root, ObjectiveID: "runtime-fixture", PolicyHash: strings.Repeat("a", 64),
		OroPath: program, OroSHA256: factoryHostDigest(programBytes), GitPath: program, GitSHA256: factoryHostDigest(programBytes),
		Repository: root, TargetRef: "refs/heads/factory-proof", Deadline: deadline, HardStop: deadline.Add(2 * time.Minute), CleanupTimeoutSeconds: 60,
		DockerPath: program, DockerSHA256: factoryHostDigest(programBytes), DockerContext: "dedicated-factory", DockerEndpoint: "unix://" + socket,
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, config.ObjectiveID, "runtime", "runtime.json")
	metaPath := filepath.Join(filepath.Dir(path), "docker", "contexts", "meta", factoryHostDigest([]byte(config.DockerContext)), "meta.json")
	if err := os.MkdirAll(filepath.Dir(metaPath), 0o700); err != nil {
		t.Fatal(err)
	}
	meta := []byte(`{"Name":"dedicated-factory","Metadata":{},"Endpoints":{"docker":{"Host":"` + config.DockerEndpoint + `","SkipTLSVerify":false}}}`)
	for path, value := range map[string][]byte{path: data, metaPath: meta, filepath.Join(filepath.Dir(path), "docker", "config.json"): []byte("{}")} {
		if err := os.WriteFile(path, value, 0o400); err != nil {
			t.Fatal(err)
		}
	}
	return factoryRuntimeConfigFixture{config: config, path: path, data: data, metaPath: metaPath}
}

func testFactoryRuntimeConfigRead(t *testing.T) {
	f := newFactoryRuntimeConfigFixture(t)
	t.Setenv("DOCKER_HOST", "unix:///missing")
	t.Setenv("DOCKER_CONTEXT", "default")
	t.Setenv("DOCKER_CONFIG", "/missing")
	got, err := readFactoryRuntimeConfig(t.Context(), f.path, factoryHostDigest(f.data))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.factoryRuntimeConfig, f.config) || got.RuntimeConfigPath != f.path || got.RuntimeConfigSHA256 != factoryHostDigest(f.data) || got.DockerConfig != filepath.Join(filepath.Dir(f.path), "docker") {
		t.Fatalf("frozen identities changed: %+v", got)
	}
	data, err := canonicalFactoryRuntimeConfig(f.config)
	if err != nil || string(data) != string(f.data) {
		t.Fatalf("noncanonical config: %s %v", data, err)
	}
	cli, meta, err := factoryDockerContextSnapshot(f.config)
	wantMeta, readErr := os.ReadFile(f.metaPath)
	if err != nil || readErr != nil || string(cli) != "{}" || string(meta) != string(wantMeta) {
		t.Fatalf("snapshot changed: %s %s %v %v", cli, meta, err, readErr)
	}
}

func testFactoryRuntimeConfigInvalid(t *testing.T) {
	f := newFactoryRuntimeConfigFixture(t)
	for name, mutate := range map[string]func(*factoryRuntimeConfig){
		"version":                   func(c *factoryRuntimeConfig) { c.Version = 2 },
		"root":                      func(c *factoryRuntimeConfig) { c.StateRoot += "/../elsewhere" },
		"objective":                 func(c *factoryRuntimeConfig) { c.ObjectiveID = "../escape" },
		"policy":                    func(c *factoryRuntimeConfig) { c.PolicyHash = strings.Repeat("A", 64) },
		"oro digest":                func(c *factoryRuntimeConfig) { c.OroSHA256 = "missing" },
		"git digest":                func(c *factoryRuntimeConfig) { c.GitSHA256 = "missing" },
		"docker digest":             func(c *factoryRuntimeConfig) { c.DockerSHA256 = "missing" },
		"oro path":                  func(c *factoryRuntimeConfig) { c.OroPath = "oro" },
		"git path":                  func(c *factoryRuntimeConfig) { c.GitPath = "/not/../canonical" },
		"docker path":               func(c *factoryRuntimeConfig) { c.DockerPath = "docker" },
		"repository":                func(c *factoryRuntimeConfig) { c.Repository = "repo" },
		"target":                    func(c *factoryRuntimeConfig) { c.TargetRef = "refs/heads/../escape" },
		"deadline zero":             func(c *factoryRuntimeConfig) { c.Deadline = time.Time{} },
		"hard stop zero":            func(c *factoryRuntimeConfig) { c.HardStop = time.Time{} },
		"deadline zone":             func(c *factoryRuntimeConfig) { c.Deadline = c.Deadline.In(time.FixedZone("offset", 3600)) },
		"hard stop range":           func(c *factoryRuntimeConfig) { c.HardStop = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) },
		"stop before work":          func(c *factoryRuntimeConfig) { c.HardStop = c.Deadline.Add(-time.Second) },
		"no cleanup":                func(c *factoryRuntimeConfig) { c.CleanupTimeoutSeconds = 0 },
		"cleanup overflow":          func(c *factoryRuntimeConfig) { c.CleanupTimeoutSeconds = int(^uint(0) >> 1) },
		"cleanup exceeds hard stop": func(c *factoryRuntimeConfig) { c.CleanupTimeoutSeconds = 121 },
		"empty context":             func(c *factoryRuntimeConfig) { c.DockerContext = "" },
		"default context":           func(c *factoryRuntimeConfig) { c.DockerContext = "default" },
		"context flags":             func(c *factoryRuntimeConfig) { c.DockerContext = "--context=default" },
		"context size":              func(c *factoryRuntimeConfig) { c.DockerContext = strings.Repeat("x", 129) },
		"tcp":                       func(c *factoryRuntimeConfig) { c.DockerEndpoint = "tcp://localhost:2375" },
		"unix authority":            func(c *factoryRuntimeConfig) { c.DockerEndpoint = "unix://host/tmp/socket" },
		"endpoint escape":           func(c *factoryRuntimeConfig) { c.DockerEndpoint = "unix:///tmp/../socket" },
		"endpoint query":            func(c *factoryRuntimeConfig) { c.DockerEndpoint += "?alternate=true" },
		"endpoint fragment":         func(c *factoryRuntimeConfig) { c.DockerEndpoint += "#other" },
		"endpoint encoding":         func(c *factoryRuntimeConfig) { c.DockerEndpoint = "unix:///tmp/%2e/socket" },
		"oversize config":           func(c *factoryRuntimeConfig) { c.Repository = "/" + strings.Repeat("x", 64<<10) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := f.config
			mutate(&changed)
			if _, err := canonicalFactoryRuntimeConfig(changed); err == nil {
				t.Fatal("invalid runtime identity accepted")
			}
			if _, _, err := factoryDockerContextSnapshot(changed); err == nil {
				t.Fatal("invalid snapshot identity accepted")
			}
		})
	}
}

func factoryRuntimeConfigWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
}

func testFactoryRuntimeConfigBytes(t *testing.T) {
	for _, kind := range []string{"unknown", "duplicate", "whitespace", "missing", "invalid JSON", "oversize", "policy changed", "deadline changed", "wrong digest", "invalid digest"} {
		t.Run(kind, func(t *testing.T) {
			f := newFactoryRuntimeConfigFixture(t)
			data := bytes.Clone(f.data)
			digest := factoryHostDigest(data)
			switch kind {
			case "unknown":
				data = append([]byte(`{"extra":true,`), data[1:]...)
			case "duplicate":
				data = append([]byte(`{"version":1,`), data[1:]...)
			case "whitespace":
				data = append(data, '\n')
			case "missing":
				data = bytes.Replace(data, []byte(`"version":1,`), nil, 1)
			case "invalid JSON":
				data = []byte("{")
			case "oversize":
				data = bytes.Repeat([]byte("x"), (64<<10)+1)
			case "policy changed":
				data = bytes.Replace(data, []byte(f.config.PolicyHash), []byte(strings.Repeat("b", 64)), 1)
			case "deadline changed":
				data = bytes.Replace(data, []byte("2025-01-02T03:04:05.000000123Z"), []byte("2025-01-02T03:04:04.000000123Z"), 1)
			case "wrong digest":
				digest = strings.Repeat("f", 64)
			case "invalid digest":
				digest = "wrong"
			}
			if kind != "policy changed" && kind != "deadline changed" && kind != "wrong digest" && kind != "invalid digest" {
				digest = factoryHostDigest(data)
			}
			factoryRuntimeConfigWrite(t, f.path, data)
			if _, err := readFactoryRuntimeConfig(t.Context(), f.path, digest); err == nil {
				t.Fatal("unbound or noncanonical runtime bytes accepted")
			}
		})
	}
	// The supplied digest is the trust input. The codec cannot independently
	// authenticate the current admission; later wiring must compare that view.
	f := newFactoryRuntimeConfigFixture(t)
	f.config.PolicyHash = strings.Repeat("b", 64)
	changed, err := canonicalFactoryRuntimeConfig(f.config)
	if err != nil {
		t.Fatal(err)
	}
	factoryRuntimeConfigWrite(t, f.path, changed)
	if _, err := readFactoryRuntimeConfig(t.Context(), f.path, factoryHostDigest(changed)); err != nil {
		t.Fatalf("reader invented journal authority: %v", err)
	}
}

func testFactoryRuntimeConfigFiles(t *testing.T) {
	for _, kind := range []string{
		"config symlink", "config dangling symlink", "config hardlink", "config FIFO", "config writable", "config public", "config missing",
		"root public", "objective symlink", "runtime symlink", "snapshot symlink", "meta symlink", "meta hardlink", "snapshot directory public",
		"extra auth", "extra plugin", "extra TLS", "extra runtime file", "extra context", "meta oversize", "meta endpoint", "meta TLS", "meta metadata", "cli auth", "cli whitespace",
		"wrong oro", "wrong git", "wrong docker", "program hardlink", "program symlink", "program writable", "program FIFO",
		"repository symlink", "repository regular", "endpoint symlink", "endpoint hardlink", "endpoint regular", "endpoint public", "endpoint missing", "foreign owner",
		"wrong root identity", "wrong objective identity", "alternate config path",
	} {
		t.Run(kind, func(t *testing.T) {
			f := newFactoryRuntimeConfigFixture(t)
			mutateFactoryRuntimeConfigFixture(t, &f, kind)
			if _, err := readFactoryRuntimeConfig(t.Context(), f.path, factoryHostDigest(f.data)); err == nil {
				t.Fatal("hostile runtime bundle accepted")
			}
		})
	}
}

func mutateFactoryRuntimeConfigFixture(t *testing.T, f *factoryRuntimeConfigFixture, kind string) {
	t.Helper()
	runtimeDir := filepath.Dir(f.path)
	dockerDir := filepath.Join(runtimeDir, "docker")
	cliPath := filepath.Join(dockerDir, "config.json")
	endpoint := strings.TrimPrefix(f.config.DockerEndpoint, "unix://")
	write := func(path string, data []byte) {
		t.Helper()
		if err := os.WriteFile(path, data, 0o400); err != nil {
			t.Fatal(err)
		}
	}
	chmod := func(path string, mode os.FileMode) {
		t.Helper()
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	remove := func(path string) {
		t.Helper()
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	link := func(path string) {
		t.Helper()
		if err := os.Link(path, path+"-alias"); err != nil {
			t.Fatal(err)
		}
	}
	symlink := func(path string) {
		t.Helper()
		if err := os.Rename(path, path+"-real"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path+"-real", path); err != nil {
			t.Fatal(err)
		}
	}
	meta, err := os.ReadFile(f.metaPath)
	if err != nil {
		t.Fatal(err)
	}
	switch kind {
	case "config symlink":
		symlink(f.path)
	case "config dangling symlink":
		remove(f.path)
		if err := os.Symlink(f.path+"-missing", f.path); err != nil {
			t.Fatal(err)
		}
	case "config hardlink":
		link(f.path)
	case "config FIFO":
		remove(f.path)
		if err := syscall.Mkfifo(f.path, 0o400); err != nil {
			t.Fatal(err)
		}
	case "config writable":
		chmod(f.path, 0o600)
	case "config public":
		chmod(f.path, 0o444)
	case "config missing":
		remove(f.path)
	case "root public":
		chmod(f.config.StateRoot, 0o755)
	case "objective symlink":
		symlink(filepath.Dir(runtimeDir))
	case "runtime symlink":
		symlink(runtimeDir)
	case "snapshot symlink":
		symlink(dockerDir)
	case "meta symlink":
		symlink(f.metaPath)
	case "meta hardlink":
		link(f.metaPath)
	case "snapshot directory public":
		chmod(filepath.Dir(f.metaPath), 0o755)
	case "extra auth":
		write(filepath.Join(dockerDir, "auth.json"), []byte("{}"))
	case "extra plugin":
		if err := os.Mkdir(filepath.Join(dockerDir, "cli-plugins"), 0o700); err != nil {
			t.Fatal(err)
		}
	case "extra TLS":
		if err := os.Mkdir(filepath.Join(dockerDir, "tls"), 0o700); err != nil {
			t.Fatal(err)
		}
	case "extra runtime file":
		write(filepath.Join(runtimeDir, "extra"), nil)
	case "extra context":
		if err := os.Mkdir(filepath.Join(dockerDir, "contexts", "meta", "other"), 0o700); err != nil {
			t.Fatal(err)
		}
	case "meta oversize":
		factoryRuntimeConfigWrite(t, f.metaPath, bytes.Repeat([]byte("x"), (8<<10)+1))
	case "meta endpoint":
		factoryRuntimeConfigWrite(t, f.metaPath, bytes.ReplaceAll(meta, []byte(endpoint), []byte("/tmp/other.sock")))
	case "meta TLS":
		factoryRuntimeConfigWrite(t, f.metaPath, bytes.Replace(meta, []byte("false"), []byte("true"), 1))
	case "meta metadata":
		factoryRuntimeConfigWrite(t, f.metaPath, bytes.Replace(meta, []byte(`"Metadata":{}`), []byte(`"Metadata":{"Description":"extra"}`), 1))
	case "cli auth":
		factoryRuntimeConfigWrite(t, cliPath, []byte(`{"credsStore":"evil"}`))
	case "cli whitespace":
		factoryRuntimeConfigWrite(t, cliPath, []byte("{}\n"))
	case "wrong oro":
		f.config.OroSHA256 = strings.Repeat("0", 64)
	case "wrong git":
		f.config.GitSHA256 = strings.Repeat("0", 64)
	case "wrong docker":
		f.config.DockerSHA256 = strings.Repeat("0", 64)
	case "program hardlink":
		link(f.config.OroPath)
	case "program symlink":
		symlink(f.config.OroPath)
	case "program writable":
		chmod(f.config.OroPath, 0o777)
	case "program FIFO":
		remove(f.config.OroPath)
		if err := syscall.Mkfifo(f.config.OroPath, 0o500); err != nil {
			t.Fatal(err)
		}
	case "repository symlink":
		symlinkPath := filepath.Join(f.config.StateRoot, "repository-link")
		if err := os.Symlink(f.config.StateRoot, symlinkPath); err != nil {
			t.Fatal(err)
		}
		f.config.Repository = symlinkPath
	case "repository regular":
		f.config.Repository = f.config.OroPath
	case "endpoint symlink":
		symlink(endpoint)
	case "endpoint hardlink":
		link(endpoint)
	case "endpoint regular":
		remove(endpoint)
		write(endpoint, nil)
	case "endpoint public":
		chmod(endpoint, 0o666)
	case "endpoint missing":
		remove(endpoint)
	case "foreign owner":
		info, statErr := os.Stat(f.path)
		if statErr != nil {
			t.Fatal(statErr)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			t.Fatal("missing Unix metadata")
		}
		if os.Getuid() != 0 {
			t.Skip("foreign ownership requires root; pure ownership predicate covered separately")
		}
		if err := os.Chown(f.path, int(stat.Uid)+1, int(stat.Gid)); err != nil {
			t.Fatal(err)
		}
	case "wrong root identity":
		f.config.StateRoot = filepath.Dir(f.config.StateRoot)
	case "wrong objective identity":
		f.config.ObjectiveID = "another-objective"
	case "alternate config path":
		newPath := filepath.Join(runtimeDir, "other.json")
		if err := os.Rename(f.path, newPath); err != nil {
			t.Fatal(err)
		}
		f.path = newPath
	}
	if strings.HasPrefix(kind, "wrong ") || strings.HasPrefix(kind, "repository ") {
		f.data, err = json.Marshal(f.config)
		if err != nil {
			t.Fatal(err)
		}
		factoryRuntimeConfigWrite(t, f.path, f.data)
	}
}

func testFactoryRuntimeConfigReadOnly(t *testing.T) {
	f := newFactoryRuntimeConfigFixture(t)
	before := factoryRuntimeTree(t, f.config.StateRoot)
	if _, err := readFactoryRuntimeConfig(t.Context(), f.path, factoryHostDigest(f.data)); err != nil {
		t.Fatal(err)
	}
	if after := factoryRuntimeTree(t, f.config.StateRoot); !reflect.DeepEqual(before, after) {
		t.Fatal("reader wrote or created runtime artifacts")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := readFactoryRuntimeConfig(ctx, f.path, factoryHostDigest(f.data)); !errors.Is(err, context.Canceled) {
		t.Fatalf("ignored cancellation: %v", err)
	}
	missing := filepath.Join(f.config.StateRoot, "missing", "runtime", "runtime.json")
	if _, err := readFactoryRuntimeConfig(t.Context(), missing, factoryHostDigest(f.data)); err == nil {
		t.Fatal("missing bundle initialized")
	}
	if after := factoryRuntimeTree(t, f.config.StateRoot); !reflect.DeepEqual(before, after) {
		t.Fatal("failed read created runtime state")
	}
}

func factoryRuntimeTree(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		value := info.Mode().String() + info.ModTime().String()
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += factoryHostDigest(data)
		}
		tree[path] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func testFactoryRuntimeConfigDirectorySwap(t *testing.T) {
	f := newFactoryRuntimeConfigFixture(t)
	root, err := os.OpenRoot(filepath.Dir(f.path))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	err = verifyFactoryRuntimeSnapshotDirectory(root, "docker", []string{"config.json", "contexts"}, func(*os.Root) error {
		path := filepath.Join(filepath.Dir(f.path), "docker")
		if err := os.Rename(path, path+"-old"); err != nil {
			return err
		}
		return os.Symlink(path+"-old", path)
	})
	if err == nil {
		t.Fatal("snapshot path replaced by an alias after validation was accepted")
	}
}

type factoryRuntimeConfigFileInfo struct {
	os.FileInfo
	metadata any
}

func (info factoryRuntimeConfigFileInfo) Sys() any { return info.metadata }

func testFactoryRuntimeConfigOwnership(t *testing.T) {
	f := newFactoryRuntimeConfigFixture(t)
	info, err := os.Stat(f.path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("missing Unix metadata")
	}
	wrongOwner := *stat
	wrongOwner.Uid++
	hardlink := *stat
	hardlink.Nlink++
	for _, metadata := range []any{nil, &wrongOwner, &hardlink} {
		if factoryRuntimeFileInfo(factoryRuntimeConfigFileInfo{FileInfo: info, metadata: metadata}, 64<<10) {
			t.Fatal("foreign owner or hardlink metadata accepted")
		}
	}
	if factoryRuntimeFileInfo(nil, 64<<10) || factoryRuntimeUniqueProgramInfo(nil) {
		t.Fatal("missing file metadata accepted")
	}
}

func testFactoryRuntimeConfigMetadataBound(t *testing.T) {
	f := newFactoryRuntimeConfigFixture(t)
	f.config.DockerEndpoint = "unix:///" + strings.Repeat("x", 8<<10)
	if _, _, err := factoryDockerContextSnapshot(f.config); err == nil {
		t.Fatal("oversize context metadata accepted")
	}
	if factoryRuntimeConfigMaxBytes+factoryRuntimeContextMaxBytes+2+factoryRuntimeBundleMetadataBytes > factoryRuntimeBundleMaxBytes ||
		2*factoryRuntimeBundleMaxBytes != 2<<20 {
		t.Fatal("final and single staging bundle exceed the admitted two-copy allowance")
	}
}
