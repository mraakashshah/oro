package factory //nolint:testpackage // Exercise the private source preparation and durable authority boundary.

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestManagedSourcePrepare(t *testing.T) {
	if os.Getenv("ORO_SOURCE_PREPARE_PROCESS") == "1" {
		sourcePrepareProcessChild(t)
		return
	}
	t.Run("fresh admission includes source state", func(t *testing.T) {
		_, _, store := journalFixture(t)
		var count int
		if err := store.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='managed_docker_sources'").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatal("fresh admission lacks bounded source preparation state")
		}
	})
	t.Run("complete production chain freezes source and spends exact budgets", testSourcePrepareChain)
	t.Run("whole vector validates before resources", testSourcePrepareVector)
	t.Run("unknown start and copy never replay", testSourcePrepareUnknown)
	t.Run("running keeper identity and raw timestamps", testSourcePrepareInspection)
	t.Run("prepared resume binds bytes and original deadline", testSourcePrepareResume)
	t.Run("acknowledged effects survive inspection failure", testSourcePrepareInspectFailure)
	t.Run("source schema cannot recreate committed history", testSourcePrepareSchema)
	t.Run("competing connections dispatch each original host once", testSourcePrepareCAS)
	t.Run("process death preserves prepared versus uncertain effects", testSourcePrepareProcessDeath)
	t.Run("canonical source and referenced scope corruption fails closed", testSourcePrepareCorruption)
	t.Run("source reservations roll back exact exhausted budgets", testSourcePrepareBudgets)
	t.Run("resume keeps original deadline before reading config", testSourcePrepareDeadline)
	t.Run("phase guards and own host acknowledgment", testSourcePrepareTransitions)
}

type sourcePrepareFixture struct {
	*sourceKeeperFixture
	runtime *sourcePrepareRuntime
	entries []sourceEntry
}
type sourcePrepareRuntime struct {
	base        *sourceKeeperRuntime
	t           *testing.T
	calls       []HostInvocation
	running     bool
	inspections int
	change      func(string, HostInvocation, *HostResult) error
	unretired   string
}

func newSourcePrepareFixture(t *testing.T) *sourcePrepareFixture {
	t.Helper()
	f := newSourceKeeperFixture(t)
	r := &sourcePrepareRuntime{base: f.runtime, t: t}
	f.adapter.host = r
	return &sourcePrepareFixture{f, r, []sourceEntry{{Path: "dir/file", Mode: 0o100644, Data: []byte("source")}, {Path: "run", Mode: 0o100755, Data: []byte("#!/bin/sh\n")}, {Path: "alias", Mode: 0o120000, Data: []byte("dir/file")}}}
}

func (r *sourcePrepareRuntime) Invoke(ctx context.Context, call HostInvocation) (HostResult, error) {
	r.calls = append(r.calls, call)
	start := len(call.Args) > 1 && call.Args[0] == "factory" && call.Args[1] == "_docker-start-ready"
	if !r.running && !start {
		return r.base.Invoke(ctx, call)
	}
	keeper, err := loadManagedDockerKeeper(ctx, r.base.base.journal, r.base.intent.Keeper)
	if err != nil {
		return HostResult{}, err
	}
	stage := "start"
	result := HostResult{}
	switch {
	case start:
		r.requireDispatched(ctx, keeper.Keeper, call.Scope, "start_dispatching")
		r.running = true
		result.Stdout = []byte("READY " + keeper.Binding.ContainerID + " " + keeper.GuardDeadline.Format(time.RFC3339Nano) + "\n")
	case len(call.Args) > 4 && call.Args[4] == "cp":
		stage = "seed"
		r.requireDispatched(ctx, keeper.Keeper, call.Scope, "seed_dispatching")
	default:
		r.inspections++
		stage = "inspect-ready"
		if r.inspections > 1 {
			stage = "inspect-seeded"
		}
		volume, err := loadManagedDockerVolume(ctx, r.base.base.journal, keeper.Volume)
		if err != nil {
			return HostResult{}, err
		}
		result = sourcePrepareRunning(r.t, keeper, volume, "2026-10-24T02:00:00-04:00")
	}
	if err := r.base.base.journal.RecordHello(ctx, call.Scope, 103, 103); err != nil {
		return HostResult{}, err
	}
	if r.unretired != stage {
		if err := r.base.base.journal.MarkQuiescent(ctx, call.Scope); err != nil {
			return HostResult{}, err
		}
	}
	if r.change != nil {
		if err := r.change(stage, call, &result); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (r *sourcePrepareRuntime) Retire(ctx context.Context, scope ScopeHandle) error {
	return r.base.Retire(ctx, scope)
}

func (r *sourcePrepareRuntime) requireDispatched(ctx context.Context, keeper, host ScopeHandle, phase string) {
	r.t.Helper()
	record, err := loadManagedDockerSource(ctx, r.base.base.journal, keeper)
	if err != nil {
		r.t.Fatal(err)
	}
	expected := record.StartHost
	if phase == "seed_dispatching" {
		expected = record.SeedHost
	}
	if record.Phase != phase || expected != host {
		r.t.Fatalf("effect entered without committed dispatch: %+v", record)
	}
}

func sourcePrepareRunning(t *testing.T, keeper managedDockerKeeperIntent, volume managedDockerVolumeIntent, started string) HostResult {
	t.Helper()
	object := sourceKeeperObject(keeper, volume)
	state := object["State"].(map[string]any)
	state["Status"] = "running"
	state["Running"] = true
	state["Pid"] = 203
	state["StartedAt"] = started
	data, err := json.Marshal([]any{object})
	if err != nil {
		t.Fatal(err)
	}
	return HostResult{Stdout: data}
}

func testSourcePrepareChain(t *testing.T) {
	f := newSourcePrepareFixture(t)
	before := dockerVolumeCounts(t, f.journal)
	ctx, cancel := context.WithDeadline(t.Context(), f.deadline)
	defer cancel()
	source, err := f.adapter.prepareSource(ctx, f.entries)
	if err != nil {
		t.Fatal(err)
	}
	record, err := loadManagedDockerSource(t.Context(), f.journal, source.Keeper)
	if err != nil {
		t.Fatal(err)
	}
	if record.Phase != "seeded" || record.StartedAt != "2026-10-24T06:00:00Z" || !record.OperationDeadline.Equal(f.deadline) {
		t.Fatalf("wrong source %+v", record)
	}
	after := dockerVolumeCounts(t, f.journal)
	for i, delta := range [4]int64{8, 10, 1, 40} {
		if after[i]-before[i] != delta {
			t.Fatalf("counter%d=%d want%d", i, after[i]-before[i], delta)
		}
	}
	if len(f.runtime.calls) != 8 {
		t.Fatalf("calls=%d", len(f.runtime.calls))
	}
	for _, call := range f.runtime.calls {
		if !call.Deadline.Equal(f.deadline) || call.Dir != f.root || !slices.Equal(call.Env, []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "LANG=C"}) {
			t.Fatal("operation changed deadline or environment")
		}
	}
	ready := f.runtime.calls[4]
	if ready.Path != f.config.OroPath || ready.Stdin != nil || ready.OutputLimit != 256 || !slices.Equal(ready.Args[:2], []string{"factory", "_docker-start-ready"}) {
		t.Fatalf("wrong READY invocation %+v", ready)
	}
	flags := map[string]string{}
	for i := 2; i < len(ready.Args); i += 2 {
		if i+1 >= len(ready.Args) || flags[ready.Args[i]] != "" {
			t.Fatal("ambiguous helper flags")
		}
		flags[ready.Args[i]] = ready.Args[i+1]
	}
	keeper, err := loadManagedDockerKeeper(t.Context(), f.journal, source.Keeper)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"--docker": f.config.DockerPath, "--docker-sha256": f.config.DockerSHA256, "--docker-config": f.config.DockerConfig, "--context": f.config.DockerContext, "--container": keeper.Binding.ContainerID, "--scope-id": source.Keeper.ID, "--scope-nonce": source.Keeper.Nonce, "--scope-kind": "keeper", "--scope-generation": fmt.Sprint(f.generation), "--objective-hash": cleanupObjectiveHash(f.journal.admission), "--policy-hash": f.manifest.PolicyHash, "--image": f.manifest.Spec.Runtime.ImageDigest, "--guard-deadline": f.manifest.HardStop.Format(time.RFC3339Nano), "--deadline": f.deadline.Format(time.RFC3339Nano)}
	if !reflect.DeepEqual(flags, want) {
		t.Fatalf("helper flags=%v", flags)
	}
	seed := f.runtime.calls[6]
	if seed.Path != f.config.DockerPath || !slices.Equal(seed.Args, []string{"--config", f.config.DockerConfig, "--context", f.config.DockerContext, "cp", "-", keeper.Binding.ContainerID + ":/source"}) || seed.OutputLimit != 1<<20 {
		t.Fatalf("wrong seed call %+v", seed)
	}
	if contentHash(seed.Stdin) != record.SeedSHA256 || int64(len(seed.Stdin)) != record.SeedBytes {
		t.Fatal("source bytes not bound")
	}
	stream := tar.NewReader(bytes.NewReader(seed.Stdin))
	linkSeen := false
	headers := 0
	for {
		header, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		headers++
		if header.Uid != 1000 || header.Gid != 1000 || header.Format != tar.FormatUSTAR {
			t.Fatal("untrusted seed metadata")
		}
		if header.Typeflag == tar.TypeSymlink {
			linkSeen = true
		} else if linkSeen {
			t.Fatal("non-link emitted after link")
		}
	}
	if headers != 4 {
		t.Fatalf("headers=%d", headers)
	}
}

func testSourcePrepareVector(t *testing.T) {
	for _, mode := range []string{"escape", "link", "mode", "duplicates", "schema", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			f := newSourcePrepareFixture(t)
			before := dockerVolumeCounts(t, f.journal)
			ctx := t.Context()
			entries := slices.Clone(f.entries)
			switch mode {
			case "escape":
				entries = append(entries, sourceEntry{Path: "../escape", Mode: 0o100644})
			case "link":
				entries[2].Data = []byte("../outside")
			case "mode":
				entries[0].Mode = 0o100600
			case "duplicates":
				entries = append(entries, entries[0])
			case "schema":
				dockerVolumeSQL(t, f.journal, "DROP TABLE managed_docker_sources")
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if _, err := f.adapter.prepareSource(ctx, entries); err == nil {
				t.Fatal("invalid source prepared")
			}
			if len(f.runtime.calls) != 0 || dockerVolumeCounts(t, f.journal) != before {
				t.Fatal("invalid complete vector caused resource effects")
			}
		})
	}
}

func testSourcePrepareUnknown(t *testing.T) {
	for _, stage := range []string{"start", "seed"} {
		for _, mode := range []string{"runtime", "nonzero", "stdout", "stderr", "unretired", "canceled", "commit"} {
			t.Run(stage+"/"+mode, func(t *testing.T) {
				f := newSourcePrepareFixture(t)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if mode == "unretired" {
					f.runtime.unretired = stage
				}
				f.runtime.change = func(got string, _ HostInvocation, result *HostResult) error {
					if got != stage {
						return nil
					}
					switch mode {
					case "runtime":
						return errors.New("unknown daemon outcome")
					case "nonzero":
						result.ExitCode = 1
					case "stdout":
						result.Stdout = append(result.Stdout, 'x')
					case "stderr":
						result.Stderr = []byte("warning")
					case "canceled":
						cancel()
					case "commit":
						dockerVolumeSQL(t, f.journal, "CREATE TRIGGER reject_source_ack BEFORE UPDATE ON managed_docker_sources BEGIN SELECT RAISE(ABORT,'lost acknowledgment'); END")
					}
					return nil
				}
				if _, err := f.adapter.prepareSource(ctx, f.entries); err == nil {
					t.Fatal("uncertain source accepted")
				}
				keeper := f.runtime.base.intent.Keeper
				record, err := loadManagedDockerSource(t.Context(), f.journal, keeper)
				if err != nil || record.Phase != stage+"_dispatching" {
					t.Fatalf("wrong uncertainty %+v %v", record, err)
				}
				other, err := newManagedDockerVolumes(t.Context(), f.manifest, f.generation, f.journal, f.runtime, func(context.Context) (ManagedDockerConfig, error) { return f.config, nil })
				if err != nil {
					t.Fatal(err)
				}
				before := len(f.runtime.calls)
				if _, err := other.resumeSource(t.Context(), keeper, f.entries); err == nil {
					t.Fatal("replacement adapter replayed unknown effect")
				}
				if len(f.runtime.calls) != before {
					t.Fatal("unknown effect invoked again")
				}
			})
		}
	}
}

func testSourcePrepareInspection(t *testing.T) {
	f := newSourcePrepareFixture(t)
	keeper, err := f.adapter.createSourceKeeper(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	volume, err := loadManagedDockerVolume(t.Context(), f.journal, keeper.Volume)
	if err != nil {
		t.Fatal(err)
	}
	for _, started := range []string{"0001-01-01T00:00:00Z", "bad", "2026-10-24T02:00:00.1234567891-04:00", "2026-10-24T2:00:00-04:00", "2026-10-24T02:00:00,1-04:00"} {
		if _, err := parseManagedDockerRunningKeeperInspection(sourcePrepareRunning(t, keeper, volume, started), keeper, volume, ""); err == nil {
			t.Fatalf("bad raw StartedAt accepted %q", started)
		}
	}
	good := sourcePrepareRunning(t, keeper, volume, "2026-10-24T02:00:00-04:00")
	if got, err := parseManagedDockerRunningKeeperInspection(good, keeper, volume, ""); err != nil || got != "2026-10-24T06:00:00Z" {
		t.Fatalf("bad canonical started time %q %v", got, err)
	}
	if _, err := parseManagedDockerRunningKeeperInspection(good, keeper, volume, "2026-10-24T06:00:01Z"); err == nil {
		t.Fatal("changed StartedAt accepted")
	}
	for _, fromTo := range [][2]string{{`"Pid":203`, `"Pid":0`}, {`"Running":true`, `"Running":false`}, {`"Paused":false`, `"Paused":true`}, {`"User":"65534:65534"`, `"User":"0:0"`}} {
		bad := HostResult{Stdout: []byte(strings.Replace(string(good.Stdout), fromTo[0], fromTo[1], 1))}
		if _, err := parseManagedDockerRunningKeeperInspection(bad, keeper, volume, ""); err == nil {
			t.Fatal("invalid running identity accepted")
		}
	}
}

func (f *sourcePrepareFixture) reserve(t *testing.T) managedDockerSourceRecord {
	t.Helper()
	keeper, err := f.adapter.createSourceKeeper(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	limits := sourceLimits{MaxArchiveBytes: f.adapter.limits.ArchiveBytes, MaxBytes: f.manifest.Spec.Runtime.SourceBytes, MaxFileBytes: f.manifest.Spec.Runtime.SourceBytes, MaxFiles: f.manifest.Spec.Runtime.MaxFiles, MaxPathBytes: f.adapter.limits.MaxPathBytes}
	seed, err := encodeDockerSourceArchive(f.entries, limits)
	if err != nil {
		t.Fatal(err)
	}
	record, err := reserveManagedDockerSource(t.Context(), f.manifest, f.journal, f.generation, keeper.Keeper, f.deadline, contentHash(seed), int64(len(seed)))
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func testSourcePrepareResume(t *testing.T) {
	f := newSourcePrepareFixture(t)
	record := f.reserve(t)
	before := dockerVolumeCounts(t, f.journal)
	changed := slices.Clone(f.entries)
	changed[0].Data = []byte("changed")
	if _, err := f.adapter.resumeSource(t.Context(), record.Keeper, changed); err == nil {
		t.Fatal("different archive resumed")
	}
	if dockerVolumeCounts(t, f.journal) != before || len(f.runtime.calls) != 4 {
		t.Fatal("changed seed spent work")
	}
	source, err := f.adapter.resumeSource(t.Context(), record.Keeper, f.entries)
	if err != nil {
		t.Fatal(err)
	}
	got, err := loadManagedDockerSource(t.Context(), f.journal, source.Keeper)
	if err != nil || got.StartHost != record.StartHost || !got.OperationDeadline.Equal(record.OperationDeadline) || got.Phase != "seeded" {
		t.Fatalf("resume changed original reservation %+v %v", got, err)
	}
	before = dockerVolumeCounts(t, f.journal)
	if _, err := f.adapter.resumeSource(t.Context(), source.Keeper, f.entries); err != nil {
		t.Fatal(err)
	}
	after := dockerVolumeCounts(t, f.journal)
	if after[0]-before[0] != 1 || after[1]-before[1] != 1 || len(f.runtime.calls) != 9 {
		t.Fatal("seeded replay did not perform one fresh continuity observation")
	}
}

func testSourcePrepareInspectFailure(t *testing.T) {
	for _, stage := range []string{"inspect-ready", "inspect-seeded"} {
		t.Run(stage, func(t *testing.T) {
			f := newSourcePrepareFixture(t)
			f.runtime.change = func(got string, _ HostInvocation, r *HostResult) error {
				if got == stage {
					r.Stdout = []byte(strings.ReplaceAll(string(r.Stdout), "02:00:00-04:00", "02:00:01-04:00"))
					if stage == "inspect-ready" {
						r.ExitCode = 1
					}
				}
				return nil
			}
			if _, err := f.adapter.prepareSource(t.Context(), f.entries); err == nil {
				t.Fatal("failed postcondition published source")
			}
			record, err := loadManagedDockerSource(t.Context(), f.journal, f.runtime.base.intent.Keeper)
			want := "start_acknowledged"
			if stage == "inspect-seeded" {
				want = "seed_acknowledged"
			}
			if err != nil || record.Phase != want {
				t.Fatalf("acknowledged effect lost %+v %v", record, err)
			}
		})
	}
}

func testSourcePrepareSchema(t *testing.T) {
	for _, used := range []bool{false, true} {
		t.Run(fmt.Sprint(used), func(t *testing.T) {
			f := newSourcePrepareFixture(t)
			if used {
				_ = f.reserve(t)
			}
			before := dockerVolumeCounts(t, f.journal)
			dockerVolumeSQL(t, f.journal, "DROP TABLE managed_docker_sources")
			if _, err := AdmitManaged(t.Context(), f.root, f.manifest.Spec, f.manifest.Reservation); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := f.journal.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_master WHERE name='managed_docker_sources'").Scan(&count); err != nil || count != 0 {
				t.Fatal("recreated source history", err)
			}
			if dockerVolumeCounts(t, f.journal) != before {
				t.Fatal("replay changed counters")
			}
		})
	}
}

func (f *sourcePrepareFixture) readyRecord(t *testing.T) managedDockerSourceRecord {
	t.Helper()
	record := f.reserve(t)
	if _, err := dispatchManagedDockerSourceStart(t.Context(), f.journal, record.Keeper, record.RuntimeConfigSHA256); err != nil {
		t.Fatal(err)
	}
	sourcePrepareRetire(t, f.journal, record.StartHost)
	if err := acknowledgeManagedDockerSourceStart(t.Context(), f.journal, record.Keeper, record.StartHost); err != nil {
		t.Fatal(err)
	}
	if err := recordManagedDockerSourceReady(t.Context(), f.journal, f.generation, record.Keeper, "2026-10-24T06:00:00Z"); err != nil {
		t.Fatal(err)
	}
	record, err := loadManagedDockerSource(t.Context(), f.journal, record.Keeper)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func sourcePrepareRetire(t *testing.T, j *managedJournal, host ScopeHandle) {
	t.Helper()
	if err := j.RecordHello(t.Context(), host, 105, 105); err != nil {
		t.Fatal(err)
	}
	if err := j.MarkQuiescent(t.Context(), host); err != nil {
		t.Fatal(err)
	}
}

func sourcePrepareDispatch(ctx context.Context, j *managedJournal, s managedDockerSourceRecord, stage string) (managedDockerSourceRecord, error) {
	if stage == "start" {
		return dispatchManagedDockerSourceStart(ctx, j, s.Keeper, s.RuntimeConfigSHA256)
	}
	return dispatchManagedDockerSeed(ctx, j, s.Keeper, s.SeedSHA256, s.SeedBytes)
}

func testSourcePrepareCAS(t *testing.T) {
	for _, stage := range []string{"start", "seed"} {
		t.Run(stage, func(t *testing.T) {
			f := newSourcePrepareFixture(t)
			var record managedDockerSourceRecord
			if stage == "start" {
				record = f.reserve(t)
			} else {
				ready := f.readyRecord(t)
				var err error
				record, err = reserveManagedDockerSeed(t.Context(), f.journal, f.generation, ready.Keeper)
				if err != nil {
					t.Fatal(err)
				}
			}
			other := openJournalFixture(t, f.root, f.manifest.Spec.ID)
			before := dockerVolumeCounts(t, f.journal)
			results := make(chan error, 2)
			var wg sync.WaitGroup
			gate := make(chan struct{})
			for _, j := range []*managedJournal{f.journal, other} {
				wg.Go(func() { <-gate; _, err := sourcePrepareDispatch(t.Context(), j, record, stage); results <- err })
			}
			close(gate)
			wg.Wait()
			close(results)
			wins := 0
			for err := range results {
				if err == nil {
					wins++
				}
			}
			if wins != 1 {
				t.Fatalf("dispatch winners=%d", wins)
			}
			after := dockerVolumeCounts(t, f.journal)
			if after[0] != before[0] || after[1] != before[1] || after[2] != before[2] || after[3]-before[3] != 1 {
				t.Fatal("CAS spent duplicate authority")
			}
		})
	}
}

type sourcePrepareProcessRequest struct {
	Root, ID, Stage string
	Keeper          ScopeHandle
	Deadline        time.Time
	SeedSHA256      string
	SeedBytes       int64
	Dispatched      bool
}

func sourcePrepareProcessChild(t *testing.T) {
	t.Helper()
	var request sourcePrepareProcessRequest
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		t.Fatal(err)
	}
	j := openJournalFixture(t, request.Root, request.ID)
	var record managedDockerSourceRecord
	var err error
	if request.Stage == "start" {
		manifest, loadErr := loadManagedManifest(t.Context(), request.Root, request.ID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		record, err = reserveManagedDockerSource(t.Context(), manifest, j, 1, request.Keeper, request.Deadline, request.SeedSHA256, request.SeedBytes)
	} else {
		record, err = reserveManagedDockerSeed(t.Context(), j, 1, request.Keeper)
	}
	if err != nil {
		t.Fatal(err)
	}
	if request.Dispatched {
		record, err = sourcePrepareDispatch(t.Context(), j, record, request.Stage)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(record); err != nil {
		t.Fatal(err)
	}
	<-t.Context().Done()
}

func testSourcePrepareProcessDeath(t *testing.T) {
	for _, stage := range []string{"start", "seed"} {
		for _, dispatched := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", stage, dispatched), func(t *testing.T) {
				f := newSourcePrepareFixture(t)
				var keeper ScopeHandle
				if stage == "start" {
					created, err := f.adapter.createSourceKeeper(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					keeper = created.Keeper
				} else {
					keeper = f.readyRecord(t).Keeper
				}
				seed, err := f.adapter.encodeSource(t.Context(), f.entries)
				if err != nil {
					t.Fatal(err)
				}
				request, err := json.Marshal(sourcePrepareProcessRequest{f.root, f.manifest.Spec.ID, stage, keeper, f.deadline, contentHash(seed), int64(len(seed)), dispatched})
				if err != nil {
					t.Fatal(err)
				}
				executable, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, executable, "-test.run=^TestManagedSourcePrepare$", "-test.timeout=30s")
				cmd.Env = append(os.Environ(), "ORO_SOURCE_PREPARE_PROCESS=1")
				cmd.Stdin = bytes.NewReader(request)
				cmd.Stderr = os.Stderr
				output, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if cmd.ProcessState == nil {
						_ = cmd.Process.Kill()
						_ = cmd.Wait()
					}
				}()
				var record managedDockerSourceRecord
				if err := json.NewDecoder(output).Decode(&record); err != nil {
					t.Fatal(err)
				}
				if err := cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				var exit *exec.ExitError
				if err := cmd.Wait(); !errors.As(err, &exit) {
					t.Fatalf("owned child not killed and reaped: %v", err)
				}
				reopened := openJournalFixture(t, f.root, f.manifest.Spec.ID)
				got, err := loadManagedDockerSource(t.Context(), reopened, keeper)
				if err != nil || got != record {
					t.Fatalf("death changed source %+v %v", got, err)
				}
				before := dockerVolumeCounts(t, reopened)
				got, err = sourcePrepareDispatch(t.Context(), reopened, record, stage)
				if dispatched {
					if err == nil {
						t.Fatal("postdispatch effect replayed after process death")
					}
				} else if err != nil || got.StartHost != record.StartHost || got.SeedHost != record.SeedHost || !got.OperationDeadline.Equal(record.OperationDeadline) {
					t.Fatalf("prepared continuation replaced original authority %+v %v", got, err)
				}
				after := dockerVolumeCounts(t, reopened)
				if after[0] != before[0] || after[1] != before[1] || after[2] != before[2] {
					t.Fatal("reopen reserved replacement capacity")
				}
			})
		}
	}
}

func testSourcePrepareCorruption(t *testing.T) {
	f := newSourcePrepareFixture(t)
	record := f.reserve(t)
	original, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"extra", "noncanonical", "phase", "deadline", "hash", "bytes", "early seed", "early started", "host column", "scope phase override", "scope backend", "host reservation"} {
		t.Run(mode, func(t *testing.T) {
			before := dockerVolumeCounts(t, f.journal)
			body := slices.Clone(original)
			modified := record
			var scopeBody []byte
			if err := f.journal.conn.QueryRowContext(t.Context(), "SELECT body FROM managed_scopes WHERE id=?", record.StartHost.ID).Scan(&scopeBody); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "extra":
				body = append(body[:len(body)-1], []byte(`,"ignored":true}`)...)
			case "noncanonical":
				body = append([]byte(" "), body...)
			case "phase":
				modified.Phase = "unknown"
			case "deadline":
				modified.OperationDeadline = f.manifest.HardStop
			case "hash":
				modified.SeedSHA256 = "bad"
			case "bytes":
				modified.SeedBytes = f.adapter.limits.ArchiveBytes + 1
			case "early seed":
				modified.SeedHost = record.StartHost
			case "early started":
				modified.StartedAt = "2026-10-24T06:00:00Z"
			case "host column":
				dockerVolumeSQL(t, f.journal, "UPDATE managed_docker_sources SET start_scope_id=?", record.Volume.ID)
			case "scope phase override":
				dockerVolumeSQL(t, f.journal, "UPDATE managed_scopes SET body=json_set(body,'$.Phase','quiescent') WHERE id=?", record.StartHost.ID)
			case "scope backend":
				dockerVolumeSQL(t, f.journal, "UPDATE managed_scopes SET backend='docker' WHERE id=?", record.StartHost.ID)
			case "host reservation":
				dockerVolumeSQL(t, f.journal, "UPDATE managed_reservations SET generation=generation+1 WHERE token=?", record.StartHost.ID)
			}
			if modified != record {
				body, err = json.Marshal(modified)
				if err != nil {
					t.Fatal(err)
				}
			}
			dockerVolumeSQL(t, f.journal, "UPDATE managed_docker_sources SET body=?", body)
			if _, err := loadManagedDockerSource(t.Context(), f.journal, record.Keeper); err == nil {
				t.Fatal("corrupt complete source accepted")
			}
			if _, err := dispatchManagedDockerSourceStart(t.Context(), f.journal, record.Keeper, record.RuntimeConfigSHA256); err == nil {
				t.Fatal("corrupt source dispatched")
			}
			if dockerVolumeCounts(t, f.journal) != before {
				t.Fatal("corruption spent authority")
			}
			dockerVolumeSQL(t, f.journal, "UPDATE managed_docker_sources SET body=?,start_scope_id=?", original, record.StartHost.ID)
			dockerVolumeSQL(t, f.journal, "UPDATE managed_scopes SET body=?,backend='launchd' WHERE id=?", scopeBody, record.StartHost.ID)
			dockerVolumeSQL(t, f.journal, "UPDATE managed_reservations SET generation=? WHERE token=?", f.generation, record.StartHost.ID)
		})
	}
}

func sourcePrepareFillBudget(t *testing.T, f *sourcePrepareFixture, mode string) {
	t.Helper()
	if mode == "host" {
		var used int
		if err := f.journal.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_reservations WHERE kind='host'").Scan(&used); err != nil {
			t.Fatal(err)
		}
		for i := used; i < f.manifest.Spec.Runtime.MaxHostInvocations; i++ {
			dockerVolumeSQL(t, f.journal, "INSERT INTO managed_reservations(kind,token,generation) VALUES('host',?,?)", fmt.Sprintf("budget-%d", i), f.generation)
		}
		return
	}
	bounds, err := managedRecordLimits(f.manifest.Spec.Recovery, f.manifest.Spec.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	var used int64
	if err := f.journal.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_scopes").Scan(&used); err != nil {
		t.Fatal(err)
	}
	for i := used; i < bounds.Scopes; i++ {
		id := fmt.Sprintf("%032x", i+200)
		scope := ScopeHandle{ID: id, Nonce: fmt.Sprintf("%032x", i+50000), Kind: "host", Backend: "launchd", Name: "capacity-" + id, Generation: f.generation}
		body, err := json.Marshal(struct {
			ScopeHandle
			Deadline time.Time
		}{scope, f.deadline})
		if err != nil {
			t.Fatal(err)
		}
		dockerVolumeSQL(t, f.journal, "INSERT INTO managed_scopes(id,nonce,body,backend,name,phase) VALUES(?,?,?,?,?,'reserved')", scope.ID, scope.Nonce, body, scope.Backend, scope.Name)
	}
}

func testSourcePrepareBudgets(t *testing.T) {
	for _, stage := range []string{"start", "seed"} {
		for _, mode := range []string{"host", "scope", "insert"} {
			t.Run(stage+"/"+mode, func(t *testing.T) {
				f := newSourcePrepareFixture(t)
				var keeper ScopeHandle
				if stage == "start" {
					created, err := f.adapter.createSourceKeeper(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					keeper = created.Keeper
				} else {
					keeper = f.readyRecord(t).Keeper
				}
				if mode == "insert" {
					action := "INSERT"
					if stage == "seed" {
						action = "UPDATE"
					}
					dockerVolumeSQL(t, f.journal, "CREATE TRIGGER reject_source BEFORE "+action+" ON managed_docker_sources BEGIN SELECT RAISE(ABORT,'source unavailable'); END")
				} else {
					sourcePrepareFillBudget(t, f, mode)
				}
				before := dockerVolumeCounts(t, f.journal)
				var err error
				if stage == "start" {
					_, err = reserveManagedDockerSource(t.Context(), f.manifest, f.journal, f.generation, keeper, f.deadline, strings.Repeat("a", 64), 2048)
				} else {
					_, err = reserveManagedDockerSeed(t.Context(), f.journal, f.generation, keeper)
				}
				if err == nil {
					t.Fatal("exhausted reservation committed")
				}
				if mode != "insert" && err.Error() != "managed "+mode+" budget exhausted" {
					t.Fatalf("wrong budget guard: %v", err)
				}
				if after := dockerVolumeCounts(t, f.journal); after != before {
					t.Fatal("atomic source reservation leaked authority")
				}
				if stage == "seed" {
					record, err := loadManagedDockerSource(t.Context(), f.journal, keeper)
					if err != nil || record.Phase != "ready" || record.SeedHost != (ScopeHandle{}) {
						t.Fatal("failed seed reservation changed state", err)
					}
				}
			})
		}
	}
}

func testSourcePrepareDeadline(t *testing.T) {
	for _, mode := range []string{"late reader", "expired", "config", "generation", "revoked"} {
		t.Run(mode, func(t *testing.T) {
			f := newSourcePrepareFixture(t)
			record := f.reserve(t)
			before := dockerVolumeCounts(t, f.journal)
			reads := 0
			f.adapter.readConfig = func(ctx context.Context) (ManagedDockerConfig, error) { reads++; return f.config, nil }
			switch mode {
			case "late reader", "expired":
				deadline := time.Now().UTC().Add(50 * time.Millisecond)
				if mode == "expired" {
					deadline = deadline.Add(-time.Second)
				}
				record.OperationDeadline = deadline
				body, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				dockerVolumeSQL(t, f.journal, "UPDATE managed_docker_sources SET body=?", body)
				scopeBody, err := json.Marshal(struct {
					ScopeHandle
					Deadline time.Time
				}{record.StartHost, deadline})
				if err != nil {
					t.Fatal(err)
				}
				dockerVolumeSQL(t, f.journal, "UPDATE managed_scopes SET body=? WHERE id=?", scopeBody, record.StartHost.ID)
				f.adapter.readConfig = func(ctx context.Context) (ManagedDockerConfig, error) {
					reads++
					got, ok := ctx.Deadline()
					if !ok || !got.Equal(deadline) {
						t.Fatal("reader lacks original source deadline")
					}
					<-ctx.Done()
					return f.config, nil
				}
			case "config":
				f.config.OroSHA256 = strings.Repeat("f", 64)
			case "generation":
				f.adapter.generation++
			case "revoked":
				dockerVolumeSQL(t, f.journal, "UPDATE managed_scopes SET phase='revoked' WHERE id=?", record.Keeper.ID)
				before = dockerVolumeCounts(t, f.journal)
			}
			if _, err := f.adapter.resumeSource(t.Context(), record.Keeper, f.entries); err == nil {
				t.Fatal("inactive source resumed")
			}
			if mode == "expired" && reads != 0 {
				t.Fatal("expired operation entered reader")
			}
			if len(f.runtime.calls) != 4 || dockerVolumeCounts(t, f.journal) != before {
				t.Fatal("inactive source spent authority")
			}
		})
	}
}

func testSourcePrepareTransitions(t *testing.T) {
	f := newSourcePrepareFixture(t)
	s := f.reserve(t)
	ctx := t.Context()
	before := dockerVolumeCounts(t, f.journal)
	if err := acknowledgeManagedDockerSourceStart(ctx, f.journal, s.Keeper, s.StartHost); err == nil {
		t.Fatal("undispatched start acknowledged")
	}
	if err := recordManagedDockerSourceReady(ctx, f.journal, f.generation, s.Keeper, "2026-10-24T06:00:00Z"); err == nil {
		t.Fatal("unacknowledged start became ready")
	}
	if _, err := reserveManagedDockerSeed(ctx, f.journal, f.generation, s.Keeper); err == nil {
		t.Fatal("seed reserved before readiness")
	}
	if _, err := dispatchManagedDockerSourceStart(ctx, f.journal, s.Keeper, strings.Repeat("f", 64)); err == nil {
		t.Fatal("changed runtime dispatched")
	}
	if dockerVolumeCounts(t, f.journal) != before {
		t.Fatal("invalid phase spent authority")
	}
	if _, err := dispatchManagedDockerSourceStart(ctx, f.journal, s.Keeper, s.RuntimeConfigSHA256); err != nil {
		t.Fatal(err)
	}
	if err := acknowledgeManagedDockerSourceStart(ctx, f.journal, s.Keeper, s.StartHost); err == nil {
		t.Fatal("unretired start acknowledged")
	}
	sourcePrepareRetire(t, f.journal, s.StartHost)
	if err := acknowledgeManagedDockerSourceStart(ctx, f.journal, s.Keeper, s.Volume); err == nil {
		t.Fatal("foreign host acknowledged")
	}
	if err := acknowledgeManagedDockerSourceStart(ctx, f.journal, s.Keeper, s.StartHost); err != nil {
		t.Fatal(err)
	}
	before = dockerVolumeCounts(t, f.journal)
	if err := acknowledgeManagedDockerSourceStart(ctx, f.journal, s.Keeper, s.StartHost); err != nil {
		t.Fatal(err)
	}
	if dockerVolumeCounts(t, f.journal) != before {
		t.Fatal("ack replay emitted event")
	}
	for _, bad := range []string{"", "2026-10-24T02:00:00-04:00"} {
		if err := recordManagedDockerSourceReady(ctx, f.journal, f.generation, s.Keeper, bad); err == nil {
			t.Fatal("noncanonical readiness accepted")
		}
	}
	if err := recordManagedDockerSourceReady(ctx, f.journal, f.generation, s.Keeper, "2026-10-24T06:00:00Z"); err != nil {
		t.Fatal(err)
	}
	s, err := reserveManagedDockerSeed(ctx, f.journal, f.generation, s.Keeper)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dispatchManagedDockerSeed(ctx, f.journal, s.Keeper, s.SeedSHA256, s.SeedBytes+1); err == nil {
		t.Fatal("changed seed size dispatched")
	}
	if err := completeManagedDockerSeed(ctx, f.journal, f.generation, s.Keeper, s.StartedAt); err == nil {
		t.Fatal("undispatched copy published source")
	}
	if _, err := dispatchManagedDockerSeed(ctx, f.journal, s.Keeper, s.SeedSHA256, s.SeedBytes); err != nil {
		t.Fatal(err)
	}
	sourcePrepareRetire(t, f.journal, s.SeedHost)
	if err := acknowledgeManagedDockerSeed(ctx, f.journal, s.Keeper, s.SeedHost); err != nil {
		t.Fatal(err)
	}
	if err := completeManagedDockerSeed(ctx, f.journal, f.generation, s.Keeper, "2026-10-24T06:00:01Z"); err == nil {
		t.Fatal("restarted keeper published source")
	}
	if err := completeManagedDockerSeed(ctx, f.journal, f.generation, s.Keeper, s.StartedAt); err != nil {
		t.Fatal(err)
	}
}
