package factory //nolint:testpackage // Exercise journal cleanup authority and its transactional lifecycle.

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestManagedCleanupAuthority(t *testing.T) {
	t.Run("final stop requires stopping and positive retirement", testManagedCleanupSeal)
	t.Run("keeper creation and grants stay within work deadline", testManagedCleanupKeeper)
	t.Run("cleanup epochs remain inside counted supervision", testManagedCleanupEpochs)
	t.Run("late exact permits survive reopen without granting", testManagedCleanupPermit)
	t.Run("invalid bindings have no authority effects", testManagedCleanupInvalid)
	t.Run("stopping recovery fences grants and spends fresh C", testManagedCleanupRecovery)
	t.Run("permit failure rolls back token scope and revision", testManagedCleanupRollback)
	t.Run("corrupt permits never grant execution", testManagedCleanupCorruption)
	t.Run("resource kinds and unknown acknowledgment inspection", testManagedCleanupKinds)
	t.Run("cleanup reservation races final sealing", testManagedCleanupSealRace)
	t.Run("cleanup reservation survives creator process exit", testManagedCleanupProcess)
	t.Run("stopping observer remains separately counted", testManagedCleanupObserver)
	t.Run("running work and cleanup serialize in both directions", testManagedCleanupWorkGate)
	t.Run("concurrent work and cleanup cannot both reserve scopes", testManagedCleanupAdmissionRace)
}

type cleanupFixture struct {
	root    string
	j       *managedJournal
	a       managedJournalAdmission
	target  ScopeHandle
	binding DockerCleanupBinding
}

func newCleanupFixture(t *testing.T) cleanupFixture {
	t.Helper()
	return newCleanupKindFixture(t, "container")
}

func newCleanupKindFixture(t *testing.T, kind string) cleanupFixture {
	t.Helper()
	return newCleanupFixtureState(t, kind, true)
}

func newCleanupFixtureState(t *testing.T, kind string, late bool) cleanupFixture {
	t.Helper()
	root, a, store := journalFixture(t)
	if late {
		a.Deadline = time.Now().UTC().Add(2 * time.Second)
		a.HardStop = a.Deadline.Add(25 * time.Second)
	}
	insertJournalFixture(t, store, a)
	j := openJournalFixture(t, root, a.ObjectiveID)
	generation, _, err := j.beginGeneration(t.Context(), "supervisor", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	target := journalScope("a", generation)
	target.Kind, target.Backend = kind, "docker"
	digest := sha256.Sum256([]byte(root + "\x00" + a.ObjectiveID))
	objective := hex.EncodeToString(digest[:8])
	target.Name = "oro-factory-" + objective + "-" + kind + "-" + target.ID + "-" + target.Nonce
	if _, err := j.reserveScope(t.Context(), target, a.Deadline, time.Now()); err != nil {
		t.Fatal(err)
	}
	if late {
		timer := time.NewTimer(time.Until(a.Deadline) + 20*time.Millisecond)
		defer timer.Stop()
		<-timer.C
		if err := j.setDesired(t.Context(), generation, "stopping", "test cleanup", time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	binding := DockerCleanupBinding{
		Kind: "container", Context: a.Runtime.DockerContext, Name: target.Name, ContainerID: strings.Repeat("b", 64),
		CreatedAt: a.Deadline.Add(-time.Minute).Format(time.RFC3339Nano), ImageID: "sha256:" + strings.Repeat("c", 64), Labels: map[string]string{},
	}
	for key, value := range map[string]string{"objective": objective, "scope": target.ID, "nonce": target.Nonce, "generation": strconv.FormatUint(generation, 10), "kind": target.Kind, "policy": a.ManifestHash} {
		binding.Labels["dev.getoro.factory."+key] = value
	}
	if kind == "volume" {
		binding.Kind, binding.ContainerID, binding.ImageID, binding.VolumeDriver = "volume", "", "", "local"
		binding.VolumeOptions = map[string]string{"type": "tmpfs", "device": "tmpfs", "o": "size=1024,nr_inodes=200,uid=1000,gid=1000"}
	}
	return cleanupFixture{root: root, j: j, a: a, target: target, binding: binding}
}

func testManagedCleanupPermit(t *testing.T) {
	t.Helper()
	f := newCleanupFixture(t)
	scope, err := reserveManagedCleanupInvocation(t.Context(), f.j, f.target.Generation, f.target, DockerCleanupStop, f.binding, f.a.HardStop)
	if err != nil {
		t.Fatal(err)
	}
	permit, err := f.j.CleanupPermit(t.Context(), scope)
	want := DockerCleanupPermit{Scope: scope, Target: f.target, Operation: DockerCleanupStop, Binding: f.binding, Deadline: f.a.HardStop}
	if err != nil || !reflect.DeepEqual(permit, want) {
		t.Fatalf("exact permit=%+v %v", permit, err)
	}
	if granted, err := f.j.Granted(t.Context(), scope); err != nil || granted {
		t.Fatalf("permit read granted execution: %v %v", granted, err)
	}
	other := openJournalFixture(t, f.root, f.a.ObjectiveID)
	got, err := other.CleanupPermit(t.Context(), scope)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("reopened permit=%+v %v", got, err)
	}
	if err := other.RecordHello(t.Context(), scope, 101, 101); err != nil {
		t.Fatal(err)
	}
	if granted, err := f.j.Granted(t.Context(), scope); err != nil || !granted {
		t.Fatalf("cleanup did not grant: %v %v", granted, err)
	}
	var work int
	if err := f.j.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_reservations WHERE kind IN ('host','reconciliation','controller')").Scan(&work); err != nil || work != 0 {
		t.Fatalf("cleanup spent ordinary allowance: %d %v", work, err)
	}
}

func testManagedCleanupSeal(t *testing.T) {
	t.Helper()
	fixture := newHostReleaseFixture(t)
	j, scope := fixture.journal, fixture.scope
	if err := j.setDesired(t.Context(), scope.Generation, "stopped", "complete", time.Time{}); err == nil {
		t.Fatal("running objective sealed without the stopping transition")
	}
	if err := j.setDesired(t.Context(), scope.Generation, "stopping", "complete", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := j.setDesired(t.Context(), scope.Generation, "stopped", "complete", time.Time{}); err == nil {
		t.Fatal("uncertain scope sealed into release eligibility")
	}
	assertHostReservationRetained(t, fixture)
	if err := j.RecordHello(t.Context(), scope, 101, 101); err == nil {
		t.Fatal("stopping granted ordinary work")
	}
	if err := j.MarkQuiescent(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	if err := j.setDesired(t.Context(), scope.Generation, "stopped", "complete", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, eligible, err := ReleasableHostReservation(t.Context(), fixture.root, fixture.admission.ObjectiveID); err != nil || !eligible {
		t.Fatalf("sealed retirement did not release: %v %v", eligible, err)
	}
	if err := j.setDesired(t.Context(), scope.Generation, "stopping", "retry", time.Time{}); err == nil {
		t.Fatal("final seal reopened cleanup authority")
	}
}

func testManagedCleanupKeeper(t *testing.T) {
	t.Helper()
	root, admission, store := journalFixture(t)
	admission.Deadline = time.Now().UTC().Add(2 * time.Second)
	admission.HardStop = admission.Deadline.Add(25 * time.Second)
	insertJournalFixture(t, store, admission)
	j := openJournalFixture(t, root, admission.ObjectiveID)
	generation, _, err := j.beginGeneration(t.Context(), "supervisor", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	keeper := journalScope("a", generation)
	keeper.Kind, keeper.Backend, keeper.Name = "keeper", "docker", "keeper-a"
	if _, err := j.reserveScope(t.Context(), keeper, admission.HardStop, time.Now()); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(time.Until(admission.Deadline) + 20*time.Millisecond)
	defer timer.Stop()
	<-timer.C
	other := journalScope("b", generation)
	other.Kind, other.Backend, other.Name = "keeper", "docker", "keeper-b"
	if _, err := j.reserveScope(t.Context(), other, admission.HardStop, time.Now()); err == nil {
		t.Fatal("keeper lifetime allowance admitted a post-work creation")
	}
	if err := j.RecordHello(t.Context(), keeper, 101, 101); err == nil {
		t.Fatal("keeper lifetime allowance granted a post-work start")
	}
}

func testManagedCleanupEpochs(t *testing.T) {
	t.Helper()
	root, admission, store := journalFixture(t)
	admission.Recovery.MaxSupervisorStarts = 1
	admission.Recovery.MaxControllerStarts = 1
	admission.Recovery.MaxReconciliationInvocations = 1
	admission.Recovery.MaxCleanupInvocations = 1
	insertJournalFixture(t, store, admission)
	j := openJournalFixture(t, root, admission.ObjectiveID)
	if _, _, err := j.beginGeneration(t.Context(), "supervisor", time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"controller", "observer", "cleanup"} {
		if _, _, err := j.advanceEpoch(t.Context(), "supervisor", token, time.Now()); err != nil {
			t.Fatalf("counted %s epoch unavailable: %v", token, err)
		}
	}
	if _, _, err := j.advanceEpoch(t.Context(), "supervisor", "exhausted", time.Now()); err == nil {
		t.Fatal("epoch aggregate budget reset")
	}
}
