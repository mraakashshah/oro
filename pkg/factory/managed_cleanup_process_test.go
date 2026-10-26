package factory //nolint:testpackage // Verify actual process separation and transactional release races.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func testManagedCleanupKinds(t *testing.T) {
	t.Helper()
	for _, kind := range []string{"container", "volume"} {
		t.Run(kind, func(t *testing.T) {
			f := newCleanupKindFixture(t, kind)
			unknown := f.binding
			unknown.ContainerID, unknown.CreatedAt = "", ""
			scope, err := reserveManagedCleanupInvocation(t.Context(), f.j, f.target.Generation, f.target, DockerCleanupInspect, unknown, f.a.HardStop)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.j.MarkQuiescent(t.Context(), scope); err != nil {
				t.Fatal(err)
			}
			if _, err := reserveManagedCleanupInvocation(t.Context(), f.j, f.target.Generation, f.target, DockerCleanupRemove, unknown, f.a.HardStop); err == nil {
				t.Fatal("unknown identity permitted removal")
			}
			if kind == "volume" {
				if _, err := reserveManagedCleanupInvocation(t.Context(), f.j, f.target.Generation, f.target, DockerCleanupStop, f.binding, f.a.HardStop); err == nil {
					t.Fatal("volume accepted stop")
				}
			}
			last, err := reserveManagedCleanupInvocation(t.Context(), f.j, f.target.Generation, f.target, DockerCleanupRemove, f.binding, f.a.HardStop)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.j.MarkQuiescent(t.Context(), f.target); err != nil {
				t.Fatal(err)
			}
			if err := f.j.setDesired(t.Context(), last.Generation, "stopped", "test cleanup", time.Time{}); err == nil {
				t.Fatal("live cleanup did not prevent final seal")
			}
			if err := f.j.MarkQuiescent(t.Context(), last); err != nil {
				t.Fatal(err)
			}
			if err := f.j.setDesired(t.Context(), last.Generation, "stopped", "test cleanup", time.Time{}); err != nil {
				t.Fatal(err)
			}
			if _, eligible, err := ReleasableHostReservation(t.Context(), f.root, f.a.ObjectiveID); err != nil || !eligible {
				t.Fatalf("late cleanup did not permit final release: %v %v", eligible, err)
			}
			if _, err := reserveManagedCleanupInvocation(t.Context(), f.j, last.Generation, f.target, DockerCleanupInspect, f.binding, f.a.HardStop); err == nil {
				t.Fatal("final seal admitted a new cleanup")
			}
			if _, _, err := f.j.beginGeneration(t.Context(), "late-supervisor", time.Now()); err == nil {
				t.Fatal("final seal admitted a new supervisor")
			}
			if err := f.j.RecordHello(t.Context(), last, 101, 101); err == nil {
				t.Fatal("final seal admitted a late HELLO")
			}
		})
	}
	t.Run("keeper", testCleanupKeeperBinding)
}

func testCleanupKeeperBinding(t *testing.T) {
	for _, operation := range []DockerCleanupOp{DockerCleanupInspect, DockerCleanupStop, DockerCleanupRemove} {
		t.Run(string(operation), func(t *testing.T) {
			f, keeper, _ := keeperInheritedFixture(t)
			if err := f.journal.setDesired(t.Context(), f.generation, "stopping", "cleanup", time.Time{}); err != nil {
				t.Fatal(err)
			}
			for _, mode := range []string{"added", "changed", "removed", "image", "lifetime", "unknown"} {
				binding := cloneManagedDockerBinding(keeper.Binding)
				switch mode {
				case "added", "changed", "removed":
					changeKeeperInheritedLabel(binding.Labels, mode)
				case "image":
					binding.ImageID = "sha256:" + strings.Repeat("e", 64)
				case "lifetime":
					binding.CreatedAt = "2026-10-24T06:00:00Z"
				case "unknown":
					binding.ContainerID, binding.CreatedAt = "", ""
				}
				before := snapshotCleanupIntegrity(t, f.journal)
				if scope, err := reserveManagedCleanupInvocation(t.Context(), f.journal, f.generation, keeper.Keeper, operation, binding, f.manifest.HardStop); err == nil || scope != (ScopeHandle{}) {
					t.Errorf("%s accepted %s", operation, mode)
				}
				assertCleanupIntegrityUnchanged(t, f.journal, before)
			}
			scope, err := reserveManagedCleanupInvocation(t.Context(), f.journal, f.generation, keeper.Keeper, operation, keeper.Binding, f.manifest.HardStop)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.journal.CleanupPermit(t.Context(), scope); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, alreadyGranted := range []bool{false, true} {
		t.Run(fmt.Sprintf("tampered permit granted=%v", alreadyGranted), func(t *testing.T) {
			f, keeper, _ := keeperInheritedFixture(t)
			if err := f.journal.setDesired(t.Context(), f.generation, "stopping", "cleanup", time.Time{}); err != nil {
				t.Fatal(err)
			}
			scope, err := reserveManagedCleanupInvocation(t.Context(), f.journal, f.generation, keeper.Keeper, DockerCleanupInspect, keeper.Binding, f.manifest.HardStop)
			if err != nil {
				t.Fatal(err)
			}
			permit, err := f.journal.CleanupPermit(t.Context(), scope)
			if err != nil {
				t.Fatal(err)
			}
			if alreadyGranted {
				if err := f.journal.RecordHello(t.Context(), scope, 101, 101); err != nil {
					t.Fatal(err)
				}
			}
			permit.Binding = cloneManagedDockerBinding(permit.Binding)
			changeKeeperInheritedLabel(permit.Binding.Labels, "changed")
			body, err := encodeCleanupPermit(f.journal.admission, permit)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.journal.conn.ExecContext(t.Context(), "UPDATE managed_cleanup_permits SET body=? WHERE scope_id=?", body, scope.ID); err != nil {
				t.Fatal(err)
			}
			before := snapshotCleanupIntegrity(t, f.journal)
			if _, err := f.journal.CleanupPermit(t.Context(), scope); err == nil {
				t.Error("tampered permit returned")
			}
			if err := f.journal.RecordHello(t.Context(), scope, 101, 101); err == nil {
				t.Error("tampered permit granted HELLO")
			}
			if granted, err := f.journal.Granted(t.Context(), scope); granted || (alreadyGranted && err == nil) {
				t.Error("tampered permit granted execution")
			}
			assertCleanupIntegrityUnchanged(t, f.journal, before)
		})
	}
	for _, phase := range []string{"prepared", "dispatching", "acknowledged"} {
		t.Run(phase, func(t *testing.T) {
			f := newSourceKeeperFixture(t)
			keeper := f.prepare(t)
			if phase != "prepared" {
				if _, err := dispatchManagedDockerKeeper(t.Context(), f.journal, keeper.Keeper, keeper.RuntimeConfigSHA256); err != nil {
					t.Fatal(err)
				}
			}
			sourcePrepareRetire(t, f.journal, keeper.CreateHost)
			if phase == "acknowledged" {
				if err := acknowledgeManagedDockerKeeper(t.Context(), f.journal, keeper.Keeper, keeper.CreateHost, strings.Repeat("d", 64)); err != nil {
					t.Fatal(err)
				}
			}
			keeper, err := loadManagedDockerKeeper(t.Context(), f.journal, keeper.Keeper)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.journal.setDesired(t.Context(), f.generation, "stopping", "cleanup", time.Time{}); err != nil {
				t.Fatal(err)
			}
			unknown := cloneManagedDockerBinding(keeper.Binding)
			unknown.ContainerID, unknown.CreatedAt = "", ""
			for _, mode := range []string{"labels", "lifetime", "remove", "stop"} {
				binding := cloneManagedDockerBinding(unknown)
				op := DockerCleanupInspect
				switch mode {
				case "labels":
					binding.Labels["image.extra"] = "new"
				case "lifetime":
					binding.ContainerID, binding.CreatedAt = strings.Repeat("d", 64), "2026-10-24T06:00:00Z"
				case "remove":
					op = DockerCleanupRemove
				case "stop":
					op = DockerCleanupStop
				}
				before := snapshotCleanupIntegrity(t, f.journal)
				if scope, err := reserveManagedCleanupInvocation(t.Context(), f.journal, f.generation, keeper.Keeper, op, binding, f.manifest.HardStop); err == nil || scope != (ScopeHandle{}) {
					t.Errorf("unbound accepted %s", mode)
				}
				assertCleanupIntegrityUnchanged(t, f.journal, before)
			}
			if _, err := reserveManagedCleanupInvocation(t.Context(), f.journal, f.generation, keeper.Keeper, DockerCleanupInspect, unknown, f.manifest.HardStop); err != nil {
				t.Fatal(err)
			}
			before := snapshotCleanupIntegrity(t, f.journal)
			observed := cloneManagedDockerBinding(keeper.Binding)
			observed.ContainerID, observed.CreatedAt = strings.Repeat("d", 64), "2026-10-24T06:00:00Z"
			if err := bindManagedDockerKeeper(t.Context(), f.journal, f.generation, keeper.Keeper, observed); err == nil {
				t.Error("cleanup inspection allowed lifetime adoption")
			}
			if _, err := dispatchManagedDockerKeeper(t.Context(), f.journal, keeper.Keeper, keeper.RuntimeConfigSHA256); err == nil {
				t.Error("cleanup inspection allowed create replay")
			}
			assertCleanupIntegrityUnchanged(t, f.journal, before)
		})
	}
	for _, mode := range []string{"missing", "corrupt"} {
		t.Run(mode, func(t *testing.T) {
			f, keeper, _ := keeperInheritedFixture(t)
			if err := f.journal.setDesired(t.Context(), f.generation, "stopping", "cleanup", time.Time{}); err != nil {
				t.Fatal(err)
			}
			scope, err := reserveManagedCleanupInvocation(t.Context(), f.journal, f.generation, keeper.Keeper, DockerCleanupInspect, keeper.Binding, f.manifest.HardStop)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.journal.RecordHello(t.Context(), scope, 101, 101); err != nil {
				t.Fatal(err)
			}
			query := "DELETE FROM managed_docker_keepers WHERE keeper_scope_id=?"
			if mode == "corrupt" {
				query = "UPDATE managed_docker_keepers SET body='{}' WHERE keeper_scope_id=?"
			}
			if _, err := f.journal.conn.ExecContext(t.Context(), query, keeper.Keeper.ID); err != nil {
				t.Fatal(err)
			}
			before := snapshotCleanupIntegrity(t, f.journal)
			if _, err := reserveManagedCleanupInvocation(t.Context(), f.journal, f.generation, keeper.Keeper, DockerCleanupInspect, keeper.Binding, f.manifest.HardStop); err == nil {
				t.Error("invalid keeper accepted")
			}
			if _, err := f.journal.CleanupPermit(t.Context(), scope); err == nil {
				t.Error("invalid keeper returned permit")
			}
			if granted, err := f.journal.Granted(t.Context(), scope); err == nil || granted {
				t.Error("invalid keeper preserved grant")
			}
			assertCleanupIntegrityUnchanged(t, f.journal, before)
		})
	}
}

func testManagedCleanupSealRace(t *testing.T) {
	t.Helper()
	for range 6 {
		f := newCleanupFixture(t)
		if err := f.j.MarkQuiescent(t.Context(), f.target); err != nil {
			t.Fatal(err)
		}
		other := openJournalFixture(t, f.root, f.a.ObjectiveID)
		start := make(chan struct{})
		reserved, stopped := make(chan error, 1), make(chan error, 1)
		go func() {
			<-start
			_, err := reserveManagedCleanupInvocation(t.Context(), f.j, f.target.Generation, f.target, DockerCleanupInspect, f.binding, f.a.HardStop)
			reserved <- err
		}()
		go func() {
			<-start
			stopped <- other.setDesired(t.Context(), f.target.Generation, "stopped", "test cleanup", time.Time{})
		}()
		close(start)
		reserveErr, stopErr := <-reserved, <-stopped
		if (reserveErr == nil) == (stopErr == nil) {
			t.Fatalf("reservation and final seal were not exclusive: reserve=%v stop=%v", reserveErr, stopErr)
		}
		_, eligible, err := ReleasableHostReservation(t.Context(), f.root, f.a.ObjectiveID)
		if err != nil || eligible != (stopErr == nil) {
			t.Fatalf("racing release=%v %v", eligible, err)
		}
		if stopErr == nil {
			if _, err := reserveManagedCleanupInvocation(t.Context(), f.j, f.target.Generation, f.target, DockerCleanupInspect, f.binding, f.a.HardStop); err == nil {
				t.Fatal("release eligibility reopened cleanup")
			}
			if _, _, err := f.j.advanceEpoch(t.Context(), "supervisor", "late", time.Now()); err == nil {
				t.Fatal("release eligibility reopened epochs")
			}
		}
	}
}

type cleanupProcessRequest struct {
	Root, ID   string
	Generation uint64
	Target     ScopeHandle
	Binding    DockerCleanupBinding
	Deadline   time.Time
}

func testManagedCleanupProcess(t *testing.T) {
	t.Helper()
	f := newCleanupFixture(t)
	request := cleanupProcessRequest{Root: f.root, ID: f.a.ObjectiveID, Generation: f.target.Generation, Target: f.target, Binding: f.binding, Deadline: f.a.HardStop}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), executable, "-test.run=^TestManagedCleanupAuthorityChild$", "-test.count=1")
	command.Env = append(os.Environ(), "ORO_CLEANUP_TEST="+string(encoded))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("child reserve failed: %s %v", output, err)
	}
	var scope ScopeHandle
	for _, line := range bytes.Split(output, []byte{'\n'}) {
		if bytes.HasPrefix(line, []byte("CLEANUP_RESULT ")) {
			if err := json.Unmarshal(bytes.TrimPrefix(line, []byte("CLEANUP_RESULT ")), &scope); err != nil {
				t.Fatal(err)
			}
		}
	}
	if scope.ID == "" {
		t.Fatalf("missing child reservation: %s", output)
	}
	if _, err := f.j.CleanupPermit(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	if got := testCleanupCounts(t, f.j); got != ([3]int{1, 1, 1}) {
		t.Fatalf("creator exit reset authority: %v", got)
	}
	if _, err := reserveManagedCleanupInvocation(t.Context(), f.j, f.target.Generation, f.target, DockerCleanupRemove, f.binding, f.a.HardStop); err == nil {
		t.Fatal("creator exit fabricated helper quiescence")
	}
}

func TestManagedCleanupAuthorityChild(t *testing.T) {
	encoded := os.Getenv("ORO_CLEANUP_TEST")
	if encoded == "" {
		t.Skip("cleanup subprocess fixture only")
	}
	var request cleanupProcessRequest
	if err := json.Unmarshal([]byte(encoded), &request); err != nil {
		t.Fatal(err)
	}
	j := openJournalFixture(t, request.Root, request.ID)
	scope, err := reserveManagedCleanupInvocation(t.Context(), j, request.Generation, request.Target, DockerCleanupInspect, request.Binding, request.Deadline)
	if err != nil {
		t.Fatal(err)
	}
	result, err := json.Marshal(scope)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("CLEANUP_RESULT %s\n", result)
}

func testManagedCleanupObserver(t *testing.T) {
	t.Helper()
	f := newCleanupFixture(t)
	if err := f.j.MarkQuiescent(t.Context(), f.target); err != nil {
		t.Fatal(err)
	}
	scope := journalScope("d", f.target.Generation)
	scope.Kind = "observer"
	scope.Name = strings.Replace(scope.Name, ".host.", ".observer.", 1)
	if _, err := f.j.reserveBudget(t.Context(), scope.Generation, "reconciliation", scope.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.j.reserveScope(t.Context(), scope, f.a.HardStop, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := f.j.RecordHello(t.Context(), scope, 101, 101); err != nil {
		t.Fatal(err)
	}
	if granted, err := f.j.Granted(t.Context(), scope); err != nil || !granted {
		t.Fatalf("stopping observer grant=%v %v", granted, err)
	}
	if got := testCleanupCounts(t, f.j); got != ([3]int{0, 1, 0}) {
		t.Fatalf("observer spent cleanup allowance: %v", got)
	}
	if err := f.j.setDesired(t.Context(), scope.Generation, "stopped", "test cleanup", time.Time{}); err == nil {
		t.Fatal("observer did not block sealing")
	}
}
