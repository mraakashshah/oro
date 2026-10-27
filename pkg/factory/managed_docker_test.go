package factory //nolint:testpackage // Exercise the private live-call adapter and durable authority boundary.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestManagedDockerVolumeCreate(t *testing.T) {
	t.Run("fixed create inspect and acknowledged exact lifetime", testDockerAdapterCreate)
	t.Run("uncertain create never dispatches again", testDockerAdapterFailures)
	t.Run("inspection failure preserves acknowledgment and capacity", testDockerAdapterInspectFailure)
	t.Run("reopened prepared dispatch keeps original reservation", testDockerAdapterPrepared)
	t.Run("configuration and committed admission are exact", testDockerAdapterConfig)
	t.Run("bounded exact volume inspection", testDockerAdapterInspection)
	t.Run("prepared reader inherits original invocation deadline", testDockerAdapterPreparedReaderDeadline)
	t.Run("concurrent prepared consumers dispatch once", testDockerAdapterConcurrentPrepared)
	t.Run("every call reopens the exact verified configuration", testDockerAdapterConfigReread)
	t.Run("readiness helper identity cannot drift", testDockerAdapterOroReread)
	t.Run("model helper identity compares values", testDockerAdapterModelHelperReread)
	t.Run("inactive dependencies and ownership never invoke", testDockerAdapterInactive)
	t.Run("local driver options encode exact keys and values", testDockerAdapterDriverOptions)
}

func testDockerAdapterDriverOptions(t *testing.T) {
	f := newDockerAdapterFixture(t, nil)
	if _, err := f.adapter.createVolume(t.Context()); err != nil {
		t.Fatal(err)
	}
	options := make(map[string]string)
	args := f.runtime.calls[0].Args
	for index, arg := range args {
		if arg != "--opt" {
			continue
		}
		if index+1 >= len(args) {
			t.Fatal("local driver option lacks its key=value operand")
		}
		key, value, ok := strings.Cut(args[index+1], "=")
		if !ok || key == "" {
			t.Fatalf("invalid local driver option %q", args[index+1])
		}
		if _, duplicate := options[key]; duplicate {
			t.Fatalf("duplicate local driver option %q", key)
		}
		options[key] = value
	}
	runtime := f.manifest.Spec.Runtime
	expected := map[string]string{
		"type": "tmpfs", "device": "tmpfs",
		"o": fmt.Sprintf("size=%d,nr_inodes=%d,uid=1000,gid=1000", runtime.SourceBytes, runtime.SourceInodes),
	}
	if !reflect.DeepEqual(options, expected) {
		t.Fatalf("local driver option map = %v, want %v", options, expected)
	}
}

func testDockerAdapterInactive(t *testing.T) {
	f := newDockerAdapterFixture(t, nil)
	reader := func(context.Context) (ManagedDockerConfig, error) { return f.config, nil }
	for _, mode := range []string{"nil journal", "nil runtime", "nil reader", "zero generation", "canceled", "manifest", "reader error", "late reader"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			manifest := f.manifest
			generation := f.generation
			journal := f.journal
			var host HostRuntime = f.runtime
			read := ManagedDockerConfigReader(reader)
			switch mode {
			case "nil journal":
				journal = nil
			case "nil runtime":
				host = nil
			case "nil reader":
				read = nil
			case "zero generation":
				generation = 0
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "manifest":
				manifest.PolicyHash = strings.Repeat("f", 64)
			case "reader error":
				read = func(context.Context) (ManagedDockerConfig, error) {
					return ManagedDockerConfig{}, errors.New("missing verified artifact")
				}
			case "late reader":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				read = func(context.Context) (ManagedDockerConfig, error) { cancel(); return f.config, nil }
			}
			if _, err := newManagedDockerVolumes(ctx, manifest, generation, journal, host, read); err == nil {
				t.Fatal("invalid dependency accepted")
			}
		})
	}
	intent := f.reserve(t)
	if _, _, err := f.adapter.inspectVolume(t.Context(), intent.Volume); err == nil {
		t.Fatal("never-dispatched intent inspected")
	}
	generation, _, err := f.journal.advanceEpoch(t.Context(), "supervisor", "replacement", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.adapter.dispatchPrepared(t.Context(), intent.Volume); err == nil {
		t.Fatal("stale prepared owner dispatched")
	}
	replacement, err := newManagedDockerVolumes(t.Context(), f.manifest, generation, f.journal, f.runtime, reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := replacement.dispatchPrepared(t.Context(), intent.Volume); err == nil {
		t.Fatal("replacement reused old host reservation")
	}
	if len(f.runtime.calls) != 0 {
		t.Fatal("inactive work invoked")
	}
}

func testDockerAdapterConcurrentPrepared(t *testing.T) {
	f := newDockerAdapterFixture(t, nil)
	intent := f.reserve(t)
	other, err := newManagedDockerVolumes(t.Context(), f.manifest, f.generation, f.journal, f.runtime, func(context.Context) (ManagedDockerConfig, error) { return f.config, nil })
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var group sync.WaitGroup
	for _, adapter := range []*managedDockerVolumes{f.adapter, other} {
		group.Go(func() { _, err := adapter.dispatchPrepared(t.Context(), intent.Volume); results <- err })
	}
	group.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		}
	}
	if wins != 1 || len(f.runtime.calls) != 2 {
		t.Fatalf("prepared consumers won=%d calls=%d", wins, len(f.runtime.calls))
	}
	if got := dockerVolumeCounts(t, f.journal); got[0] != 2 || got[1] != 3 || got[2] != 1 {
		t.Fatalf("racing consumer spent capacity: %v", got)
	}
}

func testDockerAdapterConfigReread(t *testing.T) {
	for _, stage := range []string{"dispatch", "inspection"} {
		t.Run(stage, func(t *testing.T) {
			f := newDockerAdapterFixture(t, nil)
			reads := 0
			f.adapter.readConfig = func(context.Context) (ManagedDockerConfig, error) {
				reads++
				config := f.config
				if stage == "dispatch" && reads == 2 {
					config.DockerSHA256 = strings.Repeat("c", 64)
				}
				return config, nil
			}
			if stage == "inspection" {
				f.runtime.change = func(_ HostInvocation, _ *HostResult) error {
					f.config.DockerEndpoint = "unix:///private/tmp/changed.sock"
					return nil
				}
			}
			if _, err := f.adapter.createVolume(t.Context()); err == nil {
				t.Fatal("changed call configuration accepted")
			}
			wantCalls, wantPhase := 0, "prepared"
			if stage == "inspection" {
				wantCalls, wantPhase = 1, "acknowledged"
			}
			if len(f.runtime.calls) != wantCalls {
				t.Fatalf("invoked with changed configuration: %d", len(f.runtime.calls))
			}
			var body []byte
			if err := f.journal.conn.QueryRowContext(t.Context(), "SELECT body FROM managed_docker_intents").Scan(&body); err != nil {
				t.Fatal(err)
			}
			var intent managedDockerVolumeIntent
			if err := json.Unmarshal(body, &intent); err != nil || intent.Phase != wantPhase {
				t.Fatalf("phase after config change %+v %v", intent, err)
			}
			if counts := dockerVolumeCounts(t, f.journal); counts[0] != 1 || counts[1] != 2 || counts[2] != 1 {
				t.Fatal("config mismatch spent an extra invocation")
			}
		})
	}
}

func testDockerAdapterOroReread(t *testing.T) {
	for _, field := range []string{"path", "hash"} {
		t.Run(field, func(t *testing.T) {
			f := newDockerAdapterFixture(t, nil)
			before := dockerVolumeCounts(t, f.journal)
			if field == "path" {
				f.config.OroPath += "-changed"
			} else {
				f.config.OroSHA256 = strings.Repeat("e", 64)
			}
			if _, err := f.adapter.createVolume(t.Context()); err == nil {
				t.Fatal("changed readiness helper identity accepted")
			}
			if after := dockerVolumeCounts(t, f.journal); after != before || len(f.runtime.calls) != 0 {
				t.Fatal("helper identity drift had effects")
			}
		})
	}
}

func testDockerAdapterPreparedReaderDeadline(t *testing.T) {
	f := newDockerAdapterFixture(t, nil)
	f.deadline = time.Now().UTC().Add(time.Second)
	intent := f.reserve(t)
	var seen time.Time
	f.adapter.readConfig = func(ctx context.Context) (ManagedDockerConfig, error) {
		seen, _ = ctx.Deadline()
		if !seen.Equal(intent.CreateDeadline) {
			return ManagedDockerConfig{}, errors.New("reader lacks original deadline")
		}
		<-ctx.Done()
		return ManagedDockerConfig{}, ctx.Err()
	}
	before := dockerVolumeCounts(t, f.journal)
	if _, err := f.adapter.dispatchPrepared(t.Context(), intent.Volume); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("late reader did not retain original deadline: %v", err)
	}
	if !seen.Equal(intent.CreateDeadline) {
		t.Fatalf("reader deadline %v differs from original %v", seen, intent.CreateDeadline)
	}
	if after := dockerVolumeCounts(t, f.journal); after != before || len(f.runtime.calls) != 0 {
		t.Fatal("expired configuration reader dispatched or spent capacity")
	}
	got, err := loadManagedDockerVolume(t.Context(), f.journal, intent.Volume)
	if err != nil || got.Phase != "prepared" {
		t.Fatalf("late reader changed prepared intent: %+v %v", got, err)
	}
}

type dockerAdapterFixture struct {
	dockerVolumeFixture
	config    ManagedDockerConfig
	runtime   *dockerAdapterRuntime
	adapter   *managedDockerVolumes
	readCount int
}

type dockerAdapterRuntime struct {
	t         *testing.T
	journal   *managedJournal
	calls     []HostInvocation
	intent    managedDockerVolumeIntent
	change    func(HostInvocation, *HostResult) error
	unretired bool
	mu        sync.Mutex
}

func (r *dockerAdapterRuntime) Invoke(ctx context.Context, call HostInvocation) (HostResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call)
	result := HostResult{}
	if slices.Contains(call.Args, "create") {
		r.readDispatchedIntent(ctx, call.Scope)
		result.Stdout = []byte(r.intent.Binding.Name + "\n")
	} else {
		result = dockerAdapterInspection(r.t, r.intent.Binding)
	}
	if err := r.journal.RecordHello(ctx, call.Scope, 101, 101); err != nil {
		return HostResult{}, err
	}
	if !r.unretired {
		if err := r.journal.MarkQuiescent(ctx, call.Scope); err != nil {
			return HostResult{}, err
		}
	}
	if r.change != nil {
		if err := r.change(call, &result); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (r *dockerAdapterRuntime) readDispatchedIntent(ctx context.Context, scope ScopeHandle) {
	r.t.Helper()
	var body []byte
	if err := r.journal.conn.QueryRowContext(ctx, "SELECT body FROM managed_docker_intents WHERE create_scope_id=?", scope.ID).Scan(&body); err != nil {
		r.t.Fatal(err)
	}
	if err := json.Unmarshal(body, &r.intent); err != nil {
		r.t.Fatal(err)
	}
	if r.intent.Phase != "dispatching" || r.intent.CreateHost != scope {
		r.t.Fatal("host entered without exact committed dispatch")
	}
}

func (r *dockerAdapterRuntime) Retire(ctx context.Context, scope ScopeHandle) error {
	return r.journal.MarkQuiescent(ctx, scope)
}

func newDockerAdapterFixture(t *testing.T, change func(*ManagedSpec)) *dockerAdapterFixture {
	t.Helper()
	root, spec, reservation := managedAdmissionFixture(t)
	spec.Runtime.HostRequiredBytes = 1 << 40
	if change != nil {
		change(&spec)
	}
	reservation.Bytes = spec.Runtime.HostRequiredBytes
	reservation = reservationForIntendedSpec(t, reservation, spec)
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
		t.Fatal(err)
	}
	manifest, err := loadManagedManifest(t.Context(), root, spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	journal := openJournalFixture(t, root, spec.ID)
	generation, _, err := journal.beginGeneration(t.Context(), "supervisor", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f := &dockerAdapterFixture{dockerVolumeFixture: dockerVolumeFixture{root, manifest, journal, generation, time.Now().UTC().Add(30 * time.Second)}}
	f.config = ManagedDockerConfig{
		StateRoot: root, ObjectiveID: spec.ID, PolicyHash: manifest.PolicyHash, Deadline: manifest.Deadline, HardStop: manifest.HardStop,
		OroPath: "/usr/local/bin/oro", OroSHA256: strings.Repeat("d", 64),
		RuntimeConfigPath: filepath.Join(root, spec.ID, "runtime", "runtime.json"), RuntimeConfigSHA256: strings.Repeat("a", 64), DockerPath: "/opt/homebrew/Cellar/docker/29.9.2/bin/docker", DockerSHA256: strings.Repeat("b", 64), DockerConfig: filepath.Join(root, spec.ID, "runtime", "docker"), DockerContext: spec.Runtime.DockerContext, DockerEndpoint: "unix:///private/tmp/admitted-docker.sock",
	}
	f.runtime = &dockerAdapterRuntime{t: t, journal: journal}
	f.adapter, err = newManagedDockerVolumes(t.Context(), manifest, generation, journal, f.runtime, func(ctx context.Context) (ManagedDockerConfig, error) { f.readCount++; return f.config, ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func dockerAdapterInspection(t *testing.T, binding DockerCleanupBinding) HostResult {
	t.Helper()
	data, err := json.Marshal([]any{map[string]any{"Name": binding.Name, "CreatedAt": "2026-10-24T01:49:04-04:00", "Driver": binding.VolumeDriver, "Labels": binding.Labels, "Options": binding.VolumeOptions, "Mountpoint": "/var/lib/docker/volumes/owned/_data", "Scope": "local"}})
	if err != nil {
		t.Fatal(err)
	}
	return HostResult{Stdout: data}
}

func testDockerAdapterCreate(t *testing.T) {
	f := newDockerAdapterFixture(t, nil)
	before := dockerVolumeCounts(t, f.journal)
	ctx, cancel := context.WithDeadline(t.Context(), f.deadline)
	defer cancel()
	intent, err := f.adapter.createVolume(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if intent.Phase != "acknowledged" || intent.Binding.CreatedAt != "2026-10-24T05:49:04Z" {
		t.Fatalf("wrong completed intent %+v", intent)
	}
	after := dockerVolumeCounts(t, f.journal)
	for i, delta := range [4]int64{2, 3, 1, 10} {
		if after[i]-before[i] != delta {
			t.Fatalf("create/inspect accounting %v -> %v", before, after)
		}
	}
	if len(f.runtime.calls) != 2 || f.readCount < 3 {
		t.Fatal("missing config reread or exact calls")
	}
	want := []string{"--config", f.config.DockerConfig, "--context", f.config.DockerContext, "volume", "create", "--driver", "local"}
	for _, key := range []string{"dev.getoro.factory.generation", "dev.getoro.factory.kind", "dev.getoro.factory.nonce", "dev.getoro.factory.objective", "dev.getoro.factory.policy", "dev.getoro.factory.scope"} {
		want = append(want, "--label", key+"="+intent.Binding.Labels[key])
	}
	want = append(want, "--opt", "type=tmpfs", "--opt", "device=tmpfs", "--opt", fmt.Sprintf("o=size=%d,nr_inodes=%d,uid=1000,gid=1000", f.manifest.Spec.Runtime.SourceBytes, f.manifest.Spec.Runtime.SourceInodes), intent.Binding.Name)
	if !reflect.DeepEqual(f.runtime.calls[0].Args, want) {
		t.Fatalf("create argv %q", f.runtime.calls[0].Args)
	}
	if !reflect.DeepEqual(f.runtime.calls[1].Args, []string{"--config", f.config.DockerConfig, "--context", f.config.DockerContext, "volume", "inspect", intent.Binding.Name}) {
		t.Fatal("inspect argv differs")
	}
	for _, call := range f.runtime.calls {
		if call.Path != f.config.DockerPath || call.Dir != f.root || call.Stdin != nil || call.OutputLimit != 1<<20 || !call.Deadline.Equal(f.deadline) || !reflect.DeepEqual(call.Env, []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "LANG=C"}) {
			t.Fatalf("unbound call %+v", call)
		}
	}
	observation, err := f.journal.Observe(t.Context(), intent.Volume)
	if err != nil || observation.Phase == "quiescent" {
		t.Fatal("inspection retired resource", err)
	}
}

func testDockerAdapterFailures(t *testing.T) {
	for _, mode := range []string{"nonzero", "extra", "partial", "stderr", "oversized", "runtime", "unretired", "canceled", "ack commit"} {
		t.Run(mode, func(t *testing.T) {
			f := newDockerAdapterFixture(t, nil)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			f.runtime.unretired = mode == "unretired"
			f.runtime.change = func(_ HostInvocation, result *HostResult) error {
				switch mode {
				case "nonzero":
					result.ExitCode = 1
				case "extra":
					result.Stdout = append(result.Stdout, 'x')
				case "partial":
					result.Stdout = result.Stdout[:len(result.Stdout)-1]
				case "stderr":
					result.Stderr = []byte("warning")
				case "oversized":
					result.Stdout = []byte(strings.Repeat("x", (1<<20)+1))
				case "runtime":
					return errors.New("unknown runtime completion")
				case "canceled":
					cancel()
				case "ack commit":
					dockerVolumeSQL(t, f.journal, "CREATE TRIGGER fail_ack BEFORE UPDATE ON managed_docker_intents BEGIN SELECT RAISE(ABORT,'failed ack'); END")
				}
				return nil
			}
			if got, err := f.adapter.createVolume(ctx); err == nil || !reflect.DeepEqual(got, managedDockerVolumeIntent{}) {
				t.Fatalf("uncertain create succeeded %+v %v", got, err)
			}
			if len(f.runtime.calls) != 1 {
				t.Fatal("uncertain create invoked another command")
			}
			intent, err := loadManagedDockerVolume(t.Context(), f.journal, f.runtime.intent.Volume)
			if err != nil || intent.Phase != "dispatching" {
				t.Fatalf("lost uncertainty %+v %v", intent, err)
			}
			before := dockerVolumeCounts(t, f.journal)
			if _, err := f.adapter.createVolume(t.Context()); err == nil {
				t.Fatal("failed adapter created another resource")
			}
			if _, err := f.adapter.dispatchPrepared(t.Context(), intent.Volume); err == nil {
				t.Fatal("uncertain create retried")
			}
			if _, _, err := f.adapter.inspectVolume(t.Context(), intent.Volume); err == nil {
				t.Fatal("failed adapter bypassed reconciliation")
			}
			if after := dockerVolumeCounts(t, f.journal); after != before || len(f.runtime.calls) != 1 {
				t.Fatal("rejection spent or invoked after uncertainty")
			}
		})
	}
}

func testDockerAdapterInspectFailure(t *testing.T) {
	for _, mode := range []string{"missing", "identity", "runtime", "budget"} {
		t.Run(mode, func(t *testing.T) {
			f := newDockerAdapterFixture(t, nil)
			f.runtime.change = func(call HostInvocation, result *HostResult) error {
				if slices.Contains(call.Args, "create") {
					if mode == "budget" {
						for i := 1; i < f.manifest.Spec.Runtime.MaxHostInvocations; i++ {
							dockerVolumeSQL(t, f.journal, "INSERT INTO managed_reservations(kind,token,generation) VALUES('host',?,1)", fmt.Sprint(i))
						}
					}
					return nil
				}
				switch mode {
				case "missing":
					*result = HostResult{ExitCode: 1, Stdout: []byte("[]\n"), Stderr: []byte("Error response from daemon: get " + f.runtime.intent.Binding.Name + ": no such volume\n")}
				case "identity":
					result.Stdout = []byte(strings.Replace(string(result.Stdout), "2026-10-24T01:49:04-04:00", "invalid", 1))
				case "runtime":
					return errors.New("inspect transport uncertainty")
				}
				return nil
			}
			if _, err := f.adapter.createVolume(t.Context()); err == nil {
				t.Fatal("incomplete inspection succeeded")
			}
			intent, err := loadManagedDockerVolume(t.Context(), f.journal, f.runtime.intent.Volume)
			if err != nil || intent.Phase != "acknowledged" || intent.Binding.CreatedAt != "" {
				t.Fatalf("inspection lost creation acknowledgment %+v %v", intent, err)
			}
			observation, err := f.journal.Observe(t.Context(), intent.Volume)
			if err != nil || observation.Phase == "quiescent" {
				t.Fatal("absence released capacity", err)
			}
		})
	}
}

func testDockerAdapterPrepared(t *testing.T) {
	f := newDockerAdapterFixture(t, nil)
	intent := f.reserve(t)
	before := dockerVolumeCounts(t, f.journal)
	got, err := f.adapter.dispatchPrepared(t.Context(), intent.Volume)
	if err != nil || got.CreateHost != intent.CreateHost || !got.CreateDeadline.Equal(intent.CreateDeadline) {
		t.Fatalf("prepared resume %+v %v", got, err)
	}
	after := dockerVolumeCounts(t, f.journal)
	if after[0]-before[0] != 1 || after[1]-before[1] != 1 || after[2] != before[2] {
		t.Fatal("resume replaced original reservation")
	}
	if _, err := f.adapter.dispatchPrepared(t.Context(), intent.Volume); err == nil {
		t.Fatal("replayed acknowledged create")
	}
	f.runtime.change = func(_ HostInvocation, result *HostResult) error {
		*result = HostResult{ExitCode: 1, Stdout: []byte("[]\n"), Stderr: []byte("Error response from daemon: get " + intent.Binding.Name + ": no such volume\n")}
		return nil
	}
	observed, present, err := f.adapter.inspectVolume(t.Context(), intent.Volume)
	if err != nil || present || observed.Binding.CreatedAt != got.Binding.CreatedAt {
		t.Fatalf("absence was not observation: %+v %v %v", observed, present, err)
	}
	observation, err := f.journal.Observe(t.Context(), intent.Volume)
	if err != nil || observation.Phase == "quiescent" {
		t.Fatal("absent observation released scope")
	}
}

func testDockerAdapterConfig(t *testing.T) {
	f := newDockerAdapterFixture(t, nil)
	mutations := map[string]func(*ManagedDockerConfig){"root": func(c *ManagedDockerConfig) { c.StateRoot += "-other" }, "objective": func(c *ManagedDockerConfig) { c.ObjectiveID = "other" }, "policy": func(c *ManagedDockerConfig) { c.PolicyHash = strings.Repeat("b", 64) }, "deadline": func(c *ManagedDockerConfig) { c.Deadline = c.Deadline.Add(time.Second) }, "hardstop": func(c *ManagedDockerConfig) { c.HardStop = c.HardStop.Add(time.Second) }, "artifact": func(c *ManagedDockerConfig) { c.RuntimeConfigPath += ".other" }, "digest": func(c *ManagedDockerConfig) { c.RuntimeConfigSHA256 = "invalid" }, "docker": func(c *ManagedDockerConfig) { c.DockerPath = "docker" }, "docker hash": func(c *ManagedDockerConfig) { c.DockerSHA256 = "invalid" }, "snapshot": func(c *ManagedDockerConfig) { c.DockerConfig += ".other" }, "context": func(c *ManagedDockerConfig) { c.DockerContext = "default" }, "endpoint": func(c *ManagedDockerConfig) { c.DockerEndpoint = "tcp://127.0.0.1:2375" }}
	mutations["oro empty"] = func(c *ManagedDockerConfig) { c.OroPath = "" }
	mutations["oro relative"] = func(c *ManagedDockerConfig) { c.OroPath = "oro" }
	mutations["oro unclean"] = func(c *ManagedDockerConfig) { c.OroPath = "/usr/local/../bin/oro" }
	mutations["oro hash"] = func(c *ManagedDockerConfig) { c.OroSHA256 = "invalid" }
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			config := f.config
			mutate(&config)
			reader := func(context.Context) (ManagedDockerConfig, error) { return config, nil }
			if _, err := newManagedDockerVolumes(t.Context(), f.manifest, f.generation, f.journal, f.runtime, reader); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
	f.config.DockerSHA256 = strings.Repeat("c", 64)
	before := dockerVolumeCounts(t, f.journal)
	if _, err := f.adapter.createVolume(t.Context()); err == nil {
		t.Fatal("changed runtime config accepted")
	}
	if after := dockerVolumeCounts(t, f.journal); after != before || len(f.runtime.calls) != 0 {
		t.Fatal("config mismatch had effects")
	}
	dockerVolumeSQL(t, f.journal, "UPDATE snapshots SET body=json_set(body,'$.PolicyHash','different')")
	if _, err := newManagedDockerVolumes(t.Context(), f.manifest, f.generation, f.journal, f.runtime, func(context.Context) (ManagedDockerConfig, error) { return f.config, nil }); err == nil {
		t.Fatal("artifact-only admission accepted")
	}
}

func testDockerAdapterInspection(t *testing.T) {
	f := newDockerAdapterFixture(t, nil)
	intent := f.reserve(t)
	expected := intent.Binding
	valid := dockerAdapterInspection(t, expected)
	observed, present, err := parseManagedDockerVolumeInspection(valid, expected)
	if err != nil || !present || observed.CreatedAt != "2026-10-24T05:49:04Z" {
		t.Fatalf("actual offset rejected %+v %v %v", observed, present, err)
	}
	for _, mode := range []string{"empty", "multiple", "duplicate", "nested duplicate", "missing", "null", "map null", "labels", "options", "wrong time", "fraction", "overlong", "stderr", "oversized", "false absence", "extra fields", "trailing", "invalid UTF8", "nesting", "scalar", "scalar field", "malformed", "map size", "map value size"} {
		t.Run(mode, func(t *testing.T) {
			result := HostResult{Stdout: append([]byte(nil), valid.Stdout...)}
			switch mode {
			case "empty":
				result.Stdout = []byte("[]")
			case "multiple":
				result.Stdout = append(append([]byte("["), valid.Stdout[1:len(valid.Stdout)-1]...), append([]byte(","), valid.Stdout[1:]...)...)
			case "duplicate":
				result.Stdout = []byte(strings.Replace(string(result.Stdout), "\"Name\":", "\"Name\":\"wrong\",\"Name\":", 1))
			case "nested duplicate":
				result.Stdout = []byte(strings.Replace(string(result.Stdout), "\"Scope\":\"local\"", "\"Scope\":{\"a\":1,\"a\":2}", 1))
			case "missing":
				result.Stdout = []byte(strings.Replace(string(result.Stdout), "\"Driver\"", "\"Other\"", 1))
			case "null":
				result.Stdout = []byte(strings.Replace(string(result.Stdout), "\"Driver\":\"local\"", "\"Driver\":null", 1))
			case "map null":
				result.Stdout = []byte(strings.Replace(string(result.Stdout), "\"tmpfs\"", "null", 1))
			case "labels":
				result.Stdout = []byte(strings.Replace(string(result.Stdout), "\"dev.getoro.factory.kind\":\"volume\"", "\"dev.getoro.factory.kind\":\"container\"", 1))
			case "options":
				result.Stdout = []byte(strings.Replace(string(result.Stdout), "uid=1000", "uid=0", 1))
			case "wrong time":
				expected.CreatedAt = "2026-10-24T05:49:05Z"
			case "fraction":
				result.Stdout = []byte(strings.Replace(string(result.Stdout), "04-04:00", "04.1234567890-04:00", 1))
			case "overlong":
				result.Stdout = []byte(strings.Replace(string(result.Stdout), "2026-10-24T01:49:04-04:00", strings.Repeat("0", 36), 1))
			case "stderr":
				result.Stderr = []byte("warning")
			case "oversized":
				result.Stdout = []byte(strings.Repeat(" ", (1<<20)+1))
			case "false absence":
				result.ExitCode = 1
				result.Stdout = []byte("[]")
				result.Stderr = []byte("no such volume")
			case "extra fields":
				var fields strings.Builder
				fields.WriteString("[{")
				for i := range 128 {
					fmt.Fprintf(&fields, "\"field%d\":1,", i)
				}
				fields.WriteString("\"last\":1}]")
				result.Stdout = []byte(fields.String())
			case "trailing":
				result.Stdout = append(result.Stdout, []byte(" null")...)
			case "invalid UTF8":
				result.Stdout = []byte{'[', '"', 255, '"', ']'}
			case "nesting":
				result.Stdout = []byte(strings.Repeat("[", 18) + "0" + strings.Repeat("]", 18))
			case "scalar":
				result.Stdout = []byte("1")
			case "scalar field":
				result.Stdout = []byte(strings.Replace(string(result.Stdout), "\"Driver\":\"local\"", "\"Driver\":1", 1))
			case "malformed":
				result.Stdout = []byte("[{\"Name\":")
			case "map size", "map value size":
				var objects []map[string]any
				if err := json.Unmarshal(result.Stdout, &objects); err != nil {
					t.Fatal(err)
				}
				values := map[string]string{}
				for i := range 17 {
					values[fmt.Sprint(i)] = "x"
				}
				if mode == "map value size" {
					values = map[string]string{"key": strings.Repeat("x", 1025)}
				}
				objects[0]["Labels"] = values
				data, err := json.Marshal(objects)
				if err != nil {
					t.Fatal(err)
				}
				result.Stdout = data
			}
			if _, _, err := parseManagedDockerVolumeInspection(result, expected); err == nil {
				t.Fatal("uncertain inspection accepted")
			}
			expected = intent.Binding
		})
	}
}

func testDockerAdapterModelHelperReread(t *testing.T) {
	f := newDockerAdapterFixture(t, nil)
	helper := ModelAdapterConfig{Protocol: "oro-terra-responses-v1", CredentialPath: "/private/tmp/credential", HelperSHA256: strings.Repeat("a", 64)}
	f.adapter.config.ModelHelper = &helper
	f.adapter.readConfig = func(context.Context) (ManagedDockerConfig, error) {
		c := f.config
		cloned := helper
		c.ModelHelper = &cloned
		return c, nil
	}
	if err := f.adapter.ready(t.Context()); err != nil {
		t.Fatalf("equal independent helper values: %v", err)
	}
	for _, change := range []func(*ManagedDockerConfig){
		func(c *ManagedDockerConfig) { c.ModelHelper = nil },
		func(c *ManagedDockerConfig) { c.ModelHelper.Protocol += "x" },
		func(c *ManagedDockerConfig) { c.ModelHelper.CredentialPath += "x" },
		func(c *ManagedDockerConfig) { c.ModelHelper.HelperSHA256 = strings.Repeat("b", 64) },
	} {
		f.adapter.readConfig = func(context.Context) (ManagedDockerConfig, error) {
			c := f.config
			cloned := helper
			c.ModelHelper = &cloned
			change(&c)
			return c, nil
		}
		if err := f.adapter.ready(t.Context()); err == nil {
			t.Fatal("changed helper accepted")
		}
	}
	if len(f.runtime.calls) != 0 {
		t.Fatal("configuration check dispatched host work")
	}
}
