//go:build darwin

package factory //nolint:testpackage // Native capture must exercise the private production source codecs.

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

const (
	dockerLinksNativePath    = "/opt/homebrew/Cellar/docker/29.9.2/bin/docker"
	dockerLinksNativeSHA     = "5682ae92519b289f0340461c963af2031e6c876c8ffb2a8b2459bfde6316bda0"
	dockerLinksNativeContext = "colima-oro-factory"
	dockerLinksNativeSocket  = "unix:///Users/as21/.colima/oro-factory/docker.sock"
	dockerLinksNativeImage   = "golang@sha256:27f2d3b83cfc5803c8078f8e730af0f7c431bf067afaec8f60a36fb763187cba"
	dockerLinksNativeOpts    = "size=1048576,nr_inodes=32,uid=1000,gid=1000,mode=0700,nosuid,nodev"
)

type dockerLinksNativeCommand struct {
	Args         []string `json:"args"`
	ExitCode     int      `json:"exit_code"`
	Joined       bool     `json:"joined"`
	StdinBytes   int      `json:"stdin_bytes"`
	StdinSHA256  string   `json:"stdin_sha256"`
	StdoutBytes  int      `json:"stdout_bytes"`
	StdoutSHA256 string   `json:"stdout_sha256"`
	Stderr       string   `json:"stderr"`
	Error        string   `json:"error"`
}

type dockerLinksNativeMount struct {
	Type, Name, Source, Destination, Driver, Mode, Propagation string
	RW                                                         bool
}

type dockerLinksNativeObject struct {
	ID        string `json:"Id"`
	Name      string
	Created   string
	CreatedAt string
	Image     string
	Driver    string
	Labels    map[string]string
	Options   map[string]string
	Config    struct {
		Labels          map[string]string
		User            string
		Entrypoint, Cmd []string
	}
	Mounts []dockerLinksNativeMount
	State  struct {
		Status, StartedAt, Error          string
		Running, Paused, Restarting, Dead bool
		PID                               int `json:"Pid"`
	}
}

type dockerLinksNativeResource struct {
	Kind, Name, Role   string
	Labels, Options    map[string]string
	Attempted          bool
	CreateAcknowledged bool
	StartAcknowledged  bool
	StopAcknowledged   bool
	RemoveAcknowledged bool
	Stopped, Absent    bool
	Identity           *dockerLinksNativeObject
	Uncertainty        string
}

type dockerLinksNativeReceipt struct {
	Docker, DockerSHA256, Context, Endpoint, Nonce string
	Versions                                       json.RawMessage
	ImageID                                        string
	SeedBytes, ExportBytes                         int
	SeedSHA256, ExportSHA256                       string
	DecodedExact                                   bool
	SentinelBeforeSHA256, SentinelAfterSHA256      string
	Commands                                       []dockerLinksNativeCommand
	Resources                                      []*dockerLinksNativeResource
	Passed                                         bool
}

type dockerLinksNativeFixture struct {
	dir, config string
	receipt     dockerLinksNativeReceipt
	before      []byte
}

func TestDockerSourceLinksNative(t *testing.T) {
	if os.Getenv("ORO_FACTORY_INTEGRATION") != "1" {
		t.Skip("native Docker source links require ORO_FACTORY_INTEGRATION=1")
	}
	fixture := newDockerLinksNativeFixture(t)
	t.Cleanup(func() {
		fixture.cleanup(t)
		fixture.receipt.Passed = !t.Failed()
		if err := fixture.saveReceipt(); err != nil {
			t.Errorf("save native receipt: %v", err)
		}
		t.Logf("native evidence=%s; commands=%d; pass=%t", fixture.dir, len(fixture.receipt.Commands), fixture.receipt.Passed)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	entries := []sourceEntry{
		{Path: ".claude/hooks/context_block_stop.py", Mode: 0o120000, Data: []byte("../../assets/hooks/context_block_stop.py")},
		{Path: ".claude/hooks/context_pct_writer.py", Mode: 0o120000, Data: []byte("../../assets/hooks/context_pct_writer.py")},
		{Path: "assets/hooks/context_block_stop.py", Mode: 0o100755, Data: []byte("#!/usr/bin/python\n")},
		{Path: "assets/hooks/context_pct_writer.py", Mode: 0o100644, Data: []byte("raw\x00\xff\n")},
		{Path: "alias", Mode: 0o120000, Data: []byte("assets/../assets/hooks/context_pct_writer.py")},
	}
	seed, err := encodeDockerSourceArchive(entries, dockerArchiveLimits())
	if err != nil {
		t.Fatal(err)
	}
	fixture.artifact(t, "seed.tar", seed, 1<<20)
	fixture.receipt.SeedBytes, fixture.receipt.SeedSHA256 = len(seed), dockerLinksNativeDigest(seed)
	inspectDockerLinksNativeTar(t, seed, false)
	fixture.prepare(ctx, t)
	fixture.create(ctx, t)
	fixture.roundTrip(ctx, t, entries, seed)
}

func newDockerLinksNativeFixture(t *testing.T) *dockerLinksNativeFixture {
	t.Helper()
	if os.Getenv("ORO_FACTORY_DOCKER_CONTEXT") != dockerLinksNativeContext || os.Getenv("ORO_FACTORY_DOCKER_ENDPOINT") != dockerLinksNativeSocket {
		t.Fatal("native links require the exact explicit dedicated Docker context and endpoint")
	}
	root := os.Getenv("ORO_FACTORY_NATIVE_ARTIFACT_DIR")
	actual, err := filepath.EvalSymlinks(root)
	if err != nil || !filepath.IsAbs(root) || actual != root || filepath.Clean(root) != root {
		t.Fatalf("native artifact directory must be explicit and canonical: %v", err)
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("native artifact parent is not private: %v", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		t.Fatal("native artifact parent is not owned by the current user")
	}
	if err := verifyDockerLinksNativeProgram(); err != nil {
		t.Fatal(err)
	}
	socket := strings.TrimPrefix(dockerLinksNativeSocket, "unix://")
	actual, err = filepath.EvalSymlinks(socket)
	if err != nil || actual != socket {
		t.Fatalf("dedicated Docker socket is not canonical: %v", err)
	}
	info, err = os.Lstat(socket)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("dedicated Docker endpoint is not a Unix socket: %v", err)
	}
	stat, ok = info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
		t.Fatal("dedicated Docker socket has a foreign owner or hardlink alias")
	}
	dir, err := os.MkdirTemp(root, "docker-links-native-")
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	f := &dockerLinksNativeFixture{dir: dir, config: filepath.Join(dir, "docker")}
	f.receipt = dockerLinksNativeReceipt{Docker: dockerLinksNativePath, DockerSHA256: dockerLinksNativeSHA, Context: dockerLinksNativeContext, Endpoint: dockerLinksNativeSocket, Nonce: hex.EncodeToString(nonce)}
	for _, role := range []string{"target", "holder", "sentinel"} {
		resource := &dockerLinksNativeResource{
			Kind: "volume", Name: "oro-links-native-" + f.receipt.Nonce + "-" + role, Role: role,
			Labels: map[string]string{"dev.getoro.factory.native-link.nonce": f.receipt.Nonce, "dev.getoro.factory.native-link.role": role},
		}
		if role == "holder" {
			resource.Kind = "container"
		}
		if role == "target" {
			resource.Options = map[string]string{"type": "tmpfs", "device": "tmpfs", "o": dockerLinksNativeOpts}
		}
		f.receipt.Resources = append(f.receipt.Resources, resource)
	}
	metaDir := filepath.Join(f.config, "contexts", "meta", dockerLinksNativeDigest([]byte(dockerLinksNativeContext)))
	if err := os.MkdirAll(metaDir, 0o700); err != nil {
		t.Fatal(err)
	}
	metadata := `{"Name":"` + dockerLinksNativeContext + `","Metadata":{},"Endpoints":{"docker":{"Host":"` + dockerLinksNativeSocket + `","SkipTLSVerify":false}}}`
	for name, data := range map[string]string{filepath.Join(f.config, "config.json"): "{}", filepath.Join(metaDir, "meta.json"): metadata} {
		if err := os.WriteFile(name, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func verifyDockerLinksNativeProgram() error {
	actual, err := filepath.EvalSymlinks(dockerLinksNativePath)
	if err != nil || actual != dockerLinksNativePath {
		return errors.New("pinned Docker program is not canonical")
	}
	before, err := os.Lstat(actual)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0o111 == 0 || before.Size() > 128<<20 {
		return errors.New("pinned Docker program is not a bounded regular executable")
	}
	file, err := os.OpenFile(actual, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return errors.New("Docker executable inode changed")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, (128<<20)+1))
	after, statErr := os.Lstat(actual)
	if err != nil || statErr != nil || n != before.Size() || !os.SameFile(before, after) || hex.EncodeToString(hash.Sum(nil)) != dockerLinksNativeSHA {
		return errors.New("Docker executable differs from the pinned native identity")
	}
	return nil
}

type dockerLinksNativeBuffer struct {
	bytes.Buffer
	limit  int
	cancel context.CancelFunc
}

func (buffer *dockerLinksNativeBuffer) Write(data []byte) (int, error) {
	if len(data) > buffer.limit-buffer.Len() {
		buffer.cancel()
		return 0, errors.New("native Docker output exceeded its live bound")
	}
	return buffer.Buffer.Write(data)
}

func (f *dockerLinksNativeFixture) command(ctx context.Context, input []byte, args ...string) (HostResult, error) {
	if len(f.receipt.Commands) >= 32 {
		return HostResult{}, errors.New("native Docker command allowance exhausted")
	}
	if err := verifyDockerLinksNativeProgram(); err != nil {
		return HostResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 9*time.Second)
	defer cancel()
	argv := append([]string{"--config", f.config, "--context", dockerLinksNativeContext}, args...)
	command := exec.CommandContext(ctx, dockerLinksNativePath, argv...)
	command.Dir, command.Env = f.dir, []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "LANG=C"}
	command.Stdin, command.WaitDelay = bytes.NewReader(input), 250*time.Millisecond
	stdout := &dockerLinksNativeBuffer{limit: 1 << 20, cancel: cancel}
	stderr := &dockerLinksNativeBuffer{limit: 4096, cancel: cancel}
	command.Stdout, command.Stderr = stdout, stderr
	err := command.Run() // Run joins the owned client and all pipe-copy goroutines.
	result := HostResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: -1}
	joined := command.ProcessState != nil
	if joined {
		result.ExitCode = command.ProcessState.ExitCode()
	}
	record := dockerLinksNativeCommand{Args: argv, ExitCode: result.ExitCode, Joined: joined, StdinBytes: len(input), StdinSHA256: dockerLinksNativeDigest(input), StdoutBytes: len(result.Stdout), StdoutSHA256: dockerLinksNativeDigest(result.Stdout), Stderr: string(result.Stderr)}
	if err != nil {
		record.Error = err.Error()
	}
	f.receipt.Commands = append(f.receipt.Commands, record)
	if saveErr := f.saveReceipt(); saveErr != nil {
		result.ExitCode = -1 // A matching absence response cannot hide lost evidence.
		err = errors.Join(err, saveErr)
	}
	return result, err
}

func (f *dockerLinksNativeFixture) mustCommand(ctx context.Context, t *testing.T, input []byte, args ...string) []byte {
	t.Helper()
	result, err := f.command(ctx, input, args...)
	if err != nil || result.ExitCode != 0 || len(result.Stderr) != 0 {
		t.Fatalf("native Docker %v: exit=%d err=%v stderr=%s", args, result.ExitCode, err, result.Stderr)
	}
	return result.Stdout
}

func (f *dockerLinksNativeFixture) saveReceipt() error {
	data, err := json.MarshalIndent(f.receipt, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > 256<<10 {
		return errors.New("native receipt exceeded 256 KiB")
	}
	return os.WriteFile(filepath.Join(f.dir, "receipt.json"), data, 0o600)
}

func (f *dockerLinksNativeFixture) artifact(t *testing.T, name string, data []byte, limit int) {
	t.Helper()
	if len(data) > limit {
		t.Fatalf("native artifact %s exceeded bound", name)
	}
	if err := os.WriteFile(filepath.Join(f.dir, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func dockerLinksNativeDigest(data []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func (f *dockerLinksNativeFixture) prepare(ctx context.Context, t *testing.T) {
	t.Helper()
	versions := f.mustCommand(ctx, t, nil, "version", "--format", "{{json .}}")
	if !json.Valid(versions) || len(versions) > 16<<10 {
		t.Fatal("native Docker version identity is invalid or unbounded")
	}
	f.receipt.Versions = versions
	var images []struct{ ID, Os, Architecture string }
	if err := json.Unmarshal(f.mustCommand(ctx, t, nil, "image", "inspect", dockerLinksNativeImage), &images); err != nil || len(images) != 1 {
		t.Fatalf("inspect exact cached image: %v", err)
	}
	if images[0].Os != "linux" || images[0].Architecture != "arm64" || !dockerLinksNativeHex(strings.TrimPrefix(images[0].ID, "sha256:"), 64) || !strings.HasPrefix(images[0].ID, "sha256:") {
		t.Fatalf("unsupported native image identity: %+v", images[0])
	}
	f.receipt.ImageID = images[0].ID
	for _, resource := range f.receipt.Resources {
		result, err := f.inspect(ctx, resource.Kind, resource.Name)
		if !dockerLinksNativeAbsent(resource.Kind, resource.Name, result, err) {
			t.Fatalf("fixture name is not positively absent: %s", resource.Name)
		}
	}
}

func dockerLinksNativeHex(value string, size int) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(value) == size && len(decoded)*2 == size && strings.ToLower(value) == value
}

func (f *dockerLinksNativeFixture) inspect(ctx context.Context, kind, name string) (HostResult, error) {
	if kind == "volume" {
		return f.command(ctx, nil, "volume", "inspect", name)
	}
	return f.command(ctx, nil, "inspect", "--type", "container", name)
}

func dockerLinksNativeAbsent(kind, name string, result HostResult, err error) bool {
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || result.ExitCode != 1 || string(result.Stdout) != "[]\n" {
		return false
	}
	message := "Error response from daemon: No such container: " + name + "\n"
	if kind == "volume" {
		message = "Error response from daemon: get " + name + ": no such volume\n"
	}
	return string(result.Stderr) == message
}

func (f *dockerLinksNativeFixture) create(ctx context.Context, t *testing.T) {
	t.Helper()
	for _, index := range []int{0, 2, 1} {
		resource := f.receipt.Resources[index]
		args := f.createArgs(resource)
		resource.Attempted = true // Register durable cleanup intention before create.
		if err := f.saveReceipt(); err != nil {
			t.Fatal(err)
		}
		output := f.mustCommand(ctx, t, nil, args...)
		identity := strings.TrimSuffix(string(output), "\n")
		if (resource.Kind == "volume" && identity != resource.Name) || (resource.Kind == "container" && !dockerLinksNativeHex(identity, 64)) || string(output) != identity+"\n" {
			t.Fatalf("unexpected create acknowledgment for %s: %q", resource.Name, output)
		}
		resource.CreateAcknowledged = true
		object, raw, err := f.observe(ctx, resource, identity)
		if err != nil {
			t.Fatal(err)
		}
		resource.Identity = &object
		if resource.Role == "sentinel" {
			f.before, f.receipt.SentinelBeforeSHA256 = raw, dockerLinksNativeDigest(raw)
		}
	}
	holder := f.receipt.Resources[1]
	if output := f.mustCommand(ctx, t, nil, "start", holder.Identity.ID); string(output) != holder.Identity.ID+"\n" {
		t.Fatalf("unexpected holder start acknowledgment: %q", output)
	}
	holder.StartAcknowledged = true
	object, _, err := f.observe(ctx, holder, holder.Identity.ID)
	if err != nil || !dockerLinksNativeRunning(object) {
		t.Fatalf("holder did not become running: %+v, %v", object.State, err)
	}
	holder.Identity.State.StartedAt = object.State.StartedAt
}

func (f *dockerLinksNativeFixture) createArgs(resource *dockerLinksNativeResource) []string {
	args := []string{"volume", "create", "--driver", "local"}
	if resource.Kind == "container" {
		args = []string{"create", "--name", resource.Name, "--pull=never", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--cgroupns", "private", "--ipc", "private", "--memory", "32m", "--memory-swap", "32m", "--cpus", "0.25", "--pids-limit", "8", "--log-driver", "none", "--user", "65534:65534", "--entrypoint", "/bin/sleep", "--mount", "type=volume,source=" + f.receipt.Resources[0].Name + ",target=/source,volume-nocopy"}
	}
	for _, key := range []string{"dev.getoro.factory.native-link.nonce", "dev.getoro.factory.native-link.role"} {
		args = append(args, "--label", key+"="+resource.Labels[key])
	}
	for _, key := range []string{"type", "device", "o"} {
		if value, exists := resource.Options[key]; exists {
			args = append(args, "--opt", key+"="+value)
		}
	}
	if resource.Kind == "container" {
		return append(args, f.receipt.ImageID, "120")
	}
	return append(args, resource.Name)
}

func (f *dockerLinksNativeFixture) observe(ctx context.Context, resource *dockerLinksNativeResource, identity string) (dockerLinksNativeObject, []byte, error) {
	var objects []dockerLinksNativeObject
	result, err := f.inspect(ctx, resource.Kind, identity)
	if err != nil || result.ExitCode != 0 || len(result.Stderr) != 0 {
		return dockerLinksNativeObject{}, nil, fmt.Errorf("inspect owned %s: %w", identity, errors.Join(err, errors.New(string(result.Stderr))))
	}
	if err := json.Unmarshal(result.Stdout, &objects); err != nil || len(objects) != 1 {
		return dockerLinksNativeObject{}, nil, errors.New("native resource inspection is not one exact object")
	}
	if err := f.validateObject(resource, objects[0]); err != nil {
		return dockerLinksNativeObject{}, nil, err
	}
	return objects[0], result.Stdout, nil
}

func (f *dockerLinksNativeFixture) validateObject(resource *dockerLinksNativeResource, object dockerLinksNativeObject) error {
	created := object.CreatedAt
	if resource.Kind == "container" {
		created = object.Created
		if object.Name != "/"+resource.Name || !dockerLinksNativeHex(object.ID, 64) || object.Image != f.receipt.ImageID ||
			!reflect.DeepEqual(object.Config.Labels, resource.Labels) || object.Config.User != "65534:65534" ||
			!slices.Equal(object.Config.Entrypoint, []string{"/bin/sleep"}) || !slices.Equal(object.Config.Cmd, []string{"120"}) || len(object.Mounts) != 1 {
			return errors.New("holder immutable configuration or identity changed")
		}
		mount := object.Mounts[0]
		if mount.Type != "volume" || mount.Name != f.receipt.Resources[0].Name || mount.Destination != "/source" || mount.Driver != "local" || !mount.RW {
			return errors.New("holder has an unexpected source mount")
		}
	} else if object.Name != resource.Name || object.Driver != "local" || !reflect.DeepEqual(object.Labels, resource.Labels) || !reflect.DeepEqual(object.Options, resource.Options) {
		return errors.New("volume immutable configuration or identity changed")
	}
	if _, err := CanonicalDockerCreatedAt(created); err != nil {
		return fmt.Errorf("native resource creation identity: %w", err)
	}
	if previous := resource.Identity; previous != nil {
		if object.ID != previous.ID || object.Created != previous.Created || object.CreatedAt != previous.CreatedAt || !reflect.DeepEqual(object.Mounts, previous.Mounts) {
			return errors.New("native resource lifetime changed before an operation")
		}
	}
	return nil
}

func dockerLinksNativeRunning(object dockerLinksNativeObject) bool {
	state := object.State
	_, err := CanonicalDockerCreatedAt(state.StartedAt)
	return err == nil && state.Running && state.PID > 0 && state.Status == "running" && !state.Paused && !state.Restarting && !state.Dead && state.Error == ""
}

func (f *dockerLinksNativeFixture) roundTrip(ctx context.Context, t *testing.T, entries []sourceEntry, seed []byte) {
	t.Helper()
	target, holder := f.receipt.Resources[0], f.receipt.Resources[1]
	if _, _, err := f.observe(ctx, target, target.Name); err != nil {
		t.Fatal(err)
	}
	id := holder.Identity.ID
	empty := f.mustCommand(ctx, t, nil, "cp", id+":/source/.", "-")
	if got, err := decodeDockerSourceArchive(bytes.NewReader(empty), dockerArchiveLimits()); err != nil || len(got) != 0 {
		t.Fatalf("fresh target was not empty: %+v, %v", got, err)
	}
	f.mustCommand(ctx, t, seed, "cp", "-", id+":/source")
	exported := f.mustCommand(ctx, t, nil, "cp", id+":/source/.", "-")
	f.artifact(t, "export.tar", exported, 1<<20)
	f.receipt.ExportBytes, f.receipt.ExportSHA256 = len(exported), dockerLinksNativeDigest(exported)
	headerInventory := inspectDockerLinksNativeTar(t, exported, true)
	f.artifact(t, "headers.json", headerInventory, 64<<10)
	object, _, err := f.observe(ctx, holder, id)
	if err != nil || !dockerLinksNativeRunning(object) || object.State.StartedAt != holder.Identity.State.StartedAt {
		t.Fatalf("holder lifetime changed during copy: %+v, %v", object.State, err)
	}
	got, err := decodeDockerSourceArchive(bytes.NewReader(exported), dockerArchiveLimits())
	if err != nil {
		t.Fatalf("production decoder rejected actual Docker capture: %v", err)
	}
	sortEntries := func(a, b sourceEntry) int { return strings.Compare(a.Path, b.Path) }
	slices.SortFunc(entries, sortEntries)
	slices.SortFunc(got, sortEntries)
	if !reflect.DeepEqual(entries, got) {
		t.Fatalf("native source vector changed: want=%+v got=%+v", entries, got)
	}
	f.receipt.DecodedExact = true
	t.Logf("actual Docker archive=%d bytes sha256=%s; all five entries and three raw links exact", len(exported), f.receipt.ExportSHA256)
}

func inspectDockerLinksNativeTar(t *testing.T, data []byte, exported bool) []byte {
	t.Helper()
	// Inspect raw type bytes first: archive/tar.Next must not hide extensions.
	for offset := 0; offset+512 <= len(data); {
		header := data[offset : offset+512]
		if bytes.Equal(header, make([]byte, 512)) {
			break
		}
		if !bytes.Contains([]byte{tar.TypeReg, tar.TypeDir, tar.TypeSymlink}, header[156:157]) {
			t.Fatalf("native capture contains raw metadata type %q", header[156])
		}
		size, err := strconv.ParseInt(strings.Trim(string(header[124:136]), " \x00"), 8, 64)
		if err != nil || size < 0 || size > 1<<20 {
			t.Fatalf("invalid raw native header size: %v", err)
		}
		offset += 512 + int((size+511)/512)*512
	}
	stream := tar.NewReader(bytes.NewReader(data))
	var inventory []tar.Header
	links, linkSeen := 0, false
	for {
		header, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Format != tar.FormatUSTAR || len(header.PAXRecords) != 0 || header.Uid != 1000 || header.Gid != 1000 {
			t.Fatalf("native tar metadata changed: %+v", header)
		}
		if exported && ((len(inventory) == 0 && (header.Name != "./" || header.Typeflag != tar.TypeDir)) || !strings.HasPrefix(header.Name, "./") || (len(inventory) != 0 && header.Name == "./")) {
			t.Fatalf("native root or entry prefix changed: %+v", header)
		}
		body, err := io.ReadAll(stream)
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeSymlink {
			linkSeen, links = true, links+1
			if header.Size != 0 || header.Mode != 0o777 || len(body) != 0 || header.Linkname == "" {
				t.Fatalf("native symlink body or permissions changed: %+v", header)
			}
		} else if !exported && linkSeen {
			t.Fatal("production seed emitted a non-link after a link")
		}
		inventory = append(inventory, *header)
	}
	if links != 3 {
		t.Fatalf("native tar retained %d links instead of three", links)
	}
	encoded, err := json.MarshalIndent(inventory, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func (f *dockerLinksNativeFixture) cleanup(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, index := range []int{1, 0, 2} {
		resource := f.receipt.Resources[index]
		if !resource.Attempted {
			continue
		}
		if err := f.retire(ctx, resource); err != nil {
			resource.Uncertainty = err.Error()
			t.Errorf("native cleanup unresolved %s: %v", resource.Name, err)
		}
	}
}

func (f *dockerLinksNativeFixture) retire(ctx context.Context, resource *dockerLinksNativeResource) error {
	identity := resource.Name
	if resource.Kind == "container" && resource.Identity != nil {
		identity = resource.Identity.ID
	}
	object, raw, err := f.observe(ctx, resource, identity)
	if err != nil {
		return err // Absence without an acknowledged removal is not retirement.
	}
	if resource.Identity == nil {
		resource.Identity = &object // Reconcile only this fresh, pre-registered nonce.
	}
	if resource.Role == "sentinel" {
		f.receipt.SentinelAfterSHA256 = dockerLinksNativeDigest(raw)
		if len(f.before) == 0 || !bytes.Equal(f.before, raw) {
			return errors.New("foreign sentinel changed during target operations or cleanup")
		}
	}
	if resource.Kind == "container" {
		if err := f.stopHolder(ctx, resource); err != nil {
			return err
		}
		identity = resource.Identity.ID
	}
	args := []string{"volume", "rm", identity}
	if resource.Kind == "container" {
		args = []string{"rm", identity}
	}
	result, err := f.command(ctx, nil, args...)
	if err != nil || result.ExitCode != 0 || len(result.Stderr) != 0 || string(result.Stdout) != identity+"\n" {
		return fmt.Errorf("exact remove acknowledgment missing: %w", errors.Join(err, errors.New(string(result.Stderr))))
	}
	resource.RemoveAcknowledged = true
	result, err = f.inspect(ctx, resource.Kind, identity)
	if !dockerLinksNativeAbsent(resource.Kind, identity, result, err) {
		return errors.New("removed fixture was not positively absent")
	}
	resource.Absent = true
	if !resource.CreateAcknowledged {
		return errors.New("create acknowledgment was lost; observed cleanup does not certify daemon drain")
	}
	return nil
}

func (f *dockerLinksNativeFixture) stopHolder(ctx context.Context, resource *dockerLinksNativeResource) error {
	id := resource.Identity.ID
	result, err := f.command(ctx, nil, "stop", "--signal", "SIGKILL", "--timeout", "0", id)
	if err != nil || result.ExitCode != 0 || len(result.Stderr) != 0 || string(result.Stdout) != id+"\n" {
		return fmt.Errorf("exact stop acknowledgment missing: %w", errors.Join(err, errors.New(string(result.Stderr))))
	}
	resource.StopAcknowledged = true
	object, _, err := f.observe(ctx, resource, id)
	state := object.State
	if err != nil || state.Running || state.PID != 0 || (state.Status != "exited" && state.Status != "created") || state.Paused || state.Restarting || state.Dead || state.Error != "" {
		return fmt.Errorf("holder not positively stopped: %w", errors.Join(err, errors.New(state.Status)))
	}
	resource.Stopped = true
	return nil
}
