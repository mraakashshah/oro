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
	for _, kind := range []string{"container", "keeper", "volume"} {
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
