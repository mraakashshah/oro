package factory //nolint:testpackage // Exercise the private atomic Docker intent journal.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestManagedDockerVolumeIntent(t *testing.T) {
	if os.Getenv("ORO_DOCKER_INTENT_PROCESS") == "1" {
		dockerVolumeProcessChild(t)
		return
	}
	t.Run("new admission initializes bounded intent storage", func(t *testing.T) {
		_, _, store := journalFixture(t)
		var count int
		if err := store.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='managed_docker_intents'").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatal("new admission lacks its Docker intent table")
		}
	})
	t.Run("atomic reservation and exact durable lifecycle", testDockerVolumeLifecycle)
	t.Run("failed reservation rolls back every retained charge", testDockerVolumeRollback)
	t.Run("one dispatch winner across independent connections", testDockerVolumeDispatchRace)
	t.Run("prepared recovery requires every old host retired", testDockerVolumeRecovery)
	t.Run("admission replay cannot reconstruct intent history", testDockerVolumeAdmissionReplay)
	t.Run("malformed records and changed identities deny", testDockerVolumeCorruption)
	t.Run("inactive authority and bounded reservations deny", testDockerVolumeAuthority)
	t.Run("creator death preserves exact prepared and dispatching phases", testDockerVolumeProcessDeath)
	t.Run("missing admission cannot initialize feature history", testDockerVolumeMissingAdmission)
	t.Run("unused proof rejects hidden or malformed prior scopes", testDockerVolumeRecoveryCorruption)
	t.Run("transition failures preserve exact phase and binding", testDockerVolumeTransitionGuards)
	t.Run("referenced scope body cannot override lifecycle columns", testDockerVolumeScopeOverride)
}

func testDockerVolumeScopeOverride(t *testing.T) {
	for _, phase := range []string{"acknowledge", "dispatch", "noncanonical"} {
		t.Run(phase, func(t *testing.T) {
			f := newDockerVolumeFixture(t)
			intent := f.reserve(t)
			switch phase {
			case "acknowledge":
				if _, err := dispatchManagedDockerVolume(t.Context(), f.journal, intent.Volume, intent.RuntimeConfigSHA256); err != nil {
					t.Fatal(err)
				}
				dockerVolumeSQL(t, f.journal, "UPDATE managed_scopes SET body=json_set(body,'$.Phase','quiescent') WHERE id=?", intent.CreateHost.ID)
			case "dispatch":
				if err := f.journal.MarkQuiescent(t.Context(), intent.CreateHost); err != nil {
					t.Fatal(err)
				}
				dockerVolumeSQL(t, f.journal, "UPDATE managed_scopes SET body=json_set(body,'$.Phase','reserved','$.PID',0,'$.PGID',0) WHERE id=?", intent.CreateHost.ID)
			case "noncanonical":
				dockerVolumeSQL(t, f.journal, "UPDATE managed_scopes SET body=CAST(body || char(10) AS BLOB) WHERE id=?", intent.CreateHost.ID)
			}
			before := dockerVolumeCounts(t, f.journal)
			var beforeBody string
			if err := f.journal.conn.QueryRowContext(t.Context(), "SELECT body FROM managed_docker_intents WHERE volume_scope_id=?", intent.Volume.ID).Scan(&beforeBody); err != nil {
				t.Fatal(err)
			}
			if phase == "acknowledge" {
				if err := acknowledgeManagedDockerVolume(t.Context(), f.journal, intent.Volume, intent.CreateHost); err == nil {
					t.Fatal("scope body forged positive host retirement")
				}
			} else if _, err := dispatchManagedDockerVolume(t.Context(), f.journal, intent.Volume, intent.RuntimeConfigSHA256); err == nil {
				t.Fatal("scope body forged unused host identity")
			}
			if after := dockerVolumeCounts(t, f.journal); after != before {
				t.Fatal("malformed referenced scope changed intent")
			}
			var afterBody string
			if err := f.journal.conn.QueryRowContext(t.Context(), "SELECT body FROM managed_docker_intents WHERE volume_scope_id=?", intent.Volume.ID).Scan(&afterBody); err != nil || afterBody != beforeBody {
				t.Fatal("malformed scope changed exact intent bytes", err)
			}
			canonical, err := json.Marshal(struct {
				ScopeHandle
				Deadline time.Time
			}{intent.CreateHost, intent.CreateDeadline})
			if err != nil {
				t.Fatal(err)
			}
			dockerVolumeSQL(t, f.journal, "UPDATE managed_scopes SET body=? WHERE id=?", canonical, intent.CreateHost.ID)
			got, err := loadManagedDockerVolume(t.Context(), f.journal, intent.Volume)
			if err != nil {
				t.Fatal(err)
			}
			wantPhase := "prepared"
			if phase == "acknowledge" {
				wantPhase = "dispatching"
			}
			if got.Phase != wantPhase {
				t.Fatalf("changed durable phase %s", got.Phase)
			}
		})
	}
}

func testDockerVolumeTransitionGuards(t *testing.T) {
	f := newDockerVolumeFixture(t)
	intent := f.reserve(t)
	before := dockerVolumeCounts(t, f.journal)
	for _, scope := range []ScopeHandle{intent.CreateHost, intent.Volume} {
		dockerVolumeSQL(t, f.journal, "UPDATE managed_scopes SET pid=10,pgid=10 WHERE id=?", scope.ID)
		if _, err := dispatchManagedDockerVolume(t.Context(), f.journal, intent.Volume, intent.RuntimeConfigSHA256); err == nil {
			t.Fatal("scope with process dispatched")
		}
		dockerVolumeSQL(t, f.journal, "UPDATE managed_scopes SET pid=0,pgid=0 WHERE id=?", scope.ID)
	}
	if _, err := dispatchManagedDockerVolume(t.Context(), f.journal, intent.Volume, strings.Repeat("b", 64)); err == nil {
		t.Fatal("changed runtime dispatched")
	}
	observed := intent.Binding
	observed.CreatedAt = "2026-10-24T05:49:04Z"
	if err := bindManagedDockerVolume(t.Context(), f.journal, f.generation, intent.Volume, observed); err == nil {
		t.Fatal("bound never-dispatched lifetime")
	}
	if after := dockerVolumeCounts(t, f.journal); after != before {
		t.Fatal("rejected dispatch changed history")
	}
	if _, err := dispatchManagedDockerVolume(t.Context(), f.journal, intent.Volume, intent.RuntimeConfigSHA256); err != nil {
		t.Fatal(err)
	}
	for _, created := range []string{"", "2026-10-24T01:49:04-04:00", "invalid"} {
		observed.CreatedAt = created
		if err := bindManagedDockerVolume(t.Context(), f.journal, f.generation, intent.Volume, observed); err == nil {
			t.Fatal("invalid observed creation identity accepted")
		}
	}
	observed.CreatedAt = "2026-10-24T05:49:04Z"
	if err := bindManagedDockerVolume(t.Context(), f.journal, f.generation+1, intent.Volume, observed); err == nil {
		t.Fatal("foreign generation bound lifetime")
	}
	wrong := intent.CreateHost
	wrong.Nonce = strings.Repeat("f", 32)
	if err := acknowledgeManagedDockerVolume(t.Context(), f.journal, intent.Volume, wrong); err == nil {
		t.Fatal("foreign host acknowledged")
	}
	if err := f.journal.MarkQuiescent(t.Context(), intent.CreateHost); err != nil {
		t.Fatal(err)
	}
	dockerVolumeSQL(t, f.journal, "CREATE TRIGGER reject_intent_update BEFORE UPDATE ON managed_docker_intents BEGIN SELECT RAISE(ABORT,'injected transition failure'); END")
	before = dockerVolumeCounts(t, f.journal)
	if err := acknowledgeManagedDockerVolume(t.Context(), f.journal, intent.Volume, intent.CreateHost); err == nil {
		t.Fatal("failed commit acknowledged")
	}
	if after := dockerVolumeCounts(t, f.journal); after != before {
		t.Fatal("failed transition spent history")
	}
	dockerVolumeSQL(t, f.journal, "DROP TRIGGER reject_intent_update")
	// Binding a discovered exact lifetime never upgrades a lost acknowledgment.
	if err := bindManagedDockerVolume(t.Context(), f.journal, f.generation, intent.Volume, observed); err != nil {
		t.Fatal(err)
	}
	got, err := loadManagedDockerVolume(t.Context(), f.journal, intent.Volume)
	if err != nil || got.Phase != "dispatching" {
		t.Fatalf("binding became acknowledgment: %+v %v", got, err)
	}
	if err := f.journal.setDesired(t.Context(), f.generation, "stopping", "test", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := acknowledgeManagedDockerVolume(t.Context(), f.journal, intent.Volume, intent.CreateHost); err == nil {
		t.Fatal("stopped work acknowledged")
	}
	if _, err := reserveManagedDockerVolume(t.Context(), f.manifest, nil, f.generation, f.deadline, intent.RuntimeConfigSHA256); err == nil {
		t.Fatal("nil authority reserved")
	}
	if _, err := loadManagedDockerVolume(t.Context(), nil, intent.Volume); err == nil {
		t.Fatal("nil authority read")
	}
	if _, err := dispatchManagedDockerVolume(t.Context(), nil, intent.Volume, intent.RuntimeConfigSHA256); err == nil {
		t.Fatal("nil authority dispatched")
	}
	if err := retirePreparedManagedDockerVolume(t.Context(), nil, f.generation, intent.Volume); err == nil {
		t.Fatal("nil authority retired")
	}
}

func testDockerVolumeRecoveryCorruption(t *testing.T) {
	for _, mode := range []string{"backend column", "unknown field", "missing deadline", "noncanonical deadline", "wrong nonce column"} {
		t.Run(mode, func(t *testing.T) {
			f := newDockerVolumeFixture(t)
			intent := f.reserve(t)
			extra, err := reserveManagedHostInvocation(t.Context(), f.journal, f.generation, f.deadline)
			if err != nil {
				t.Fatal(err)
			}
			generation, _, err := f.journal.advanceEpoch(t.Context(), "supervisor", "recovery", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if err := f.journal.MarkQuiescent(t.Context(), intent.CreateHost); err != nil {
				t.Fatal(err)
			}
			if mode == "backend column" {
				dockerVolumeSQL(t, f.journal, "UPDATE managed_scopes SET backend='docker' WHERE id=?", extra.ID)
			} else {
				if err := f.journal.MarkQuiescent(t.Context(), extra); err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "unknown field":
					dockerVolumeSQL(t, f.journal, "UPDATE managed_scopes SET body=json_set(body,'$.unknown',1) WHERE id=?", extra.ID)
				case "missing deadline":
					dockerVolumeSQL(t, f.journal, "UPDATE managed_scopes SET body=json_remove(body,'$.Deadline') WHERE id=?", extra.ID)
				case "noncanonical deadline":
					dockerVolumeSQL(t, f.journal, "UPDATE managed_scopes SET body=json_set(body,'$.Deadline','2026-10-24T01:00:00-04:00') WHERE id=?", extra.ID)
				case "wrong nonce column":
					dockerVolumeSQL(t, f.journal, "UPDATE managed_scopes SET nonce=? WHERE id=?", strings.Repeat("f", 32), extra.ID)
				}
			}
			before := dockerVolumeCounts(t, f.journal)
			if err := retirePreparedManagedDockerVolume(t.Context(), f.journal, generation, intent.Volume); err == nil {
				t.Fatal("corrupt prior host certified no-effect retirement")
			}
			if after := dockerVolumeCounts(t, f.journal); before != after {
				t.Fatal("corruption changed retirement history")
			}
		})
	}
}

type dockerVolumeFixture struct {
	root       string
	manifest   managedManifest
	journal    *managedJournal
	generation uint64
	deadline   time.Time
}

func newDockerVolumeFixture(t *testing.T) dockerVolumeFixture {
	t.Helper()
	root, spec, reservation := managedAdmissionFixture(t)
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
	return dockerVolumeFixture{root, manifest, journal, generation, time.Now().UTC().Add(30 * time.Second)}
}

func (f dockerVolumeFixture) reserve(t *testing.T) managedDockerVolumeIntent {
	t.Helper()
	intent, err := reserveManagedDockerVolume(t.Context(), f.manifest, f.journal, f.generation, f.deadline, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	return intent
}

func dockerVolumeCounts(t *testing.T, j *managedJournal) [4]int64 {
	t.Helper()
	var counts [4]int64
	for index, query := range []string{"SELECT count(*) FROM managed_reservations WHERE kind='host'", "SELECT count(*) FROM managed_scopes", "SELECT count(*) FROM managed_docker_intents", "SELECT count(*) FROM managed_events"} {
		if err := j.conn.QueryRowContext(t.Context(), query).Scan(&counts[index]); err != nil {
			t.Fatal(err)
		}
	}
	return counts
}

func testDockerVolumeLifecycle(t *testing.T) {
	f := newDockerVolumeFixture(t)
	before := dockerVolumeCounts(t, f.journal)
	intent := f.reserve(t)
	after := dockerVolumeCounts(t, f.journal)
	for index, delta := range [4]int64{1, 2, 1, 1} {
		if after[index]-before[index] != delta {
			t.Fatalf("atomic counts %v -> %v", before, after)
		}
	}
	if intent.Phase != "prepared" || intent.Version != 1 || intent.Volume.Generation != f.generation || intent.CreateHost.Generation != f.generation || !intent.CreateDeadline.Equal(f.deadline) || !intent.WorkDeadline.Equal(f.manifest.Deadline) {
		t.Fatalf("wrong intent: %+v", intent)
	}
	a := f.journal.admission
	objective := cleanupObjectiveHash(a)
	if intent.Volume.Name != "oro-factory-"+objective+"-volume-"+intent.Volume.ID+"-"+intent.Volume.Nonce || intent.CreateHost.Name != "dev.getoro.factory."+objective+".host."+intent.CreateHost.ID+"."+intent.CreateHost.Nonce {
		t.Fatal("unbound scope name")
	}
	if intent.Binding.VolumeOptions["o"] != fmt.Sprintf("size=%d,nr_inodes=%d,uid=1000,gid=1000", a.Runtime.SourceBytes, a.Runtime.SourceInodes) || len(intent.Binding.Labels) != 6 || intent.Binding.CreatedAt != "" {
		t.Fatal("wrong source options or ownership")
	}
	if err := validateCleanupBinding(a, DockerCleanupPermit{Target: intent.Volume, Operation: DockerCleanupInspect, Binding: intent.Binding}); err != nil {
		t.Fatal(err)
	}
	reopened := openJournalFixture(t, f.root, a.ObjectiveID)
	loaded, err := loadManagedDockerVolume(t.Context(), reopened, intent.Volume)
	if err != nil || !reflect.DeepEqual(loaded, intent) {
		t.Fatalf("reopen changed intent: %+v %v", loaded, err)
	}
	loaded.Binding.Labels["dev.getoro.factory.kind"] = "changed"
	loaded, err = loadManagedDockerVolume(t.Context(), reopened, intent.Volume)
	if err != nil || !reflect.DeepEqual(loaded, intent) {
		t.Fatal("returned map changed durable intent")
	}
	if err := acknowledgeManagedDockerVolume(t.Context(), reopened, intent.Volume, intent.CreateHost); err == nil {
		t.Fatal("acknowledged before dispatch")
	}
	dispatched, err := dispatchManagedDockerVolume(t.Context(), reopened, intent.Volume, intent.RuntimeConfigSHA256)
	if err != nil || dispatched.Phase != "dispatching" {
		t.Fatalf("dispatch %+v %v", dispatched, err)
	}
	if _, err := dispatchManagedDockerVolume(t.Context(), f.journal, intent.Volume, intent.RuntimeConfigSHA256); err == nil {
		t.Fatal("redispatched")
	}
	if err := acknowledgeManagedDockerVolume(t.Context(), reopened, intent.Volume, intent.CreateHost); err == nil {
		t.Fatal("acknowledged unretired host")
	}
	if err := reopened.MarkQuiescent(t.Context(), intent.CreateHost); err != nil {
		t.Fatal(err)
	}
	if err := acknowledgeManagedDockerVolume(t.Context(), reopened, intent.Volume, intent.CreateHost); err != nil {
		t.Fatal(err)
	}
	observed := intent.Binding
	observed.CreatedAt = "2026-10-24T05:49:04Z"
	if err := bindManagedDockerVolume(t.Context(), reopened, f.generation, intent.Volume, observed); err != nil {
		t.Fatal(err)
	}
	before = dockerVolumeCounts(t, reopened)
	if err := acknowledgeManagedDockerVolume(t.Context(), reopened, intent.Volume, intent.CreateHost); err != nil {
		t.Fatal(err)
	}
	if err := bindManagedDockerVolume(t.Context(), reopened, f.generation, intent.Volume, observed); err != nil {
		t.Fatal(err)
	}
	if after = dockerVolumeCounts(t, reopened); after != before {
		t.Fatal("idempotent replay spent budget")
	}
	observed.CreatedAt = "2026-10-24T05:49:05Z"
	if err := bindManagedDockerVolume(t.Context(), reopened, f.generation, intent.Volume, observed); err == nil {
		t.Fatal("replaced lifetime")
	}
	loaded, err = loadManagedDockerVolume(t.Context(), reopened, intent.Volume)
	if err != nil || loaded.Phase != "acknowledged" || loaded.Binding.CreatedAt != "2026-10-24T05:49:04Z" {
		t.Fatalf("final %+v %v", loaded, err)
	}
}

func testDockerVolumeRollback(t *testing.T) {
	for _, mode := range []string{"last insert", "one scope left", "source versions", "host tokens", "event ceiling"} {
		t.Run(mode, func(t *testing.T) {
			f := newDockerVolumeFixture(t)
			switch mode {
			case "last insert":
				dockerVolumeSQL(t, f.journal, "CREATE TRIGGER reject_intent BEFORE INSERT ON managed_docker_intents BEGIN SELECT RAISE(ABORT,'injected final insert failure'); END")
			case "one scope left":
				seedDockerVolumeScopeLimit(t, f)
			case "source versions":
				limits, err := ManagedHostRetention(f.manifest.Spec)
				if err != nil {
					t.Fatal(err)
				}
				for i := int64(0); i < limits.SourceVersions; i++ {
					dockerVolumeSQL(t, f.journal, "INSERT INTO managed_docker_intents(volume_scope_id,create_scope_id,body) VALUES(?,?, '{}')", fmt.Sprint(i), fmt.Sprint(i))
				}
			case "host tokens":
				for i := 0; i < f.manifest.Spec.Runtime.MaxHostInvocations; i++ {
					dockerVolumeSQL(t, f.journal, "INSERT INTO managed_reservations(kind,token,generation) VALUES('host',?,1)", fmt.Sprint(i))
				}
			case "event ceiling":
				state, err := f.journal.supervision(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				limit, err := journalRecordLimit(f.journal.admission)
				if err != nil {
					t.Fatal(err)
				}
				state.Revision = 8*limit + 16
				body, err := json.Marshal(state)
				if err != nil {
					t.Fatal(err)
				}
				dockerVolumeSQL(t, f.journal, "UPDATE managed_supervision SET revision=?,body=?", state.Revision, body)
			}
			before := dockerVolumeCounts(t, f.journal)
			beforeState := snapshotDockerVolumeAuthority(t, f.journal)
			got, err := reserveManagedDockerVolume(t.Context(), f.manifest, f.journal, f.generation, f.deadline, strings.Repeat("a", 64))
			if err == nil || !reflect.DeepEqual(got, managedDockerVolumeIntent{}) {
				t.Fatalf("invalid reservation %+v %v", got, err)
			}
			wantError := map[string]string{
				"one scope left":  "managed scope budget exhausted",
				"source versions": "managed Docker source version budget exhausted",
				"host tokens":     "managed host budget exhausted",
				"event ceiling":   "managed event budget exhausted",
			}[mode]
			if wantError != "" && err.Error() != wantError {
				t.Fatalf("%s failed before its intended budget: got %q, want %q", mode, err, wantError)
			}
			if after := dockerVolumeCounts(t, f.journal); before != after {
				t.Fatalf("partial reservation %v -> %v", before, after)
			}
			if snapshotDockerVolumeAuthority(t, f.journal) != beforeState {
				t.Fatal("rejected reservation changed exact authority state")
			}
		})
	}
}

func seedDockerVolumeScopeLimit(t *testing.T, f dockerVolumeFixture) {
	t.Helper()
	limit, err := journalRecordLimit(f.journal.admission)
	if err != nil {
		t.Fatal(err)
	}
	var scopes []ScopeHandle
	// Batch only fixture construction through the real canonical encoder and
	// scope limit, avoiding quadratic admission scans before the test begins.
	if err := f.journal.mutate(t.Context(), "test-scope-cap", "fixture", func(tx *sql.Tx, _ *managedSupervision) (bool, error) {
		for range limit - 1 {
			scope := newManagedDockerVolumeIntent(f.journal, f.generation, f.deadline, strings.Repeat("a", 64)).Volume
			if _, err := f.journal.insertScope(t.Context(), tx, scope, f.deadline); err != nil {
				return false, err
			}
			scopes = append(scopes, scope)
		}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, scope := range scopes {
		if err := f.journal.MarkQuiescent(t.Context(), scope); err != nil {
			t.Fatal(err)
		}
	}
	records, err := f.journal.scopes(t.Context())
	if err != nil || int64(len(records)) != limit-1 {
		t.Fatalf("fixture must leave exactly one valid scope slot: count=%d limit=%d error=%v", len(records), limit, err)
	}
	for _, record := range records {
		if record.Phase != "quiescent" || record.CleanupEvidence == "" {
			t.Fatal("capacity fixture lacks positive scope retirement")
		}
	}
}

func seedDockerVolumeLiveCleanup(t *testing.T, f dockerVolumeFixture) {
	t.Helper()
	intent := f.reserve(t)
	if err := f.journal.MarkQuiescent(t.Context(), intent.CreateHost); err != nil {
		t.Fatal(err)
	}
	if _, err := reserveManagedCleanupInvocation(t.Context(), f.journal, f.generation, intent.Volume, DockerCleanupInspect, intent.Binding, f.journal.admission.HardStop); err != nil {
		t.Fatal(err)
	}
}

func snapshotDockerVolumeAuthority(t *testing.T, j *managedJournal) string {
	t.Helper()
	var intents string
	if err := j.conn.QueryRowContext(t.Context(), "SELECT coalesce(group_concat(volume_scope_id||':'||create_scope_id||':'||hex(body),'|'),'') FROM (SELECT * FROM managed_docker_intents ORDER BY volume_scope_id)").Scan(&intents); err != nil {
		t.Fatal(err)
	}
	return snapshotCleanupIntegrity(t, j) + "\n" + intents
}

func dockerVolumeSQL(t *testing.T, j *managedJournal, query string, args ...any) {
	t.Helper()
	if _, err := j.conn.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func testDockerVolumeDispatchRace(t *testing.T) {
	f := newDockerVolumeFixture(t)
	intent := f.reserve(t)
	other := openJournalFixture(t, f.root, f.manifest.Spec.ID)
	var wg sync.WaitGroup
	out := make(chan error, 2)
	for _, j := range []*managedJournal{f.journal, other} {
		wg.Go(func() {
			_, err := dispatchManagedDockerVolume(t.Context(), j, intent.Volume, intent.RuntimeConfigSHA256)
			out <- err
		})
	}
	wg.Wait()
	close(out)
	wins := 0
	for err := range out {
		if err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("dispatch winners=%d", wins)
	}
}

func testDockerVolumeRecovery(t *testing.T) {
	for _, dispatched := range []bool{false, true} {
		t.Run(fmt.Sprint(dispatched), func(t *testing.T) {
			f := newDockerVolumeFixture(t)
			intent := f.reserve(t)
			extra, err := reserveManagedHostInvocation(t.Context(), f.journal, f.generation, f.deadline)
			if err != nil {
				t.Fatal(err)
			}
			if dispatched {
				if _, err := dispatchManagedDockerVolume(t.Context(), f.journal, intent.Volume, intent.RuntimeConfigSHA256); err != nil {
					t.Fatal(err)
				}
			}
			if err := retirePreparedManagedDockerVolume(t.Context(), f.journal, f.generation, intent.Volume); err == nil {
				t.Fatal("current owner retired prepared")
			}
			generation, _, err := f.journal.advanceEpoch(t.Context(), "supervisor", "recovery", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if err := retirePreparedManagedDockerVolume(t.Context(), f.journal, generation, intent.Volume); err == nil {
				t.Fatal("retired with old host live")
			}
			if err := f.journal.MarkQuiescent(t.Context(), intent.CreateHost); err != nil {
				t.Fatal(err)
			}
			if err := retirePreparedManagedDockerVolume(t.Context(), f.journal, generation, intent.Volume); err == nil {
				t.Fatal("ignored another old host")
			}
			if err := f.journal.MarkQuiescent(t.Context(), extra); err != nil {
				t.Fatal(err)
			}
			before := dockerVolumeCounts(t, f.journal)
			err = retirePreparedManagedDockerVolume(t.Context(), f.journal, generation, intent.Volume)
			if dispatched {
				if err == nil {
					t.Fatal("dispatch uncertainty became quiescence")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			after := dockerVolumeCounts(t, f.journal)
			if after[0] != before[0] || after[1] != before[1] || after[2] != before[2] || after[3] != before[3]+1 {
				t.Fatal("retirement refunded capacity")
			}
			observation, err := f.journal.Observe(t.Context(), intent.Volume)
			if err != nil || observation.Phase != "quiescent" {
				t.Fatalf("retirement %+v %v", observation, err)
			}
			if _, err := dispatchManagedDockerVolume(t.Context(), f.journal, intent.Volume, intent.RuntimeConfigSHA256); err == nil {
				t.Fatal("old owner dispatched after proof")
			}
		})
	}
}

func testDockerVolumeAdmissionReplay(t *testing.T) {
	for _, used := range []bool{false, true} {
		t.Run(fmt.Sprint(used), func(t *testing.T) {
			f := newDockerVolumeFixture(t)
			if used {
				intent := f.reserve(t)
				if _, err := dispatchManagedDockerVolume(t.Context(), f.journal, intent.Volume, intent.RuntimeConfigSHA256); err != nil {
					t.Fatal(err)
				}
			}
			dockerVolumeSQL(t, f.journal, "DROP TABLE managed_docker_intents")
			before := dockerVolumeSnapshot(t, f.journal)
			for range 2 {
				if _, err := AdmitManaged(t.Context(), f.root, f.manifest.Spec, f.manifest.Reservation); err != nil {
					t.Fatal(err)
				}
			}
			if after := dockerVolumeSnapshot(t, f.journal); after != before {
				t.Fatal("admission replay changed durable identity or history")
			}
			var count int
			if err := f.journal.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_master WHERE name='managed_docker_intents'").Scan(&count); err != nil || count != 0 {
				t.Fatal("admission replay recreated absent history", err)
			}
			if _, err := reserveManagedDockerVolume(t.Context(), f.manifest, f.journal, f.generation, f.deadline, strings.Repeat("a", 64)); err == nil {
				t.Fatal("missing feature storage granted fresh capacity")
			}
		})
	}
	t.Run("failed schema query denies DDL", func(t *testing.T) {
		_, _, store := journalFixture(t)
		if _, err := store.conn.ExecContext(t.Context(), "DROP TABLE managed_docker_intents; DROP TABLE managed_admissions"); err != nil {
			t.Fatal(err)
		}
		if err := initManagedDockerVolumeSchema(t.Context(), store.conn); err == nil {
			t.Fatal("missing admission table accepted")
		}
		var count int
		if err := store.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_master WHERE name='managed_docker_intents'").Scan(&count); err != nil || count != 0 {
			t.Fatal("failed query created feature table", err)
		}
	})
}

func dockerVolumeSnapshot(t *testing.T, j *managedJournal) string {
	t.Helper()
	var result strings.Builder
	for _, table := range []string{"managed_admissions", "managed_supervision", "managed_reservations", "managed_scopes", "managed_events", "snapshots"} {
		rows, err := j.conn.QueryContext(t.Context(), "SELECT * FROM "+table+" ORDER BY 1")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintln(&result, values)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return result.String()
}

func testDockerVolumeCorruption(t *testing.T) {
	for _, mode := range []string{"noncanonical", "version", "admission", "config", "nonce", "host", "deadline", "binding", "columns", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			f := newDockerVolumeFixture(t)
			intent := f.reserve(t)
			bad := intent
			switch mode {
			case "version":
				bad.Version = 2
			case "admission":
				bad.AdmissionSHA256 = strings.Repeat("b", 64)
			case "config":
				bad.RuntimeConfigSHA256 = "bad"
			case "nonce":
				bad.Volume.Nonce = strings.Repeat("b", 32)
			case "host":
				bad.CreateHost.Name = "other"
			case "deadline":
				bad.CreateDeadline = bad.WorkDeadline.Add(time.Second)
			case "binding":
				bad.Binding.VolumeOptions["o"] = "size=1"
			}
			body, err := json.Marshal(bad)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "noncanonical" {
				body = append(body, '\n')
			}
			if mode == "columns" {
				dockerVolumeSQL(t, f.journal, "UPDATE managed_scopes SET name='different' WHERE id=?", intent.Volume.ID)
			}
			if mode == "oversize" {
				body = []byte(strings.Repeat(" ", 16385))
				if _, err := f.journal.conn.ExecContext(t.Context(), "UPDATE managed_docker_intents SET body=?", body); err == nil {
					t.Fatal("oversized intent stored")
				}
				return
			}
			dockerVolumeSQL(t, f.journal, "UPDATE managed_docker_intents SET body=?", body)
			before := dockerVolumeCounts(t, f.journal)
			if got, err := loadManagedDockerVolume(t.Context(), f.journal, intent.Volume); err == nil || !reflect.DeepEqual(got, managedDockerVolumeIntent{}) {
				t.Fatalf("corrupt load %+v %v", got, err)
			}
			if _, err := dispatchManagedDockerVolume(t.Context(), f.journal, intent.Volume, intent.RuntimeConfigSHA256); err == nil {
				t.Fatal("corrupt dispatch accepted")
			}
			if after := dockerVolumeCounts(t, f.journal); after != before {
				t.Fatal("corrupt read changed state")
			}
		})
	}
}

func testDockerVolumeAuthority(t *testing.T) {
	for _, mode := range []string{"canceled", "expired", "future", "stale", "stopping", "stopped", "manifest", "digest", "live cleanup"} {
		t.Run(mode, func(t *testing.T) {
			f := newDockerVolumeFixture(t)
			ctx := t.Context()
			generation := f.generation
			deadline := f.deadline
			digest := strings.Repeat("a", 64)
			switch mode {
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "expired":
				deadline = time.Now().Add(-time.Second)
			case "future":
				deadline = f.manifest.Deadline.Add(time.Second)
			case "stale":
				generation++
			case "stopping", "stopped":
				if err := f.journal.setDesired(ctx, generation, "stopping", "test", time.Time{}); err != nil {
					t.Fatal(err)
				}
				if mode == "stopped" {
					if err := f.journal.setDesired(ctx, generation, "stopped", "test", time.Time{}); err != nil {
						t.Fatal(err)
					}
				}
			case "manifest":
				f.manifest.Spec.Objective = "different"
			case "digest":
				digest = "bad"
			case "live cleanup":
				seedDockerVolumeLiveCleanup(t, f)
			}
			before := dockerVolumeCounts(t, f.journal)
			beforeState := snapshotDockerVolumeAuthority(t, f.journal)
			if _, err := reserveManagedDockerVolume(ctx, f.manifest, f.journal, generation, deadline, digest); err == nil {
				t.Fatal("inactive authority accepted")
			} else if mode == "live cleanup" && err.Error() != "ordinary work requires positive cleanup-helper retirement" {
				t.Fatalf("live cleanup failed before its exclusion gate: %v", err)
			}
			if after := dockerVolumeCounts(t, f.journal); after != before {
				t.Fatal("denial spent resources")
			}
			if snapshotDockerVolumeAuthority(t, f.journal) != beforeState {
				t.Fatal("denial changed exact authority state")
			}
		})
	}
	f := newDockerVolumeFixture(t)
	var body []byte
	if err := f.journal.conn.QueryRowContext(t.Context(), "SELECT body FROM managed_docker_intents").Scan(&body); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("fresh table: %v", err)
	}
}

func testDockerVolumeMissingAdmission(t *testing.T) {
	for _, mode := range []string{"missing row", "missing table", "query corrupt"} {
		t.Run(mode, func(t *testing.T) {
			f := newDockerVolumeFixture(t)
			_ = f.reserve(t)
			dockerVolumeSQL(t, f.journal, "DROP TABLE managed_docker_intents")
			switch mode {
			case "missing row":
				dockerVolumeSQL(t, f.journal, "DELETE FROM managed_admissions")
			case "missing table":
				dockerVolumeSQL(t, f.journal, "DROP TABLE managed_admissions")
			case "query corrupt":
				dockerVolumeSQL(t, f.journal, "DROP TABLE managed_admissions; CREATE VIEW managed_admissions AS SELECT * FROM nonexistent_authority")
			}
			if _, err := AdmitManaged(t.Context(), f.root, f.manifest.Spec, f.manifest.Reservation); err == nil {
				t.Fatal("missing or corrupt admission replay succeeded")
			}
			var count int
			if err := f.journal.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_master WHERE name='managed_docker_intents'").Scan(&count); err != nil || count != 0 {
				t.Fatal("damaged admission recreated feature history", err)
			}
		})
	}
}

type dockerVolumeProcessRequest struct {
	Root, ID   string
	Dispatched bool
}

func dockerVolumeProcessChild(t *testing.T) {
	t.Helper()
	var request dockerVolumeProcessRequest
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		t.Fatal(err)
	}
	manifest, err := loadManagedManifest(t.Context(), request.Root, request.ID)
	if err != nil {
		t.Fatal(err)
	}
	journal := openJournalFixture(t, request.Root, request.ID)
	intent, err := reserveManagedDockerVolume(t.Context(), manifest, journal, 1, time.Now().UTC().Add(20*time.Second), strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if request.Dispatched {
		intent, err = dispatchManagedDockerVolume(t.Context(), journal, intent.Volume, intent.RuntimeConfigSHA256)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(intent); err != nil {
		t.Fatal(err)
	}
	<-t.Context().Done()
}

func testDockerVolumeProcessDeath(t *testing.T) {
	for _, dispatched := range []bool{false, true} {
		t.Run(fmt.Sprint(dispatched), func(t *testing.T) {
			f := newDockerVolumeFixture(t)
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			request, err := json.Marshal(dockerVolumeProcessRequest{f.root, f.manifest.Spec.ID, dispatched})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestManagedDockerVolumeIntent$", "-test.timeout=30s")
			cmd.Env = append(os.Environ(), "ORO_DOCKER_INTENT_PROCESS=1")
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
			var intent managedDockerVolumeIntent
			if err := json.NewDecoder(output).Decode(&intent); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			var exit *exec.ExitError
			if err := cmd.Wait(); !errors.As(err, &exit) {
				t.Fatalf("owned child did not die: %v", err)
			}
			reopened := openJournalFixture(t, f.root, f.manifest.Spec.ID)
			got, err := loadManagedDockerVolume(t.Context(), reopened, intent.Volume)
			if err != nil || !reflect.DeepEqual(got, intent) {
				t.Fatalf("crash changed intent %+v %v", got, err)
			}
			before := dockerVolumeCounts(t, reopened)
			got, err = dispatchManagedDockerVolume(t.Context(), reopened, intent.Volume, intent.RuntimeConfigSHA256)
			if dispatched {
				if err == nil {
					t.Fatal("crashed dispatch was reused")
				}
			} else if err != nil || got.CreateHost != intent.CreateHost || !got.CreateDeadline.Equal(intent.CreateDeadline) {
				t.Fatalf("prepared v1 unusable: %+v %v", got, err)
			}
			after := dockerVolumeCounts(t, reopened)
			if after[0] != before[0] || after[1] != before[1] || after[2] != before[2] {
				t.Fatal("reopen reserved new capacity")
			}
		})
	}
}
