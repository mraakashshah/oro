//nolint:testpackage // Exercises the private managed controller path.
package factory

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
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

func TestManagedControllerWorkerCheckout(t *testing.T) {
	if os.Getenv("ORO_MANAGED_ENGINE_LEADER") != "1" {
		for _, mode := range []string{"success", "prior-attempt", "lost-ack", "changed-reader", "revoked-save"} {
			t.Run(mode, func(t *testing.T) {
				cmd := exec.Command(os.Args[0], "-test.run=^TestManagedControllerWorkerCheckout$", "-test.v")
				cmd.Env = append(os.Environ(), "ORO_MANAGED_ENGINE_LEADER=1", "ORO_MANAGED_ENGINE_MODE="+mode)
				cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("leader subprocess: %v\n%s", err, out)
				}
			})
		}
		return
	}
	root, spec, reservation := managedAdmissionFixture(t)
	if runtime.GOOS == "darwin" {
		path, err := exec.Command("xcrun", "--find", "git").Output()
		if err != nil {
			t.Fatal(err)
		}
		spec.Runtime.GitExecutable = strings.TrimSpace(string(path))
	}
	spec.Runtime.HostRequiredBytes = 1 << 40
	reservation.Bytes = spec.Runtime.HostRequiredBytes
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
	mode := os.Getenv("ORO_MANAGED_ENGINE_MODE")
	reads := 0
	reader := func(context.Context) (ManagedDockerConfig, error) {
		reads++
		if mode == "revoked-save" {
			if _, err := j.conn.ExecContext(t.Context(), "UPDATE managed_scopes SET phase='revoked' WHERE id=?", scope.ID); err != nil {
				t.Fatal(err)
			}
		}
		c := config
		if mode == "changed-reader" && reads > 2 {
			c.RuntimeConfigSHA256 = strings.Repeat("f", 64)
		}
		return c, nil
	}
	var before string
	stateQuery := `SELECT (SELECT revision||':'||hex(body) FROM snapshots)||':'||(SELECT coalesce(group_concat(revision||':'||state||':'||hex(body),'|'),'') FROM events)||':'||(SELECT coalesce(group_concat(kind||':'||token||':'||generation,'|'),'') FROM managed_reservations)||':'||(SELECT coalesce(group_concat(revision||':'||kind||':'||ref||':'||hex(body),'|'),'') FROM managed_events)`
	if err := j.conn.QueryRowContext(t.Context(), stateQuery).Scan(&before); err != nil {
		t.Fatal(err)
	}
	var nilHost *managedEngineRuntime
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Errorf("typed-nil host panicked: %v", p)
			}
		}()
		if _, err := RunManagedController(t.Context(), root, spec.ID, gen, scope.ID, nilHost, reader); err == nil {
			t.Error("typed-nil host accepted")
		}
	}()
	var afterNil string
	if err := j.conn.QueryRowContext(t.Context(), stateQuery).Scan(&afterNil); err != nil || afterNil != before || reads != 0 {
		t.Fatalf("typed-nil host changed state or read config: %q != %q, reads %d, %v", afterNil, before, reads, err)
	}
	for _, bad := range []struct {
		generation uint64
		token      string
		reader     ManagedDockerConfigReader
	}{
		{gen + 1, scope.ID, reader},
		{gen, strings.Repeat("0", 32), reader},
		{gen, scope.ID, func(context.Context) (ManagedDockerConfig, error) {
			changed := config
			changed.RuntimeConfigSHA256 = strings.Repeat("f", 64)
			return changed, nil
		}},
	} {
		if _, err := RunManagedController(t.Context(), root, spec.ID, bad.generation, bad.token, runtime, bad.reader); err == nil {
			t.Fatal("invalid grant or config accepted")
		}
		if len(git.calls) != 0 || len(docker.calls) != 0 {
			t.Fatal("invalid entry invoked host")
		}
	}
	if _, err := j.conn.ExecContext(t.Context(), "UPDATE managed_scopes SET pid=? WHERE id=?", os.Getpid()+1, scope.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := RunManagedController(t.Context(), root, spec.ID, gen, scope.ID, runtime, reader); err == nil || len(git.calls) != 0 || len(docker.calls) != 0 {
		t.Fatalf("foreign process accepted: %v", err)
	}
	if _, err := j.conn.ExecContext(t.Context(), "UPDATE managed_scopes SET pid=? WHERE id=?", os.Getpid(), scope.ID); err != nil {
		t.Fatal(err)
	}
	reads = 0
	expectedAttempt := 1
	if mode == "prior-attempt" { //nolint:nestif // Seed a previously spent durable attempt through the real owner guard.
		store, _, err := openManagedConnection(t.Context(), filepath.Join(root, spec.ID), false)
		if err != nil {
			t.Fatal(err)
		}
		store.owner = &managedControllerOwner{journal: j, manifest: manifest, scope: scope, runtime: ref}
		state, err := store.load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		state.Attempts = 1
		if err := store.save(t.Context(), state, state.Revision); err != nil {
			t.Fatal(err)
		}
		if err := store.close(); err != nil {
			t.Fatal(err)
		}
		expectedAttempt = 2
	}
	if mode == "lost-ack" {
		docker.change = func(stage string, _ HostInvocation, _ *HostResult) error {
			if stage == "start" {
				return errors.New("lost start acknowledgment")
			}
			return nil
		}
	}
	status, runErr := RunManagedController(t.Context(), root, spec.ID, gen, scope.ID, runtime, reader)
	if mode == "revoked-save" {
		var after string
		if err := j.conn.QueryRowContext(t.Context(), stateQuery).Scan(&after); err != nil || after != before || runErr == nil || status.State != "pending" || status.Attempts != 0 || len(git.calls) != 0 || len(docker.calls) != 0 {
			t.Fatalf("revoked public entry changed state or invoked host: %+v, %v, %v", status, runErr, err)
		}
		return
	}
	if len(git.calls) == 0 {
		t.Fatal("managed controller never invoked target Git")
	}
	if !isManagedInterruption(runErr) || status.State != "running" || status.Attempts != expectedAttempt {
		t.Fatalf("controller result = %+v, %v", status, runErr)
	}
	if mode == "success" || mode == "prior-attempt" {
		var interruption *managedInterruptionError
		if !errors.As(runErr, &interruption) || interruption.stage != "candidate" {
			t.Fatalf("expected candidate interruption: %v", runErr)
		}
	}
	if mode == "lost-ack" || mode == "changed-reader" { //nolint:nestif // Verify retained state and replay after uncertain dispatch.
		var id, phase string
		if err := j.conn.QueryRowContext(t.Context(), "SELECT id,json_extract(body, '$.phase') FROM managed_workspaces").Scan(&id, &phase); err != nil || phase != "preparing" {
			t.Fatalf("uncertain workspace = %q %q, %v", id, phase, err)
		}
		owner := &managedControllerOwner{journal: j, manifest: manifest, scope: scope, runtime: ref}
		backend, err := newManagedDeliveryBackend(t.Context(), owner, runtime, func(context.Context) (ManagedDockerConfig, error) { return config, nil })
		if err != nil {
			t.Fatal(err)
		}
		var sha string
		if err := j.conn.QueryRowContext(t.Context(), "SELECT json_extract(body,'$.source_sha') FROM managed_workspaces WHERE id=?", id).Scan(&sha); err != nil {
			t.Fatal(err)
		}
		beforeGit, beforeDocker := len(git.calls), len(docker.calls)
		if _, err := backend.checkout(t.Context(), "worker", 1, sha, manifest.Deadline); !isManagedInterruption(err) {
			t.Fatalf("preparing replay accepted: %v", err)
		}
		if len(git.calls) != beforeGit || len(docker.calls) != beforeDocker {
			t.Fatal("preparing workspace redispatched")
		}
		return
	}
	if len(docker.calls) != 8 {
		t.Fatalf("Docker calls = %d, want 8: %v", len(docker.calls), runErr)
	}
	store, _, err := openManagedConnection(t.Context(), filepath.Join(root, spec.ID), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	state, err := store.load(t.Context())
	if err != nil || state.BaseSHA == "" || state.Worktree == "" || state.CandidateSHA != "" {
		t.Fatalf("snapshot = %+v, %v", state, err)
	}
	var phase string
	if err := j.conn.QueryRowContext(t.Context(), "SELECT json_extract(body, '$.phase') FROM managed_workspaces WHERE id=?", state.Worktree).Scan(&phase); err != nil || phase != "ready" {
		t.Fatalf("workspace phase = %q, %v", phase, err)
	}
	var workspace managedWorkspace
	err = j.mutate(t.Context(), "", "", func(tx *sql.Tx, _ *managedSupervision) (bool, error) {
		owner := &managedControllerOwner{journal: j, manifest: manifest, scope: scope, runtime: ref}
		var e error
		workspace, e = readManagedWorkspace(t.Context(), tx, owner, state.Worktree)
		return false, e
	})
	if err != nil || workspace.SourceSHA != state.BaseSHA || workspace.Keeper == (ScopeHandle{}) || workspace.SeedSHA256 == "" {
		t.Fatalf("workspace = %+v, %v", workspace, err)
	}
	if state.BaseSHA != gitTestRun(t, spec.Repository, "rev-parse", "delivery") {
		t.Fatal("snapshot did not retain target SHA")
	}
	if len(git.calls) != 6 {
		t.Fatalf("Git calls = %d", len(git.calls))
	}
	for i, command := range []string{"show-ref", "cat-file", "worktree", "cat-file", "ls-tree", "cat-file"} {
		if !slices.Contains(git.calls[i].Args, command) {
			t.Fatalf("Git call %d = %v, want %s", i, git.calls[i].Args, command)
		}
		if i >= 3 && !git.calls[i].Deadline.Equal(workspace.OperationDeadline) {
			t.Fatal("export deadline changed")
		}
	}
	seed := docker.calls[6].Stdin
	if contentHash(seed) != workspace.SeedSHA256 || int64(len(seed)) != workspace.SeedBytes {
		t.Fatal("seed bytes differ from retained workspace")
	}
	source, err := loadManagedDockerSource(t.Context(), j, workspace.Keeper)
	if err != nil || source.SeedSHA256 != workspace.SeedSHA256 || source.SeedBytes != workspace.SeedBytes {
		t.Fatalf("source seed = %+v, %v", source, err)
	}
	archive := tar.NewReader(bytes.NewReader(seed))
	files := map[string]string{}
	for {
		h, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(archive)
		if err != nil {
			t.Fatal(err)
		}
		files[h.Name] = string(body)
	}
	if !reflect.DeepEqual(files, map[string]string{".gitignore": "ignored-output\n", "source.txt": "original\n"}) {
		t.Fatalf("source archive = %v", files)
	}
	for _, call := range docker.calls {
		if !call.Deadline.Equal(workspace.OperationDeadline) {
			t.Fatalf("Docker deadline %v != retained %v", call.Deadline, workspace.OperationDeadline)
		}
	}
	if workspace.OperationDeadline.After(manifest.Deadline) {
		t.Fatal("workspace deadline exceeds admission")
	}
	owner := &managedControllerOwner{journal: j, manifest: manifest, scope: scope, runtime: ref}
	changed := false
	backend, err := newManagedDeliveryBackend(t.Context(), owner, runtime, func(context.Context) (ManagedDockerConfig, error) {
		c := config
		if changed {
			c.RuntimeConfigSHA256 = strings.Repeat("f", 64)
		}
		return c, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	changed = true
	beforeExport, beforeContinuity := len(git.calls), len(docker.calls)
	if _, err := backend.checkout(t.Context(), "worker", expectedAttempt, state.BaseSHA, state.Deadline); !isManagedInterruption(err) {
		t.Fatalf("changed continuity accepted: %v", err)
	}
	if len(git.calls) <= beforeExport || len(docker.calls) != beforeContinuity {
		t.Fatal("ready continuity did not reexport before denying changed runtime")
	}
	backend, err = newManagedDeliveryBackend(t.Context(), owner, runtime, func(context.Context) (ManagedDockerConfig, error) { return config, nil })
	if err != nil {
		t.Fatal(err)
	}
	docker.change = func(stage string, _ HostInvocation, result *HostResult) error {
		if stage == "inspect-seeded" {
			result.Stdout = bytes.ReplaceAll(result.Stdout, []byte("2026-10-24T02:00:00-04:00"), []byte("2026-10-24T03:00:00-04:00"))
		}
		return nil
	}
	beforeExport, beforeContinuity = len(git.calls), len(docker.calls)
	if _, err := backend.checkout(t.Context(), "worker", expectedAttempt, state.BaseSHA, state.Deadline); !isManagedInterruption(err) {
		t.Fatalf("changed keeper continuity accepted: %v", err)
	}
	if len(git.calls) != beforeExport+3 || len(docker.calls) != beforeContinuity+1 || !docker.calls[len(docker.calls)-1].Deadline.Equal(workspace.OperationDeadline) {
		t.Fatal("ready replay failed to reexport and inspect under retained deadline")
	}
	beforeGit, beforeDocker := len(git.calls), len(docker.calls)
	status, err = RunManagedController(t.Context(), root, spec.ID, gen, scope.ID, runtime, reader)
	if !isManagedInterruption(err) || status.State != "running" || status.Attempts != expectedAttempt || len(git.calls) != beforeGit || len(docker.calls) != beforeDocker {
		t.Fatalf("reentry = %+v, %v", status, err)
	}
	store.owner = owner
	if _, err := j.conn.ExecContext(t.Context(), "UPDATE managed_scopes SET phase='revoked' WHERE id=?", scope.ID); err != nil {
		t.Fatal(err)
	}
	next := state
	next.Feedback = "must not persist"
	if err := store.save(t.Context(), next, state.Revision); err == nil {
		t.Fatal("revoked owner saved snapshot")
	}
	after, err := store.load(t.Context())
	if err != nil || !reflect.DeepEqual(after, state) {
		t.Fatalf("revoked save changed snapshot: %+v, %v", after, err)
	}
}

type managedEngineRuntime struct {
	git    *sourceGitRuntime
	docker *sourcePrepareRuntime
}

func (r *managedEngineRuntime) Invoke(ctx context.Context, call HostInvocation) (HostResult, error) {
	if call.Path == filepath.Join(r.git.j.admission.Reservation.StateRoot, r.git.j.admission.ObjectiveID, "git") {
		return r.git.Invoke(ctx, call)
	}
	if call.Path == "/usr/local/bin/docker" || call.Path == "/usr/local/bin/oro" {
		return r.docker.Invoke(ctx, call)
	}
	return HostResult{}, errors.New("unexpected managed host path")
}

func (r *managedEngineRuntime) Retire(ctx context.Context, scope ScopeHandle) error {
	return r.git.Retire(ctx, scope)
}
