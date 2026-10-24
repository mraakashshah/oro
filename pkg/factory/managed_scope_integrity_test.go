package factory //nolint:testpackage // Exercise persisted authority corruption through the journal API.

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestManagedScopeIntegrity(t *testing.T) {
	t.Run("body cannot override authenticated lifecycle", testScopeIntegrityLifecycle)
	t.Run("body cannot grant reserved scope", testScopeIntegrityFalseGrant)
	t.Run("body cannot reopen consumed cleanup permit", testScopeIntegrityCleanup)
	t.Run("canonical body and redundant columns required", testScopeIntegrityCorruption)
	t.Run("original current and old epoch semantics", testScopeIntegrityValid)
}

func scopeIntegrityFixture(t *testing.T) (*managedJournal, ScopeHandle) {
	t.Helper()
	root, admission, store := journalFixture(t)
	insertJournalFixture(t, store, admission)
	j := openJournalFixture(t, root, admission.ObjectiveID)
	generation, _, err := j.beginGeneration(t.Context(), "integrity", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	scope := journalScope("a", generation)
	if _, err := j.reserveBudget(t.Context(), generation, "host", scope.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := j.reserveScope(t.Context(), scope, time.Now().UTC().Add(time.Minute), time.Now()); err != nil {
		t.Fatal(err)
	}
	return j, scope
}

func testScopeIntegrityLifecycle(t *testing.T) {
	t.Helper()
	j, scope := scopeIntegrityFixture(t)
	if err := j.RecordHello(t.Context(), scope, 101, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := j.conn.ExecContext(t.Context(), `UPDATE managed_scopes SET body=json_set(body,'$.Phase','quiescent','$.PID',0,'$.PGID',0) WHERE id=?`, scope.ID); err != nil {
		t.Fatal(err)
	}
	assertScopeIntegrityDenied(t, j, scope)
}

func testScopeIntegrityFalseGrant(t *testing.T) {
	t.Helper()
	j, scope := scopeIntegrityFixture(t)
	if _, err := j.conn.ExecContext(t.Context(), `UPDATE managed_scopes SET body=json_set(body,'$.Phase','granted','$.PID',101,'$.PGID',100) WHERE id=?`, scope.ID); err != nil {
		t.Fatal(err)
	}
	assertScopeIntegrityDenied(t, j, scope)
}

func testScopeIntegrityCleanup(t *testing.T) {
	t.Helper()
	f := newCleanupFixture(t)
	scope, err := reserveManagedCleanupInvocation(t.Context(), f.j, f.target.Generation, f.target, DockerCleanupStop, f.binding, f.a.HardStop)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.j.RecordHello(t.Context(), scope, 101, 100); err != nil {
		t.Fatal(err)
	}
	if err := f.j.MarkQuiescent(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	if _, err := f.j.conn.ExecContext(t.Context(), `UPDATE managed_scopes SET body=json_set(body,'$.Phase','reserved','$.PID',0,'$.PGID',0) WHERE id=?`, scope.ID); err != nil {
		t.Fatal(err)
	}
	before := snapshotScopeIntegrity(t, f.j)
	if permit, err := f.j.CleanupPermit(t.Context(), scope); err == nil || !reflect.DeepEqual(permit, DockerCleanupPermit{}) {
		t.Errorf("consumed cleanup permit reopened from body: %+v %v", permit, err)
	}
	assertScopeIntegrityDenied(t, f.j, scope)
	if after := snapshotScopeIntegrity(t, f.j); after != before {
		t.Error("cleanup denial mutated persisted authority")
	}
}

func assertScopeIntegrityDenied(t *testing.T, j *managedJournal, scope ScopeHandle) {
	t.Helper()
	before := snapshotScopeIntegrity(t, j)
	if got, err := j.Observe(t.Context(), scope); err == nil || got != (ScopeObservation{}) {
		t.Errorf("malformed scope observed authority: %+v %v", got, err)
	}
	if got, err := j.Granted(t.Context(), scope); err == nil || got {
		t.Errorf("malformed scope grant=%v error=%v", got, err)
	}
	if got, err := j.scopes(t.Context()); err == nil || got != nil {
		t.Errorf("malformed listing returned scopes: %+v %v", got, err)
	}
	if err := j.RecordHello(t.Context(), scope, 101, 100); err == nil {
		t.Error("malformed scope accepted HELLO")
	}
	if err := j.MarkQuiescent(t.Context(), scope); err == nil {
		t.Error("malformed scope accepted quiescence")
	}
	if after := snapshotScopeIntegrity(t, j); after != before {
		t.Error("rejected authority changed rows, bytes, revision, events or reservations")
	}
}

func snapshotScopeIntegrity(t *testing.T, j *managedJournal) string {
	t.Helper()
	var state, scopes, events, reservations string
	queries := []struct {
		query  string
		target *string
	}{
		{"SELECT revision||':'||hex(body) FROM managed_supervision WHERE id=1", &state},
		{"SELECT coalesce(group_concat(id||':'||nonce||':'||backend||':'||name||':'||hex(body)||':'||phase||':'||pid||':'||pgid||':'||hex(cleanup),'|'),'') FROM (SELECT * FROM managed_scopes ORDER BY id)", &scopes},
		{"SELECT coalesce(group_concat(revision||':'||kind||':'||ref||':'||hex(body),'|'),'') FROM (SELECT * FROM managed_events ORDER BY revision)", &events},
		{"SELECT coalesce(group_concat(kind||':'||token||':'||generation,'|'),'') FROM (SELECT * FROM managed_reservations ORDER BY kind,token)", &reservations},
	}
	for _, query := range queries {
		if err := j.conn.QueryRowContext(t.Context(), query.query).Scan(query.target); err != nil {
			t.Fatal(err)
		}
	}
	return state + "\n" + scopes + "\n" + events + "\n" + reservations
}

func testScopeIntegrityCorruption(t *testing.T) {
	t.Helper()
	for _, kind := range []string{"unknown", "duplicate", "case alias", "whitespace", "trailing", "malformed", "oversized", "missing deadline", "zero deadline", "offset deadline", "year zero", "invalid handle", "id", "nonce", "backend", "name", "phase", "negative pid", "partial peer", "reserved peer", "granted missing peer", "quiet missing evidence", "oversized cleanup"} {
		t.Run(kind, func(t *testing.T) {
			j, scope := scopeIntegrityFixture(t)
			corruptScopeIntegrity(t, j, scope, kind)
			assertScopeIntegrityDenied(t, j, scope)
		})
	}
}

func corruptScopeIntegrity(t *testing.T, j *managedJournal, scope ScopeHandle, kind string) {
	t.Helper()
	var body []byte
	if err := j.conn.QueryRowContext(t.Context(), "SELECT body FROM managed_scopes WHERE id=?", scope.ID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	query := "UPDATE managed_scopes SET body=? WHERE id=?"
	var value any
	switch kind {
	case "unknown":
		value = append(append([]byte(nil), body[:len(body)-1]...), []byte(`,"unknown":true}`)...)
	case "duplicate":
		value = append(append([]byte(nil), body[:len(body)-1]...), []byte(`,"id":"`+scope.ID+`"}`)...)
	case "case alias":
		value = bytes.Replace(body, []byte(`"Deadline"`), []byte(`"deadline"`), 1)
	case "whitespace":
		value = append(body, ' ')
	case "trailing":
		value = append(body, []byte(`{}`)...)
	case "malformed":
		value = []byte(`{`)
	case "oversized":
		if _, err := j.conn.ExecContext(t.Context(), "PRAGMA ignore_check_constraints=ON"); err != nil {
			t.Fatal(err)
		}
		value = append(body, bytes.Repeat([]byte(" "), 2049)...)
	case "missing deadline", "zero deadline", "offset deadline", "year zero", "invalid handle":
		value = invalidScopeIdentity(t, body, kind)
	case "id", "nonce", "backend", "name":
		query = "UPDATE managed_scopes SET " + kind + "=? WHERE id=?"
		value = strings.Repeat("b", 32)
	case "phase":
		if _, err := j.conn.ExecContext(t.Context(), "PRAGMA ignore_check_constraints=ON"); err != nil {
			t.Fatal(err)
		}
		query, value = "UPDATE managed_scopes SET phase=? WHERE id=?", "unknown"
	case "negative pid":
		query, value = "UPDATE managed_scopes SET pid=? WHERE id=?", -1
	case "partial peer":
		query, value = "UPDATE managed_scopes SET phase='revoked',pid=? WHERE id=?", 101
	case "reserved peer":
		query, value = "UPDATE managed_scopes SET pid=?,pgid=100 WHERE id=?", 101
	case "granted missing peer":
		query, value = "UPDATE managed_scopes SET phase=? WHERE id=?", "granted"
	case "quiet missing evidence":
		query, value = "UPDATE managed_scopes SET phase=? WHERE id=?", "quiescent"
	case "oversized cleanup":
		if _, err := j.conn.ExecContext(t.Context(), "PRAGMA ignore_check_constraints=ON"); err != nil {
			t.Fatal(err)
		}
		query, value = "UPDATE managed_scopes SET cleanup=? WHERE id=?", strings.Repeat("x", 257)
	}
	if _, err := j.conn.ExecContext(t.Context(), query, value, scope.ID); err != nil {
		t.Fatal(err)
	}
}

func invalidScopeIdentity(t *testing.T, body []byte, kind string) []byte {
	t.Helper()
	var identity struct {
		ScopeHandle
		Deadline time.Time
	}
	if err := json.Unmarshal(body, &identity); err != nil {
		t.Fatal(err)
	}
	switch kind {
	case "missing deadline":
		data, err := json.Marshal(identity.ScopeHandle)
		if err != nil {
			t.Fatal(err)
		}
		return data
	case "zero deadline":
		identity.Deadline = time.Time{}
	case "offset deadline":
		identity.Deadline = identity.Deadline.In(time.FixedZone("offset", 3600))
	case "year zero":
		identity.Deadline = time.Date(0, time.January, 2, 0, 0, 0, 0, time.UTC)
	case "invalid handle":
		identity.Kind = ""
	}
	// Keep the original canonical field order to reach semantic validation,
	// rather than failing earlier just because a map reordered the JSON keys.
	data, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func testScopeIntegrityValid(t *testing.T) {
	t.Helper()
	j, scope := scopeIntegrityFixture(t)
	initial, err := j.Observe(t.Context(), scope)
	if err != nil || initial.Phase != "reserved" || initial.PID != 0 || initial.PGID != 0 {
		t.Fatalf("valid reserved scope: %+v %v", initial, err)
	}
	if err := j.RecordHello(t.Context(), scope, 101, 100); err != nil {
		t.Fatal(err)
	}
	if granted, err := j.Granted(t.Context(), scope); err != nil || !granted {
		t.Fatalf("valid unequal authenticated peers denied: %v %v", granted, err)
	}
	if _, _, err := j.beginGeneration(t.Context(), "successor", time.Now()); err != nil {
		t.Fatal(err)
	}
	old, err := j.Observe(t.Context(), scope)
	if err != nil || old.Phase != "revoked" || old.PID != 101 || old.PGID != 100 || !old.Deadline.Equal(initial.Deadline) {
		t.Fatalf("valid old epoch lost ownership: %+v %v", old, err)
	}
	if err := j.MarkQuiescent(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	before := snapshotScopeIntegrity(t, j)
	if err := j.MarkQuiescent(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	if snapshotScopeIntegrity(t, j) != before {
		t.Fatal("valid quiescence replay mutated state")
	}
	records, err := j.scopes(t.Context())
	if err != nil || len(records) != 1 || records[0].Phase != "quiescent" || records[0].PID != 101 || records[0].PGID != 100 || records[0].CleanupEvidence == "" {
		t.Fatalf("valid canonical listing: %+v %v", records, err)
	}
	assertScopeIntegrityZeroPeer(t, j, scope.Generation+1)
}

func assertScopeIntegrityZeroPeer(t *testing.T, j *managedJournal, generation uint64) {
	t.Helper()
	scope := journalScope("b", generation)
	if _, err := j.reserveScope(t.Context(), scope, time.Now().UTC().Add(time.Minute), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := j.MarkQuiescent(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	observation, err := j.Observe(t.Context(), scope)
	if err != nil || observation.Phase != "quiescent" || observation.PID != 0 || observation.PGID != 0 {
		t.Fatalf("unstarted scope lost positive retirement: %+v %v", observation, err)
	}
}
