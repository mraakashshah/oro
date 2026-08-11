//nolint:testpackage // This test exercises the private journal fault-injection seam.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

type runtimeReservationRequestProbe struct {
	acquireCalls int
}

func (probe *runtimeReservationRequestProbe) AcquireRuntimeReservation(context.Context, RuntimeReservationRequest) (RuntimeReservation, error) {
	probe.acquireCalls++
	return RuntimeReservation{ID: "probe-reservation", State: ManifestAllocating}, nil
}

func (*runtimeReservationRequestProbe) TransitionRuntimeReservation(context.Context, string, ManifestState, ManifestState, ProcessIdentity) error {
	return nil
}

func (*runtimeReservationRequestProbe) ReleaseRuntimeReservation(context.Context, string, ProcessIdentity) error {
	return nil
}

func TestRuntimeReservationRequestMutationOwner(t *testing.T) {
	now := time.Date(2026, time.August, 10, 17, 0, 0, 0, time.UTC)
	fixture := newRuntimeReservationFixture(t, now)

	t.Run("runtime reservation errors preserve nil, cause, stage, and typed identity", func(t *testing.T) {
		cause := errors.New("request cause")
		if got := runtimeReservationError("nil", nil); got != nil {
			t.Fatalf("nil error wrapper = %v, want nil", got)
		}
		wrapped := runtimeReservationError("request", cause)
		var typed *RuntimeReservationError
		if !errors.As(wrapped, &typed) || typed.Stage != "request" || !errors.Is(wrapped, cause) {
			t.Fatalf("wrapped error lost stage or cause: %v", wrapped)
		}
		got := runtimeReservationError("other", typed)
		var preserved *RuntimeReservationError
		if !errors.As(got, &preserved) || preserved != typed || reflect.ValueOf(got).Pointer() != reflect.ValueOf(typed).Pointer() {
			t.Fatal("typed runtime reservation error was wrapped a second time")
		}
	})

	t.Run("default transition hook requires catalog context", func(t *testing.T) {
		hook := newRuntimeReservationHooks().transition
		if err := hook(context.Background(), "reservation", ManifestAllocating, ManifestActive, fixture.request.Identity.Process); err == nil || err.Error() != "runtime reservation catalog missing from context" {
			t.Fatalf("missing catalog context error = %v", err)
		}
	})

	t.Run("request failures are typed, staged, and side-effect free", func(t *testing.T) {
		cases := []struct {
			name         string
			mutate       func(*RuntimeReservationRequest)
			wantContains string
			wantIs       error
		}{
			{
				name:         "invalid runtime identity",
				mutate:       func(request *RuntimeReservationRequest) { request.Identity.TaskID = "" },
				wantContains: "task_id is required",
				wantIs:       ErrInvalidRuntimeIdentity,
			},
			{
				name: "invalid identity precedes invalid lease",
				mutate: func(request *RuntimeReservationRequest) {
					request.Identity.TaskID = ""
					request.Lease.Namespace = ""
				},
				wantContains: "task_id is required",
				wantIs:       ErrInvalidRuntimeIdentity,
			},
			{
				name:         "invalid lease",
				mutate:       func(request *RuntimeReservationRequest) { request.Lease.Namespace = "" },
				wantContains: "invalid lease request",
			},
			{
				name:         "pid mismatch",
				mutate:       func(request *RuntimeReservationRequest) { request.Lease.PID++ },
				wantContains: "lease PID does not match process identity",
			},
			{
				name:         "missing lease ID",
				mutate:       func(request *RuntimeReservationRequest) { request.Lease.ID = " " },
				wantContains: "lease and workdir are required",
			},
			{
				name:         "missing workdir",
				mutate:       func(request *RuntimeReservationRequest) { request.Workdir = "" },
				wantContains: "lease and workdir are required",
			},
			{
				name:         "relative workdir",
				mutate:       func(request *RuntimeReservationRequest) { request.Workdir = "relative" },
				wantContains: "workdir is not canonical",
			},
			{
				name:         "noncanonical workdir",
				mutate:       func(request *RuntimeReservationRequest) { request.Workdir += string(filepath.Separator) + ".." },
				wantContains: "workdir is not canonical",
			},
			{
				name: "workdir is missing",
				mutate: func(request *RuntimeReservationRequest) {
					request.Workdir = filepath.Join(request.Workdir, "missing")
				},
				wantContains: "workdir is not a directory",
			},
			{
				name: "workdir is not a directory",
				mutate: func(request *RuntimeReservationRequest) {
					request.Workdir = filepath.Join(request.Workdir, "catalog.db")
				},
				wantContains: "workdir is not a directory",
			},
			{
				name:         "negative retention",
				mutate:       func(request *RuntimeReservationRequest) { request.Retention = -time.Nanosecond },
				wantContains: "retention must not be negative",
			},
			{
				name:         "roots required",
				mutate:       func(request *RuntimeReservationRequest) { request.Roots = nil },
				wantContains: "roots are required",
			},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				request := fixture.request
				testCase.mutate(&request)
				directErr := validateRuntimeReservationRequest(request)
				if directErr == nil || !strings.Contains(directErr.Error(), testCase.wantContains) {
					t.Fatalf("validation error = %v, want containing %q", directErr, testCase.wantContains)
				}
				if testCase.wantIs != nil && !errors.Is(directErr, testCase.wantIs) {
					t.Fatalf("validation error = %v, want errors.Is(%v)", directErr, testCase.wantIs)
				}
				probe := &runtimeReservationRequestProbe{}
				_, err := reserveRuntimeWithHooks(context.Background(), probe, request, runtimeReservationHooks{
					writeManifest: func(string, RuntimeManifest) error { return nil },
					mkdir:         func(string, os.FileMode) error { return nil },
					transition:    func(context.Context, string, ManifestState, ManifestState, ProcessIdentity) error { return nil },
				})
				if err == nil {
					t.Fatal("invalid request unexpectedly succeeded")
				}
				var typed *RuntimeReservationError
				if !errors.As(err, &typed) {
					t.Fatalf("error = %v, want RuntimeReservationError", err)
				}
				if typed.Stage != "validate" || !strings.Contains(typed.Err.Error(), testCase.wantContains) || probe.acquireCalls != 0 {
					t.Fatalf("error stage/cause/calls = %q/%v/%d", typed.Stage, typed.Err, probe.acquireCalls)
				}
				if testCase.wantIs != nil && !errors.Is(err, testCase.wantIs) {
					t.Fatalf("wrapped error = %v, want errors.Is(%v)", err, testCase.wantIs)
				}
			})
		}

		t.Run("canceled context", func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			probe := &runtimeReservationRequestProbe{}
			_, err := reserveRuntimeWithHooks(ctx, probe, fixture.request, runtimeReservationHooks{
				writeManifest: func(string, RuntimeManifest) error { return nil },
				mkdir:         func(string, os.FileMode) error { return nil },
				transition:    func(context.Context, string, ManifestState, ManifestState, ProcessIdentity) error { return nil },
			})
			var typed *RuntimeReservationError
			if !errors.As(err, &typed) || typed.Stage != "context" || !errors.Is(err, context.Canceled) || probe.acquireCalls != 0 {
				t.Fatalf("canceled context error/calls = %v/%d", err, probe.acquireCalls)
			}
		})

		t.Run("nil catalog", func(t *testing.T) {
			_, err := reserveRuntimeWithHooks(context.Background(), nil, fixture.request, runtimeReservationHooks{})
			var typed *RuntimeReservationError
			if !errors.As(err, &typed) || typed.Stage != "catalog" || !strings.Contains(err.Error(), "catalog is nil") {
				t.Fatalf("nil catalog error = %v", err)
			}
		})
	})

	t.Run("each incomplete hook is rejected before acquisition", func(t *testing.T) {
		cases := []struct {
			name   string
			mutate func(*runtimeReservationHooks)
		}{
			{name: "write manifest", mutate: func(hooks *runtimeReservationHooks) { hooks.writeManifest = nil }},
			{name: "mkdir", mutate: func(hooks *runtimeReservationHooks) { hooks.mkdir = nil }},
			{name: "transition", mutate: func(hooks *runtimeReservationHooks) { hooks.transition = nil }},
		}
		for _, testCase := range cases {
			t.Run(testCase.name, func(t *testing.T) {
				probe := &runtimeReservationRequestProbe{}
				hooks := runtimeReservationHooks{
					writeManifest: func(string, RuntimeManifest) error { return nil },
					mkdir:         func(string, os.FileMode) error { return nil },
					transition:    func(context.Context, string, ManifestState, ManifestState, ProcessIdentity) error { return nil },
				}
				testCase.mutate(&hooks)
				_, err := reserveRuntimeWithHooks(context.Background(), probe, fixture.request, hooks)
				var typed *RuntimeReservationError
				if !errors.As(err, &typed) || typed.Stage != "hooks" || probe.acquireCalls != 0 {
					t.Fatalf("incomplete hook error/calls = %v/%d", err, probe.acquireCalls)
				}
			})
		}
	})

	t.Run("root absence distinguishes existing and non-ENOENT paths", func(t *testing.T) {
		existing := fixture.request.Roots[0].Path
		if err := os.Mkdir(existing, 0o750); err != nil {
			t.Fatalf("create existing root fixture: %v", err)
		}
		t.Cleanup(func() { _ = os.Remove(existing) })
		if err := ensureRuntimeReservationRootsAbsent([]ManagedRoot{{Path: existing}}); err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("existing root error = %v", err)
		}
		nulPath := string([]byte{'n', 0, 'u', 'l'})
		err := ensureRuntimeReservationRootsAbsent([]ManagedRoot{{Path: nulPath}})
		if err == nil || errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "inspect managed root") {
			t.Fatalf("NUL root error = %v", err)
		}
	})

	t.Run("zero retention is accepted", func(t *testing.T) {
		request := fixture.request
		request.Retention = 0
		if err := validateRuntimeReservationRequest(request); err != nil {
			t.Fatalf("zero retention rejected: %v", err)
		}
	})

	t.Run("valid request completes reservation", func(t *testing.T) {
		probe := &runtimeReservationRequestProbe{}
		reservation, err := reserveRuntimeWithHooks(context.Background(), probe, fixture.request, runtimeReservationHooks{
			writeManifest: func(string, RuntimeManifest) error { return nil },
			mkdir:         func(string, os.FileMode) error { return nil },
			transition:    func(context.Context, string, ManifestState, ManifestState, ProcessIdentity) error { return nil },
		})
		if err != nil || probe.acquireCalls != 1 || reservation.State != ManifestActive || reservation.ManifestPath == "" {
			t.Fatalf("valid reservation/error/calls = %#v/%v/%d", reservation, err, probe.acquireCalls)
		}
	})
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
