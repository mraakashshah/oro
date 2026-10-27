//nolint:testpackage // Exercises the managed controller and private candidate transport.
package factory

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestManagedCandidateExecution(t *testing.T) {
	if os.Getenv("ORO_CANDIDATE_EXEC_LEADER") != "1" {
		runCandidateExecutionLeader(t)
		return
	}
	t.Run("expired checkout", candidateExecutionExpiredCheckout)
	for _, mode := range []string{"success", "before_stop", "before_epoch", "after_stop", "lost_create", "lost_start", "bad_memory", "bad_mount", "bad_labels", "bad_scratch", "bad_image", "bad_guard", "bad_uts", "bad_uts_alias", "bad_init", "bad_init_alias", "null_init", "missing_init", "bad_lifetime", "bad_ready", "bad_running"} {
		t.Run(mode, func(t *testing.T) { runCandidateExecution(t, mode) })
	}
}

func candidateExecutionExpiredCheckout(t *testing.T) {
	owner, _, workspace := candidateReadyFixture(t, 8*time.Second)
	time.Sleep(time.Until(workspace.OperationDeadline) + 20*time.Millisecond)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	intent, err := reserveManagedCandidate(ctx, owner, workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	keeper, err := loadManagedDockerKeeper(ctx, owner.journal, intent.Source.Keeper)
	if err != nil {
		t.Fatal(err)
	}
	volume, err := loadManagedDockerVolume(ctx, owner.journal, intent.Source.Volume)
	if err != nil {
		t.Fatal(err)
	}
	base := &dockerAdapterRuntime{t: t, journal: owner.journal, intent: volume}
	keeperRuntime := &sourceKeeperRuntime{base: base, intent: keeper}
	sourceRuntime := &sourcePrepareRuntime{base: keeperRuntime, t: t, running: true}
	host := &candidateExecutionRuntime{base: &managedEngineRuntime{git: &sourceGitRuntime{t: t, j: owner.journal}, docker: sourceRuntime}, journal: owner.journal, t: t, mode: "success"}
	manifest := owner.manifest
	config := ManagedDockerConfig{StateRoot: owner.journal.admission.Reservation.StateRoot, ObjectiveID: owner.journal.admission.ObjectiveID, PolicyHash: manifest.PolicyHash, Deadline: manifest.Deadline, HardStop: manifest.HardStop, RuntimeConfigPath: owner.runtime.Path, RuntimeConfigSHA256: owner.runtime.SHA256, OroPath: "/usr/local/bin/oro", OroSHA256: strings.Repeat("d", 64), DockerPath: "/usr/local/bin/docker", DockerSHA256: strings.Repeat("b", 64), DockerConfig: filepath.Join(owner.journal.admission.Reservation.StateRoot, owner.journal.admission.ObjectiveID, "runtime", "docker"), DockerContext: manifest.Spec.Runtime.DockerContext, DockerEndpoint: "unix:///private/tmp/admitted-docker.sock"}
	host.config = config
	adapter, err := newManagedDockerVolumes(ctx, manifest, owner.scope.Generation, owner.journal, host, func(context.Context) (ManagedDockerConfig, error) { return config, nil })
	if err != nil {
		t.Fatal(err)
	}
	ready, err := adapter.prepareCandidate(ctx, owner, intent)
	if err != nil || ready.Phase != "ready" {
		t.Fatalf("candidate after checkout deadline: %s %v", ready.Phase, err)
	}
	if !ready.ExecutionStartedAt.After(workspace.OperationDeadline) || !ready.Source.OperationDeadline.Equal(workspace.OperationDeadline) {
		t.Fatal("candidate renewed checkout identity")
	}
	if len(sourceRuntime.calls) != 2 {
		t.Fatalf("source continuity calls: %d, want two keeper inspections", len(sourceRuntime.calls))
	}
	for _, call := range sourceRuntime.calls {
		if len(call.Args) < 6 || call.Args[4] != "container" || call.Args[5] != "inspect" {
			t.Fatalf("source reseeded: %v", call.Args)
		}
	}
}

func runCandidateExecutionLeader(t *testing.T) {
	t.Helper()
	timeout := 180 * time.Second
	if deadline, ok := t.Deadline(); ok && time.Until(deadline)-5*time.Second < timeout {
		timeout = time.Until(deadline) - 5*time.Second
	}
	if timeout <= time.Second {
		t.Fatal("insufficient time for candidate leader")
	}
	ctx, cancel := context.WithTimeout(t.Context(), timeout+time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestManagedCandidateExecution$", "-test.timeout="+timeout.String(), "-test.v")
	if f := flag.Lookup("test.gocoverdir"); f != nil && f.Value.String() != "" {
		cmd.Args = append(cmd.Args, "-test.gocoverdir="+f.Value.String())
	}
	cmd.Env = append(os.Environ(), "ORO_CANDIDATE_EXEC_LEADER=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 3 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("leader: %v\n%s", err, out)
	}
}

func runCandidateExecution(t *testing.T, mode string) {
	t.Helper()
	root, spec, reservation := managedAdmissionFixture(t)
	if runtime.GOOS == "darwin" {
		path, err := exec.Command("xcrun", "--find", "git").Output()
		if err != nil {
			t.Fatal(err)
		}
		spec.Runtime.GitExecutable = strings.TrimSpace(string(path))
	}
	reservation = reservationForIntendedSpec(t, reservation, spec)
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
		t.Fatal(err)
	}
	ref := ManagedRuntimeReference{Path: filepath.Join(root, spec.ID, "runtime", "runtime.json"), SHA256: strings.Repeat("a", 64)}
	if err := ActivateManaged(t.Context(), root, spec.ID, ref); err != nil {
		t.Fatal(err)
	}
	j := openJournalFixture(t, root, spec.ID)
	gen, _, err := j.beginGeneration(t.Context(), "supervisor", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	scope, err := reserveManagedController(t.Context(), j, "supervisor", gen)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.RecordHello(t.Context(), scope, os.Getpid(), os.Getpid()); err != nil {
		t.Fatal(err)
	}
	manifest, err := loadManagedManifest(t.Context(), root, spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	config := ManagedDockerConfig{StateRoot: root, ObjectiveID: spec.ID, PolicyHash: manifest.PolicyHash, Deadline: manifest.Deadline, HardStop: manifest.HardStop, RuntimeConfigPath: ref.Path, RuntimeConfigSHA256: ref.SHA256, OroPath: "/usr/local/bin/oro", OroSHA256: strings.Repeat("d", 64), DockerPath: "/usr/local/bin/docker", DockerSHA256: strings.Repeat("b", 64), DockerConfig: filepath.Join(root, spec.ID, "runtime", "docker"), DockerContext: spec.Runtime.DockerContext, DockerEndpoint: "unix:///private/tmp/admitted-docker.sock"}
	git := &sourceGitRuntime{t: t, j: j}
	base := &dockerAdapterRuntime{t: t, journal: j}
	keeper := &sourceKeeperRuntime{base: base}
	docker := &sourcePrepareRuntime{base: keeper, t: t}
	runtime := &managedEngineRuntime{git: git, docker: docker}

	recording := &candidateExecutionRuntime{base: runtime, journal: j, t: t, mode: mode, config: config}
	transport := &managedCandidateRecordingRuntime{base: recording}
	status, err := RunManagedController(t.Context(), root, spec.ID, gen, scope.ID, transport, func(context.Context) (ManagedDockerConfig, error) { return config, nil })
	if !isManagedInterruption(err) || status.State != "running" || status.Attempts != 1 {
		t.Fatalf("controller = %+v, %v", status, err)
	}
	var phase string
	expected := map[string]string{"success": "retired", "before_stop": "create_dispatching", "before_epoch": "create_dispatching", "after_stop": "create_dispatching", "lost_create": "create_dispatching", "lost_start": "start_dispatching", "bad_memory": "created", "bad_mount": "created", "bad_labels": "created", "bad_scratch": "created", "bad_image": "created", "bad_guard": "created", "bad_uts": "created", "bad_uts_alias": "created", "bad_init_alias": "created", "bad_init": "created", "null_init": "created", "missing_init": "created", "bad_lifetime": "start_acknowledged", "bad_ready": "start_dispatching", "bad_running": "start_acknowledged"}[mode]
	if err := j.conn.QueryRowContext(t.Context(), "SELECT json_extract(body,'$.phase') FROM managed_docker_candidates").Scan(&phase); err != nil || phase != expected {
		t.Fatalf("candidate not ready: %s %v", phase, err)
	}
	var body []byte
	if queryErr := j.conn.QueryRowContext(t.Context(), "SELECT body FROM managed_docker_candidates").Scan(&body); queryErr != nil {
		t.Fatal(queryErr)
	}
	var intent managedCandidateIntent
	if decodeErr := json.Unmarshal(body, &intent); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if !intent.GuardDeadline.Equal(intent.ExecutionDeadline) || !intent.ExecutionDeadline.Before(manifest.HardStop) {
		t.Fatalf("candidate deadlines: %+v", intent)
	}
	observation, observeErr := j.Observe(t.Context(), intent.Container)
	if observeErr != nil || !observation.Deadline.Equal(intent.ExecutionDeadline) {
		t.Fatalf("candidate scope deadline: %v, %v", observation.Deadline, observeErr)
	}

	wantCalls := map[string]int{"success": 4, "before_stop": 1, "before_epoch": 1, "after_stop": 1, "lost_create": 1, "lost_start": 3, "bad_memory": 2, "bad_mount": 2, "bad_labels": 2, "bad_scratch": 2, "bad_image": 2, "bad_guard": 2, "bad_uts": 2, "bad_uts_alias": 2, "bad_init_alias": 2, "bad_init": 2, "null_init": 2, "missing_init": 2, "bad_lifetime": 4, "bad_ready": 3, "bad_running": 4}[mode]
	if recording.calls != wantCalls {
		t.Fatalf("candidate calls = %d, want %d", recording.calls, wantCalls)
	}
	wantCleanup := 0
	if mode == "success" {
		wantCleanup = 1
	}
	if len(transport.cleanup) != wantCleanup {
		t.Fatalf("cleanup calls = %d, want %d", len(transport.cleanup), wantCleanup)
	}
	if mode == "success" {
		if intent.Retirement == nil || transport.cleanup[0].Scope != intent.Retirement.Scope || observation.Phase != "quiescent" {
			t.Fatalf("retirement = %+v, scope = %+v", intent.Retirement, observation)
		}
		cleanup, err := j.Observe(t.Context(), intent.Retirement.Scope)
		if err != nil || cleanup.Phase != "quiescent" || !cleanup.Deadline.Equal(intent.Retirement.Deadline) {
			t.Fatalf("cleanup scope = %+v, %v", cleanup, err)
		}
	}
	var modelCount, candidateCount, createCount, startCount int
	query := `SELECT (SELECT count(*) FROM managed_reservations WHERE kind='model'), (SELECT count(*) FROM managed_docker_candidates), (SELECT count(*) FROM managed_reservations WHERE kind='host' AND token=json_extract((SELECT body FROM managed_docker_candidates), '$.create_host.id')), (SELECT count(*) FROM managed_reservations WHERE kind='host' AND token=json_extract((SELECT body FROM managed_docker_candidates), '$.start_host.id'))`
	if queryErr := j.conn.QueryRowContext(t.Context(), query).Scan(&modelCount, &candidateCount, &createCount, &startCount); queryErr != nil {
		t.Fatal(queryErr)
	}
	wantStart := 0
	if wantCalls >= 3 {
		wantStart = 1
	}
	if modelCount != 0 || candidateCount != 1 || createCount != 1 || startCount != wantStart {
		t.Fatalf("reservations: models=%d candidates=%d create=%d start=%d", modelCount, candidateCount, createCount, startCount)
	}
	if mode == "success" && !strings.Contains(err.Error(), "engineer-adapter-unavailable") {
		t.Fatalf("missing engineer interruption: %v", err)
	}
}

type candidateExecutionRuntime struct {
	base    *managedEngineRuntime
	journal *managedJournal
	t       *testing.T
	running bool
	mode    string
	config  ManagedDockerConfig
	calls   int
}

func (r *candidateExecutionRuntime) Invoke(ctx context.Context, call HostInvocation) (HostResult, error) {
	var body []byte
	err := r.journal.conn.QueryRowContext(ctx, "SELECT body FROM managed_docker_candidates LIMIT 1").Scan(&body)
	if err != nil {
		return r.base.Invoke(ctx, call)
	}
	var intent managedCandidateIntent
	if err := json.Unmarshal(body, &intent); err != nil {
		return HostResult{}, err
	}
	isCreate := call.Scope == intent.CreateHost && intent.Phase == "create_dispatching"
	isStart := call.Scope == intent.StartHost && intent.Phase == "start_dispatching"
	isInspect := len(call.Args) > 6 && call.Args[4] == "container" && call.Args[5] == "inspect" && call.Args[6] == intent.Binding.ContainerID && intent.Binding.ContainerID != ""
	if !isCreate && !isStart && !isInspect {
		return r.base.Invoke(ctx, call)
	}
	r.calls++
	if isCreate || isStart {
		expected := r.expectedCandidateCall(intent, isCreate)
		if r.mode == "success" {
			r.testTransportRejections(expected)
		}
		if !candidateTransportMatches(call, expected) {
			return HostResult{}, fmt.Errorf("candidate transport differs: got %+v, want %+v", call, expected)
		}
	}
	result := HostResult{}
	switch {
	case isCreate:
		if !slices.Contains(call.Args, "--init=false") {
			r.t.Error("candidate create missing --init=false")
		}
		result.Stdout = []byte(strings.Repeat("e", 64) + "\n")
	case isStart:
		r.running = true
		result.Stdout = []byte("READY " + intent.Binding.ContainerID + " " + intent.GuardDeadline.Format(time.RFC3339Nano) + "\n")
		if r.mode == "bad_ready" {
			result.Stdout = []byte("READY wrong\n")
		}
	case isInspect:
		keeper, err := loadManagedDockerKeeper(ctx, r.journal, intent.Source.Keeper)
		if err != nil {
			return HostResult{}, err
		}
		volume, err := loadManagedDockerVolume(ctx, r.journal, intent.Source.Volume)
		if err != nil {
			return HostResult{}, err
		}
		object := sourceKeeperObject(keeper, volume)
		object["Id"] = intent.Binding.ContainerID
		object["Name"] = "/" + intent.Container.Name
		config := object["Config"].(map[string]any)
		config["Cmd"] = []string{"--deadline", intent.GuardDeadline.Format(time.RFC3339Nano)}
		labels := maps.Clone(keeper.Binding.Labels)
		for k, v := range intent.Binding.Labels {
			labels[k] = v
		}
		config["Labels"] = labels
		host := object["HostConfig"].(map[string]any)
		host["UTSMode"], host["Init"] = "", false
		policy := r.journal.admission.Runtime
		host["Memory"], host["MemorySwap"], host["NanoCpus"], host["PidsLimit"] = policy.MemoryBytes, policy.MemoryBytes, int64(policy.CPUs*1e9), int64(policy.PIDs)
		host["Tmpfs"] = map[string]string{"/scratch": fmt.Sprintf("rw,size=%d,nr_inodes=%d,uid=1000,gid=1000,mode=0700", policy.ScratchBytes, policy.ScratchInodes)}
		if r.running {
			state := object["State"].(map[string]any)
			state["Status"] = "running"
			state["Running"] = true
			state["Pid"] = 203
			state["StartedAt"] = "2026-10-24T06:10:00Z"
		}
		if !r.running {
			switch r.mode {
			case "bad_memory":
				host["Memory"] = int64(1)
			case "bad_mount":
				object["Mounts"] = []any{}
			case "bad_labels":
				labels["foreign"] = "value"
			case "bad_scratch":
				host["Tmpfs"] = map[string]string{"/scratch": "rw,size=1"}
			case "bad_image":
				object["Image"] = "sha256:" + strings.Repeat("f", 64)
			case "bad_uts":
				host["UTSMode"] = "host"
			case "bad_uts_alias":
				host["UTSMode"], host["utsmode"] = "host", ""
			case "bad_init_alias":
				host["Init"], host["init"] = true, false
			case "bad_init":
				host["Init"] = true
			case "null_init":
				host["Init"] = nil
			case "missing_init":
				delete(host, "Init")
			case "bad_guard":
				config["Cmd"] = []string{"--deadline", "2099-01-01T00:00:00Z"}
			}
		} else if r.mode == "bad_lifetime" {
			object["Created"] = "2026-10-25T06:10:00Z"
		} else if r.mode == "bad_running" {
			object["State"].(map[string]any)["Running"] = false
		}
		data, err := json.Marshal([]any{object})
		if err != nil {
			return HostResult{}, err
		}
		result.Stdout = data
	}
	if isCreate && (r.mode == "before_stop" || r.mode == "before_epoch") {
		return HostResult{}, r.rejectBeforeGrant(ctx, call)
	}
	if err := r.journal.RecordHello(ctx, call.Scope, 103, 103); err != nil {
		return HostResult{}, fmt.Errorf("candidate test host hello: %w", err)
	}
	if isCreate && r.mode == "after_stop" {
		ok, err := r.journal.Granted(ctx, call.Scope)
		if err != nil || !ok {
			return HostResult{}, fmt.Errorf("host grant missing: %w", err)
		}
		if err := r.journal.setDesired(ctx, call.Scope.Generation, "stopping", "test", time.Time{}); err != nil {
			return HostResult{}, err
		}
	}
	if err := r.journal.MarkQuiescent(ctx, call.Scope); err != nil {
		return HostResult{}, fmt.Errorf("candidate test host retire: %w", err)
	}
	if (isCreate && r.mode == "lost_create") || (isStart && r.mode == "lost_start") {
		return HostResult{}, fmt.Errorf("lost transport acknowledgment")
	}
	return result, nil
}

func candidateTransportMatches(actual, expected HostInvocation) bool {
	return reflect.DeepEqual(actual, expected)
}

func (r *candidateExecutionRuntime) testTransportRejections(expected HostInvocation) {
	r.t.Helper()
	mutations := []struct {
		name   string
		change func(*HostInvocation)
	}{
		{"missing flag", func(c *HostInvocation) { c.Args = c.Args[1:] }},
		{"path", func(c *HostInvocation) { c.Path = "/tmp/replacement" }},
		{"environment", func(c *HostInvocation) { c.Env = []string{"PATH=/tmp"} }},
		{"deadline", func(c *HostInvocation) { c.Deadline = c.Deadline.Add(time.Second) }},
		{"scope", func(c *HostInvocation) { c.Scope.Nonce = "replacement" }},
		{"working directory", func(c *HostInvocation) { c.Dir = "/tmp" }},
		{"stdin", func(c *HostInvocation) { c.Stdin = []byte("unexpected") }},
		{"output limit", func(c *HostInvocation) { c.OutputLimit++ }},
	}
	for _, flag := range []string{"--init=false", "--read-only", "--mount", "--tmpfs", "--docker", "--guard-deadline"} {
		index := slices.Index(expected.Args, flag)
		if index < 0 {
			continue
		}
		changed := expected
		changed.Args = slices.Clone(expected.Args)
		changed.Args = append(changed.Args[:index], changed.Args[index+1:]...)
		if candidateTransportMatches(changed, expected) {
			r.t.Errorf("transport accepted omitted %s", flag)
		}
	}
	for _, mutation := range mutations {
		changed := expected
		changed.Args = slices.Clone(expected.Args)
		changed.Env = slices.Clone(expected.Env)
		mutation.change(&changed)
		if candidateTransportMatches(changed, expected) {
			r.t.Errorf("transport accepted changed %s", mutation.name)
		}
	}
}

func (r *candidateExecutionRuntime) expectedCandidateCall(i managedCandidateIntent, create bool) HostInvocation {
	c := r.config
	p := r.journal.admission.Runtime
	deadline := i.ExecutionDeadline.Format(time.RFC3339Nano)
	guard := i.GuardDeadline.Format(time.RFC3339Nano)
	call := HostInvocation{Scope: i.CreateHost, Path: c.DockerPath, Env: []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "LANG=C"}, Dir: c.StateRoot, Deadline: i.ExecutionDeadline, OutputLimit: 1 << 20}
	if create {
		call.Args = []string{"--config", c.DockerConfig, "--context", c.DockerContext, "container", "create", "--name", i.Container.Name, "--pull=never", "--init=false", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--ipc", "private", "--cgroupns", "private", "--restart", "no", "--log-driver", "none", "--memory", strconv.FormatInt(p.MemoryBytes, 10), "--memory-swap", strconv.FormatInt(p.MemoryBytes, 10), "--cpus", strconv.FormatFloat(p.CPUs, 'f', 9, 64), "--pids-limit", strconv.Itoa(p.PIDs), "--shm-size", "1048576", "--user", "65534:65534", "--entrypoint", "/usr/local/bin/oro-factory-guard", "--mount", "type=volume,source=" + i.Source.Volume.Name + ",target=/source,volume-nocopy", "--tmpfs", fmt.Sprintf("/scratch:rw,size=%d,nr_inodes=%d,uid=1000,gid=1000,mode=0700", p.ScratchBytes, p.ScratchInodes)}
		for _, key := range slices.Sorted(maps.Keys(i.Binding.Labels)) {
			call.Args = append(call.Args, "--label", key+"="+i.Binding.Labels[key])
		}
		call.Args = append(call.Args, i.Binding.ImageID, "--deadline", guard)
		return call
	}
	call.Scope, call.Path, call.OutputLimit = i.StartHost, c.OroPath, 256
	call.Args = []string{"factory", "_docker-start-ready", "--docker", c.DockerPath, "--docker-sha256", c.DockerSHA256, "--docker-config", c.DockerConfig, "--context", c.DockerContext, "--container", i.Binding.ContainerID, "--scope-id", i.Container.ID, "--scope-nonce", i.Container.Nonce, "--scope-generation", strconv.FormatUint(i.Container.Generation, 10), "--scope-kind", "container", "--objective-hash", cleanupObjectiveHash(r.journal.admission), "--policy-hash", c.PolicyHash, "--image", i.Binding.ImageID, "--guard-deadline", guard, "--deadline", deadline}
	return call
}

func (r *candidateExecutionRuntime) rejectBeforeGrant(ctx context.Context, call HostInvocation) error {
	if r.mode == "before_stop" {
		if err := r.journal.setDesired(ctx, call.Scope.Generation, "stopping", "test", time.Time{}); err != nil {
			return err
		}
	} else if _, _, err := r.journal.beginGeneration(ctx, "new-supervisor", time.Now()); err != nil {
		return err
	}
	if err := r.journal.RecordHello(ctx, call.Scope, 103, 103); err == nil {
		return fmt.Errorf("revoked host was granted")
	}
	return fmt.Errorf("host authorization denied before effect")
}

func (r *candidateExecutionRuntime) Retire(ctx context.Context, scope ScopeHandle) error {
	return r.base.Retire(ctx, scope)
}

func TestCandidateNamespaceExactFields(t *testing.T) {
	for _, body := range []string{
		`{"HostConfig":{"UTSMode":"host","utsmode":"","Init":false}}`,
		`{"HostConfig":{"UTSMode":"","Init":true,"init":false}}`,
		`{"HostConfig":{"UTSMode":null,"Init":false}}`,
		`{"HostConfig":{"Init":false}}`,
		`{"HostConfig":{"UTSMode":1,"Init":false}}`,
		`{"HostConfig":{"UTSMode":"","Init":null}}`,
		`{"HostConfig":{"UTSMode":""}}`,
		`{"HostConfig":{"UTSMode":"","Init":"false"}}`,
	} {
		if err := validateManagedGuardNamespaces(json.RawMessage(body)); err == nil {
			t.Errorf("accepted invalid namespaces: %s", body)
		}
	}
	if err := validateManagedGuardNamespaces(json.RawMessage(`{"HostConfig":{"UTSMode":"","Init":false}}`)); err != nil {
		t.Fatalf("rejected valid namespaces: %v", err)
	}
}
