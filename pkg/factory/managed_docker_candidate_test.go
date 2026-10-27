package factory //nolint:testpackage // Exercises private candidate authority.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestManagedCandidateIntent(t *testing.T) {
	if os.Getenv("ORO_CANDIDATE_BLOCK") == "1" {
		time.Sleep(30 * time.Second)
		return
	}
	if os.Getenv("ORO_CANDIDATE_LEADER") != "1" {
		runCandidateLeader(t)
		return
	}
	t.Run("basic", candidateBasicTest)
	t.Run("concurrent", candidateConcurrentTest)
	t.Run("expired checkout", candidateExpiredCheckoutTest)
	t.Run("expired worker", candidateExpiredWorkerTest)
	t.Run("expired objective", candidateExpiredObjectiveTest)
	t.Run("large keeper", candidateLargeKeeperTest)
	t.Run("keeper drift after reserve", candidateKeeperDriftTest)
	t.Run("worker guard", candidateGuardDeadlineTest)
	t.Run("counted budgets", candidateBudgetTest)
	t.Run("host budget exhausted", candidateHostBudgetTest)
	t.Run("guards", candidateGuardTest)
	t.Run("rollback", candidateRollbackTest)
	t.Run("history", candidateHistoryTest)
	t.Run("deleted row", candidateDeletedRowTest)
}

func candidateBasicTest(t *testing.T) {
	owner, ctx, w := candidateReadyFixture(t)
	before := time.Now().UTC()
	intent, err := reserveManagedCandidate(ctx, owner, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if intent.WorkspaceID != w.ID || intent.ExecutionStartedAt.Before(before) || !intent.ExecutionDeadline.After(intent.ExecutionStartedAt) {
		t.Fatalf("candidate: %+v", intent)
	}
	wantDeadline := intent.ExecutionStartedAt.Add(time.Duration(owner.manifest.Spec.StepTimeoutSeconds) * time.Second)
	if owner.manifest.Deadline.Before(wantDeadline) {
		wantDeadline = owner.manifest.Deadline
	}
	if d, ok := ctx.Deadline(); ok && d.Before(wantDeadline) {
		wantDeadline = d.UTC()
	}
	if !intent.ExecutionDeadline.Equal(wantDeadline) {
		t.Fatalf("worker deadline = %v, want %v", intent.ExecutionDeadline, wantDeadline)
	}
	beforeCounts := candidateCounts(t, owner.journal)
	if _, err := reserveManagedCandidate(ctx, owner, w.ID); err == nil {
		t.Fatal("duplicate candidate accepted")
	}
	if after := candidateCounts(t, owner.journal); after != beforeCounts {
		t.Fatal("duplicate spent budget")
	}
	replay, err := candidateRead(ctx, t, owner, w.ID)
	if err != nil || !reflect.DeepEqual(intent, replay) {
		t.Fatalf("candidate identity changed: %v", err)
	}
}

func candidateReadyFixture(t *testing.T, duration ...time.Duration) (*managedControllerOwner, context.Context, managedWorkspace) {
	t.Helper()
	root, spec, reservation := managedAdmissionFixture(t)
	if len(duration) > 1 {
		spec.TimeoutSeconds = int(duration[1] / time.Second)
		spec.StepTimeoutSeconds = 8
	}
	reservation = reservationForIntendedSpec(t, reservation, spec)
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
		t.Fatal(err)
	}
	ref := ManagedRuntimeReference{Path: filepath.Join(root, spec.ID, "runtime", "runtime.json"), SHA256: strings.Repeat("a", 64)}
	if err := ActivateManaged(t.Context(), root, spec.ID, ref); err != nil {
		t.Fatal(err)
	}
	j, err := openManagedJournal(t.Context(), root, spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	gen, _, err := j.beginGeneration(t.Context(), "supervisor", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	controller, err := reserveManagedController(t.Context(), j, "supervisor", gen)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.RecordHello(t.Context(), controller, os.Getpid(), os.Getpid()); err != nil {
		t.Fatal(err)
	}
	manifest, err := loadManagedManifest(t.Context(), root, spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	store, err := openManagedFactoryStore(t.Context(), filepath.Join(root, spec.ID))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.close() })
	owner := &managedControllerOwner{journal: j, manifest: manifest, scope: controller, runtime: ref}
	store.owner = owner
	state, err := store.load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	state.State = "running"
	state.Attempts = 1
	if err = store.save(t.Context(), state, state.Revision); err != nil {
		t.Fatal(err)
	}
	interval := 25 * time.Second
	if len(duration) > 0 {
		interval = duration[0]
	}
	deadline := time.Now().UTC().Add(interval)
	ctx, cancel := context.WithDeadline(t.Context(), deadline)
	t.Cleanup(cancel)
	w, err := reserveManagedWorkspace(ctx, owner, 1, "worker", strings.Repeat("a", 40), deadline)
	if err != nil {
		t.Fatal(err)
	}
	config := ManagedDockerConfig{StateRoot: root, ObjectiveID: spec.ID, PolicyHash: manifest.PolicyHash, Deadline: manifest.Deadline, HardStop: manifest.HardStop, RuntimeConfigPath: ref.Path, RuntimeConfigSHA256: ref.SHA256, OroPath: "/usr/local/bin/oro", OroSHA256: strings.Repeat("d", 64), DockerPath: "/usr/local/bin/docker", DockerSHA256: strings.Repeat("b", 64), DockerConfig: filepath.Join(root, spec.ID, "runtime", "docker"), DockerContext: spec.Runtime.DockerContext, DockerEndpoint: "unix:///private/tmp/admitted-docker.sock"}
	base := &dockerAdapterRuntime{t: t, journal: j}
	keeperRuntime := &sourceKeeperRuntime{base: base}
	runtime := &sourcePrepareRuntime{base: keeperRuntime, t: t}
	adapter, err := newManagedDockerVolumes(ctx, manifest, gen, j, runtime, func(ctx context.Context) (ManagedDockerConfig, error) { return config, ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	entries := []sourceEntry{{Path: "file", Mode: 0o100644, Data: []byte("data")}}
	seed, err := adapter.encodeSource(ctx, entries)
	if err != nil {
		t.Fatal(err)
	}
	if err = beginManagedWorkspacePreparation(ctx, owner, w.ID, seed); err != nil {
		t.Fatal(err)
	}
	source, err := adapter.prepareSource(ctx, entries)
	if err != nil {
		t.Fatal(err)
	}

	if err := bindManagedWorkspaceSource(ctx, owner, w.ID, source.Keeper); err != nil {
		t.Fatal(err)
	}
	state, err = store.load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state.BaseSHA, state.Worktree = w.SourceSHA, w.ID
	if err := store.save(ctx, state, state.Revision); err != nil {
		t.Fatal(err)
	}
	return owner, ctx, workspaceRead(ctx, t, owner, w.ID)
}

func candidateRead(ctx context.Context, t *testing.T, owner *managedControllerOwner, id string) (managedCandidateIntent, error) {
	t.Helper()
	var intent managedCandidateIntent
	err := owner.journal.mutate(ctx, "", "", func(tx *sql.Tx, _ *managedSupervision) (bool, error) {
		var err error
		intent, err = readManagedCandidate(ctx, tx, owner, id)
		return false, err
	})
	return intent, err
}

func candidateCounts(t *testing.T, j *managedJournal) [4]int {
	t.Helper()
	var result [4]int
	for i, table := range []string{"managed_docker_candidates", "managed_scopes", "managed_reservations", "managed_events"} {
		if err := j.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&result[i]); err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func candidateExpiredCheckoutTest(t *testing.T) {
	owner, _, w := candidateReadyFixture(t, 6*time.Second)
	time.Sleep(time.Until(w.OperationDeadline) + 20*time.Millisecond)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	before := candidateCounts(t, owner.journal)
	intent, err := reserveManagedCandidate(ctx, owner, w.ID)
	if err != nil {
		t.Fatalf("expired checkout rejected: %v", err)
	}
	if !intent.ExecutionStartedAt.After(w.OperationDeadline) || intent.Source.OperationDeadline != w.OperationDeadline {
		t.Fatal("checkout deadline changed")
	}
	after := candidateCounts(t, owner.journal)
	if after[0]-before[0] != 1 || after[1]-before[1] != 2 || after[2]-before[2] != 1 || after[3]-before[3] != 1 {
		t.Fatalf("candidate accounting %v -> %v", before, after)
	}
	got, err := candidateRead(ctx, t, owner, w.ID)
	if err != nil || !reflect.DeepEqual(got, intent) {
		t.Fatalf("candidate read: %+v %v", got, err)
	}
}

func candidateGuardTest(t *testing.T) {
	for _, kind := range []string{"snapshot", "runtime", "owner", "source", "keeper", "foreign workspace", "cleanup", "caller"} {
		t.Run(kind, func(t *testing.T) {
			owner, ctx, w := candidateReadyFixture(t)
			before := candidateCounts(t, owner.journal)
			active := owner
			switch kind {
			case "snapshot":
				store, err := openManagedFactoryStore(ctx, filepath.Join(owner.manifest.Reservation.StateRoot, owner.manifest.Spec.ID))
				if err != nil {
					t.Fatal(err)
				}
				defer store.close()
				store.owner = owner
				s, err := store.load(ctx)
				if err != nil {
					t.Fatal(err)
				}
				s.BaseSHA = strings.Repeat("b", 40)
				if err := store.save(ctx, s, s.Revision); err != nil {
					t.Fatal(err)
				}
			case "runtime":
				changed := *owner
				changed.runtime.SHA256 = strings.Repeat("b", 64)
				active = &changed
			case "owner":
				if err := owner.journal.setDesired(ctx, owner.scope.Generation, "stopping", "test", time.Time{}); err != nil {
					t.Fatal(err)
				}
				before = candidateCounts(t, owner.journal)
			case "source":
				if _, err := owner.journal.conn.ExecContext(ctx, "UPDATE managed_scopes SET phase='revoked' WHERE id=?", w.Keeper.ID); err != nil {
					t.Fatal(err)
				}
			case "keeper":
				if _, err := owner.journal.conn.ExecContext(ctx, "UPDATE managed_docker_keepers SET body='{}' WHERE keeper_scope_id=?", w.Keeper.ID); err != nil {
					t.Fatal(err)
				}
			case "foreign workspace":
				w.ID = strings.Repeat("f", 32)
			case "cleanup":
				generated := newManagedDockerVolumeIntent(owner.journal, owner.scope.Generation, owner.journal.admission.Deadline, owner.runtime.SHA256)
				cleanup := generated.CreateHost
				cleanup.Kind = "cleanup"
				tx, err := owner.journal.conn.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := owner.journal.insertScope(ctx, tx, cleanup, owner.journal.admission.HardStop); err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
				before = candidateCounts(t, owner.journal)
			case "caller":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if _, err := reserveManagedCandidate(ctx, active, w.ID); err == nil {
				t.Fatalf("%s guard accepted", kind)
			}
			if after := candidateCounts(t, owner.journal); after != before {
				t.Fatalf("%s spent state %v -> %v", kind, before, after)
			}
		})
	}
}

func candidateRollbackTest(t *testing.T) {
	owner, _, w := candidateReadyFixture(t)
	before := candidateCounts(t, owner.journal)
	op := func(ctx context.Context) error { _, err := reserveManagedCandidate(ctx, owner, w.ID); return err }
	candidateAssertEventCap(t, owner.journal, op)
	if after := candidateCounts(t, owner.journal); after != before {
		t.Fatalf("event rollback %v -> %v", before, after)
	}
	candidateAssertCommitFailure(t, owner.journal, op)
	if after := candidateCounts(t, owner.journal); after != before {
		t.Fatalf("commit rollback %v -> %v", before, after)
	}
}

func candidateHistoryTest(t *testing.T) {
	owner, ctx, w := candidateReadyFixture(t)
	intent, err := reserveManagedCandidate(ctx, owner, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.journal.conn.ExecContext(ctx, "UPDATE managed_docker_candidates SET body=body||' ' WHERE workspace_id=?", w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := candidateRead(ctx, t, owner, w.ID); err == nil {
		t.Fatal("noncanonical candidate accepted")
	}
	before := candidateCounts(t, owner.journal)
	if _, err := reserveManagedCandidate(ctx, owner, w.ID); err == nil {
		t.Fatal("corrupt candidate replaced")
	}
	if after := candidateCounts(t, owner.journal); after != before {
		t.Fatal("corruption spent budget")
	}
	if intent.ExecutionStartedAt.IsZero() {
		t.Fatal("missing execution identity")
	}
	if _, err := owner.journal.conn.ExecContext(ctx, "DROP TABLE managed_docker_candidates"); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManagedRuntimeReference(ctx, owner.manifest.Reservation.StateRoot, owner.manifest.Spec.ID); err == nil {
		t.Fatal("missing candidate history accepted")
	}
	if err := initManagedSchema(ctx, owner.journal.conn); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := owner.journal.conn.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='managed_docker_candidates'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("candidate history recreated: %d %v", n, err)
	}
}

func candidateExpiredWorkerTest(t *testing.T) {
	owner, _, w := candidateReadyFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	intent, err := reserveManagedCandidate(ctx, owner, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	<-ctx.Done()
	before := candidateCounts(t, owner.journal)
	fresh, freshCancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer freshCancel()
	if _, err := reserveManagedCandidate(fresh, owner, w.ID); err == nil {
		t.Fatal("expired worker renewed")
	}
	after := candidateCounts(t, owner.journal)
	got, err := candidateRead(fresh, t, owner, w.ID)
	if err != nil || !reflect.DeepEqual(intent, got) || after != before {
		t.Fatalf("expired worker changed: %+v %v", got, err)
	}
}

func candidateBudgetTest(t *testing.T) {
	owner, ctx, w := candidateReadyFixture(t)
	j := owner.journal
	limits, err := ManagedHostRetention(owner.manifest.Spec)
	if err != nil {
		t.Fatal(err)
	}
	bounds, err := managedRecordLimits(j.admission.Recovery, j.admission.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	if managedCandidateBytes+managedWorkspaceBytes+3*managedDockerIntentBytes >= 1<<20 || limits.ScopeRecords != bounds.Scopes || limits.SourceVersions <= 0 {
		t.Fatal("candidate metadata is outside existing allowances")
	}
	before := candidateCounts(t, j)
	intent, err := reserveManagedCandidate(ctx, owner, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	after := candidateCounts(t, j)
	if after != [4]int{before[0] + 1, before[1] + 2, before[2] + 1, before[3] + 1} {
		t.Fatalf("accounting %v -> %v", before, after)
	}
	var hosts, models int
	if err := j.conn.QueryRowContext(ctx, "SELECT count(*) FROM managed_reservations WHERE kind='host' AND token=?", intent.CreateHost.ID).Scan(&hosts); err != nil {
		t.Fatal(err)
	}
	if err := j.conn.QueryRowContext(ctx, "SELECT count(*) FROM managed_reservations WHERE kind='model' AND token=?", intent.Container.ID).Scan(&models); err != nil {
		t.Fatal(err)
	}
	if hosts != 1 || models != 0 {
		t.Fatalf("host %d model %d", hosts, models)
	}
}

func candidateExpiredObjectiveTest(t *testing.T) {
	owner, _, w := candidateReadyFixture(t, 6*time.Second, 12*time.Second)
	time.Sleep(time.Until(owner.manifest.Deadline) + 20*time.Millisecond)
	before := candidateCounts(t, owner.journal)
	if _, err := reserveManagedCandidate(t.Context(), owner, w.ID); err == nil {
		t.Fatal("expired objective admitted")
	}
	if after := candidateCounts(t, owner.journal); after != before {
		t.Fatal("expired objective spent state")
	}
}

func candidateHostBudgetTest(t *testing.T) {
	owner, ctx, w := candidateReadyFixture(t)
	j := owner.journal
	var count int
	if err := j.conn.QueryRowContext(ctx, "SELECT count(*) FROM managed_reservations WHERE kind='host'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	for i := count; i < j.admission.Runtime.MaxHostInvocations; i++ {
		if _, err := j.conn.ExecContext(ctx, "INSERT INTO managed_reservations(kind,token,generation) VALUES('host',?,?)", fmt.Sprintf("candidate-test-%d", i), owner.scope.Generation); err != nil {
			t.Fatal(err)
		}
	}
	before := candidateCounts(t, j)
	if _, err := reserveManagedCandidate(ctx, owner, w.ID); err == nil {
		t.Fatal("exhausted host budget accepted")
	}
	if after := candidateCounts(t, j); after != before {
		t.Fatal("host budget failure changed state")
	}
}

func candidateAssertCommitFailure(t *testing.T, j *managedJournal, operation func(context.Context) error) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(j.admission.Reservation.StateRoot, j.admission.ObjectiveID, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reader, err := db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	var revision int
	if err := reader.QueryRowContext(t.Context(), "SELECT revision FROM snapshots WHERE id=1").Scan(&revision); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	err = operation(ctx)
	if err == nil || !strings.Contains(err.Error(), "commit managed transition") {
		t.Fatalf("expected actual commit failure: %v", err)
	}
	if err := reader.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func candidateDeletedRowTest(t *testing.T) {
	owner, ctx, w := candidateReadyFixture(t)
	if _, err := reserveManagedCandidate(ctx, owner, w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.journal.conn.ExecContext(ctx, "DELETE FROM managed_docker_candidates WHERE workspace_id=?", w.ID); err != nil {
		t.Fatal(err)
	}
	before := candidateCounts(t, owner.journal)
	if _, err := reserveManagedCandidate(ctx, owner, w.ID); err == nil {
		t.Fatal("deleted candidate recreated")
	}
	if after := candidateCounts(t, owner.journal); after != before {
		t.Fatalf("deleted candidate spent state %v -> %v", before, after)
	}
	if _, err := owner.journal.conn.ExecContext(ctx, "DELETE FROM managed_events WHERE kind='candidate-prepared' AND ref=?", w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManagedRuntimeReference(ctx, owner.manifest.Reservation.StateRoot, owner.manifest.Spec.ID); err == nil {
		t.Fatal("deleted candidate history accepted as active")
	}
	before = candidateCounts(t, owner.journal)
	if _, err := reserveManagedCandidate(ctx, owner, w.ID); err == nil {
		t.Fatal("deleted candidate row and event recreated")
	}
	if after := candidateCounts(t, owner.journal); after != before {
		t.Fatal("deleted history spent state")
	}
}

func candidateGuardDeadlineTest(t *testing.T) {
	owner, _, w := candidateReadyFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	intent, err := reserveManagedCandidate(ctx, owner, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !intent.GuardDeadline.Equal(intent.ExecutionDeadline) || !intent.ExecutionDeadline.Before(owner.journal.admission.Deadline) || !intent.ExecutionDeadline.Before(owner.journal.admission.HardStop) {
		t.Fatalf("candidate guard does not match worker bound: %+v", intent)
	}
	record, err := loadManagedScope(ctx, owner.journal.conn, intent.Container)
	if err != nil || !record.Deadline.Equal(intent.ExecutionDeadline) {
		t.Fatalf("container deadline %+v: %v", record, err)
	}
}

func candidateLargeKeeperTest(t *testing.T) {
	owner, ctx, w := candidateReadyFixture(t)
	j := owner.journal
	var keeper managedDockerKeeperIntent
	err := j.mutate(ctx, "", "", func(tx *sql.Tx, _ *managedSupervision) (bool, error) {
		var err error
		keeper, err = readManagedDockerKeeper(ctx, tx, j, w.Keeper)
		return false, err
	})
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 10; n++ {
		keeper.Binding.Labels[fmt.Sprintf("extra-%d", n)] = strings.Repeat("\"", 700)
	}
	body, err := encodeManagedDockerKeeper(j, keeper)
	if err != nil {
		t.Fatalf("valid bounded keeper rejected: %v", err)
	}
	if len(body) < 14<<10 {
		t.Fatalf("fixture too small: %d", len(body))
	}
	if _, err := j.conn.ExecContext(ctx, "UPDATE managed_docker_keepers SET body=? WHERE keeper_scope_id=?", body, w.Keeper.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := reserveManagedCandidate(ctx, owner, w.ID); err != nil {
		t.Fatalf("valid large keeper narrowed: %v", err)
	}
}

func candidateKeeperDriftTest(t *testing.T) {
	owner, ctx, w := candidateReadyFixture(t)
	if _, err := reserveManagedCandidate(ctx, owner, w.ID); err != nil {
		t.Fatal(err)
	}
	var keeper managedDockerKeeperIntent
	err := owner.journal.mutate(ctx, "", "", func(tx *sql.Tx, _ *managedSupervision) (bool, error) {
		var err error
		keeper, err = readManagedDockerKeeper(ctx, tx, owner.journal, w.Keeper)
		return false, err
	})
	if err != nil {
		t.Fatal(err)
	}
	keeper.Binding.CreatedAt = "2026-10-24T05:49:05Z"
	body, err := encodeManagedDockerKeeper(owner.journal, keeper)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.journal.conn.ExecContext(ctx, "UPDATE managed_docker_keepers SET body=? WHERE keeper_scope_id=?", body, w.Keeper.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := candidateRead(ctx, t, owner, w.ID); err == nil {
		t.Fatal("changed keeper accepted")
	}
}

func candidateAssertEventCap(t *testing.T, j *managedJournal, operation func(context.Context) error) {
	t.Helper()
	original, err := j.supervision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	limit, err := journalRecordLimit(j.admission)
	if err != nil {
		t.Fatal(err)
	}
	capped := original
	capped.Revision = 8*limit + 16
	body, err := json.Marshal(capped)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := j.conn.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for rev := original.Revision + 1; rev <= capped.Revision; rev++ {
		if _, err := tx.ExecContext(t.Context(), "INSERT INTO managed_events(revision,kind,ref,body) VALUES(?,'test-padding','test',?)", rev, body); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(t.Context(), "UPDATE managed_supervision SET revision=?,body=? WHERE id=1", capped.Revision, body); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	before := candidateCounts(t, j)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := operation(ctx); err == nil || !strings.Contains(err.Error(), "managed event budget exhausted") {
		t.Fatalf("expected event cap: %v", err)
	}
	if after := candidateCounts(t, j); after != before {
		t.Fatal("event cap changed candidate state")
	}
	tx, err = j.conn.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), "DELETE FROM managed_events WHERE revision>?", original.Revision); err != nil {
		t.Fatal(err)
	}
	body, err = json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), "UPDATE managed_supervision SET revision=?,body=? WHERE id=1", original.Revision, body); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func candidateConcurrentTest(t *testing.T) {
	owner, ctx, w := candidateReadyFixture(t)
	other, err := openManagedJournal(ctx, owner.manifest.Reservation.StateRoot, owner.manifest.Spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	second := *owner
	second.journal = other
	before := candidateCounts(t, owner.journal)
	var wg sync.WaitGroup
	successes := make(chan managedCandidateIntent, 2)
	for _, o := range []*managedControllerOwner{owner, &second} {
		wg.Add(1)
		go func(o *managedControllerOwner) {
			defer wg.Done()
			intent, err := reserveManagedCandidate(ctx, o, w.ID)
			if err == nil {
				successes <- intent
			}
		}(o)
	}
	wg.Wait()
	close(successes)
	n := 0
	for range successes {
		n++
	}
	if n != 1 {
		t.Fatalf("concurrent candidates %d", n)
	}
	after := candidateCounts(t, owner.journal)
	if after != [4]int{before[0] + 1, before[1] + 2, before[2] + 1, before[3] + 1} {
		t.Fatalf("concurrent accounting %v -> %v", before, after)
	}
}

func TestCandidateLeaderHarness(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cmd := candidateLeaderCommand(ctx, 2*time.Second)
	cmd.Env = append(cmd.Env, "ORO_CANDIDATE_BLOCK=1")
	if !slices.Contains(cmd.Args, "-test.run=^TestManagedCandidateIntent$") || !slices.Contains(cmd.Args, "-test.timeout=2s") {
		t.Fatalf("child arguments: %v", cmd.Args)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	cancel()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled child succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled child not reaped")
	}
	boundedCtx, boundedCancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer boundedCancel()
	bounded := candidateLeaderCommand(boundedCtx, 200*time.Millisecond)
	bounded.Env = append(bounded.Env, "ORO_CANDIDATE_BLOCK=1")
	output, err := bounded.CombinedOutput()
	if err == nil || !bytes.Contains(output, []byte("test timed out")) {
		t.Fatalf("child self-timeout missing: %v %s", err, output)
	}
}

func candidateLeaderCommand(ctx context.Context, timeout time.Duration) *exec.Cmd {
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestManagedCandidateIntent$", "-test.timeout="+timeout.String(), "-test.v")
	cmd.Env = append(os.Environ(), "ORO_CANDIDATE_LEADER=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

func runCandidateLeader(t *testing.T) {
	t.Helper()
	timeout := 120 * time.Second
	if deadline, ok := t.Deadline(); ok {
		remaining := time.Until(deadline) - 5*time.Second
		if remaining < timeout {
			timeout = remaining
		}
	}
	if timeout <= time.Second {
		t.Fatal("insufficient time for candidate leader")
	}
	ctx, cancel := context.WithTimeout(t.Context(), timeout+time.Second)
	defer cancel()
	cmd := candidateLeaderCommand(ctx, timeout)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("leader: %v\n%s", err, out)
	}
}
