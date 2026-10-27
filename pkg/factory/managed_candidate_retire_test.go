//nolint:testpackage // Exercises private candidate and cleanup authority through the controller.
package factory

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestManagedCandidateRetire(t *testing.T) {
	modes := []string{"concurrent_reservation", "reservation_rollback", "success", "lost", "absent", "unretired", "malformed", "missing_ack", "wrong_binding", "wrong_lifetime", "wrong_scope", "wrong_kind", "commit_failure", "other_host", "deadline_cross", "small_source", "revoked", "epoch_change", "tamper_fence", "history_deleted", "snapshot_tamper", "permit_tamper", "cleanup_expired", "reserve_after_deadline"}
	selected := os.Getenv("ORO_RETIRE_MODE")
	if os.Getenv("ORO_RETIRE_LEADER") != "1" {
		for _, mode := range modes {
			if selected != "" && selected != mode {
				continue
			}
			t.Run(mode, func(t *testing.T) { runCandidateRetirementLeader(t, mode) })
		}
		return
	}
	switch selected {
	case "concurrent_reservation":
		testConcurrentRetirementReservation(t)
	case "reservation_rollback":
		testRetirementReservationRollback(t)
	default:
		if !slices.Contains(modes, selected) {
			t.Fatal("invalid retirement leader mode")
		}
		testManagedCandidateRetire(t, selected)
	}
}

func readyRetirementFixture(t *testing.T) (*managedControllerOwner, context.Context, managedCandidateIntent, ManagedDockerConfig, HostRetentionLimits) {
	t.Helper()
	owner, ctx, workspace := candidateReadyFixture(t)
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
	source := &sourcePrepareRuntime{base: &sourceKeeperRuntime{base: base, intent: keeper}, t: t, running: true}
	manifest := owner.manifest
	root := owner.journal.admission.Reservation.StateRoot
	id := owner.journal.admission.ObjectiveID
	config := ManagedDockerConfig{StateRoot: root, ObjectiveID: id, PolicyHash: manifest.PolicyHash, Deadline: manifest.Deadline, HardStop: manifest.HardStop, RuntimeConfigPath: owner.runtime.Path, RuntimeConfigSHA256: owner.runtime.SHA256, OroPath: "/usr/local/bin/oro", OroSHA256: strings.Repeat("d", 64), DockerPath: "/usr/local/bin/docker", DockerSHA256: strings.Repeat("b", 64), DockerConfig: filepath.Join(root, id, "runtime", "docker"), DockerContext: manifest.Spec.Runtime.DockerContext, DockerEndpoint: "unix:///private/tmp/admitted-docker.sock"}
	host := &candidateExecutionRuntime{base: &managedEngineRuntime{git: &sourceGitRuntime{t: t, j: owner.journal}, docker: source}, journal: owner.journal, t: t, mode: "success", config: config}
	d, err := newManagedDockerVolumes(ctx, manifest, owner.scope.Generation, owner.journal, host, func(context.Context) (ManagedDockerConfig, error) { return config, nil })
	if err != nil {
		t.Fatal(err)
	}
	ready, err := d.prepareCandidate(ctx, owner, intent)
	if err != nil {
		t.Fatal(err)
	}
	return owner, ctx, ready, config, d.limits
}

func testConcurrentRetirementReservation(t *testing.T) {
	t.Helper()
	owner, ctx, ready, config, limits := readyRetirementFixture(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	for n := 0; n < 2; n++ {
		go func() {
			<-start
			_, _, _, err := reserveCandidateRetirement(ctx, owner, ready, config, limits)
			results <- err
		}()
	}
	close(start)
	successes := 0
	for n := 0; n < 2; n++ {
		if err := <-results; err == nil {
			successes++
		}
	}
	var count, events int
	if err := owner.journal.conn.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM managed_reservations WHERE kind='cleanup'),(SELECT count(*) FROM managed_events WHERE kind='candidate-retiring')").Scan(&count, &events); err != nil || successes != 1 || count != 1 || events != 1 {
		t.Fatalf("successes=%d cleanup=%d events=%d: %v", successes, count, events, err)
	}
}

func testRetirementReservationRollback(t *testing.T) {
	t.Helper()
	owner, ctx, ready, config, limits := readyRetirementFixture(t)
	before := candidateCounts(t, owner.journal)
	if _, err := owner.journal.conn.ExecContext(ctx, `CREATE TRIGGER deny_retirement_event BEFORE INSERT ON managed_events WHEN NEW.kind='candidate-retiring' BEGIN SELECT RAISE(ABORT,'test denial'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := reserveCandidateRetirement(ctx, owner, ready, config, limits); err == nil || !strings.Contains(err.Error(), "append managed event") || !strings.Contains(err.Error(), "test denial") {
		t.Fatalf("retirement did not reach rejected event insertion: %v", err)
	}
	if after := candidateCounts(t, owner.journal); after != before {
		t.Fatalf("failed retirement changed counts %v -> %v", before, after)
	}
	var body []byte
	if err := owner.journal.conn.QueryRowContext(ctx, "SELECT body FROM managed_docker_candidates WHERE workspace_id=?", ready.WorkspaceID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var actual managedCandidateIntent
	if err := json.Unmarshal(body, &actual); err != nil || !reflect.DeepEqual(actual, ready) {
		t.Fatalf("failed retirement changed candidate: %v", err)
	}
}

func runCandidateRetirementLeader(t *testing.T, mode string) {
	t.Helper()
	timeout := 90 * time.Second
	if deadline, ok := t.Deadline(); ok && time.Until(deadline)-5*time.Second < timeout {
		timeout = time.Until(deadline) - 5*time.Second
	}
	if timeout <= time.Second {
		t.Fatal("insufficient leader time")
	}
	ctx, cancel := context.WithTimeout(t.Context(), timeout+time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestManagedCandidateRetire$", "-test.timeout="+timeout.String(), "-test.v")
	if f := flag.Lookup("test.gocoverdir"); f != nil && f.Value.String() != "" {
		cmd.Args = append(cmd.Args, "-test.gocoverdir="+f.Value.String())
	}
	cmd.Env = append(os.Environ(), "ORO_RETIRE_LEADER=1", "ORO_RETIRE_MODE="+mode, "GORACE=atexit_sleep_ms=0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 3 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("leader: %v\n%s", err, out)
	}
	if testing.Verbose() && os.Getenv("ORO_RETIRE_MODE") != "" {
		t.Logf("%s", out)
	}
}

func testManagedCandidateRetire(t *testing.T, mode string) {
	t.Helper()
	root, spec, reservation := managedAdmissionFixture(t)
	if mode == "deadline_cross" || mode == "reserve_after_deadline" {
		spec.TimeoutSeconds = 30
		spec.StepTimeoutSeconds = 25
	}
	if mode == "deadline_cross" || mode == "reserve_after_deadline" {
		spec.Recovery.CleanupTimeoutSeconds = 40
	}
	if mode == "small_source" {
		spec.Runtime.SourceBytes = 64
		spec.Runtime.MaxFiles = 2
		spec.Runtime.SourceInodes = 2
	}
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
	if mode == "success" || mode == "small_source" {
		keeper.change = func(stage string, _ HostInvocation, result *HostResult) error {
			if stage != "inspect" {
				return nil
			}
			var objects []map[string]any
			if err := json.Unmarshal(result.Stdout, &objects); err != nil {
				return err
			}
			labels := objects[0]["Config"].(map[string]any)["Labels"].(map[string]any)
			labels["image.origin"] = "inherited"
			if mode == "small_source" {
				for n := 0; n < 4; n++ {
					labels[fmt.Sprintf("image.large.%d", n)] = strings.Repeat("a", 1024)
				}
			}
			data, err := json.Marshal(objects)
			result.Stdout = data
			return err
		}
	}
	docker := &sourcePrepareRuntime{base: keeper, t: t}
	runtime := &managedEngineRuntime{git: git, docker: docker}

	recording := &candidateExecutionRuntime{base: runtime, journal: j, t: t, mode: "success", config: config}
	host := &candidateRetirementRuntime{base: recording, journal: j, mode: mode, t: t, config: config}
	ctx := t.Context()
	if mode == "reserve_after_deadline" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, manifest.Deadline)
		defer cancel()
	}
	waited := false
	reader := func(readCtx context.Context) (ManagedDockerConfig, error) {
		if mode == "reserve_after_deadline" && !waited {
			var phase string
			if e := j.conn.QueryRowContext(readCtx, "SELECT json_extract(body,'$.phase') FROM managed_docker_candidates LIMIT 1").Scan(&phase); e == nil && phase == "ready" {
				if bound, ok := readCtx.Deadline(); !ok || !bound.After(manifest.Deadline) {
					return ManagedDockerConfig{}, errors.New("cleanup context does not span objective expiry")
				}
				waited = true
				timer := time.NewTimer(time.Until(manifest.Deadline) + 20*time.Millisecond)
				defer timer.Stop()
				select {
				case <-readCtx.Done():
					return ManagedDockerConfig{}, readCtx.Err()
				case <-timer.C:
				}
			}
		}
		return config, nil
	}
	status, err := RunManagedController(ctx, root, spec.ID, gen, scope.ID, host, reader)
	if host.reader != nil {
		if e := host.reader.Rollback(); e != nil {
			t.Fatal(e)
		}
	}
	if !isManagedInterruption(err) || status.State != "running" || status.Attempts != 1 || status.CandidateSHA != "" {
		t.Fatalf("controller: %+v %v", status, err)
	}
	var phase string
	if e := j.conn.QueryRowContext(t.Context(), "SELECT json_extract(body,'$.phase') FROM managed_docker_candidates").Scan(&phase); e != nil {
		t.Fatal(e)
	}
	want := "retiring"
	if mode == "other_host" || mode == "tamper_fence" {
		want = "ready"
	}
	if mode == "success" || mode == "deadline_cross" || mode == "small_source" || mode == "reserve_after_deadline" {
		want = "retired"
	}
	wantCalls := 1
	if mode == "other_host" {
		wantCalls = 0
	}
	if phase != want || host.calls != wantCalls {
		t.Fatalf("candidate phase=%s cleanup calls=%d want=%s", phase, host.calls, want)
	}
	if slices.Contains([]string{"permit_tamper", "snapshot_tamper", "history_deleted", "tamper_fence", "revoked", "epoch_change", "cleanup_expired"}, mode) && !host.verified {
		t.Fatalf("%s fault setup/denial was not verified: %v", mode, err)
	}
	if mode != "other_host" && mode != "permit_tamper" && !host.authorized {
		t.Fatalf("%s did not reach authenticated cleanup helper: %v", mode, err)
	}
	if slices.Contains([]string{"success", "absent", "unretired", "malformed", "missing_ack", "wrong_binding", "wrong_lifetime", "wrong_scope", "wrong_kind", "deadline_cross", "small_source", "reserve_after_deadline"}, mode) && !host.receiptWritten {
		t.Fatalf("%s did not produce cleanup receipt: %v", mode, err)
	}
	if mode == "lost" && !strings.Contains(err.Error(), "lost cleanup acknowledgment") {
		t.Fatalf("lost acknowledgment not reached: %v", err)
	}
	var cleanupCount, retiringEvents, retiredEvents int
	if e := j.conn.QueryRowContext(t.Context(), `SELECT (SELECT count(*) FROM managed_reservations WHERE kind='cleanup'),(SELECT count(*) FROM managed_events WHERE kind='candidate-retiring'),(SELECT count(*) FROM managed_events WHERE kind='candidate-retired')`).Scan(&cleanupCount, &retiringEvents, &retiredEvents); e != nil {
		t.Fatal(e)
	}
	wantRetiring := wantCalls
	if mode == "history_deleted" {
		wantRetiring = 0
	}
	wantRetired := 0
	if want == "retired" {
		wantRetired = 1
	}
	if cleanupCount != wantCalls || retiringEvents != wantRetiring || retiredEvents != wantRetired {
		t.Fatalf("cleanup=%d retiring=%d retired=%d", cleanupCount, retiringEvents, retiredEvents)
	}
	if want == "retired" {
		assertCandidateRetired(t, j)
	}
	if mode == "other_host" && (!host.otherAdded || !strings.Contains(err.Error(), "other host scopes quiescent")) {
		t.Fatalf("other-host exclusion was not reached: %v", err)
	}
	if mode == "commit_failure" && !strings.Contains(err.Error(), "commit managed transition") {
		t.Fatalf("final commit was not reached: %v", err)
	}
	if want == "retiring" {
		assertNoRetirementReplay(t, j, manifest, scope, ref, config)
	}
	if (mode == "success" || mode == "deadline_cross" || mode == "small_source" || mode == "reserve_after_deadline") && !strings.Contains(err.Error(), "engineer-adapter-unavailable") {
		t.Fatalf("unavailable result: %v", err)
	}
}

func assertCandidateRetired(t *testing.T, j *managedJournal) {
	t.Helper()
	var body []byte
	if e := j.conn.QueryRowContext(t.Context(), "SELECT body FROM managed_docker_candidates").Scan(&body); e != nil {
		t.Fatal(e)
	}
	var candidate managedCandidateIntent
	if e := json.Unmarshal(body, &candidate); e != nil {
		t.Fatal(e)
	}
	for _, check := range []struct {
		scope ScopeHandle
		phase string
	}{{candidate.Container, "quiescent"}, {candidate.Source.Keeper, "reserved"}, {candidate.Source.Volume, "reserved"}, {candidate.Retirement.Scope, "quiescent"}} {
		observation, e := j.Observe(t.Context(), check.scope)
		if e != nil || observation.Phase != check.phase {
			t.Fatalf("scope %s phase %s, want %s: %v", check.scope.Kind, observation.Phase, check.phase, e)
		}
	}
	if e := j.mutate(t.Context(), "", "", func(tx *sql.Tx, _ *managedSupervision) (bool, error) {
		return false, requireNoManagedCleanup(t.Context(), tx)
	}); e != nil {
		t.Fatalf("completed retirement fence remained blocked: %v", e)
	}
}

func assertNoRetirementReplay(t *testing.T, j *managedJournal, manifest managedManifest, scope ScopeHandle, ref ManagedRuntimeReference, config ManagedDockerConfig) {
	t.Helper()
	var before []byte
	var beforeCounts string
	if e := j.conn.QueryRowContext(t.Context(), "SELECT body FROM managed_docker_candidates").Scan(&before); e != nil {
		t.Fatal(e)
	}
	query := `SELECT (SELECT revision FROM managed_supervision)||':'||(SELECT count(*) FROM managed_reservations)||':'||(SELECT count(*) FROM managed_scopes)||':'||(SELECT count(*) FROM managed_events)`
	if e := j.conn.QueryRowContext(t.Context(), query).Scan(&beforeCounts); e != nil {
		t.Fatal(e)
	}
	var current managedCandidateIntent
	if e := json.Unmarshal(before, &current); e != nil {
		t.Fatal(e)
	}
	owner := &managedControllerOwner{journal: j, manifest: manifest, scope: scope, runtime: ref}
	limits, e := ManagedHostRetention(manifest.Spec)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, _, e := reserveCandidateRetirement(t.Context(), owner, current, config, limits); e == nil {
		t.Fatal("uncertain retirement redispatched")
	}
	var after []byte
	var afterCounts string
	if e := j.conn.QueryRowContext(t.Context(), "SELECT body FROM managed_docker_candidates").Scan(&after); e != nil {
		t.Fatal(e)
	}
	if e := j.conn.QueryRowContext(t.Context(), query).Scan(&afterCounts); e != nil || !bytes.Equal(before, after) || beforeCounts != afterCounts {
		t.Fatalf("duplicate changed retirement identity/budgets: %v", e)
	}
}

type candidateRetirementRuntime struct {
	base           *candidateExecutionRuntime
	journal        *managedJournal
	config         ManagedDockerConfig
	otherAdded     bool
	t              *testing.T
	mode           string
	calls          int
	reader         *sql.Tx
	verified       bool
	authorized     bool
	receiptWritten bool
}

func (r *candidateRetirementRuntime) Invoke(ctx context.Context, call HostInvocation) (HostResult, error) {
	if call.Scope.Kind != "cleanup" {
		return r.invokeOrdinary(ctx, call)
	}
	r.calls++
	permit, err := r.journal.CleanupPermit(ctx, call.Scope)
	if err != nil {
		return HostResult{}, err
	}
	if (r.mode == "success" || r.mode == "small_source") && permit.Binding.Labels["image.origin"] != "inherited" {
		return HostResult{}, errors.New("inherited label omitted from cleanup permit")
	}
	body, err := json.Marshal(permit)
	if err != nil {
		return HostResult{}, err
	}
	want := []string{"factory", "_docker-cleanup", "--runtime-config", r.config.RuntimeConfigPath, "--runtime-config-sha256", r.config.RuntimeConfigSHA256}
	if call.Path != r.config.OroPath || call.Dir != r.config.StateRoot || !slices.Equal(call.Args, want) || !slices.Equal(call.Env, []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "LANG=C"}) || string(call.Stdin) != string(body) || call.OutputLimit != 1<<20 || !call.Deadline.Equal(permit.Deadline) {
		return HostResult{}, errors.New("cleanup request identity differs")
	}
	if r.mode == "permit_tamper" {
		result, err := r.journal.conn.ExecContext(ctx, "UPDATE managed_cleanup_permits SET body=json_set(body,'$.binding.container_id',?) WHERE scope_id=?", strings.Repeat("f", 64), permit.Scope.ID)
		if err != nil {
			return HostResult{}, err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return HostResult{}, errors.Join(errors.New("permit tamper did not affect one row"), err)
		}
		if err := r.journal.RecordHello(ctx, call.Scope, 103, 103); err == nil {
			r.t.Error("tampered permit granted")
			return HostResult{}, errors.New("tampered permit granted")
		}
		r.verified = true
		return HostResult{}, errors.New("tampered permit rejected")
	}
	if err := r.journal.RecordHello(ctx, call.Scope, 103, 103); err != nil {
		return HostResult{}, err
	}
	r.authorized = true
	if err := r.verifyRetirementWorkGate(ctx, permit); err != nil {
		return HostResult{}, err
	}
	if r.mode == "snapshot_tamper" {
		return HostResult{}, r.rejectTamperedSnapshot(ctx, permit)
	}
	if r.mode == "deadline_cross" {
		if err := r.verifyAfterWorkDeadline(ctx, call, permit); err != nil {
			return HostResult{}, err
		}
	}
	if r.mode == "cleanup_expired" {
		<-ctx.Done()
		if time.Now().Before(permit.Deadline) {
			r.t.Error("cleanup context expired before permit deadline")
		} else {
			r.verified = true
		}
		return HostResult{}, ctx.Err()
	}
	if r.mode == "revoked" {
		if err := r.journal.setDesired(ctx, permit.Scope.Generation, "stopping", "test revocation", time.Time{}); err != nil {
			r.t.Errorf("revocation setup: %v", err)
			return HostResult{}, err
		}
		r.verified = true
	}
	if r.mode == "epoch_change" {
		state, err := r.journal.supervision(ctx)
		if err != nil {
			return HostResult{}, err
		}
		if _, _, err := r.journal.advanceEpoch(ctx, state.SupervisorToken, "retirement-test-epoch", time.Now()); err != nil {
			r.t.Errorf("epoch change setup: %v", err)
			return HostResult{}, err
		}
		r.verified = true
	}

	if r.mode == "lost" {
		return HostResult{}, errors.New("lost cleanup acknowledgment")
	}
	if r.mode != "unretired" {
		if err := r.journal.MarkQuiescent(ctx, call.Scope); err != nil {
			return HostResult{}, err
		}
	}
	if r.mode != "unretired" && r.mode != "deadline_cross" && r.mode != "reserve_after_deadline" && r.mode != "revoked" && r.mode != "epoch_change" {
		r.assertRetirementFence(ctx, permit)
	}
	if r.mode == "history_deleted" {
		result, err := r.journal.conn.ExecContext(ctx, "DELETE FROM managed_events WHERE kind='candidate-retiring'")
		if err != nil {
			return HostResult{}, err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return HostResult{}, errors.Join(errors.New("retirement event deletion did not affect one row"), err)
		}
		if fresh, err := r.journal.reserveBudget(ctx, permit.Scope.Generation, "host", "deleted-fence", time.Now()); err == nil || fresh {
			r.t.Error("deleted retirement event allowed work")
			return HostResult{}, errors.New("deleted retirement event allowed work")
		}
		r.verified = true
	}
	if r.mode == "tamper_fence" {
		result, err := r.journal.conn.ExecContext(ctx, "UPDATE managed_docker_candidates SET body=json_set(body,'$.phase','ready')")
		if err != nil {
			return HostResult{}, err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return HostResult{}, errors.Join(errors.New("candidate tamper did not affect one row"), err)
		}
		if fresh, err := r.journal.reserveBudget(ctx, permit.Scope.Generation, "host", "corrupt-fence", time.Now()); err == nil || fresh {
			r.t.Error("corrupt retirement fence allowed work")
			return HostResult{}, errors.New("corrupt retirement fence allowed work")
		}
		r.verified = true
	}
	if r.mode == "success" {
		if err := r.provePermitLabelCapacity(ctx, permit); err != nil {
			return HostResult{}, err
		}
	}
	if r.mode == "commit_failure" {
		r.reader = pinManagedReader(r.t, filepath.Join(r.journal.admission.Reservation.StateRoot, r.journal.admission.ObjectiveID), "SELECT count(*) FROM managed_events")
	}
	observation := "stopped"
	ack := true
	if r.mode == "absent" {
		observation = "observed_absent"
		ack = false
	}
	result := DockerCleanupResult{Version: 1, PermitSHA256: contentHash(body), Scope: permit.Scope, Target: permit.Target, Operation: permit.Operation, Binding: permit.Binding, Observation: observation, MutationAcknowledged: ack}
	data, err := EncodeDockerCleanupResult(result, permit)
	if err != nil {
		return HostResult{}, err
	}
	switch r.mode {
	case "malformed":
		data = []byte("{}")
	case "missing_ack":
		result.MutationAcknowledged = false
		data, err = json.Marshal(result)
	case "wrong_binding":
		result.Binding.ContainerID = strings.Repeat("f", 64)
		data, err = json.Marshal(result)
	case "wrong_lifetime":
		result.Binding.CreatedAt = "2026-10-25T00:00:00Z"
		data, err = json.Marshal(result)
	case "wrong_scope":
		result.Scope.Nonce = strings.Repeat("f", 32)
		data, err = json.Marshal(result)
	case "wrong_kind":
		result.Binding.Kind = "volume"
		data, err = json.Marshal(result)
	}
	r.receiptWritten = true
	return HostResult{Stdout: data}, err
}

func (r *candidateRetirementRuntime) rejectTamperedSnapshot(ctx context.Context, permit DockerCleanupPermit) error {
	state, err := decodeSnapshot(r.journal.conn.QueryRowContext(ctx, "SELECT body FROM snapshots WHERE id=1"))
	if err != nil {
		return err
	}
	state.PolicyDigests = map[string]string{"unexpected": "value"}
	body, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if _, err := r.journal.conn.ExecContext(ctx, "UPDATE snapshots SET body=? WHERE id=1", body); err != nil {
		return err
	}
	if _, err := r.journal.CleanupPermit(ctx, permit.Scope); err == nil {
		r.t.Error("tampered snapshot accepted")
		return errors.New("tampered snapshot accepted")
	}
	r.verified = true
	return errors.New("tampered snapshot rejected")
}

func (r *candidateRetirementRuntime) verifyRetirementWorkGate(ctx context.Context, permit DockerCleanupPermit) error {
	if r.mode != "reserve_after_deadline" {
		r.assertRetirementFence(ctx, permit)
		return nil
	}
	var body []byte
	if err := r.journal.conn.QueryRowContext(ctx, "SELECT body FROM managed_docker_candidates WHERE container_id=?", permit.Target.ID).Scan(&body); err != nil {
		return err
	}
	var candidate managedCandidateIntent
	if err := json.Unmarshal(body, &candidate); err != nil {
		return err
	}
	if granted, err := r.journal.Granted(ctx, candidate.Workspace.Controller); err != nil || granted {
		return errors.Join(errors.New("expired parent granted work"), err)
	}
	return nil
}

func (r *candidateRetirementRuntime) provePermitLabelCapacity(ctx context.Context, permit DockerCleanupPermit) error {
	var body []byte
	if err := r.journal.conn.QueryRowContext(ctx, "SELECT body FROM managed_docker_candidates WHERE container_id=?", permit.Target.ID).Scan(&body); err != nil {
		return err
	}
	var candidate managedCandidateIntent
	if err := json.Unmarshal(body, &candidate); err != nil {
		return err
	}
	keeper, err := loadManagedDockerKeeper(ctx, r.journal, candidate.Source.Keeper)
	if err != nil {
		return err
	}
	base := maps.Clone(keeper.Binding.Labels)
	var keeperBody []byte
	low, high := 0, 1024
	for low <= high {
		n := (low + high) / 2
		keeper.Binding.Labels = maps.Clone(base)
		for k := 0; k < 9; k++ {
			keeper.Binding.Labels[fmt.Sprintf("image.extra.%d", k)] = strings.Repeat("\x01", n)
		}
		encoded, e := encodeManagedDockerKeeper(r.journal, keeper)
		if e == nil {
			keeperBody = encoded
			low = n + 1
		} else {
			high = n - 1
		}
	}
	if len(keeperBody) == 0 {
		return errors.New("no bounded inherited-label keeper")
	}
	keeper.Binding.Labels = maps.Clone(base)
	for k := 0; k < 9; k++ {
		keeper.Binding.Labels[fmt.Sprintf("image.extra.%d", k)] = strings.Repeat("\x01", high)
	}
	expanded := permit
	expanded.Binding.Labels = maps.Clone(keeper.Binding.Labels)
	for k, v := range candidate.Binding.Labels {
		expanded.Binding.Labels[k] = v
	}
	permitBody, err := encodeCleanupPermit(r.journal.admission, expanded)
	if err != nil {
		return fmt.Errorf("valid maximal sampled keeper exceeds permit: %w", err)
	}
	r.t.Logf("bounded inherited-label sample: %d keeper bytes, %d permit bytes, value length %d", len(keeperBody), len(permitBody), high)
	return nil
}

func (r *candidateRetirementRuntime) verifyAfterWorkDeadline(ctx context.Context, call HostInvocation, permit DockerCleanupPermit) error {
	if !permit.Deadline.After(r.journal.admission.Deadline) {
		return errors.New("cleanup deadline does not span objective expiry")
	}
	timer := time.NewTimer(time.Until(r.journal.admission.Deadline) + 20*time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	if _, err := r.journal.CleanupPermit(ctx, call.Scope); err != nil {
		return fmt.Errorf("cleanup after work deadline: %w", err)
	}
	var body []byte
	if err := r.journal.conn.QueryRowContext(ctx, "SELECT body FROM managed_docker_candidates WHERE container_id=?", permit.Target.ID).Scan(&body); err != nil {
		return err
	}
	var candidate managedCandidateIntent
	if err := json.Unmarshal(body, &candidate); err != nil {
		return err
	}
	if granted, err := r.journal.Granted(ctx, candidate.Workspace.Controller); err != nil || granted {
		return errors.Join(errors.New("expired controller grant accepted"), err)
	}
	return nil
}

func (r *candidateRetirementRuntime) invokeOrdinary(ctx context.Context, call HostInvocation) (HostResult, error) {
	result, err := r.base.Invoke(ctx, call)
	if err == nil && r.mode == "other_host" && !r.otherAdded && r.base.running && len(call.Args) > 6 && call.Args[4] == "container" && call.Args[5] == "inspect" && call.Args[6] == strings.Repeat("e", 64) {
		other := journalScope("f", call.Scope.Generation)
		if fresh, e := r.journal.reserveBudget(ctx, other.Generation, "host", other.ID, time.Now()); e != nil || !fresh {
			return HostResult{}, errors.Join(errors.New("reserve other host"), e)
		}
		if fresh, e := r.journal.reserveScope(ctx, other, r.journal.admission.Deadline, time.Now()); e != nil || !fresh {
			return HostResult{}, errors.Join(errors.New("reserve other scope"), e)
		}
		r.otherAdded = true
	}
	return result, err
}

func (r *candidateRetirementRuntime) assertRetirementFence(ctx context.Context, permit DockerCleanupPermit) {
	r.t.Helper()
	if fresh, err := r.journal.reserveBudget(ctx, permit.Scope.Generation, "host", "fence-probe", time.Now()); err == nil || fresh || !strings.Contains(err.Error(), "candidate retirement") {
		r.t.Error("retiring candidate allowed host budget")
	}
	probe := journalScope("f", permit.Scope.Generation)
	if fresh, err := r.journal.reserveScope(ctx, probe, r.journal.admission.Deadline, time.Now()); err == nil || fresh || !strings.Contains(err.Error(), "candidate retirement") {
		r.t.Error("retiring candidate allowed ordinary scope")
	}
}

func (r *candidateRetirementRuntime) Retire(ctx context.Context, scope ScopeHandle) error {
	return r.base.Retire(ctx, scope)
}

func TestCandidateCleanupContext(t *testing.T) {
	manifest := managedManifest{Deadline: time.Now().Add(3 * time.Second), HardStop: time.Now().Add(6 * time.Second), Spec: ManagedSpec{Recovery: RecoveryPolicy{CleanupTimeoutSeconds: 4}}}
	t.Run("explicit cancel", func(t *testing.T) {
		parent, cancel := context.WithCancel(t.Context())
		ctx, release, err := candidateCleanupContext(parent, manifest)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		cancel()
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
			t.Fatal("cleanup ignored explicit cancellation")
		}
	})
	t.Run("earlier deadline", func(t *testing.T) {
		bound := time.Now().Add(100 * time.Millisecond)
		parent, cancel := context.WithDeadline(t.Context(), bound)
		defer cancel()
		ctx, release, err := candidateCleanupContext(parent, manifest)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		got, ok := ctx.Deadline()
		if !ok || !got.Equal(bound) {
			t.Fatalf("deadline %v want %v", got, bound)
		}
		<-ctx.Done()
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Fatal(ctx.Err())
		}
	})
	t.Run("expired objective only", func(t *testing.T) {
		m := manifest
		m.Deadline = time.Now().Add(-time.Second)
		parent, cancel := context.WithDeadline(t.Context(), m.Deadline)
		defer cancel()
		ctx, release, err := candidateCleanupContext(parent, m)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		if ctx.Err() != nil {
			t.Fatalf("objective expiry canceled cleanup: %v", ctx.Err())
		}
	})
	t.Run("already canceled", func(t *testing.T) {
		parent, cancel := context.WithCancel(t.Context())
		cancel()
		if _, _, err := candidateCleanupContext(parent, manifest); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled: %v", err)
		}
	})
	t.Run("hard stop expired", func(t *testing.T) {
		m := manifest
		m.HardStop = time.Now().Add(-time.Second)
		if _, _, err := candidateCleanupContext(t.Context(), m); err == nil {
			t.Fatal("expired hard stop accepted")
		}
	})
}
