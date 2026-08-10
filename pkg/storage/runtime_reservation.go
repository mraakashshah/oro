package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RuntimeReservationCatalog persists reservations and their owned roots.
type RuntimeReservationCatalog interface {
	AcquireRuntimeReservation(context.Context, RuntimeReservationRequest) (RuntimeReservation, error)
	TransitionRuntimeReservation(context.Context, string, ManifestState, ManifestState, ProcessIdentity) error
	ReleaseRuntimeReservation(context.Context, string, ProcessIdentity) error
}

// RuntimeReservationRequest describes one transactionally acquired runtime.
type RuntimeReservationRequest struct {
	Lease     LeaseRequest
	Identity  RuntimeIdentity
	Workdir   string
	Roots     []ManagedRoot
	Retention time.Duration
}

// RuntimeReservation is the catalog record and manifest identity for one runtime.
type RuntimeReservation struct {
	ID, ManifestPath, Workdir string
	Roots                     []ManagedRoot
	Lease                     Lease
	Identity                  RuntimeIdentity
	State                     ManifestState
	CreatedAt, UpdatedAt      time.Time
}

// ReservationTransitionError reports a compare-and-swap state failure.
type ReservationTransitionError struct {
	ReservationID string
	Expected      ManifestState
	Actual        ManifestState
	Reason        string
}

// RuntimeReservationError reports a failed reservation journal stage.
type RuntimeReservationError struct {
	Stage string
	Err   error
}

func (err *RuntimeReservationError) Error() string {
	return fmt.Sprintf("runtime reservation %s: %v", err.Stage, err.Err)
}

func (err *RuntimeReservationError) Unwrap() error { return err.Err }

func runtimeReservationError(stage string, err error) error {
	if err == nil {
		return nil
	}
	var typed *RuntimeReservationError
	if errors.As(err, &typed) {
		return err
	}
	return &RuntimeReservationError{Stage: stage, Err: err}
}

func (err *ReservationTransitionError) Error() string {
	return fmt.Sprintf("reservation %s transition %s to %s: %s", err.ReservationID, err.Expected, err.Actual, err.Reason)
}

// ReserveRuntime journals catalog ownership, manifest allocation, root creation,
// and activation in that order. Failed journal edges retain an allocating record
// so recovery can account for every root.
func ReserveRuntime(ctx context.Context, catalog RuntimeReservationCatalog, req RuntimeReservationRequest) (RuntimeReservation, error) {
	return reserveRuntimeWithHooks(ctx, catalog, req, newRuntimeReservationHooks())
}

type runtimeReservationHooks struct {
	writeManifest func(string, RuntimeManifest) error
	mkdir         func(string, os.FileMode) error
	transition    func(context.Context, string, ManifestState, ManifestState, ProcessIdentity) error
}

func newRuntimeReservationHooks() runtimeReservationHooks {
	return runtimeReservationHooks{
		writeManifest: WriteRuntimeManifestAtomic,
		mkdir:         os.Mkdir,
		transition: func(ctx context.Context, id string, from, to ManifestState, identity ProcessIdentity) error {
			catalog, ok := runtimeReservationCatalogFromContext(ctx)
			if !ok {
				return errors.New("runtime reservation catalog missing from context")
			}
			return catalog.TransitionRuntimeReservation(ctx, id, from, to, identity)
		},
	}
}

type runtimeReservationCatalogContextKey struct{}

func runtimeReservationCatalogFromContext(ctx context.Context) (RuntimeReservationCatalog, bool) {
	catalog, ok := ctx.Value(runtimeReservationCatalogContextKey{}).(RuntimeReservationCatalog)
	return catalog, ok
}

func reserveRuntimeWithHooks(ctx context.Context, catalog RuntimeReservationCatalog, req RuntimeReservationRequest, hooks runtimeReservationHooks) (RuntimeReservation, error) {
	if err := ctx.Err(); err != nil {
		return RuntimeReservation{}, runtimeReservationError("context", err)
	}
	if catalog == nil {
		return RuntimeReservation{}, runtimeReservationError("catalog", errors.New("catalog is nil"))
	}
	if err := validateRuntimeReservationRequest(req); err != nil {
		return RuntimeReservation{}, runtimeReservationError("validate", err)
	}
	if err := validateRuntimeReservationHooks(hooks); err != nil {
		return RuntimeReservation{}, runtimeReservationError("hooks", err)
	}
	manifestPath := filepath.Join(req.Workdir, "runtime-manifest.json")
	manifest := RuntimeManifest{
		SchemaVersion: runtimeManifestSchemaVersion,
		Identity:      req.Identity,
		ReservationID: "reservation-" + string(req.Lease.ID),
		LeaseID:       string(req.Lease.ID),
		ManifestPath:  manifestPath,
		Roots:         append([]ManagedRoot(nil), req.Roots...),
		State:         ManifestAllocating,
	}
	if err := validateRuntimeManifest(manifestPath, manifest); err != nil {
		return RuntimeReservation{}, runtimeReservationError("validate manifest", err)
	}
	if err := ensureRuntimeReservationRootsAbsent(req.Roots); err != nil {
		return RuntimeReservation{}, runtimeReservationError("inspect roots", err)
	}

	requestWithCatalog := context.WithValue(ctx, runtimeReservationCatalogContextKey{}, catalog)
	reservation, err := catalog.AcquireRuntimeReservation(requestWithCatalog, req)
	if err != nil {
		return RuntimeReservation{}, runtimeReservationError("catalog allocating", err)
	}
	if err := hooks.writeManifest(manifestPath, manifest); err != nil {
		return reservation, runtimeReservationError("write allocating manifest", err)
	}
	created := 0
	for _, root := range req.Roots {
		if err := hooks.mkdir(root.Path, 0o750); err != nil {
			return reservation, runtimeReservationError("create roots", fmt.Errorf("create managed root %s: %w", root.Path, err))
		}
		created++
	}
	_ = created
	manifest.State = ManifestActive
	if err := hooks.writeManifest(manifestPath, manifest); err != nil {
		return reservation, runtimeReservationError("write active manifest", err)
	}
	if err := hooks.transition(requestWithCatalog, reservation.ID, ManifestAllocating, ManifestActive, req.Identity.Process); err != nil {
		return reservation, runtimeReservationError("activate", err)
	}
	reservation.State = ManifestActive
	reservation.ManifestPath = manifestPath
	return reservation, nil
}

func validateRuntimeReservationHooks(hooks runtimeReservationHooks) error {
	if hooks.writeManifest == nil || hooks.mkdir == nil || hooks.transition == nil {
		return errors.New("hooks are incomplete")
	}
	return nil
}

func ensureRuntimeReservationRootsAbsent(roots []ManagedRoot) error {
	for _, root := range roots {
		if _, err := os.Lstat(root.Path); err == nil {
			return fmt.Errorf("managed root already exists: %s", root.Path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect managed root %s: %w", root.Path, err)
		}
	}
	return nil
}

func validateRuntimeReservationRequest(req RuntimeReservationRequest) error {
	if err := req.Identity.Validate(); err != nil {
		return err
	}
	if err := validateLeaseRequest(req.Lease); err != nil {
		return err
	}
	if req.Lease.PID != req.Identity.Process.PID {
		return errors.New("runtime reservation lease PID does not match process identity")
	}
	if strings.TrimSpace(string(req.Lease.ID)) == "" || strings.TrimSpace(req.Workdir) == "" {
		return errors.New("runtime reservation lease and workdir are required")
	}
	if !filepath.IsAbs(req.Workdir) || filepath.Clean(req.Workdir) != req.Workdir {
		return fmt.Errorf("runtime reservation workdir is not canonical: %q", req.Workdir)
	}
	info, err := os.Stat(req.Workdir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("runtime reservation workdir is not a directory: %s", req.Workdir)
	}
	if req.Retention < 0 {
		return errors.New("runtime reservation retention must not be negative")
	}
	if len(req.Roots) == 0 {
		return errors.New("runtime reservation roots are required")
	}
	manifestPath := filepath.Join(req.Workdir, "runtime-manifest.json")
	manifest := RuntimeManifest{
		SchemaVersion: runtimeManifestSchemaVersion,
		Identity:      req.Identity,
		ReservationID: "reservation-" + string(req.Lease.ID),
		LeaseID:       string(req.Lease.ID),
		ManifestPath:  manifestPath,
		Roots:         append([]ManagedRoot(nil), req.Roots...),
		State:         ManifestAllocating,
	}
	return validateRuntimeManifest(manifestPath, manifest)
}

// AcquireRuntimeReservation atomically records a lease, reservation, and roots.
func (c *Catalog) AcquireRuntimeReservation(ctx context.Context, req RuntimeReservationRequest) (RuntimeReservation, error) {
	if err := validateRuntimeReservationRequest(req); err != nil {
		return RuntimeReservation{}, err
	}
	reservationID := "reservation-" + string(req.Lease.ID)
	manifestPath := filepath.Join(req.Workdir, "runtime-manifest.json")
	now := time.Now().UTC()
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return RuntimeReservation{}, fmt.Errorf("begin runtime reservation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO runtime_leases (id, namespace, controller_id, owner_id, pid, process_start, acquired_at, heartbeat_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, req.Lease.ID, req.Lease.Namespace, req.Lease.ControllerID, req.Lease.OwnerID, req.Lease.PID, formatTime(req.Lease.ProcessStart), formatTime(req.Lease.AcquiredAt), formatTime(req.Lease.HeartbeatAt)); err != nil {
		return RuntimeReservation{}, fmt.Errorf("insert runtime lease: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO runtime_reservations (id, lease_id, task_id, run_id, bead_id, worker_id, assignment_id, generation, pid, process_start_marker, executable, process_group, manifest_path, workdir, state, retention_until, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, reservationID, req.Lease.ID, req.Identity.TaskID, req.Identity.RunID, req.Identity.BeadID, req.Identity.WorkerID, req.Identity.AssignmentID, req.Identity.Generation, req.Identity.Process.PID, req.Identity.Process.StartMarker, req.Identity.Process.Executable, req.Identity.Process.ProcessGroup, manifestPath, req.Workdir, ManifestAllocating, formatTime(req.Identity.RetainUntil), formatTime(now), formatTime(now)); err != nil {
		return RuntimeReservation{}, fmt.Errorf("insert runtime reservation: %w", err)
	}
	for _, root := range req.Roots {
		if _, err := tx.ExecContext(ctx, `INSERT INTO runtime_reservation_roots (reservation_id, path, class, disposition) VALUES (?, ?, ?, ?)`, reservationID, root.Path, root.Class, root.Disposition); err != nil {
			return RuntimeReservation{}, fmt.Errorf("insert runtime reservation root %s: %w", root.Path, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return RuntimeReservation{}, fmt.Errorf("commit runtime reservation: %w", err)
	}
	return RuntimeReservation{
		ID:           reservationID,
		ManifestPath: manifestPath,
		Workdir:      req.Workdir,
		Roots:        append([]ManagedRoot(nil), req.Roots...),
		Lease:        Lease{LeaseRequest: req.Lease},
		Identity:     req.Identity,
		State:        ManifestAllocating,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

// TransitionRuntimeReservation performs a process-authorized state CAS.
func (c *Catalog) TransitionRuntimeReservation(ctx context.Context, id string, expected, next ManifestState, observed ProcessIdentity) error {
	if strings.TrimSpace(id) == "" {
		return &ReservationTransitionError{ReservationID: id, Expected: expected, Reason: "reservation ID is required"}
	}
	if err := validateProcessIdentity(observed); err != nil {
		return err
	}
	var actual ManifestState
	var identity ProcessIdentity
	err := c.db.QueryRowContext(ctx, `SELECT state, pid, process_start_marker, executable, process_group FROM runtime_reservations WHERE id=?`, id).Scan(&actual, &identity.PID, &identity.StartMarker, &identity.Executable, &identity.ProcessGroup)
	if errors.Is(err, sql.ErrNoRows) {
		return &ReservationTransitionError{ReservationID: id, Expected: expected, Actual: "", Reason: "reservation not found"}
	}
	if err != nil {
		return fmt.Errorf("load runtime reservation %s: %w", id, err)
	}
	if actual != expected {
		return &ReservationTransitionError{ReservationID: id, Expected: expected, Actual: actual, Reason: "state compare-and-swap failed"}
	}
	if !identity.Matches(observed) {
		return &ReservationTransitionError{ReservationID: id, Expected: expected, Actual: actual, Reason: "process identity mismatch"}
	}
	if !validManifestTransition(expected, next) {
		return &ReservationTransitionError{ReservationID: id, Expected: expected, Actual: actual, Reason: fmt.Sprintf("invalid transition to %s", next)}
	}
	result, err := c.db.ExecContext(ctx, `UPDATE runtime_reservations SET state=?, updated_at=? WHERE id=? AND state=?`, next, formatTime(time.Now().UTC()), id, expected)
	if err != nil {
		return fmt.Errorf("transition runtime reservation %s: %w", id, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("transition runtime reservation %s rows affected: %w", id, err)
	}
	if changed != 1 {
		return &ReservationTransitionError{ReservationID: id, Expected: expected, Actual: actual, Reason: "state compare-and-swap changed no rows"}
	}
	return nil
}

// ReleaseRuntimeReservation interrupts an active/finalizing reservation and releases its lease.
func (c *Catalog) ReleaseRuntimeReservation(ctx context.Context, id string, observed ProcessIdentity) error {
	if err := validateProcessIdentity(observed); err != nil {
		return runtimeReservationError("release", &ReservationTransitionError{ReservationID: id, Reason: err.Error()})
	}
	var actual ManifestState
	var identity ProcessIdentity
	var leaseID LeaseID
	if err := c.db.QueryRowContext(ctx, `SELECT state, pid, process_start_marker, executable, process_group, lease_id FROM runtime_reservations WHERE id=?`, id).Scan(&actual, &identity.PID, &identity.StartMarker, &identity.Executable, &identity.ProcessGroup, &leaseID); err != nil {
		return runtimeReservationError("release", &ReservationTransitionError{ReservationID: id, Actual: actual, Reason: fmt.Sprintf("load reservation: %v", err)})
	}
	if !identity.Matches(observed) {
		return runtimeReservationError("release", &ReservationTransitionError{ReservationID: id, Actual: actual, Reason: "process identity mismatch"})
	}
	if actual != ManifestActive && actual != ManifestFinalizing {
		return runtimeReservationError("release", &ReservationTransitionError{ReservationID: id, Actual: actual, Reason: "only active or finalizing reservations can be interrupted"})
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin release runtime reservation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE runtime_reservations SET state=?, updated_at=? WHERE id=? AND state=? AND pid=? AND process_start_marker=? AND executable=? AND process_group=?`, ManifestInterrupted, formatTime(time.Now().UTC()), id, actual, observed.PID, observed.StartMarker, observed.Executable, observed.ProcessGroup)
	if err != nil {
		return fmt.Errorf("release runtime reservation %s: %w", id, err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return runtimeReservationError("release", &ReservationTransitionError{ReservationID: id, Expected: actual, Actual: actual, Reason: "release compare-and-swap changed no rows"})
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runtime_leases SET released_at=? WHERE id=? AND released_at IS NULL`, formatTime(time.Now().UTC()), leaseID); err != nil {
		return fmt.Errorf("release runtime lease %s: %w", leaseID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit release runtime reservation %s: %w", id, err)
	}
	return nil
}
