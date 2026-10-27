package factory //nolint:testpackage // Exercise durable keeper ownership through the private production adapter.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestManagedSourceKeeperCreate(t *testing.T) {
	if os.Getenv("ORO_KEEPER_PROCESS") == "1" {
		sourceKeeperProcessChild(t)
		return
	}
	t.Run("fresh admission includes keeper intent storage", func(t *testing.T) {
		_, _, store := journalFixture(t)
		var count int
		if err := store.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='managed_docker_keepers'").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatal("fresh admission lacks durable keeper intent storage")
		}
	})
	t.Run("production volume keeper chain and exact budgets", testSourceKeeperCreate)
	t.Run("unknown create cannot replay", testSourceKeeperFailures)
	t.Run("inspection failure retains acknowledged lifetime", testSourceKeeperInspectFailure)
	t.Run("prepared replay preserves original host and deadline", testSourceKeeperPrepared)
	t.Run("complete creation configuration and lifetime", testSourceKeeperInspection)
	t.Run("journal rejects corrupted intent and scope", testSourceKeeperCorruption)
	t.Run("reservation rollback and single volume ownership", testSourceKeeperReservation)
	t.Run("process death preserves one shot dispatch", testSourceKeeperProcessDeath)
	t.Run("admission replay cannot recreate keeper history", testSourceKeeperSchemaReplay)
	t.Run("original authority and config bound every call", testSourceKeeperAuthority)
	t.Run("all resource reservations roll back together", testSourceKeeperBudgets)
	t.Run("acknowledgment and lifetime transitions are exact", testSourceKeeperTransitions)
	t.Run("concurrent dispatch has one durable winner", testSourceKeeperDispatchRace)
}

type sourceKeeperFixture struct {
	*dockerAdapterFixture
	runtime *sourceKeeperRuntime
}

type sourceKeeperRuntime struct {
	base      *dockerAdapterRuntime
	calls     []HostInvocation
	intent    managedDockerKeeperIntent
	change    func(string, HostInvocation, *HostResult) error
	unretired bool
}

func newSourceKeeperFixture(t *testing.T) *sourceKeeperFixture {
	t.Helper()
	return newSourceKeeperFixtureWith(t, nil)
}

func newSourceKeeperFixtureWith(t *testing.T, change func(*ManagedSpec)) *sourceKeeperFixture {
	t.Helper()
	f := newDockerAdapterFixture(t, func(s *ManagedSpec) {
		s.Runtime.ImageDigest = "sha256:" + strings.Repeat("c", 64)
		if change != nil {
			change(s)
		}
	})
	r := &sourceKeeperRuntime{base: f.runtime}
	f.adapter.host = r
	return &sourceKeeperFixture{f, r}
}

func (r *sourceKeeperRuntime) Invoke(ctx context.Context, call HostInvocation) (HostResult, error) {
	r.calls = append(r.calls, call)
	if call.Args[4] == "volume" {
		return r.base.Invoke(ctx, call)
	}
	stage := call.Args[5]
	if stage == "create" {
		var body []byte
		if err := r.base.journal.conn.QueryRowContext(ctx, "SELECT body FROM managed_docker_keepers WHERE create_scope_id=?", call.Scope.ID).Scan(&body); err != nil {
			return HostResult{}, err
		}
		if err := json.Unmarshal(body, &r.intent); err != nil {
			return HostResult{}, err
		}
		if r.intent.Phase != "dispatching" || r.intent.CreateHost != call.Scope {
			r.base.t.Fatal("keeper launched before exact committed dispatch")
		}
	}
	result := HostResult{Stdout: []byte(strings.Repeat("d", 64) + "\n")}
	if stage == "inspect" {
		result = sourceKeeperInspection(r.base.t, r.intent, r.base.intent)
	}
	if err := r.base.journal.RecordHello(ctx, call.Scope, 102, 102); err != nil {
		return HostResult{}, err
	}
	if !r.unretired {
		if err := r.base.journal.MarkQuiescent(ctx, call.Scope); err != nil {
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

func (r *sourceKeeperRuntime) Retire(ctx context.Context, scope ScopeHandle) error {
	return r.base.Retire(ctx, scope)
}

func sourceKeeperObject(intent managedDockerKeeperIntent, volume managedDockerVolumeIntent) map[string]any {
	object := map[string]any{
		"Id": strings.Repeat("d", 64), "Name": "/" + intent.Keeper.Name, "Image": intent.Binding.ImageID, "Created": "2026-10-24T01:49:04-04:00",
		"Config":     map[string]any{"User": "65534:65534", "Entrypoint": []string{"/usr/local/bin/oro-factory-guard"}, "Cmd": []string{"--deadline", intent.GuardDeadline.Format(time.RFC3339Nano)}, "Labels": intent.Binding.Labels, "Tty": false, "OpenStdin": false, "AttachStdout": true},
		"State":      map[string]any{"Status": "created", "Running": false, "Paused": false, "Restarting": false, "Dead": false, "Pid": 0, "ExitCode": 0, "Error": "", "StartedAt": "0001-01-01T00:00:00Z", "FinishedAt": "0001-01-01T00:00:00Z"},
		"HostConfig": map[string]any{"NetworkMode": "none", "PidMode": "", "IpcMode": "private", "CgroupnsMode": "private", "ReadonlyRootfs": true, "Privileged": false, "CapAdd": nil, "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges"}, "Memory": int64(64 << 20), "MemorySwap": int64(64 << 20), "NanoCpus": int64(250000000), "PidsLimit": int64(16), "ShmSize": int64(1 << 20), "RestartPolicy": map[string]any{"Name": "no", "MaximumRetryCount": 0}, "LogConfig": map[string]any{"Type": "none", "Config": map[string]string{}}, "Binds": nil, "VolumesFrom": nil, "Tmpfs": nil, "Devices": nil, "DeviceRequests": nil, "Mounts": []any{map[string]any{"Type": "volume", "Source": volume.Binding.Name, "Target": "/source", "ReadOnly": false, "VolumeOptions": map[string]any{"NoCopy": true}}}},
		"Mounts":     []any{map[string]any{"Type": "volume", "Name": volume.Binding.Name, "Source": "/var/lib/docker/volumes/owned/_data", "Destination": "/source", "Driver": "local", "RW": true, "Propagation": ""}},
	}
	host := object["HostConfig"].(map[string]any)
	host["Init"], host["UTSMode"] = false, ""
	return object
}

func sourceKeeperInspection(t *testing.T, intent managedDockerKeeperIntent, volume managedDockerVolumeIntent) HostResult {
	t.Helper()
	data, err := json.Marshal([]any{sourceKeeperObject(intent, volume)})
	if err != nil {
		t.Fatal(err)
	}
	return HostResult{Stdout: data}
}

func testSourceKeeperCreate(t *testing.T) {
	f := newSourceKeeperFixture(t)
	before := dockerVolumeCounts(t, f.journal)
	ctx, cancel := context.WithDeadline(t.Context(), f.deadline)
	defer cancel()
	got, err := f.adapter.createSourceKeeper(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != "acknowledged" || got.Binding.ContainerID != strings.Repeat("d", 64) || got.Binding.CreatedAt != "2026-10-24T05:49:04Z" || !got.GuardDeadline.Equal(f.manifest.HardStop) {
		t.Fatalf("wrong keeper %+v", got)
	}
	after := dockerVolumeCounts(t, f.journal)
	for i, delta := range [4]int64{4, 6, 1, 20} {
		if after[i]-before[i] != delta {
			t.Fatalf("counter %d delta=%d want=%d", i, after[i]-before[i], delta)
		}
	}
	if len(f.runtime.calls) != 4 {
		t.Fatalf("calls=%d", len(f.runtime.calls))
	}
	for _, call := range f.runtime.calls {
		if !call.Deadline.Equal(f.deadline) || call.Path != f.config.DockerPath || call.Dir != f.root || call.Stdin != nil || call.OutputLimit != 1<<20 || !slices.Equal(call.Env, []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "LANG=C"}) {
			t.Fatalf("wrong fixed invocation %+v", call)
		}
	}
	call := f.runtime.calls[2]
	options := map[string]string{}
	flags := map[string]bool{}
	labels := map[string]string{}
	args := call.Args[6 : len(call.Args)-3]
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--pull=never" || arg == "--read-only" || arg == "--init=false" {
			flags[arg] = true
			continue
		}
		if i+1 >= len(args) {
			t.Fatalf("missing operand %q", arg)
		}
		i++
		if arg == "--label" {
			k, v, ok := strings.Cut(args[i], "=")
			if !ok || labels[k] != "" {
				t.Fatal("invalid label")
			}
			labels[k] = v
			continue
		}
		if _, ok := options[arg]; ok {
			t.Fatalf("duplicate option %s", arg)
		}
		options[arg] = args[i]
	}
	want := map[string]string{"--name": got.Keeper.Name, "--network": "none", "--cap-drop": "ALL", "--security-opt": "no-new-privileges", "--ipc": "private", "--cgroupns": "private", "--restart": "no", "--log-driver": "none", "--memory": "67108864", "--memory-swap": "67108864", "--cpus": "0.25", "--pids-limit": "16", "--shm-size": "1048576", "--user": "65534:65534", "--entrypoint": "/usr/local/bin/oro-factory-guard", "--mount": "type=volume,source=" + got.Volume.Name + ",target=/source,volume-nocopy"}
	if !reflect.DeepEqual(options, want) || !reflect.DeepEqual(labels, got.Binding.Labels) || !reflect.DeepEqual(flags, map[string]bool{"--pull=never": true, "--read-only": true, "--init=false": true}) {
		t.Fatalf("wrong exact create options %v flags %v labels %v", options, flags, labels)
	}
	if !slices.Equal(call.Args[len(call.Args)-3:], []string{f.manifest.Spec.Runtime.ImageDigest, "--deadline", f.manifest.HardStop.Format(time.RFC3339Nano)}) {
		t.Fatal("wrong image/guard deadline")
	}
	if !slices.Equal(f.runtime.calls[3].Args, []string{"--config", f.config.DockerConfig, "--context", f.config.DockerContext, "container", "inspect", got.Binding.ContainerID}) {
		t.Fatal("wrong inspection")
	}
	stored, err := loadManagedDockerKeeper(t.Context(), f.journal, got.Keeper)
	if err != nil || !reflect.DeepEqual(stored, got) {
		t.Fatalf("durable readback %v %v", stored, err)
	}
}

func testSourceKeeperFailures(t *testing.T) {
	for _, mode := range []string{"runtime", "nonzero", "partial", "extra", "stderr", "oversized", "unretired", "canceled", "commit"} {
		t.Run(mode, func(t *testing.T) {
			f := newSourceKeeperFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			f.runtime.change = func(stage string, _ HostInvocation, r *HostResult) error {
				if stage != "create" {
					return nil
				}
				switch mode {
				case "runtime":
					return errors.New("unknown daemon completion")
				case "nonzero":
					r.ExitCode = 1
				case "partial":
					r.Stdout = r.Stdout[:10]
				case "extra":
					r.Stdout = append(r.Stdout, 'x')
				case "stderr":
					r.Stderr = []byte("warning")
				case "oversized":
					r.Stdout = make([]byte, 1<<20+1)
				case "canceled":
					cancel()
				case "commit":
					dockerVolumeSQL(t, f.journal, "CREATE TRIGGER reject_keeper BEFORE UPDATE ON managed_docker_keepers BEGIN SELECT RAISE(ABORT,'commit failed'); END")
				}
				return nil
			}
			f.runtime.unretired = mode == "unretired"
			if _, err := f.adapter.createSourceKeeper(ctx); err == nil {
				t.Fatal("uncertain create accepted")
			}
			intent, err := loadManagedDockerKeeper(t.Context(), f.journal, f.runtime.intent.Keeper)
			if err != nil || intent.Phase != "dispatching" || intent.Binding.ContainerID != "" {
				t.Fatalf("uncertainty lost %+v %v", intent, err)
			}
			before := len(f.runtime.calls)
			if _, err := f.adapter.dispatchPreparedKeeper(t.Context(), intent.Keeper); err == nil {
				t.Fatal("uncertain replay accepted")
			}
			if len(f.runtime.calls) != before {
				t.Fatal("uncertain create invoked twice")
			}
			for _, scope := range []ScopeHandle{intent.Keeper, intent.Volume} {
				observation, err := f.journal.Observe(t.Context(), scope)
				if err != nil || observation.Phase == "quiescent" {
					t.Fatal("unknown daemon effect released", err)
				}
			}
		})
	}
}

func testSourceKeeperInspectFailure(t *testing.T) {
	for _, mode := range []string{"absent", "identity", "runtime"} {
		t.Run(mode, func(t *testing.T) {
			f := newSourceKeeperFixture(t)
			f.runtime.change = func(stage string, _ HostInvocation, r *HostResult) error {
				if stage != "inspect" {
					return nil
				}
				switch mode {
				case "absent":
					r.Stdout = []byte("[]\n")
					r.ExitCode = 1
				case "identity":
					r.Stdout = []byte(strings.ReplaceAll(string(r.Stdout), "65534:65534", "0:0"))
				case "runtime":
					return errors.New("inspection failed")
				}
				return nil
			}
			if _, err := f.adapter.createSourceKeeper(t.Context()); err == nil {
				t.Fatal("bad inspection accepted")
			}
			intent, err := loadManagedDockerKeeper(t.Context(), f.journal, f.runtime.intent.Keeper)
			if err != nil || intent.Phase != "acknowledged" || intent.Binding.ContainerID != strings.Repeat("d", 64) || intent.Binding.CreatedAt != "" {
				t.Fatalf("wrong retained create %+v %v", intent, err)
			}
		})
	}
}

func (f *sourceKeeperFixture) prepare(t *testing.T) managedDockerKeeperIntent {
	t.Helper()
	volume, err := f.adapter.createVolume(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	intent, err := reserveManagedDockerKeeper(t.Context(), f.manifest, f.journal, f.generation, volume.Volume, f.deadline, f.config.RuntimeConfigSHA256)
	if err != nil {
		t.Fatal(err)
	}
	return intent
}

func testSourceKeeperPrepared(t *testing.T) {
	f := newSourceKeeperFixture(t)
	intent := f.prepare(t)
	before := dockerVolumeCounts(t, f.journal)
	got, err := f.adapter.dispatchPreparedKeeper(t.Context(), intent.Keeper)
	if err != nil {
		t.Fatal(err)
	}
	after := dockerVolumeCounts(t, f.journal)
	if got.CreateHost != intent.CreateHost || !got.CreateDeadline.Equal(intent.CreateDeadline) || after[0]-before[0] != 1 || after[1]-before[1] != 1 {
		t.Fatal("prepared replay replaced its create reservation")
	}
	if _, err := f.adapter.dispatchPreparedKeeper(t.Context(), intent.Keeper); err == nil {
		t.Fatal("completed keeper recreated")
	}
}

func testSourceKeeperInspection(t *testing.T) {
	f := newSourceKeeperFixture(t)
	intent := f.prepare(t)
	intent.Phase = "acknowledged"
	intent.Binding.ContainerID = strings.Repeat("d", 64)
	volume, err := loadManagedDockerVolume(t.Context(), f.journal, intent.Volume)
	if err != nil {
		t.Fatal(err)
	}
	changes := map[string]func(map[string]any){
		"init true":    func(v map[string]any) { v["HostConfig"].(map[string]any)["Init"] = true },
		"init null":    func(v map[string]any) { v["HostConfig"].(map[string]any)["Init"] = nil },
		"init type":    func(v map[string]any) { v["HostConfig"].(map[string]any)["Init"] = "false" },
		"init missing": func(v map[string]any) { delete(v["HostConfig"].(map[string]any), "Init") },
		"init alias":   func(v map[string]any) { v["HostConfig"].(map[string]any)["init"] = false },
		"uts host":     func(v map[string]any) { v["HostConfig"].(map[string]any)["UTSMode"] = "host" },
		"uts null":     func(v map[string]any) { v["HostConfig"].(map[string]any)["UTSMode"] = nil },
		"uts type":     func(v map[string]any) { v["HostConfig"].(map[string]any)["UTSMode"] = false },
		"uts missing":  func(v map[string]any) { delete(v["HostConfig"].(map[string]any), "UTSMode") },
		"uts alias":    func(v map[string]any) { v["HostConfig"].(map[string]any)["utsmode"] = "" },
		"host alias":   func(v map[string]any) { v["hostconfig"] = v["HostConfig"] },
		"mount alias":  func(v map[string]any) { v["Mounts"].([]any)[0].(map[string]any)["rw"] = true },
		"volume subpath": func(v map[string]any) {
			v["HostConfig"].(map[string]any)["Mounts"].([]any)[0].(map[string]any)["VolumeOptions"].(map[string]any)["Subpath"] = "foreign"
		},
		"volume driver override": func(v map[string]any) {
			v["HostConfig"].(map[string]any)["Mounts"].([]any)[0].(map[string]any)["VolumeOptions"].(map[string]any)["DriverConfig"] = map[string]any{"Name": "foreign"}
		},
		"wrong id": func(v map[string]any) { v["Id"] = strings.Repeat("e", 64) }, "wrong image": func(v map[string]any) { v["Image"] = "sha256:" + strings.Repeat("e", 64) }, "wrong name": func(v map[string]any) { v["Name"] = "/other" }, "bad time": func(v map[string]any) { v["Created"] = "bad" }, "state missing": func(v map[string]any) { delete(v, "State") }, "state null": func(v map[string]any) { v["State"] = nil }, "wrong user": func(v map[string]any) { v["Config"].(map[string]any)["User"] = "0:0" }, "guard": func(v map[string]any) {
			v["Config"].(map[string]any)["Cmd"] = []string{"--deadline", "2030-01-01T00:00:00Z"}
		}, "running": func(v map[string]any) { v["State"].(map[string]any)["Running"] = true }, "started": func(v map[string]any) { v["State"].(map[string]any)["StartedAt"] = "2026-10-24T05:49:04Z" }, "labels": func(v map[string]any) { v["Config"].(map[string]any)["Labels"] = map[string]string{} }, "mount": func(v map[string]any) { v["Mounts"].([]any)[0].(map[string]any)["Name"] = "foreign" }, "readonly mount": func(v map[string]any) { v["Mounts"].([]any)[0].(map[string]any)["RW"] = false }, "extra mount": func(v map[string]any) { v["Mounts"] = append(v["Mounts"].([]any), map[string]any{}) }, "host bind": func(v map[string]any) { v["HostConfig"].(map[string]any)["Binds"] = []string{"/tmp:/host"} }, "copyup": func(v map[string]any) {
			v["HostConfig"].(map[string]any)["Mounts"].([]any)[0].(map[string]any)["VolumeOptions"] = map[string]any{"NoCopy": false}
		},
	}
	for _, field := range []string{"Memory", "MemorySwap", "NanoCpus", "PidsLimit", "ShmSize"} {
		changes[field] = func(v map[string]any) { v["HostConfig"].(map[string]any)[field] = 1 }
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			v := sourceKeeperObject(intent, volume)
			change(v)
			data, err := json.Marshal([]any{v})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parseManagedDockerKeeperInspection(HostResult{Stdout: data}, intent, volume); err == nil {
				t.Fatal("bad keeper inspection accepted")
			}
		})
	}
	good := sourceKeeperInspection(t, intent, volume)
	got, err := parseManagedDockerKeeperInspection(good, intent, volume)
	if err != nil || got.CreatedAt != "2026-10-24T05:49:04Z" {
		t.Fatalf("canonical lifetime %v %v", got, err)
	}
	for _, data := range []string{"[]", "[null]", string(good.Stdout) + "x", strings.Replace(string(good.Stdout), `"Id":`, `"Id":"duplicate","Id":`, 1)} {
		if _, err := parseManagedDockerKeeperInspection(HostResult{Stdout: []byte(data)}, intent, volume); err == nil {
			t.Fatal("malformed inspection accepted")
		}
	}
}

func testSourceKeeperCorruption(t *testing.T) {
	for _, mode := range []string{"extra field", "deadline", "host phase", "volume binding", "column"} {
		t.Run(mode, func(t *testing.T) {
			f := newSourceKeeperFixture(t)
			intent := f.prepare(t)
			switch mode {
			case "extra field":
				dockerVolumeSQL(t, f.journal, "UPDATE managed_docker_keepers SET body=json_set(body,'$.unknown',1)")
			case "deadline":
				dockerVolumeSQL(t, f.journal, "UPDATE managed_docker_keepers SET body=json_set(body,'$.guard_deadline','2030-01-01T00:00:00Z')")
			case "host phase":
				dockerVolumeSQL(t, f.journal, "UPDATE managed_scopes SET body=json_set(body,'$.Phase','quiescent') WHERE id=?", intent.CreateHost.ID)
			case "volume binding":
				dockerVolumeSQL(t, f.journal, "UPDATE managed_docker_intents SET body=json_set(body,'$.binding.created_at','')")
			case "column":
				dockerVolumeSQL(t, f.journal, "UPDATE managed_docker_keepers SET create_scope_id=?", intent.Volume.ID)
			}
			before := dockerVolumeCounts(t, f.journal)
			if _, err := loadManagedDockerKeeper(t.Context(), f.journal, intent.Keeper); err == nil {
				t.Fatal("corrupt intent loaded")
			}
			if _, err := f.adapter.dispatchPreparedKeeper(t.Context(), intent.Keeper); err == nil {
				t.Fatal("corrupt keeper dispatched")
			}
			if after := dockerVolumeCounts(t, f.journal); after != before {
				t.Fatal("corrupt read changed counters")
			}
		})
	}
}

func testSourceKeeperReservation(t *testing.T) {
	f := newSourceKeeperFixture(t)
	intent := f.prepare(t)
	before := dockerVolumeCounts(t, f.journal)
	if _, err := reserveManagedDockerKeeper(t.Context(), f.manifest, f.journal, f.generation, intent.Volume, f.deadline, f.config.RuntimeConfigSHA256); err == nil {
		t.Fatal("second keeper reserved for same volume")
	}
	if after := dockerVolumeCounts(t, f.journal); after != before {
		t.Fatal("failed keeper reservation leaked charges")
	}
	for _, bad := range []ScopeHandle{{}, intent.Keeper} {
		if _, err := f.adapter.createKeeperForVolume(t.Context(), bad); err == nil {
			t.Fatal("foreign volume accepted")
		}
	}
}

type sourceKeeperProcessRequest struct {
	Root, ID   string
	Volume     ScopeHandle
	Dispatched bool
}

func sourceKeeperProcessChild(t *testing.T) {
	t.Helper()
	var request sourceKeeperProcessRequest
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		t.Fatal(err)
	}
	manifest, err := loadManagedManifest(t.Context(), request.Root, request.ID)
	if err != nil {
		t.Fatal(err)
	}
	journal := openJournalFixture(t, request.Root, request.ID)
	intent, err := reserveManagedDockerKeeper(t.Context(), manifest, journal, 1, request.Volume, time.Now().UTC().Add(20*time.Second), strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if request.Dispatched {
		intent, err = dispatchManagedDockerKeeper(t.Context(), journal, intent.Keeper, intent.RuntimeConfigSHA256)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(intent); err != nil {
		t.Fatal(err)
	}
	<-t.Context().Done()
}

func testSourceKeeperProcessDeath(t *testing.T) {
	for _, dispatched := range []bool{false, true} {
		t.Run(fmt.Sprint(dispatched), func(t *testing.T) {
			f := newSourceKeeperFixture(t)
			volume, err := f.adapter.createVolume(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			request, err := json.Marshal(sourceKeeperProcessRequest{f.root, f.manifest.Spec.ID, volume.Volume, dispatched})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestManagedSourceKeeperCreate$", "-test.timeout=30s")
			cmd.Env = append(os.Environ(), "ORO_KEEPER_PROCESS=1")
			cmd.Stdin = strings.NewReader(string(request))
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
			var intent managedDockerKeeperIntent
			if err := json.NewDecoder(output).Decode(&intent); err != nil {
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
			got, err := loadManagedDockerKeeper(t.Context(), reopened, intent.Keeper)
			if err != nil || !reflect.DeepEqual(got, intent) {
				t.Fatalf("death changed durable keeper %+v %v", got, err)
			}
			before := dockerVolumeCounts(t, reopened)
			got, err = dispatchManagedDockerKeeper(t.Context(), reopened, intent.Keeper, intent.RuntimeConfigSHA256)
			if dispatched {
				if err == nil {
					t.Fatal("unknown create replayed after death")
				}
			} else if err != nil || got.CreateHost != intent.CreateHost || !got.CreateDeadline.Equal(intent.CreateDeadline) {
				t.Fatalf("prepared dispatch changed reservation %+v %v", got, err)
			}
			after := dockerVolumeCounts(t, reopened)
			if after[0] != before[0] || after[1] != before[1] || after[2] != before[2] {
				t.Fatal("reopen spent new capacity")
			}
		})
	}
}

func testSourceKeeperSchemaReplay(t *testing.T) {
	for _, used := range []bool{false, true} {
		t.Run(fmt.Sprint(used), func(t *testing.T) {
			f := newSourceKeeperFixture(t)
			if used {
				_ = f.prepare(t)
			}
			before := dockerVolumeCounts(t, f.journal)
			dockerVolumeSQL(t, f.journal, "DROP TABLE managed_docker_keepers")
			if _, err := AdmitManaged(t.Context(), f.root, f.manifest.Spec, f.manifest.Reservation); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := f.journal.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_master WHERE name='managed_docker_keepers'").Scan(&count); err != nil || count != 0 {
				t.Fatal("admission reconstructed lost keeper history", err)
			}
			if after := dockerVolumeCounts(t, f.journal); after != before {
				t.Fatal("admission reset counters")
			}
		})
	}
}

func testSourceKeeperAuthority(t *testing.T) {
	t.Run("unsupported image has no resource effect", func(t *testing.T) {
		f := newSourceKeeperFixtureWith(t, func(s *ManagedSpec) { s.Runtime.ImageDigest = "golang@sha256:" + strings.Repeat("b", 64) })
		before := dockerVolumeCounts(t, f.journal)
		if _, err := f.adapter.createSourceKeeper(t.Context()); err == nil {
			t.Fatal("repository digest substituted for derived image ID")
		}
		if len(f.runtime.calls) != 0 || dockerVolumeCounts(t, f.journal) != before {
			t.Fatal("invalid image spent capacity")
		}
	})
	for _, mode := range []string{"canceled", "expired", "stale", "config", "read late"} {
		t.Run(mode, func(t *testing.T) {
			f := newSourceKeeperFixture(t)
			intent := f.prepare(t)
			before := dockerVolumeCounts(t, f.journal)
			ctx := t.Context()
			switch mode {
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "expired":
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
			case "stale":
				if _, _, err := f.journal.advanceEpoch(t.Context(), "supervisor", "replacement", time.Now()); err != nil {
					t.Fatal(err)
				}
				before = dockerVolumeCounts(t, f.journal)
			case "config":
				f.config.DockerSHA256 = strings.Repeat("f", 64)
			case "read late":
				deadline := time.Now().UTC().Add(30 * time.Millisecond)
				intent.CreateDeadline = deadline
				body, err := json.Marshal(intent)
				if err != nil {
					t.Fatal(err)
				}
				dockerVolumeSQL(t, f.journal, "UPDATE managed_docker_keepers SET body=?", body)
				scopeBody, err := json.Marshal(struct {
					ScopeHandle
					Deadline time.Time
				}{intent.CreateHost, deadline})
				if err != nil {
					t.Fatal(err)
				}
				dockerVolumeSQL(t, f.journal, "UPDATE managed_scopes SET body=? WHERE id=?", scopeBody, intent.CreateHost.ID)
				f.adapter.readConfig = func(readCtx context.Context) (ManagedDockerConfig, error) {
					got, ok := readCtx.Deadline()
					if !ok || !got.Equal(deadline) {
						t.Fatal("reader did not inherit original create deadline")
					}
					<-readCtx.Done()
					return f.config, nil
				}
			}
			if _, err := f.adapter.dispatchPreparedKeeper(ctx, intent.Keeper); err == nil {
				t.Fatal("inactive keeper dispatched")
			}
			if len(f.runtime.calls) != 2 || dockerVolumeCounts(t, f.journal) != before {
				t.Fatal("inactive keeper spent work")
			}
		})
	}
}

func testSourceKeeperBudgets(t *testing.T) {
	for _, mode := range []string{"host", "insert", "scope"} {
		t.Run(mode, func(t *testing.T) {
			f := newSourceKeeperFixtureWith(t, func(s *ManagedSpec) {
				if mode == "host" {
					s.Runtime.MaxHostInvocations = 2
				}
			})
			volume, err := f.adapter.createVolume(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "insert":
				dockerVolumeSQL(t, f.journal, "CREATE TRIGGER reject_keeper_insert BEFORE INSERT ON managed_docker_keepers BEGIN SELECT RAISE(ABORT,'intent unavailable'); END")
			case "scope":
				bounds, err := managedRecordLimits(f.manifest.Spec.Recovery, f.manifest.Spec.Runtime)
				if err != nil {
					t.Fatal(err)
				}
				for i := int64(3); i < bounds.Scopes-1; i++ {
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
			before := dockerVolumeCounts(t, f.journal)
			_, err = reserveManagedDockerKeeper(t.Context(), f.manifest, f.journal, f.generation, volume.Volume, f.deadline, f.config.RuntimeConfigSHA256)
			if err == nil {
				t.Fatal("exhausted transaction committed")
			}
			if want := map[string]string{"host": "managed host budget exhausted", "scope": "managed scope budget exhausted"}[mode]; want != "" && err.Error() != want {
				t.Fatalf("reservation failed for %q, want exact budget error %q", err, want)
			}
			if after := dockerVolumeCounts(t, f.journal); after != before {
				t.Fatal("failed transaction leaked reservations")
			}
			var keepers int
			if err := f.journal.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_docker_keepers").Scan(&keepers); err != nil || keepers != 0 {
				t.Fatal("failed transaction retained intent", err)
			}
		})
	}
}

func testSourceKeeperTransitions(t *testing.T) {
	f := newSourceKeeperFixture(t)
	intent := f.prepare(t)
	observed := cloneManagedDockerBinding(intent.Binding)
	observed.ContainerID = strings.Repeat("d", 64)
	observed.CreatedAt = "2026-10-24T05:49:04Z"
	if err := bindManagedDockerKeeper(t.Context(), f.journal, f.generation, intent.Keeper, observed); err == nil {
		t.Fatal("never dispatched keeper bound")
	}
	if err := acknowledgeManagedDockerKeeper(t.Context(), f.journal, intent.Keeper, intent.CreateHost, observed.ContainerID); err == nil {
		t.Fatal("unused host acknowledged")
	}
	if _, err := dispatchManagedDockerKeeper(t.Context(), f.journal, intent.Keeper, intent.RuntimeConfigSHA256); err != nil {
		t.Fatal(err)
	}
	if err := acknowledgeManagedDockerKeeper(t.Context(), f.journal, intent.Keeper, intent.CreateHost, observed.ContainerID); err == nil {
		t.Fatal("unretired host acknowledged")
	}
	if err := f.journal.MarkQuiescent(t.Context(), intent.CreateHost); err != nil {
		t.Fatal(err)
	}
	wrongHost := intent.CreateHost
	wrongHost.Nonce = strings.Repeat("e", 32)
	if err := acknowledgeManagedDockerKeeper(t.Context(), f.journal, intent.Keeper, wrongHost, observed.ContainerID); err == nil {
		t.Fatal("foreign host acknowledged")
	}
	if err := acknowledgeManagedDockerKeeper(t.Context(), f.journal, intent.Keeper, intent.CreateHost, "short"); err == nil {
		t.Fatal("partial container ID acknowledged")
	}
	if err := acknowledgeManagedDockerKeeper(t.Context(), f.journal, intent.Keeper, intent.CreateHost, observed.ContainerID); err != nil {
		t.Fatal(err)
	}
	before := dockerVolumeCounts(t, f.journal)
	if err := acknowledgeManagedDockerKeeper(t.Context(), f.journal, intent.Keeper, intent.CreateHost, observed.ContainerID); err != nil {
		t.Fatal(err)
	}
	if after := dockerVolumeCounts(t, f.journal); after != before {
		t.Fatal("repeated acknowledgment wrote history")
	}
	if err := acknowledgeManagedDockerKeeper(t.Context(), f.journal, intent.Keeper, intent.CreateHost, strings.Repeat("e", 64)); err == nil {
		t.Fatal("acknowledged ID replaced")
	}
	for _, created := range []string{"", "bad", "2026-10-24T01:49:04-04:00"} {
		bad := cloneManagedDockerBinding(observed)
		bad.CreatedAt = created
		if err := bindManagedDockerKeeper(t.Context(), f.journal, f.generation, intent.Keeper, bad); err == nil {
			t.Fatal("noncanonical lifetime bound")
		}
	}
	if err := bindManagedDockerKeeper(t.Context(), f.journal, f.generation+1, intent.Keeper, observed); err == nil {
		t.Fatal("unowned epoch bound lifetime")
	}
	bad := cloneManagedDockerBinding(observed)
	bad.Labels["dev.getoro.factory.kind"] = "container"
	if err := bindManagedDockerKeeper(t.Context(), f.journal, f.generation, intent.Keeper, bad); err == nil {
		t.Fatal("foreign label bound")
	}
	if err := bindManagedDockerKeeper(t.Context(), f.journal, f.generation, intent.Keeper, observed); err != nil {
		t.Fatal(err)
	}
	before = dockerVolumeCounts(t, f.journal)
	if err := bindManagedDockerKeeper(t.Context(), f.journal, f.generation, intent.Keeper, observed); err != nil {
		t.Fatal(err)
	}
	if after := dockerVolumeCounts(t, f.journal); after != before {
		t.Fatal("same lifetime wrote history")
	}
	bad = cloneManagedDockerBinding(observed)
	bad.CreatedAt = "2026-10-24T05:49:05Z"
	if err := bindManagedDockerKeeper(t.Context(), f.journal, f.generation, intent.Keeper, bad); err == nil {
		t.Fatal("creation lifetime replaced")
	}
	loaded, err := loadManagedDockerKeeper(t.Context(), f.journal, intent.Keeper)
	if err != nil || !reflect.DeepEqual(loaded.Binding, observed) {
		t.Fatalf("lifetime changed %+v %v", loaded, err)
	}
	loaded.Binding.Labels["dev.getoro.factory.kind"] = "changed"
	reloaded, err := loadManagedDockerKeeper(t.Context(), f.journal, intent.Keeper)
	if err != nil || !reflect.DeepEqual(reloaded.Binding, observed) {
		t.Fatal("caller mutated retained labels", err)
	}
}

func testSourceKeeperDispatchRace(t *testing.T) {
	f := newSourceKeeperFixture(t)
	intent := f.prepare(t)
	other := openJournalFixture(t, f.root, f.manifest.Spec.ID)
	var group sync.WaitGroup
	results := make(chan error, 2)
	for _, journal := range []*managedJournal{f.journal, other} {
		group.Go(func() {
			_, err := dispatchManagedDockerKeeper(t.Context(), journal, intent.Keeper, intent.RuntimeConfigSHA256)
			results <- err
		})
	}
	group.Wait()
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
}
