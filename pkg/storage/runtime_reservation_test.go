//nolint:testpackage // This test exercises the private journal fault-injection seam.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReserveRuntimeLeaseManifestTransaction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, time.August, 10, 17, 0, 0, 0, time.UTC)
	if CatalogSchemaVersion != 5 {
		t.Fatalf("catalog schema version = %d, want 5 for runtime reservations", CatalogSchemaVersion)
	}

	t.Run("active reservation owns canonical roots", func(t *testing.T) {
		fixture := newRuntimeReservationFixture(t, now)
		reservation, err := ReserveRuntime(ctx, fixture.catalog, fixture.request)
		if err != nil {
			t.Fatalf("ReserveRuntime() error = %v", err)
		}
		if reservation.State != ManifestActive || reservation.ID == "" || reservation.ManifestPath == "" {
			t.Fatalf("unexpected active reservation: %+v", reservation)
		}
		manifest, err := ReadRuntimeManifest(reservation.ManifestPath)
		if err != nil {
			t.Fatalf("ReadRuntimeManifest() error = %v", err)
		}
		if manifest.State != ManifestActive || manifest.ReservationID != reservation.ID || manifest.Identity.MatchesObserved(fixture.request.Identity.Process) != nil {
			t.Fatalf("manifest does not match reservation: %+v", manifest)
		}
		assertReservationState(t, fixture.catalog.DB(), reservation.ID, ManifestActive)
		assertOwnedRoots(t, fixture.catalog.DB(), reservation.ID, fixture.request.Roots)
		for _, root := range fixture.request.Roots {
			info, err := os.Stat(root.Path)
			if err != nil || !info.IsDir() {
				t.Fatalf("managed root not created: %s (%v)", root.Path, err)
			}
		}
	})

	t.Run("duplicate root and assignment are rejected", func(t *testing.T) {
		fixture := newRuntimeReservationFixture(t, now)
		first, err := ReserveRuntime(ctx, fixture.catalog, fixture.request)
		if err != nil {
			t.Fatalf("first ReserveRuntime() error = %v", err)
		}
		duplicateRoot := fixture.request
		duplicateRoot.Lease.ID = LeaseID("lease-duplicate-root")
		if _, err := ReserveRuntime(ctx, fixture.catalog, duplicateRoot); !isRuntimeReservationError(err) {
			t.Fatal("duplicate canonical roots unexpectedly acquired a reservation")
		}
		duplicateAssignment := fixture.request
		duplicateAssignment.Lease.ID = LeaseID("lease-duplicate-assignment")
		duplicateAssignment.Roots = runtimeReservationRoots(t, fixture.root, "other")
		if _, err := ReserveRuntime(ctx, fixture.catalog, duplicateAssignment); !isRuntimeReservationError(err) {
			t.Fatal("duplicate assignment unexpectedly acquired a reservation")
		}
		if err := fixture.catalog.ReleaseRuntimeReservation(ctx, first.ID, fixture.request.Identity.Process); err != nil {
			t.Fatalf("release first reservation: %v", err)
		}
		if err := fixture.catalog.ReleaseRuntimeReservation(ctx, first.ID, fixture.request.Identity.Process); !isRuntimeReservationError(err) {
			t.Fatal("releasing an interrupted reservation unexpectedly succeeded")
		}
		mismatchedLease := fixture.request
		mismatchedLease.Lease.ID = LeaseID("lease-mismatched-pid")
		mismatchedLease.Lease.PID++
		if _, err := ReserveRuntime(ctx, fixture.catalog, mismatchedLease); !isRuntimeReservationError(err) {
			t.Fatal("mismatched lease/process PID unexpectedly acquired a reservation")
		}
	})

	t.Run("stale CAS and every process identity mismatch fail closed", func(t *testing.T) {
		fixture := newRuntimeReservationFixture(t, now)
		reservation, err := ReserveRuntime(ctx, fixture.catalog, fixture.request)
		if err != nil {
			t.Fatalf("ReserveRuntime() error = %v", err)
		}
		var transitionErr *ReservationTransitionError
		if err := fixture.catalog.TransitionRuntimeReservation(ctx, reservation.ID, ManifestAllocating, ManifestActive, fixture.request.Identity.Process); !errors.As(err, &transitionErr) {
			t.Fatalf("stale transition error = %v, want ReservationTransitionError", err)
		}
		if transitionErr.Expected != ManifestAllocating || transitionErr.Actual != ManifestActive {
			t.Fatalf("stale transition = %+v", transitionErr)
		}
		for name, mutate := range map[string]func(*ProcessIdentity){
			"pid":           func(identity *ProcessIdentity) { identity.PID++ },
			"start marker":  func(identity *ProcessIdentity) { identity.StartMarker = "linux:other" },
			"executable":    func(identity *ProcessIdentity) { identity.Executable = "/bin/other" },
			"process group": func(identity *ProcessIdentity) { identity.ProcessGroup++ },
		} {
			t.Run(name, func(t *testing.T) {
				observed := fixture.request.Identity.Process
				mutate(&observed)
				if err := fixture.catalog.TransitionRuntimeReservation(ctx, reservation.ID, ManifestActive, ManifestInterrupted, observed); !isReservationTransitionError(err) {
					t.Fatal("identity mismatch unexpectedly transitioned reservation")
				}
				assertReservationState(t, fixture.catalog.DB(), reservation.ID, ManifestActive)
			})
		}
	})

	failureCases := []struct {
		name      string
		configure func(*runtimeReservationHooks)
		wantRoots int
		wantState ManifestState
	}{
		{
			name: "manifest allocating failure",
			configure: func(hooks *runtimeReservationHooks) {
				hooks.writeManifest = func(_ string, manifest RuntimeManifest) error {
					if manifest.State == ManifestAllocating {
						return errors.New("injected allocating manifest failure")
					}
					return nil
				}
			},
			wantState: ManifestAllocating,
		},
		{
			name: "second mkdir failure",
			configure: func(hooks *runtimeReservationHooks) {
				calls := 0
				hooks.mkdir = func(path string, mode os.FileMode) error {
					calls++
					if calls == 2 {
						return errors.New("injected mkdir failure")
					}
					return os.Mkdir(path, mode)
				}
			},
			wantRoots: 1,
			wantState: ManifestAllocating,
		},
		{
			name: "active manifest failure",
			configure: func(hooks *runtimeReservationHooks) {
				hooks.writeManifest = func(_ string, manifest RuntimeManifest) error {
					if manifest.State == ManifestActive {
						return errors.New("injected active manifest failure")
					}
					return nil
				}
			},
			wantRoots: 3,
			wantState: ManifestAllocating,
		},
		{
			name: "post-active CAS failure",
			configure: func(hooks *runtimeReservationHooks) {
				hooks.transition = func(context.Context, string, ManifestState, ManifestState, ProcessIdentity) error {
					return errors.New("injected active CAS failure")
				}
			},
			wantRoots: 3,
			wantState: ManifestAllocating,
		},
	}
	for _, failure := range failureCases {
		t.Run(failure.name, func(t *testing.T) {
			fixture := newRuntimeReservationFixture(t, now)
			hooks := newRuntimeReservationHooks()
			failure.configure(&hooks)
			reservation, err := reserveRuntimeWithHooks(ctx, fixture.catalog, fixture.request, hooks)
			if !isRuntimeReservationError(err) {
				t.Fatal("injected failure unexpectedly succeeded")
			}
			if reservation.State != failure.wantState {
				t.Fatalf("returned reservation state = %s, want %s", reservation.State, failure.wantState)
			}
			assertReservationState(t, fixture.catalog.DB(), reservation.ID, failure.wantState)
			assertOwnedRootCount(t, fixture.catalog.DB(), reservation.ID, len(fixture.request.Roots))
			assertPhysicalRootCount(t, fixture.request.Roots, failure.wantRoots)
			assertNoUnownedRoots(t, fixture.catalog.DB(), fixture.request.Roots)
		})
	}
}

func isRuntimeReservationError(err error) bool {
	var typed *RuntimeReservationError
	return err != nil && errors.As(err, &typed)
}

func isReservationTransitionError(err error) bool {
	var typed *ReservationTransitionError
	return err != nil && errors.As(err, &typed)
}

type runtimeReservationFixture struct {
	catalog *Catalog
	root    string
	request RuntimeReservationRequest
}

func newRuntimeReservationFixture(t *testing.T, now time.Time) *runtimeReservationFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve test root: %v", err)
	}
	catalog, err := OpenCatalog(context.Background(), filepath.Join(root, "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	identity := RuntimeIdentity{
		TaskID: "task-1", RunID: "run-1", BeadID: "bead-1", WorkerID: "worker-1",
		AssignmentID: 7, Generation: 3,
		Process:   ProcessIdentity{PID: 42, StartMarker: "linux:12345", Executable: "/usr/local/bin/oro", ProcessGroup: 42},
		CreatedAt: now, RetainUntil: now.Add(time.Hour),
	}
	return &runtimeReservationFixture{
		catalog: catalog,
		root:    root,
		request: RuntimeReservationRequest{
			Lease:     LeaseRequest{ID: LeaseID("lease-1"), Namespace: "repo/worktree", ControllerID: "controller-1", OwnerID: "worker-1", PID: identity.Process.PID, ProcessStart: now, AcquiredAt: now, HeartbeatAt: now},
			Identity:  identity,
			Workdir:   root,
			Roots:     runtimeReservationRoots(t, root, "primary"),
			Retention: time.Hour,
		},
	}
}

func runtimeReservationRoots(t *testing.T, root, suffix string) []ManagedRoot {
	t.Helper()
	return []ManagedRoot{
		{Path: filepath.Join(root, suffix+"-cache"), Class: RootCache, Disposition: RootDisposable},
		{Path: filepath.Join(root, suffix+"-tmp"), Class: RootTemp, Disposition: RootDisposable},
		{Path: filepath.Join(root, suffix+"-evidence"), Class: RootEvidence, Disposition: RootDurable},
	}
}

func assertReservationState(t *testing.T, db *sql.DB, id string, want ManifestState) {
	t.Helper()
	var got string
	if err := db.QueryRow(`SELECT state FROM runtime_reservations WHERE id=?`, id).Scan(&got); err != nil {
		t.Fatalf("load reservation state: %v", err)
	}
	if ManifestState(got) != want {
		t.Fatalf("reservation state = %q, want %q", got, want)
	}
}

func assertOwnedRoots(t *testing.T, db *sql.DB, id string, roots []ManagedRoot) {
	t.Helper()
	assertOwnedRootCount(t, db, id, len(roots))
	for _, root := range roots {
		var owner string
		if err := db.QueryRow(`SELECT reservation_id FROM runtime_reservation_roots WHERE path=?`, root.Path).Scan(&owner); err != nil {
			t.Fatalf("load root owner %s: %v", root.Path, err)
		}
		if owner != id {
			t.Fatalf("root %s owned by %s, want %s", root.Path, owner, id)
		}
	}
}

func assertOwnedRootCount(t *testing.T, db *sql.DB, id string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`SELECT COUNT(*) FROM runtime_reservation_roots WHERE reservation_id=?`, id).Scan(&got); err != nil {
		t.Fatalf("count reservation roots: %v", err)
	}
	if got != want {
		t.Fatalf("owned root count = %d, want %d", got, want)
	}
}

func assertPhysicalRootCount(t *testing.T, roots []ManagedRoot, want int) {
	t.Helper()
	got := 0
	for _, root := range roots {
		if _, err := os.Stat(root.Path); err == nil {
			got++
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stat root %s: %v", root.Path, err)
		}
	}
	if got != want {
		t.Fatalf("physical root count = %d, want %d", got, want)
	}
}

func assertNoUnownedRoots(t *testing.T, db *sql.DB, roots []ManagedRoot) {
	t.Helper()
	for _, root := range roots {
		if _, err := os.Stat(root.Path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			t.Fatalf("stat root %s: %v", root.Path, err)
		}
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM runtime_reservation_roots WHERE path=?`, root.Path).Scan(&count); err != nil {
			t.Fatalf("count owner for root %s: %v", root.Path, err)
		}
		if count != 1 {
			t.Fatalf("root %s has %d owners, want 1", root.Path, count)
		}
	}
}
