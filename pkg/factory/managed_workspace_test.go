//nolint:testpackage // Exercises private managed workspace authority.
package factory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestManagedWorkspace(t *testing.T) {
	if os.Getenv("ORO_WORKSPACE_LEADER") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestManagedWorkspace$", "-test.v")
		cmd.Env = append(os.Environ(), "ORO_WORKSPACE_LEADER=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("leader subprocess: %v\n%s", err, out)
		}
		return
	}
	t.Run("reserve and prepare", testWorkspaceReservePrepare)
	t.Run("concurrent reservations and preparation", testWorkspaceRaces)
	t.Run("owner and input boundaries", testWorkspaceBoundaries)
	t.Run("corrupt records", testWorkspaceCorruption)
	t.Run("retained workspace history", testWorkspaceHistory)
	t.Run("seeded source binding", testWorkspaceReady)
	t.Run("budget and rollback", testWorkspaceBudgetRollback)
	t.Run("rollback boundaries", testWorkspaceRollbackBoundaries)
	t.Run("missing table admission replay", testWorkspaceMissingTableReplay)
}

func workspaceFixture(t *testing.T) (*managedControllerOwner, context.Context, context.CancelFunc, time.Time) {
	t.Helper()
	store, _, _ := ownerFixture(t)
	state, err := store.load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	state.State = "running"
	state.Attempts = 1
	if err = store.save(t.Context(), state, state.Revision); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().UTC().Add(20 * time.Second)
	ctx, cancel := context.WithDeadline(t.Context(), deadline.Add(time.Second))
	return store.owner, ctx, cancel, deadline
}

func reserveWorkspaceFixture(t *testing.T) (*managedControllerOwner, context.Context, context.CancelFunc, managedWorkspace) {
	t.Helper()
	owner, ctx, cancel, deadline := workspaceFixture(t)
	w, err := reserveManagedWorkspace(ctx, owner, 1, "worker", strings.Repeat("a", 40), deadline)
	if err != nil {
		t.Fatal(err)
	}
	return owner, ctx, cancel, w
}

func workspaceRead(ctx context.Context, t *testing.T, owner *managedControllerOwner, id string) managedWorkspace {
	t.Helper()
	var w managedWorkspace
	err := owner.journal.mutate(ctx, "", "", func(tx *sql.Tx, _ *managedSupervision) (bool, error) {
		var e error
		w, e = readManagedWorkspace(ctx, tx, owner, id)
		return false, e
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func testWorkspaceReservePrepare(t *testing.T) {
	owner, ctx, cancel, w := reserveWorkspaceFixture(t)
	defer cancel()
	if w.Phase != "reserved" || w.SeedBytes != 0 {
		t.Fatalf("reserved %+v", w)
	}
	if err := beginManagedWorkspacePreparation(ctx, owner, w.ID, []byte("seed")); err != nil {
		t.Fatal(err)
	}
	got := workspaceRead(ctx, t, owner, w.ID)
	if got.Phase != "preparing" || got.SeedSHA256 != contentHash([]byte("seed")) || got.SeedBytes != 4 || !got.OperationDeadline.Equal(w.OperationDeadline) {
		t.Fatalf("prepared %+v", got)
	}
	if err := beginManagedWorkspacePreparation(ctx, owner, w.ID, []byte("different")); err == nil {
		t.Fatal("duplicate preparation accepted")
	}
	if err := bindManagedWorkspaceSource(ctx, owner, w.ID, ScopeHandle{}); err == nil {
		t.Fatal("missing source accepted")
	}
	var count int
	if err := owner.journal.conn.QueryRowContext(ctx, "SELECT count(*) FROM managed_events WHERE kind IN ('workspace-reserved','workspace-preparing')").Scan(&count); err != nil || count != 2 {
		t.Fatalf("events=%d error=%v", count, err)
	}
}

func testWorkspaceRaces(t *testing.T) {
	owner, ctx, cancel, deadline := workspaceFixture(t)
	defer cancel()
	other, err := openManagedJournal(ctx, owner.journal.admission.Reservation.StateRoot, owner.journal.admission.ObjectiveID)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	copyOwner := *owner
	copyOwner.journal = other
	var wg sync.WaitGroup
	results := make(chan managedWorkspace, 2)
	for _, o := range []*managedControllerOwner{owner, &copyOwner} {
		wg.Add(1)
		go func(o *managedControllerOwner) {
			defer wg.Done()
			w, e := reserveManagedWorkspace(ctx, o, 1, "worker", strings.Repeat("a", 40), deadline)
			if e == nil {
				results <- w
			}
		}(o)
	}
	wg.Wait()
	close(results)
	var found []managedWorkspace
	for w := range results {
		found = append(found, w)
	}
	if len(found) != 1 {
		t.Fatalf("reserved=%d", len(found))
	}
	successes := make(chan bool, 2)
	for _, o := range []*managedControllerOwner{owner, &copyOwner} {
		wg.Add(1)
		go func(o *managedControllerOwner) {
			defer wg.Done()
			if beginManagedWorkspacePreparation(ctx, o, found[0].ID, []byte("seed")) == nil {
				successes <- true
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
		t.Fatalf("preparation winners=%d", n)
	}
}

func testWorkspaceBoundaries(t *testing.T) {
	owner, ctx, cancel, deadline := workspaceFixture(t)
	defer cancel()
	for _, v := range []struct {
		attempt    int
		stage, sha string
		deadline   time.Time
	}{{2, "worker", strings.Repeat("a", 40), deadline}, {1, "review", strings.Repeat("a", 40), deadline}, {1, "worker", "invalid", deadline}, {1, "worker", strings.Repeat("a", 40), deadline.Add(time.Minute)}} {
		if _, err := reserveManagedWorkspace(ctx, owner, v.attempt, v.stage, v.sha, v.deadline); err == nil {
			t.Fatalf("accepted %+v", v)
		}
	}
	if _, err := reserveManagedWorkspace(t.Context(), owner, 1, "worker", strings.Repeat("a", 40), deadline); err == nil {
		t.Fatal("missing context deadline accepted")
	}
	w, err := reserveManagedWorkspace(ctx, owner, 1, "worker", strings.Repeat("a", 40), deadline)
	if err != nil {
		t.Fatal(err)
	}
	changed := *owner
	changed.runtime.SHA256 = strings.Repeat("b", 64)
	if err = beginManagedWorkspacePreparation(ctx, &changed, w.ID, []byte("seed")); err == nil {
		t.Fatal("changed runtime accepted")
	}
	if _, err = reserveManagedWorkspace(ctx, owner, 1, "worker", strings.Repeat("b", 40), deadline); err == nil {
		t.Fatal("changed source duplicate accepted")
	}
	if _, _, err = owner.journal.advanceEpoch(ctx, "supervisor", "revocation", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = beginManagedWorkspacePreparation(ctx, owner, w.ID, []byte("seed")); err == nil {
		t.Fatal("revoked owner accepted")
	}
}

func testWorkspaceCorruption(t *testing.T) {
	for _, query := range []string{
		"UPDATE managed_workspaces SET controller_id='other'", "UPDATE managed_workspaces SET attempt=2", "UPDATE managed_workspaces SET stage='review'", "UPDATE managed_workspaces SET keeper_id='other'", "UPDATE managed_workspaces SET body='{}'",
		"UPDATE managed_workspaces SET body=body||' '", "UPDATE managed_workspaces SET body=zeroblob(2049)", "DELETE FROM managed_workspaces", "DROP TABLE managed_workspaces",
	} {
		t.Run(query, func(t *testing.T) {
			owner, ctx, cancel, w := reserveWorkspaceFixture(t)
			defer cancel()
			if _, err := owner.journal.conn.ExecContext(ctx, "PRAGMA ignore_check_constraints=ON"); err != nil {
				t.Fatal(err)
			}
			if _, err := owner.journal.conn.ExecContext(ctx, query); err != nil {
				t.Fatal(err)
			}
			if err := beginManagedWorkspacePreparation(ctx, owner, w.ID, []byte("seed")); err == nil {
				t.Fatal("corrupt record accepted")
			}
		})
	}
}

func testWorkspaceHistory(t *testing.T) {
	owner, ctx, cancel, _ := reserveWorkspaceFixture(t)
	defer cancel()
	conn := owner.journal.conn
	if _, err := conn.ExecContext(ctx, "DELETE FROM managed_admissions"); err != nil {
		t.Fatal(err)
	}
	if err := initManagedSchema(ctx, conn); err == nil {
		t.Fatal("retained workspace accepted without admission")
	}
}

func testWorkspaceReady(t *testing.T) {
	root, spec, reservation := managedAdmissionFixture(t)
	spec.Runtime.HostRequiredBytes = 1 << 40
	reservation.Bytes = spec.Runtime.HostRequiredBytes
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
	defer j.Close()
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
	defer store.close()
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
	deadline := time.Now().UTC().Add(25 * time.Second)
	ctx, cancel := context.WithDeadline(t.Context(), deadline)
	defer cancel()
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
	record, err := loadManagedDockerSource(ctx, j, source.Keeper)
	if err != nil {
		t.Fatal(err)
	}
	original, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	record.Phase = "seed_acknowledged"
	notSeeded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = j.conn.ExecContext(ctx, "UPDATE managed_docker_sources SET body=? WHERE keeper_scope_id=?", notSeeded, source.Keeper.ID); err != nil {
		t.Fatal(err)
	}
	if err = bindManagedWorkspaceSource(ctx, owner, w.ID, source.Keeper); err == nil {
		t.Fatal("nonseeded source accepted")
	}
	if _, err = j.conn.ExecContext(ctx, "UPDATE managed_docker_sources SET body=? WHERE keeper_scope_id=?", original, source.Keeper.ID); err != nil {
		t.Fatal(err)
	}
	foreign := source.Keeper
	foreign.Nonce = strings.Repeat("f", 32)
	if err = bindManagedWorkspaceSource(ctx, owner, w.ID, foreign); err == nil {
		t.Fatal("foreign keeper accepted")
	}
	record.Phase = "seeded"
	record.SeedSHA256 = strings.Repeat("f", 64)
	wrongSeed, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = j.conn.ExecContext(ctx, "UPDATE managed_docker_sources SET body=? WHERE keeper_scope_id=?", wrongSeed, source.Keeper.ID); err != nil {
		t.Fatal(err)
	}
	if err = bindManagedWorkspaceSource(ctx, owner, w.ID, source.Keeper); err == nil {
		t.Fatal("different seed accepted")
	}
	if _, err = j.conn.ExecContext(ctx, "UPDATE managed_docker_sources SET body=? WHERE keeper_scope_id=?", original, source.Keeper.ID); err != nil {
		t.Fatal(err)
	}
	bind := func(c context.Context) error { return bindManagedWorkspaceSource(c, owner, w.ID, source.Keeper) }
	workspaceAssertEventCap(t, j, bind)
	workspaceAssertCommitFailure(t, j, bind)
	before := len(runtime.calls) + len(keeperRuntime.calls) + len(base.calls)
	if err = bindManagedWorkspaceSource(ctx, owner, w.ID, source.Keeper); err != nil {
		t.Fatal(err)
	}
	got := workspaceRead(ctx, t, owner, w.ID)
	if got.Phase != "ready" || got.Keeper != source.Keeper || got.SeedSHA256 != contentHash(seed) {
		t.Fatalf("ready=%+v", got)
	}
	if err = bindManagedWorkspaceSource(ctx, owner, w.ID, source.Keeper); err != nil {
		t.Fatal(err)
	}
	if len(runtime.calls)+len(keeperRuntime.calls)+len(base.calls) != before {
		t.Fatal("workspace binding invoked runtime")
	}
	if err = bindManagedWorkspaceSource(ctx, owner, w.ID, ScopeHandle{}); err == nil {
		t.Fatal("different keeper accepted")
	}
	next, err := store.load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	next.Attempts = 2
	if err = store.save(ctx, next, next.Revision); err != nil {
		t.Fatal(err)
	}
	second, err := reserveManagedWorkspace(ctx, owner, 2, "worker", strings.Repeat("a", 40), deadline)
	if err != nil {
		t.Fatal(err)
	}
	if err = beginManagedWorkspacePreparation(ctx, owner, second.ID, seed); err != nil {
		t.Fatal(err)
	}
	if err = bindManagedWorkspaceSource(ctx, owner, second.ID, source.Keeper); err == nil {
		t.Fatal("keeper bound to two workspaces")
	}
}

func testWorkspaceBudgetRollback(t *testing.T) {
	t.Run("oversized seed denied without transition", func(t *testing.T) {
		owner, ctx, cancel, w := reserveWorkspaceFixture(t)
		defer cancel()
		limits, err := ManagedHostRetention(owner.manifest.Spec)
		if err != nil {
			t.Fatal(err)
		}
		if err = beginManagedWorkspacePreparation(ctx, owner, w.ID, make([]byte, limits.ArchiveBytes+1)); err == nil {
			t.Fatal("oversized seed accepted")
		}
		if got := workspaceRead(ctx, t, owner, w.ID); got.Phase != "reserved" {
			t.Fatalf("workspace=%+v", got)
		}
	})
	t.Run("event failure rolls back", func(t *testing.T) {
		owner, ctx, cancel, deadline := workspaceFixture(t)
		defer cancel()
		j := owner.journal
		if _, err := j.conn.ExecContext(ctx, `CREATE TRIGGER fail_workspace_event BEFORE INSERT ON managed_events WHEN NEW.kind='workspace-reserved' BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
			t.Fatal(err)
		}
		if _, err := reserveManagedWorkspace(ctx, owner, 1, "worker", strings.Repeat("a", 40), deadline); err == nil {
			t.Fatal("injected event failure accepted")
		}
		var n int
		if err := j.conn.QueryRowContext(ctx, "SELECT count(*) FROM managed_workspaces").Scan(&n); err != nil || n != 0 {
			t.Fatalf("rows=%d error=%v", n, err)
		}
	})
	t.Run("all incomplete rows count", func(t *testing.T) {
		owner, ctx, cancel, deadline := workspaceFixture(t)
		defer cancel()
		limits, err := ManagedHostRetention(owner.manifest.Spec)
		if err != nil {
			t.Fatal(err)
		}
		for i := int64(0); i < limits.SourceVersions; i++ {
			if _, err = owner.journal.conn.ExecContext(ctx, "INSERT INTO managed_workspaces(id,controller_id,attempt,stage,body) VALUES(?,?,?,?,?)", fmt.Sprintf("retained%d", i), fmt.Sprintf("controller%d", i), 1, "worker", []byte("{}")); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = reserveManagedWorkspace(ctx, owner, 1, "worker", strings.Repeat("a", 40), deadline); err == nil || !strings.Contains(err.Error(), "allowance exhausted") {
			t.Fatalf("error=%v", err)
		}
	})
}

func workspaceJournalBytes(t *testing.T, j *managedJournal) string {
	t.Helper()
	var value string
	query := `SELECT (SELECT revision||':'||hex(body) FROM managed_supervision)||':'||
 (SELECT coalesce(group_concat(id||':'||controller_id||':'||attempt||':'||stage||':'||coalesce(keeper_id,'NULL')||':'||hex(body),'|'),'') FROM (SELECT * FROM managed_workspaces ORDER BY id))||':'||
 (SELECT coalesce(group_concat(revision||':'||kind||':'||ref||':'||hex(body),'|'),'') FROM (SELECT * FROM managed_events ORDER BY revision))`
	if err := j.conn.QueryRowContext(t.Context(), query).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func workspaceAssertEventCap(t *testing.T, j *managedJournal, operation func(context.Context) error) {
	t.Helper()
	state, err := j.supervision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	original := state
	limit, err := journalRecordLimit(j.admission)
	if err != nil {
		t.Fatal(err)
	}
	state.Revision = 8*limit + 16
	write := func(s managedSupervision) {
		body, e := json.Marshal(s)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = j.conn.ExecContext(t.Context(), "UPDATE managed_supervision SET revision=?,body=? WHERE id=1", s.Revision, body); e != nil {
			t.Fatal(e)
		}
	}
	write(state)
	before := workspaceJournalBytes(t, j)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	err = operation(ctx)
	if err == nil || !strings.Contains(err.Error(), "managed event budget exhausted") {
		t.Fatalf("expected event cap: %v", err)
	}
	if after := workspaceJournalBytes(t, j); after != before {
		t.Fatal("event cap changed workspace, revision or events")
	}
	write(original)
}

func workspaceAssertCommitFailure(t *testing.T, j *managedJournal, operation func(context.Context) error) {
	t.Helper()
	before := workspaceJournalBytes(t, j)
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
	if err = reader.QueryRowContext(t.Context(), "SELECT revision FROM snapshots WHERE id=1").Scan(&revision); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	err = operation(ctx)
	if err == nil || !strings.Contains(err.Error(), "commit managed transition") {
		t.Fatalf("expected actual commit failure: %v", err)
	}
	if err = reader.Rollback(); err != nil {
		t.Fatal(err)
	}
	if after := workspaceJournalBytes(t, j); after != before {
		t.Fatal("commit failure changed workspace, revision or events")
	}
}

func testWorkspaceRollbackBoundaries(t *testing.T) {
	t.Run("reserve", func(t *testing.T) {
		owner, _, cancel, _ := workspaceFixture(t)
		defer cancel()
		op := func(c context.Context) error {
			deadline, _ := c.Deadline()
			_, err := reserveManagedWorkspace(c, owner, 1, "worker", strings.Repeat("a", 40), deadline.UTC())
			return err
		}
		workspaceAssertEventCap(t, owner.journal, op)
		workspaceAssertCommitFailure(t, owner.journal, op)
	})
	t.Run("prepare", func(t *testing.T) {
		owner, _, cancel, w := reserveWorkspaceFixture(t)
		defer cancel()
		op := func(c context.Context) error { return beginManagedWorkspacePreparation(c, owner, w.ID, []byte("seed")) }
		workspaceAssertEventCap(t, owner.journal, op)
		workspaceAssertCommitFailure(t, owner.journal, op)
	})
}

func testWorkspaceMissingTableReplay(t *testing.T) {
	owner, ctx, cancel, w := reserveWorkspaceFixture(t)
	defer cancel()
	j := owner.journal
	root := j.admission.Reservation.StateRoot
	id := j.admission.ObjectiveID
	if _, err := j.conn.ExecContext(ctx, "DROP TABLE managed_workspaces"); err != nil {
		t.Fatal(err)
	}
	_, _ = AdmitManaged(ctx, root, owner.manifest.Spec, owner.manifest.Reservation)
	var n int
	if err := j.conn.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='managed_workspaces'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("workspace recreated: count=%d error=%v", n, err)
	}
	if _, err := ReadManagedRuntimeReference(ctx, root, id); err == nil {
		t.Fatal("active runtime accepted missing workspace history")
	}
	if err := beginManagedWorkspacePreparation(ctx, owner, w.ID, []byte("seed")); err == nil {
		t.Fatal("preparation accepted missing workspace history")
	}
	if _, err := reserveManagedWorkspace(ctx, owner, 1, "worker", strings.Repeat("a", 40), w.OperationDeadline); err == nil {
		t.Fatal("reservation accepted missing workspace history")
	}
}
