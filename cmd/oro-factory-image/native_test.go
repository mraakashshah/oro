//go:build darwin

package main //nolint:testpackage // Qualify the actual private bootstrap entry point, without a runner substitution.

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	nativeGuardContext       = "colima-oro-factory"
	nativeGuardLabel         = "dev.getoro.factory.native-guard"
	nativeGuardRole          = "dev.getoro.factory.native-role"
	nativeGuardVolumeOptions = "size=1048576,nr_inodes=32,uid=1000,gid=1000"
)

type nativeGuardObject struct {
	ID                                      string `json:"Id"`
	Name, Created, CreatedAt, Image, Driver string
	Labels, Options                         map[string]string
	RepoTags                                []string
	Config                                  struct {
		User            string
		Labels          map[string]string
		Entrypoint, Cmd []string
	}
	State struct {
		Status, StartedAt, FinishedAt, Error         string
		Running, Paused, Restarting, Dead, OOMKilled bool
		PID                                          int `json:"Pid"`
		ExitCode                                     int
	}
	HostConfig struct {
		ReadonlyRootfs, Privileged                       bool
		NetworkMode, PidMode, IpcMode, CgroupnsMode      string
		Memory, MemorySwap, NanoCpus, PidsLimit, ShmSize int64
		Binds, CapAdd, CapDrop, SecurityOpt              []string
		Tmpfs                                            map[string]string
		LogConfig                                        struct{ Type string }
	}
	Mounts []struct {
		Type, Name, Destination string
		RW                      bool
	}
}

type nativeGuardResource struct {
	Kind, Name, Role, Image string
	Labels                  map[string]string
	Attempted, Absent       bool
	Identity                *nativeGuardObject
}

type nativeGuardCall struct {
	Program                            string
	Args                               []string
	Started, Finished                  time.Time
	PID                                int
	Retired                            bool
	InputBytes                         int
	InputSHA256, Stdout, Stderr, Error string
}

type nativeGuardFixture struct {
	dir, config, home, colima, colimaSHA, nonce string
	options                                     guardImageOptions
	hardStop                                    time.Time
	work                                        context.Context
	calls                                       []nativeGuardCall
	charged                                     int
	image                                       guardImageReceipt
	resources                                   []*nativeGuardResource
	deadline                                    time.Time
	observations                                map[string]any
}

// TestGuardImageNative has no backend effects unless explicitly enabled. Root
// runs this direct native lane only after reviewing the frozen source. It is a
// bootstrap/namespace proof, not managed admission or launchd qualification.
func TestGuardImageNative(t *testing.T) {
	if os.Getenv("ORO_FACTORY_INTEGRATION") != "1" {
		t.Skip("requires explicit ORO_FACTORY_INTEGRATION=1 and reviewed native inputs")
	}
	started := time.Now()
	f := newNativeGuardFixture(t, started.Add(210*time.Second))
	ctx, cancel := context.WithDeadline(context.Background(), f.hardStop.Add(-30*time.Second))
	defer cancel()
	f.work = ctx
	defer func() {
		cleanup, stop := context.WithDeadline(context.Background(), f.hardStop)
		defer stop()
		if err := f.cleanup(cleanup); err != nil {
			t.Errorf("exact owned cleanup: %v", err)
		}
		if !time.Now().Before(f.hardStop) {
			t.Error("native fixture exceeded original210s hard stop")
		}
		f.observations["elapsed"] = time.Since(started).String()
		f.observations["passed"] = !t.Failed() && time.Now().Before(f.hardStop)
		if err := f.save(); err != nil {
			t.Errorf("save native receipt: %v", err)
		}
		if !time.Now().Before(f.hardStop) {
			t.Error("final evidence publication exceeded original210s hard stop")
		}
		t.Logf("native evidence=%s calls=%d original_hard_stop=%s", f.dir, f.charged, f.hardStop.Format(time.RFC3339Nano))
	}()
	if err := f.preflight(); err != nil {
		t.Fatal(err)
	}
	if err := f.build(); err != nil {
		t.Fatal(err)
	}
	if err := f.namespace(); err != nil {
		t.Fatal(err)
	}
}

func newNativeGuardFixture(t *testing.T, hardStop time.Time) *nativeGuardFixture {
	t.Helper()
	root := os.Getenv("ORO_FACTORY_EVIDENCE_DIR")
	if err := canonicalGuardPath(root, true); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm()&0o077 != 0 || stat.Uid != uint32(os.Getuid()) {
		t.Fatal("evidence parent must be owned and private")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	f := &nativeGuardFixture{nonce: hex.EncodeToString(nonce[:]), hardStop: hardStop, observations: make(map[string]any)}
	f.dir = filepath.Join(root, "guard-image-native-"+f.nonce)
	if err := os.Mkdir(f.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f.home, err = os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	f.options = guardImageOptions{Guard: os.Getenv("ORO_FACTORY_GUARD_BIN"), GuardSHA256: os.Getenv("ORO_FACTORY_GUARD_SHA256"), Docker: os.Getenv("ORO_FACTORY_DOCKER_BIN"), DockerSHA256: os.Getenv("ORO_FACTORY_DOCKER_SHA256"), Context: os.Getenv("ORO_FACTORY_DOCKER_CONTEXT"), Endpoint: os.Getenv("ORO_FACTORY_DOCKER_ENDPOINT"), Output: filepath.Join(f.dir, "build")}
	f.colima, f.colimaSHA = os.Getenv("ORO_FACTORY_COLIMA_BIN"), os.Getenv("ORO_FACTORY_COLIMA_SHA256")
	f.config = filepath.Join(f.dir, "docker")
	f.observations["inputs"] = f.options
	f.observations["colima"] = []string{f.colima, f.colimaSHA}
	f.observations["hard_stop"] = hardStop
	if err := f.save(); err != nil {
		t.Fatal(err)
	}
	return f
}

func nativeGuardRead(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("native fixture file exceeds bound")
	}
	return data, nil
}

func (f *nativeGuardFixture) artifact(name string, data []byte) error {
	if len(data) > guardOutputLimit {
		return errors.New("native evidence exceeds1MiB")
	}
	file, err := os.OpenFile(filepath.Join(f.dir, name), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err = file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

func (f *nativeGuardFixture) save() error {
	data, err := json.MarshalIndent(struct {
		Observations map[string]any
		Calls        []nativeGuardCall
		ChargedCalls int
		Resources    []*nativeGuardResource
		Image        guardImageReceipt
	}{f.observations, f.calls, f.charged, f.resources, f.image}, "", "  ")
	if err != nil {
		return err
	}
	return f.artifact("native-receipt.json", data)
}

func (f *nativeGuardFixture) preflight() error {
	if f.options.Context != nativeGuardContext || f.options.Endpoint != "unix://"+filepath.Join(f.home, ".colima/oro-factory/docker.sock") {
		return errors.New("native proof requires exact dedicated context and endpoint")
	}
	if _, err := validateGuardImageInputs(f.work, f.options); err != nil {
		return err
	}
	if _, err := readGuardProgram(f.work, f.colima, f.colimaSHA, guardDockerLimit, false); err != nil {
		return err
	}
	var capacity syscall.Statfs_t
	if err := syscall.Statfs(f.dir, &capacity); err != nil {
		return err
	}
	available := capacity.Bavail * uint64(capacity.Bsize)
	if available < 1536<<20 {
		return errors.New("host lacks512MiB fixture allowance plus1GiB margin")
	}
	f.observations["host_available_bytes"] = available
	profile, err := nativeGuardRead(filepath.Join(f.home, ".colima/oro-factory/colima.yaml"), 64<<10)
	if err != nil {
		return err
	}
	var config struct {
		Mounts        []any `yaml:"mounts"`
		ForwardAgent  *bool `yaml:"forwardAgent"`
		Runtime, Arch string
	}
	if err = yaml.Unmarshal(profile, &config); err != nil {
		return err
	}
	if len(config.Mounts) != 0 || config.ForwardAgent == nil || *config.ForwardAgent || config.Runtime != "docker" || config.Arch != "aarch64" {
		return errors.New("dedicated profile isolation differs")
	}
	if err = f.artifact("colima-profile.yaml", profile); err != nil {
		return err
	}
	effective, err := nativeGuardRead(filepath.Join(f.home, ".colima/_lima/colima-oro-factory/lima.yaml"), 64<<10)
	if err != nil {
		return err
	}
	var lima struct {
		Mounts []any
		SSH    struct {
			ForwardAgent *bool `yaml:"forwardAgent"`
		}
	}
	if err = yaml.Unmarshal(effective, &lima); err != nil {
		return err
	}
	if len(lima.Mounts) != 0 || lima.SSH.ForwardAgent == nil || *lima.SSH.ForwardAgent {
		return errors.New("effective VM mounts or agent forwarding differ")
	}
	if err = f.artifact("lima-effective.yaml", effective); err != nil {
		return err
	}
	if err = f.prepareConfig(); err != nil {
		return err
	}
	disk, err := f.colimaRead("df", "-Pk", "/var/lib/docker")
	if err != nil {
		return err
	}
	mem, err := f.colimaRead("cat", "/proc/meminfo")
	if err != nil {
		return err
	}
	if err = nativeGuardCapacity(disk, mem); err != nil {
		return err
	}
	data, err := f.docker(f.work, nil, "info", "--format", "{{.OSType}}/{{.Architecture}}")
	if err != nil || strings.TrimSpace(string(data)) != "linux/aarch64" {
		return errors.Join(errors.New("dedicated backend is not Linux arm64"), err)
	}
	_, err = f.docker(f.work, nil, "version", "--format", "{{json .}}")
	return err
}

func nativeGuardCapacity(disk, mem []byte) error {
	lines := strings.Split(strings.TrimSpace(string(disk)), "\n")
	if len(lines) != 2 {
		return errors.New("unexpected guest disk capacity")
	}
	fields := strings.Fields(lines[1])
	if len(fields) != 6 {
		return errors.New("unexpected guest disk fields")
	}
	free, err := strconv.ParseUint(fields[3], 10, 64)
	if err != nil || free < 2<<20 {
		return errors.New("guest has less than2GiB available disk")
	}
	for _, line := range strings.Split(string(mem), "\n") {
		fields = strings.Fields(line)
		if len(fields) == 3 && fields[0] == "MemAvailable:" && fields[2] == "kB" {
			free, err = strconv.ParseUint(fields[1], 10, 64)
			if err == nil && free >= 512<<10 {
				return nil
			}
		}
	}
	return errors.New("guest has less than512MiB MemAvailable")
}

func (f *nativeGuardFixture) prepareConfig() error {
	meta := filepath.Join(f.config, "contexts/meta", guardDigest([]byte(nativeGuardContext)))
	if err := os.MkdirAll(meta, 0o700); err != nil {
		return err
	}
	value := map[string]any{"Name": nativeGuardContext, "Metadata": map[string]string{}, "Endpoints": map[string]any{"docker": map[string]any{"Host": f.options.Endpoint, "SkipTLSVerify": false}}}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(meta, "meta.json"), data, 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(f.config, "config.json"), []byte("{}"), 0o600)
}

func (f *nativeGuardFixture) colimaRead(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(f.work, 5*time.Second)
	defer cancel()
	if _, err := readGuardProgram(ctx, f.colima, f.colimaSHA, guardDockerLimit, false); err != nil {
		return nil, err
	}
	return f.command(ctx, f.colima, append([]string{"-p", "oro-factory", "ssh", "--"}, args...), nil, false, true)
}

func (f *nativeGuardFixture) docker(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := readGuardProgram(ctx, f.options.Docker, f.options.DockerSHA256, guardDockerLimit, false); err != nil {
		return nil, err
	}
	if err := validateGuardEndpoint(f.options.Endpoint); err != nil {
		return nil, err
	}
	return f.command(ctx, f.options.Docker, append([]string{"--config", f.config, "--context", nativeGuardContext}, args...), input, false, false)
}

type nativeGuardCapture struct {
	mu                 sync.Mutex
	stdout, stderr     bytes.Buffer
	cancel             context.CancelFunc
	ready              chan struct{}
	signaled, overflow bool
}
type nativeGuardStream struct {
	capture *nativeGuardCapture
	stderr  bool
}

func (stream nativeGuardStream) Write(data []byte) (int, error) {
	c := stream.capture
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stdout.Len()+c.stderr.Len()+len(data) > guardOutputLimit {
		c.overflow = true
		c.cancel()
		return 0, errors.New("native output exceeds1MiB")
	}
	if stream.stderr {
		_, _ = c.stderr.Write(data)
	} else {
		_, _ = c.stdout.Write(data)
	}
	if c.ready != nil && !c.signaled && c.stdout.Len() >= 6 {
		c.signaled = true
		close(c.ready)
	}
	return len(data), nil
}

func (f *nativeGuardFixture) command(ctx context.Context, program string, args []string, input []byte, attach, colima bool) ([]byte, error) {
	if f.charged >= 64 {
		return nil, errors.New("native fixed64-call budget exhausted")
	}
	f.charged++
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) < 2*time.Second {
		return nil, errors.New("native child lacks bounded retirement allowance")
	}
	run, stop := context.WithDeadline(ctx, deadline.Add(-2*time.Second))
	defer stop()
	run, cancel := context.WithCancel(run)
	defer cancel()
	capture := &nativeGuardCapture{cancel: cancel}
	if attach {
		capture.ready = make(chan struct{})
	}
	command := exec.CommandContext(run, program, args...)
	command.Env = []string{"PATH=/opt/homebrew/bin:/usr/bin:/bin", "LC_ALL=C", "LANG=C", "HOME=" + f.dir}
	if colima {
		command.Env[3] = "HOME=" + f.home
	}
	command.Dir = f.dir
	command.Stdin = bytes.NewReader(input)
	command.Stdout = nativeGuardStream{capture: capture}
	command.Stderr = nativeGuardStream{capture: capture, stderr: true}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return killGuardGroup(command.Process.Pid) }
	command.WaitDelay = time.Second
	call := nativeGuardCall{Program: program, Args: args, Started: time.Now(), InputBytes: len(input), InputSHA256: guardDigest(input)}
	err := command.Start()
	if err == nil {
		call.PID = command.Process.Pid
		if attach {
			select {
			case <-capture.ready:
			case <-run.Done():
			}
			cancel()
		}
		waitErr := command.Wait()
		retireErr := retireGuardGroup(command.Process.Pid)
		call.Retired = retireErr == nil
		err = errors.Join(waitErr, retireErr)
		if attach && retireErr == nil && capture.stdout.String() == "READY\n" && capture.stderr.Len() == 0 && waitErr != nil && ctx.Err() == nil {
			err = nil
		}
		if !attach && run.Err() != nil {
			err = errors.Join(err, run.Err())
		}
	}
	if capture.overflow {
		err = errors.Join(err, errors.New("native output overflow"))
	}
	call.Finished = time.Now()
	call.Stdout = capture.stdout.String()
	call.Stderr = capture.stderr.String()
	if err != nil {
		call.Error = err.Error()
	}
	f.calls = append(f.calls, call)
	// Raw per-call evidence is independently bounded; the summary stores hashes
	// to keep its aggregate below1MiB even if all64 calls reach their output cap.
	data, marshalErr := json.Marshal(call)
	if marshalErr == nil && len(data) > 2*guardOutputLimit {
		marshalErr = errors.New("encoded command evidence exceeds2MiB")
	}
	if marshalErr == nil {
		marshalErr = os.WriteFile(filepath.Join(f.dir, fmt.Sprintf("call-%02d.json", f.charged)), data, 0o600)
	}
	f.calls[len(f.calls)-1].Stdout = guardDigest([]byte(call.Stdout))
	f.calls[len(f.calls)-1].Stderr = guardDigest([]byte(call.Stderr))
	err = errors.Join(err, marshalErr, f.save())
	return capture.stdout.Bytes(), err
}

func (f *nativeGuardFixture) build() error {
	if f.charged > 61 {
		return errors.New("builder lacks three remaining fixed calls")
	}
	f.charged += 3 // The unchanged production builder has exactly inspect/build/inspect, without retries.
	var stdout, stderr bytes.Buffer
	args := []string{"--guard", f.options.Guard, "--guard-sha256", f.options.GuardSHA256, "--docker", f.options.Docker, "--docker-sha256", f.options.DockerSHA256, "--context", f.options.Context, "--endpoint", f.options.Endpoint, "--output", f.options.Output}
	code := runCLI(f.work, args, &stdout, &stderr)
	if err := f.artifact("builder-output.json", stdout.Bytes()); err != nil {
		return err
	}
	if err := f.artifact("builder-diagnostic.txt", stderr.Bytes()); err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("actual production builder exit%d: %s", code, stderr.String())
	}
	persisted, err := nativeGuardRead(filepath.Join(f.options.Output, "receipt.json"), 4096)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(persisted, &f.image); err != nil {
		return err
	}
	canonical, err := json.Marshal(f.image)
	if err != nil {
		return err
	}
	if !bytes.Equal(persisted, canonical) || !bytes.Equal(stdout.Bytes(), append(slices.Clone(canonical), '\n')) || f.image.Version != 1 || f.image.BaseReference != guardBaseReference || f.image.GuardSHA256 != f.options.GuardSHA256 || f.image.Platform != "linux/arm64" || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(f.image.BuildLabel) {
		return errors.New("production receipt identity/canonical bytes differ")
	}
	entries, err := os.ReadDir(filepath.Join(f.options.Output, "context"))
	if err != nil {
		return err
	}
	if len(entries) != 2 || entries[0].Name() != "Dockerfile" || entries[1].Name() != "guard" {
		return errors.New("build context contains other inputs")
	}
	if _, err = readGuardProgram(f.work, filepath.Join(f.options.Output, "context/guard"), f.options.GuardSHA256, guardProgramLimit, false); err != nil {
		return err
	}
	recipe, err := nativeGuardRead(filepath.Join(f.options.Output, "context/Dockerfile"), 4096)
	if err != nil {
		return err
	}
	wantRecipe := "FROM " + guardBaseReference + "\nCOPY guard /usr/local/bin/oro-factory-guard\nUSER 65534:65534\nENTRYPOINT [\"/usr/local/bin/oro-factory-guard\"]\n"
	if string(recipe) != wantRecipe {
		return errors.New("retained minimal build recipe differs")
	}
	base, err := f.docker(f.work, nil, "image", "inspect", "--format", "{{json .}}", guardBaseReference)
	if err != nil {
		return err
	}
	id, err := inspectGuardImage(base, "")
	if err != nil || id != f.image.BaseID {
		return errors.Join(errors.New("base ID differs"), err)
	}
	derived, err := f.docker(f.work, nil, "image", "inspect", "--format", "{{json .}}", f.image.ImageID)
	if err != nil {
		return err
	}
	id, err = inspectGuardImage(derived, f.image.BuildLabel)
	if err != nil || id != f.image.ImageID || id == f.image.BaseID {
		return errors.Join(errors.New("derived image identity differs"), err)
	}
	return f.save()
}

func (f *nativeGuardFixture) inspect(ctx context.Context, kind, id string) (nativeGuardObject, error) {
	data, err := f.docker(ctx, nil, kind, "inspect", "--format", "{{json .}}", id)
	var object nativeGuardObject
	if err != nil {
		return object, err
	}
	if err = validateGuardImageJSON(data); err != nil {
		return object, err
	}
	err = json.Unmarshal(data, &object)
	return object, err
}

func (f *nativeGuardFixture) resource(kind, role, image string, labels map[string]string) *nativeGuardResource {
	owned := make(map[string]string, len(labels)+2)
	for key, value := range labels {
		owned[key] = value
	}
	owned[nativeGuardLabel] = f.nonce
	owned[nativeGuardRole] = role
	r := &nativeGuardResource{Kind: kind, Name: "oro-guard-native-" + f.nonce + "-" + role, Role: role, Image: image, Labels: owned}
	f.resources = append(f.resources, r)
	return r
}

func nativeGuardIsolationArgs(r *nativeGuardResource) []string {
	args := []string{"create", "--pull=never", "--name", r.Name}
	keys := make([]string, 0, len(r.Labels))
	for key := range r.Labels {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		args = append(args, "--label", key+"="+r.Labels[key])
	}
	return append(args, "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--cgroupns", "private", "--ipc", "private", "--memory", "64m", "--memory-swap", "64m", "--cpus", "0.5", "--pids-limit", "64", "--log-driver", "none", "--shm-size", "1m", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=4194304,nr_inodes=1024,mode=1777", "--user", "65534:65534")
}

func (f *nativeGuardFixture) createContainer(r *nativeGuardResource, args []string) error {
	r.Attempted = true
	if err := f.save(); err != nil {
		return err
	}
	data, err := f.docker(f.work, nil, args...)
	if err != nil {
		return err
	} // Lost acknowledgments never authorize adopting a name.
	id := strings.TrimSpace(string(data))
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(id) {
		return errors.New("create did not acknowledge exact container ID")
	}
	object, err := f.inspect(f.work, "container", id)
	if err != nil {
		return err
	}
	if object.ID != id || object.Name != "/"+r.Name || object.Image != r.Image || object.Created == "" || !reflect.DeepEqual(object.Config.Labels, r.Labels) {
		return errors.New("new container differs from intended ownership")
	}
	r.Identity = &object
	if err = f.save(); err != nil {
		return err
	}
	return nativeGuardIsolation(object, r.Role)
}

func nativeGuardIsolation(object nativeGuardObject, role string) error {
	h := object.HostConfig
	if object.Config.User != "65534:65534" || !h.ReadonlyRootfs || h.Privileged || h.NetworkMode != "none" || h.PidMode != "" || h.IpcMode != "private" || h.CgroupnsMode != "private" {
		return errors.New("container namespace isolation differs")
	}
	if h.Memory != 64<<20 || h.MemorySwap != h.Memory || h.NanoCpus != 500000000 || h.PidsLimit != 64 || h.ShmSize != 1<<20 || h.LogConfig.Type != "none" {
		return errors.New("container resource limits differ")
	}
	if len(h.Binds) != 0 || len(h.CapAdd) != 0 || !slices.Equal(h.CapDrop, []string{"ALL"}) || !slices.Equal(h.SecurityOpt, []string{"no-new-privileges"}) || len(h.Tmpfs) != 1 || h.Tmpfs["/tmp"] != "rw,nosuid,nodev,noexec,size=4194304,nr_inodes=1024,mode=1777" {
		return errors.New("container authority or scratch limits differ")
	}
	volumes := 0
	for _, mount := range object.Mounts {
		if mount.Type == "volume" && role == "guard" && mount.Destination == "/source" && mount.RW {
			volumes++
			continue
		}
		if mount.Type != "tmpfs" || mount.Destination != "/tmp" {
			return errors.New("unexpected container mount")
		}
	}
	if (role == "guard" && volumes != 1) || (role != "guard" && volumes != 0) {
		return errors.New("container source mount differs")
	}
	return nil
}

func (f *nativeGuardFixture) createVolume() (*nativeGuardResource, error) {
	r := f.resource("volume", "source", "", nil)
	// Unlike container create, volume create is idempotent by name. A positive
	// missing observation must precede this attempt; an existing name is rejected.
	if err := f.absence(f.work, "volume", r.Name); err != nil {
		return r, err
	}
	r.Attempted = true
	if err := f.save(); err != nil {
		return r, err
	}
	args := []string{"volume", "create", "--driver", "local", "--opt", "type=tmpfs", "--opt", "device=tmpfs", "--opt", "o=" + nativeGuardVolumeOptions, "--label", nativeGuardLabel + "=" + f.nonce, "--label", nativeGuardRole + "=source", r.Name}
	data, err := f.docker(f.work, nil, args...)
	if err != nil {
		return r, err
	}
	if strings.TrimSpace(string(data)) != r.Name {
		return r, errors.New("volume acknowledgment differs")
	}
	object, err := f.inspect(f.work, "volume", r.Name)
	if err != nil {
		return r, err
	}
	if object.Name != r.Name || object.CreatedAt == "" || object.Driver != "local" || !reflect.DeepEqual(object.Labels, r.Labels) || !reflect.DeepEqual(object.Options, map[string]string{"type": "tmpfs", "device": "tmpfs", "o": nativeGuardVolumeOptions}) {
		return r, errors.New("volume identity differs")
	}
	r.Identity = &object
	return r, f.save()
}

func (f *nativeGuardFixture) namespace() error {
	base, err := f.inspect(f.work, "image", f.image.BaseID)
	if err != nil {
		return err
	}
	sentinel := f.resource("container", "sentinel", f.image.BaseID, base.Config.Labels)
	args := append(nativeGuardIsolationArgs(sentinel), "--entrypoint", "/bin/sleep", sentinel.Image, "120")
	if err = f.createContainer(sentinel, args); err != nil {
		return err
	}
	if _, err = f.docker(f.work, nil, "start", sentinel.Identity.ID); err != nil {
		return err
	}
	before, err := f.boundContainer(f.work, sentinel, true)
	if err != nil {
		return err
	}
	f.observations["sentinel_before"] = before
	volume, err := f.createVolume()
	if err != nil {
		return err
	}
	derived, err := f.inspect(f.work, "image", f.image.ImageID)
	if err != nil {
		return err
	}
	guard := f.resource("container", "guard", f.image.ImageID, derived.Config.Labels)
	f.deadline = time.Now().Add(30 * time.Second).UTC()
	if f.deadline.Add(10 * time.Second).After(f.hardStop.Add(-30 * time.Second)) {
		return errors.New("insufficient original work budget for namespace proof")
	}
	f.observations["guard_deadline"] = f.deadline
	args = append(nativeGuardIsolationArgs(guard), "--mount", "type=volume,src="+volume.Name+",dst=/source,volume-nocopy", guard.Image, "--deadline", f.deadline.Format(time.RFC3339Nano))
	if err = f.createContainer(guard, args); err != nil {
		return err
	}
	if !slices.Equal(guard.Identity.Config.Entrypoint, []string{"/usr/local/bin/oro-factory-guard"}) || !slices.Equal(guard.Identity.Config.Cmd, []string{"--deadline", f.deadline.Format(time.RFC3339Nano)}) {
		return errors.New("guard command differs from original deadline")
	}
	for _, mount := range guard.Identity.Mounts {
		if mount.Type == "volume" && mount.Name != volume.Name {
			return errors.New("source volume mount identity differs")
		}
	}
	if err = f.attach(guard); err != nil {
		return err
	}
	// A tmpfs keeper must already be running before cp. This positive running
	// observation follows the attaching host process group's positive ESRCH.
	if _, err = f.boundContainer(f.work, guard, true); err != nil {
		return err
	}
	if err = f.seedAndProbe(guard); err != nil {
		return err
	}
	if err = f.attacks(guard); err != nil {
		return err
	}
	remaining := time.Until(f.deadline.Add(400 * time.Millisecond))
	if remaining < time.Second {
		return errors.New("setup consumed original autonomous observation interval")
	}
	f.observations["autonomous_wait_started"] = time.Now()
	if err = f.save(); err != nil {
		return err
	}
	// No daemon call, polling, or cleanup occurs during this original-deadline crossing.
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-f.work.Done():
		return f.work.Err()
	}
	if err = f.expired(guard); err != nil {
		return err
	}
	after, err := f.boundContainer(f.work, sentinel, true)
	if err != nil {
		return err
	}
	if before.State != after.State {
		return errors.New("foreign sentinel state changed during guard work")
	}
	f.observations["sentinel_after"] = after
	return f.save()
}

func (f *nativeGuardFixture) attach(guard *nativeGuardResource) error {
	ctx, cancel := context.WithTimeout(f.work, 5*time.Second)
	defer cancel()
	if _, err := readGuardProgram(ctx, f.options.Docker, f.options.DockerSHA256, guardDockerLimit, false); err != nil {
		return err
	}
	if err := validateGuardEndpoint(f.options.Endpoint); err != nil {
		return err
	}
	_, err := f.command(ctx, f.options.Docker, []string{"--config", f.config, "--context", nativeGuardContext, "start", "--attach", guard.Identity.ID}, nil, true, false)
	if err == nil {
		f.observations["ready_and_client_retired"] = time.Now()
	}
	return err
}

func nativeGuardSeed() ([]byte, error) {
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, entry := range []struct {
		name, data string
		mode       int64
		kind       byte
	}{{"nested/", "", 0o755, tar.TypeDir}, {"nested/original.txt", "original\n", 0o644, tar.TypeReg}, {"seed.txt", "seed\n", 0o644, tar.TypeReg}} {
		header := &tar.Header{Name: entry.name, Mode: entry.mode, Uid: 1000, Gid: 1000, Size: int64(len(entry.data)), Typeflag: entry.kind, Format: tar.FormatUSTAR, ModTime: time.Unix(0, 0)}
		if err := writer.WriteHeader(header); err != nil {
			return nil, err
		}
		if _, err := writer.Write([]byte(entry.data)); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	if buffer.Len() > 8<<10 {
		return nil, errors.New("trusted seed exceeds8KiB")
	}
	return buffer.Bytes(), nil
}

func (f *nativeGuardFixture) seedAndProbe(guard *nativeGuardResource) error {
	seed, err := nativeGuardSeed()
	if err != nil {
		return err
	}
	if err = f.artifact("seed.tar", seed); err != nil {
		return err
	}
	f.observations["seed_sha256"] = guardDigest(seed)
	var headers []*tar.Header
	reader := tar.NewReader(bytes.NewReader(seed))
	for {
		header, readErr := reader.Next()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
		headers = append(headers, header)
	}
	f.observations["seed_headers"] = headers
	if _, err = f.docker(f.work, seed, "cp", "-", guard.Identity.ID+":/source"); err != nil {
		return err
	}
	script := `set -eu
test "$(id -u):$(id -g)" = 1000:1000
test "$(stat -c '%u:%g:%a' /source/nested)" = 1000:1000:755
test "$(stat -c '%u:%g:%a' /source/nested/original.txt)" = 1000:1000:644
test "$(stat -c '%u:%g:%a' /source/seed.txt)" = 1000:1000:644
printf 'appended\n' >> /source/nested/original.txt
printf 'created\n' > /source/nested/created.txt
printf 'original\nappended\n' | cmp -s - /source/nested/original.txt
printf 'created\n' | cmp -s - /source/nested/created.txt
printf 'seed\n' | cmp -s - /source/seed.txt
printf UID1000_WRITABLE`
	data, err := f.docker(f.work, nil, "exec", "--user", "1000:1000", guard.Identity.ID, "/bin/sh", "-c", script)
	if err != nil || string(data) != "UID1000_WRITABLE" {
		return errors.Join(errors.New("stdin tar ownership/writability proof failed"), err)
	}
	return nil
}

func (f *nativeGuardFixture) attacks(guard *nativeGuardResource) error {
	script := `set -eu
test "$(id -u):$(id -g)" = 1000:1000
test "$(awk '/^Uid:/ {print $2}' /proc/1/status)" = 65534
test "$(awk '/^CapEff:/ {print $2}' /proc/self/status)" = 0000000000000000
test "$(awk '/^NoNewPrivs:/ {print $2}' /proc/self/status)" = 1
for signal in STOP KILL TERM; do if kill -"$signal" 1 2>/dev/null; then exit 1; fi; done
if (printf replace > /usr/local/bin/oro-factory-guard) 2>/dev/null; then exit 1; fi
if (printf extend > /proc/1/fd/0) 2>/dev/null; then exit 1; fi
if (printf extend > /proc/1/mem) 2>/dev/null; then exit 1; fi
if /usr/local/bin/oro-factory-guard --deadline 2099-01-01T00:00:00Z >/tmp/replacement 2>&1; then exit 1; fi
printf ATTACKS_DENIED`
	data, err := f.docker(f.work, nil, "exec", "--user", "1000:1000", guard.Identity.ID, "/bin/sh", "-c", script)
	if err != nil || string(data) != "ATTACKS_DENIED" {
		return errors.Join(errors.New("guard authority attack proof failed"), err)
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	writer := `echo $$ > /tmp/writer.pid; while :; do echo tick >> /tmp/heartbeat; sleep 0.05; done`
	inner := "setsid /bin/sh -c " + quote(writer) + " </dev/null >/dev/null 2>/tmp/writer.log &"
	outer := "setsid /bin/sh -c " + quote(inner) + " </dev/null >/dev/null 2>/tmp/detach.log &"
	if _, err = f.docker(f.work, nil, "exec", "--user", "1000:1000", guard.Identity.ID, "/bin/sh", "-c", outer); err != nil {
		return err
	}
	proof := `set -eu
for n in 1 2 3 4 5 6 7 8 9 10; do if test -s /tmp/writer.pid && test -s /tmp/heartbeat; then break; fi; sleep 0.05; done
pid=$(cat /tmp/writer.pid)
test "$(awk '/^PPid:/ {print $2}' /proc/$pid/status)" = 1
test "$(awk '{print $6}' /proc/$pid/stat)" = "$pid"
before=$(wc -c < /tmp/heartbeat); sleep 0.15; after=$(wc -c < /tmp/heartbeat)
test "$after" -gt "$before"
printf 'writer_pid=%s heartbeat_before=%s heartbeat_after=%s' "$pid" "$before" "$after"`
	data, err = f.docker(f.work, nil, "exec", "--user", "1000:1000", guard.Identity.ID, "/bin/sh", "-c", proof)
	if err == nil {
		f.observations["detached_writer"] = string(data)
	}
	return err
}

func (f *nativeGuardFixture) boundContainer(ctx context.Context, r *nativeGuardResource, running bool) (nativeGuardObject, error) {
	object, err := f.inspect(ctx, "container", r.Identity.ID)
	if err != nil {
		return object, err
	}
	before := r.Identity
	if object.ID != before.ID || object.Name != before.Name || object.Created != before.Created || object.Image != before.Image || !reflect.DeepEqual(object.Config, before.Config) || !reflect.DeepEqual(object.HostConfig, before.HostConfig) || !reflect.DeepEqual(object.Mounts, before.Mounts) {
		return object, errors.New("container immutable binding changed")
	}
	if running && (!object.State.Running || object.State.PID <= 0 || object.State.Status != "running" || object.State.Paused || object.State.Restarting || object.State.Dead) {
		return object, errors.New("container is not positively running")
	}
	return object, nil
}

func (f *nativeGuardFixture) expired(guard *nativeGuardResource) error {
	object, err := f.boundContainer(f.work, guard, false)
	if err != nil {
		return err
	}
	state := object.State
	finished, err := time.Parse(time.RFC3339Nano, state.FinishedAt)
	if err != nil || state.Running || state.PID != 0 || state.Status != "exited" || state.OOMKilled || state.ExitCode != 0 || state.Error != "" || finished.Before(f.deadline) || finished.After(f.deadline.Add(2*time.Second)) {
		return errors.New("namespace did not expire normally at original deadline")
	}
	f.observations["guard_expired"] = object
	_, err = f.docker(f.work, nil, "top", guard.Identity.ID)
	if !f.refusedNotRunning(err, guard.Identity.ID) {
		return errors.New("stopped namespace still exposes top or unknown refusal")
	}
	_, err = f.docker(f.work, nil, "exec", "--user", "1000:1000", guard.Identity.ID, "/bin/true")
	if !f.refusedNotRunning(err, guard.Identity.ID) {
		return errors.New("stopped namespace still accepts exec or unknown refusal")
	}
	return nil
}

func (f *nativeGuardFixture) refusedNotRunning(err error, id string) bool {
	if err == nil || len(f.calls) == 0 {
		return false
	}
	// Read raw bounded command evidence because in-memory transcripts retain digests.
	data, readErr := nativeGuardRead(filepath.Join(f.dir, fmt.Sprintf("call-%02d.json", f.charged)), 2*guardOutputLimit)
	var call nativeGuardCall
	if readErr != nil || json.Unmarshal(data, &call) != nil || !call.Retired {
		return false
	}
	return strings.TrimSpace(call.Stderr) == "Error response from daemon: container "+id+" is not running" && strings.TrimSpace(call.Stdout) == ""
}

func (f *nativeGuardFixture) cleanup(ctx context.Context) error {
	// Retire only positively bound objects. Unknown acknowledgments remain
	// recorded and retained; a matching name alone is never cleanup authority.
	var failures []error
	for i := len(f.resources) - 1; i >= 0; i-- {
		r := f.resources[i]
		if r.Kind == "container" && r.Identity != nil {
			failures = append(failures, f.removeContainer(ctx, r))
		}
	}
	for _, r := range f.resources {
		if r.Kind == "volume" && r.Identity != nil {
			failures = append(failures, f.removeVolume(ctx, r))
		}
		if r.Attempted && !r.Absent {
			failures = append(failures, fmt.Errorf("resource retained or uncertain: %s", r.Name))
		}
	}
	if f.image.ImageID != "" {
		failures = append(failures, f.removeImage(ctx))
	}
	return errors.Join(failures...)
}

func (f *nativeGuardFixture) removeContainer(ctx context.Context, r *nativeGuardResource) error {
	object, err := f.boundContainer(ctx, r, false)
	if err != nil {
		return err
	}
	if object.State.Running || object.State.PID != 0 {
		if _, err = f.docker(ctx, nil, "stop", "--signal", "SIGKILL", "--timeout", "0", object.ID); err != nil {
			return err
		}
		object, err = f.boundContainer(ctx, r, false)
		if err != nil {
			return err
		}
	}
	started, parseErr := time.Parse(time.RFC3339Nano, object.State.StartedAt)
	neverStarted := object.State.Status == "created" && parseErr == nil && started.IsZero()
	if object.State.Running || object.State.PID != 0 || object.State.Paused || object.State.Restarting || object.State.Dead || (object.State.Status != "exited" && !neverStarted) {
		return errors.New("container cleanup lacks positive stopped evidence")
	}
	if _, err = f.docker(ctx, nil, "container", "rm", object.ID); err != nil {
		return err
	}
	if err = f.absence(ctx, "container", object.ID); err != nil {
		return err
	}
	r.Absent = true
	return f.save()
}

func (f *nativeGuardFixture) removeVolume(ctx context.Context, r *nativeGuardResource) error {
	for _, other := range f.resources {
		if other.Kind == "container" && other.Role == "guard" && other.Attempted && !other.Absent {
			return errors.New("source retained until guard positively absent")
		}
	}
	object, err := f.inspect(ctx, "volume", r.Name)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(object, *r.Identity) {
		return errors.New("volume lifetime/options/ownership changed")
	}
	if _, err = f.docker(ctx, nil, "volume", "rm", r.Name); err != nil {
		return err
	}
	if err = f.absence(ctx, "volume", r.Name); err != nil {
		return err
	}
	r.Absent = true
	return f.save()
}

func (f *nativeGuardFixture) removeImage(ctx context.Context) error {
	data, err := f.docker(ctx, nil, "image", "inspect", "--format", "{{json .}}", f.image.ImageID)
	if err != nil {
		return err
	}
	id, err := inspectGuardImage(data, f.image.BuildLabel)
	if err != nil || id != f.image.ImageID {
		return errors.Join(errors.New("derived image cleanup binding differs"), err)
	}
	var object nativeGuardObject
	if err = json.Unmarshal(data, &object); err != nil {
		return err
	}
	if !slices.Equal(object.RepoTags, []string{"oro-factory-guard:" + f.image.BuildLabel}) {
		return errors.New("derived image has other tags; retain it")
	}
	if _, err = f.docker(ctx, nil, "image", "rm", f.image.ImageID); err != nil {
		return err
	}
	if err = f.absence(ctx, "image", f.image.ImageID); err != nil {
		return err
	}
	f.observations["derived_image_absent"] = true
	return nil
}

func (f *nativeGuardFixture) absence(ctx context.Context, kind, id string) error {
	_, err := f.docker(ctx, nil, kind, "inspect", "--format", "{{json .}}", id)
	if err == nil {
		return errors.New("removed resource remains present")
	}
	data, readErr := nativeGuardRead(filepath.Join(f.dir, fmt.Sprintf("call-%02d.json", f.charged)), 2*guardOutputLimit)
	var call nativeGuardCall
	if readErr != nil || json.Unmarshal(data, &call) != nil || !call.Retired {
		return errors.New("absence lacks joined exact daemon refusal")
	}
	want := "Error response from daemon: No such " + kind + ": " + id
	if kind == "volume" {
		want = "Error response from daemon: get " + id + ": no such volume"
	}
	if strings.TrimSpace(call.Stderr) != want || strings.TrimSpace(call.Stdout) != "" {
		return fmt.Errorf("unrecognized absence: %q", call.Stderr)
	}
	return nil
}

func TestGuardImageNativeFixture(t *testing.T) {
	t.Run("owned readiness client retires without a backend", func(t *testing.T) {
		dir := t.TempDir()
		f := &nativeGuardFixture{dir: dir, observations: make(map[string]any)}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		data, err := f.command(ctx, "/bin/sh", []string{"-c", "printf 'READY\\n'; sleep 60"}, nil, true, false)
		if err != nil || string(data) != "READY\n" || len(f.calls) != 1 || !f.calls[0].Retired {
			t.Fatalf("readiness retirement: %q %v %+v", data, err, f.calls)
		}
		if err = syscall.Kill(-f.calls[0].PID, 0); !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("group survived: %v", err)
		}
	})
	t.Run("output overflow and call budget stop owned commands", func(t *testing.T) {
		f := &nativeGuardFixture{dir: t.TempDir(), observations: make(map[string]any)}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		_, err := f.command(ctx, "/bin/sh", []string{"-c", "/usr/bin/yes x"}, nil, false, false)
		if err == nil || len(f.calls) != 1 || !f.calls[0].Retired {
			t.Fatalf("overflow did not retire: %v %+v", err, f.calls)
		}
		if err = syscall.Kill(-f.calls[0].PID, 0); !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("overflow group survived: %v", err)
		}
		f.charged = 64
		_, err = f.command(ctx, "/must-not-execute", nil, nil, false, false)
		if err == nil || len(f.calls) != 1 || f.charged != 64 {
			t.Fatal("call cap allowed another effect")
		}
	})
	t.Run("exact bounded USTAR seed", func(t *testing.T) {
		data, err := nativeGuardSeed()
		if err != nil {
			t.Fatal(err)
		}
		if len(data) > 8192 {
			t.Fatal("seed exceeded bound")
		}
		reader := tar.NewReader(bytes.NewReader(data))
		names := []string{"nested/", "nested/original.txt", "seed.txt"}
		for i, name := range names {
			header, readErr := reader.Next()
			if readErr != nil {
				t.Fatal(readErr)
			}
			mode, kind := int64(0o644), byte(tar.TypeReg)
			if i == 0 {
				mode, kind = 0o755, tar.TypeDir
			}
			if header.Name != name || header.Mode != mode || header.Typeflag != kind || header.Uid != 1000 || header.Gid != 1000 || header.Format != tar.FormatUSTAR {
				t.Fatalf("unexpected header %+v", header)
			}
			content, readErr := io.ReadAll(reader)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(content) > 64 {
				t.Fatal("seed content overflow")
			}
		}
		if _, err = reader.Next(); !errors.Is(err, io.EOF) {
			t.Fatal("unexpected extra seed entry")
		}
	})
	t.Run("guest exact capacity boundaries", func(t *testing.T) {
		for _, tc := range []struct {
			disk, mem string
			valid     bool
		}{{"2097152", "524288", true}, {"2097151", "524288", false}, {"2097152", "524287", false}, {"-1", "524288", false}, {"2097152", "missing", false}} {
			disk := []byte("Filesystem 1024-blocks Used Available Capacity Mounted\n/dev/root 4000000 1 " + tc.disk + " 1% /var/lib/docker\n")
			err := nativeGuardCapacity(disk, []byte("MemAvailable: "+tc.mem+" kB\n"))
			if (err == nil) != tc.valid {
				t.Fatalf("boundary %+v: %v", tc, err)
			}
		}
	})
}
