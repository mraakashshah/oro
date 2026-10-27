package factory

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type managedJournalAdmission struct {
	ObjectiveID, ManifestHash string
	Deadline, HardStop        time.Time
	Recovery                  RecoveryPolicy
	Runtime                   RuntimePolicy
	Reservation               HostReservation
}

type managedSupervision struct {
	Revision             int64
	Generation           uint64
	SupervisorToken      string
	DesiredState, Reason string
	NextStart            time.Time
}

type managedScopeRecord struct {
	ScopeHandle
	Deadline        time.Time
	Phase           string
	PID, PGID       int
	CleanupEvidence string
}

type managedJournal struct {
	mu        sync.Mutex
	db        *sql.DB
	conn      *sql.Conn
	admission managedJournalAdmission
	identity  []byte
}

// initManagedSchema runs before admission's transaction. insertManagedAdmission
// then shares that transaction with the first delivery snapshot and marker.
func initManagedSchema(ctx context.Context, conn *sql.Conn) error {
	if err := managedDurability(ctx, conn); err != nil {
		return err
	}
	_, err := conn.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS managed_admissions (
 id INTEGER PRIMARY KEY CHECK(id=1), body BLOB NOT NULL CHECK(length(body)<=65536)
);
CREATE TABLE IF NOT EXISTS managed_supervision (
 id INTEGER PRIMARY KEY CHECK(id=1), revision INTEGER NOT NULL CHECK(revision>0),
 body BLOB NOT NULL CHECK(length(body)<=8192)
);
CREATE TABLE IF NOT EXISTS managed_reservations (
 kind TEXT NOT NULL, token TEXT NOT NULL CHECK(length(token)<=64),
 generation INTEGER NOT NULL CHECK(generation>0), PRIMARY KEY(kind,token)
);
CREATE TABLE IF NOT EXISTS managed_scopes (
 id TEXT PRIMARY KEY, nonce TEXT NOT NULL UNIQUE, body BLOB NOT NULL CHECK(length(body)<=2048),
 backend TEXT NOT NULL, name TEXT NOT NULL,
 phase TEXT NOT NULL CHECK(phase IN ('reserved','granted','revoked','quiescent')),
 pid INTEGER NOT NULL DEFAULT 0, pgid INTEGER NOT NULL DEFAULT 0,
 cleanup TEXT NOT NULL DEFAULT '' CHECK(length(cleanup)<=256), UNIQUE(backend,name)
);
CREATE TABLE IF NOT EXISTS managed_events (
 revision INTEGER PRIMARY KEY CHECK(revision>1), kind TEXT NOT NULL,
 ref TEXT NOT NULL CHECK(length(ref)<=256), body BLOB NOT NULL CHECK(length(body)<=8192)
);
CREATE TABLE IF NOT EXISTS managed_cleanup_permits (
 scope_id TEXT PRIMARY KEY REFERENCES managed_scopes(id),
 body BLOB NOT NULL CHECK(length(body)<=16384)
);`)
	if err != nil {
		return fmt.Errorf("initialize managed journal: %w", err)
	}
	if err := initManagedWorkspaceSchema(ctx, conn); err != nil {
		return err
	}
	if err := initManagedDockerSchema(ctx, conn, `CREATE TABLE IF NOT EXISTS managed_activation (
 id INTEGER PRIMARY KEY CHECK(id=1), body BLOB NOT NULL CHECK(length(body)<=2048)
);`); err != nil {
		return err
	}
	if err := initManagedDockerVolumeSchema(ctx, conn); err != nil {
		return err
	}
	if err := initManagedCandidateSchema(ctx, conn); err != nil {
		return err
	}
	return initManagedDockerSchema(ctx, conn, `CREATE TABLE IF NOT EXISTS managed_docker_keepers (
 keeper_scope_id TEXT PRIMARY KEY REFERENCES managed_scopes(id),
 volume_scope_id TEXT NOT NULL UNIQUE REFERENCES managed_scopes(id),
 create_scope_id TEXT NOT NULL UNIQUE REFERENCES managed_scopes(id),
 body BLOB NOT NULL CHECK(length(body)<=16384)
);
CREATE TABLE IF NOT EXISTS managed_docker_sources (
 keeper_scope_id TEXT PRIMARY KEY REFERENCES managed_scopes(id),
 volume_scope_id TEXT NOT NULL UNIQUE REFERENCES managed_scopes(id),
 start_scope_id TEXT NOT NULL UNIQUE REFERENCES managed_scopes(id),
 seed_scope_id TEXT UNIQUE REFERENCES managed_scopes(id),
 body BLOB NOT NULL CHECK(length(body)<=16384)
);`)
}

func managedDurability(ctx context.Context, conn *sql.Conn) error {
	return configureManagedStorage(ctx, conn, false)
}

func validateJournalAdmission(a managedJournalAdmission) error {
	if !managedIdentifier(a.ObjectiveID) || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(a.ManifestHash) || a.Deadline.IsZero() {
		return errors.New("invalid managed journal admission identity")
	}
	spec := ManagedSpec{ID: a.ObjectiveID, MaxAttempts: 1, TimeoutSeconds: 1, StepTimeoutSeconds: 1, Recovery: a.Recovery, Runtime: a.Runtime}
	if err := validateManagedRecovery(spec); err != nil {
		return err
	}
	if err := validateManagedResources(a.Runtime); err != nil {
		return err
	}
	if err := validateHostReservation(a.Reservation, spec, a.Reservation.StateRoot); err != nil {
		return err
	}
	allowance := time.Duration(a.Recovery.CleanupTimeoutSeconds+a.Recovery.ReconciliationSeconds) * time.Second
	if !a.HardStop.Equal(a.Deadline.Add(allowance)) {
		return errors.New("managed hard stop differs from its immutable recovery allowance")
	}
	_, err := journalRecordLimit(a)
	return err
}

func journalRecordLimit(a managedJournalAdmission) (int64, error) {
	bounds, err := managedRecordLimits(a.Recovery, a.Runtime)
	return bounds.Scopes, err
}

func insertManagedAdmission(ctx context.Context, tx *sql.Tx, a managedJournalAdmission) error {
	if err := validateJournalAdmission(a); err != nil {
		return err
	}
	a.Deadline, a.HardStop = a.Deadline.UTC(), a.HardStop.UTC()
	body, err := json.Marshal(a)
	if err != nil || len(body) > 65536 {
		return errors.New("managed admission exceeds its encoded journal bound")
	}
	var existing []byte
	err = tx.QueryRowContext(ctx, "SELECT body FROM managed_admissions WHERE id=1").Scan(&existing)
	if err == nil {
		if !bytes.Equal(existing, body) {
			return errors.New("managed admission identity is immutable")
		}
		_, err = loadManagedActivation(ctx, tx, existing, a.Reservation.StateRoot, a.ObjectiveID)
		return err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read managed admission: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO managed_admissions(id,body) VALUES(1,?)", body); err != nil {
		return fmt.Errorf("persist managed admission: %w", err)
	}
	activation, err := json.Marshal(managedActivation{Version: 1, AdmissionSHA256: contentHash(body), Phase: "pending"})
	if err != nil {
		return fmt.Errorf("encode pending activation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO managed_activation(id,body) VALUES(1,?)", activation); err != nil {
		return fmt.Errorf("persist pending activation: %w", err)
	}
	initial, err := json.Marshal(managedSupervision{Revision: 1, DesiredState: "running"})
	if err != nil {
		return fmt.Errorf("encode initial supervision: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO managed_supervision(id,revision,body) VALUES(1,1,?)", initial); err != nil {
		return fmt.Errorf("persist initial supervision: %w", err)
	}
	return nil
}

func openManagedJournal(ctx context.Context, root, id string) (*managedJournal, error) {
	if !managedAbsolutePath(root) || !managedIdentifier(id) {
		return nil, errors.New("invalid managed state root or objective ID")
	}
	store, _, err := openManagedConnection(ctx, filepath.Join(root, id), false)
	if err != nil {
		return nil, err
	}
	j := &managedJournal{db: store.db, conn: store.conn}
	if err := j.loadAdmission(ctx, root, id); err != nil {
		_ = j.Close()
		return nil, err
	}
	return j, nil
}

func (j *managedJournal) loadAdmission(ctx context.Context, root, id string) error {
	if err := managedDurability(ctx, j.conn); err != nil {
		return err
	}
	if err := j.conn.QueryRowContext(ctx, "SELECT body FROM managed_admissions WHERE id=1").Scan(&j.identity); err != nil {
		return fmt.Errorf("read existing managed admission: %w", err)
	}
	if err := json.Unmarshal(j.identity, &j.admission); err != nil {
		return fmt.Errorf("decode managed admission: %w", err)
	}
	if err := validateJournalAdmission(j.admission); err != nil {
		return err
	}
	if j.admission.ObjectiveID != id || j.admission.Reservation.StateRoot != root {
		return errors.New("managed authority does not match its admitted objective and state root")
	}
	_, err := j.supervision(ctx)
	return err
}

// OpenScopeAuthority opens an existing, admitted objective without creating or
// initializing state. Peer PID/UID authentication remains the backend's duty.
func OpenScopeAuthority(ctx context.Context, stateDir, id string) (ScopeAuthority, error) {
	j, err := openManagedJournal(ctx, stateDir, id)
	if err != nil {
		return nil, err
	}
	return j, nil
}

// Close releases this authority's dedicated database connection.
func (j *managedJournal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := errors.Join(j.conn.Close(), j.db.Close()); err != nil {
		return fmt.Errorf("close managed journal: %w", err)
	}
	return nil
}

type managedRowReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadManagedSupervision(ctx context.Context, reader managedRowReader) (managedSupervision, error) {
	var state managedSupervision
	var body []byte
	var revision int64
	if err := reader.QueryRowContext(ctx, "SELECT revision,body FROM managed_supervision WHERE id=1").Scan(&revision, &body); err != nil {
		return state, fmt.Errorf("read supervision: %w", err)
	}
	if err := json.Unmarshal(body, &state); err != nil {
		return state, fmt.Errorf("decode supervision: %w", err)
	}
	if state.Revision != revision || revision <= 0 || state.Generation > math.MaxInt64 ||
		(state.DesiredState != "running" && state.DesiredState != "stopping" && state.DesiredState != "stopped") {
		return state, errors.New("invalid durable supervision state")
	}
	return state, nil
}

func (j *managedJournal) supervision(ctx context.Context) (managedSupervision, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return loadManagedSupervision(ctx, j.conn)
}

// Every ownership mutation uses its own BEGIN IMMEDIATE transaction and revision.
// The legacy objective snapshot is never read or rewritten by these transitions.
func (j *managedJournal) mutate(ctx context.Context, kind, ref string, change func(*sql.Tx, *managedSupervision) (bool, error)) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	tx, err := j.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin managed transition: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var identity []byte
	if err := tx.QueryRowContext(ctx, "SELECT body FROM managed_admissions WHERE id=1").Scan(&identity); err != nil {
		return fmt.Errorf("check bound admission: %w", err)
	}
	if !bytes.Equal(identity, j.identity) {
		return errors.New("bound managed admission changed")
	}
	state, err := loadManagedSupervision(ctx, tx)
	if err != nil {
		return err
	}
	changed, err := change(tx, &state)
	if err != nil || !changed {
		return err
	}
	limit, err := journalRecordLimit(j.admission)
	if err != nil || state.Revision >= 8*limit+16 {
		return errors.New("managed event budget exhausted")
	}
	state.Revision++
	body, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode managed transition: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE managed_supervision SET revision=?,body=? WHERE id=1", state.Revision, body); err != nil {
		return fmt.Errorf("persist supervision transition: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO managed_events(revision,kind,ref,body) VALUES(?,?,?,?)", state.Revision, kind, ref, body); err != nil {
		return fmt.Errorf("append managed event: %w", err)
	}
	if err := commitManagedTransaction(j.conn, tx); err != nil {
		return fmt.Errorf("commit managed transition: %w", err)
	}
	return nil
}

func reserveManagedToken(ctx context.Context, tx *sql.Tx, kind, token string, generation uint64, limit int64) (bool, error) {
	if !managedIdentifier(token) || generation == 0 || generation > math.MaxInt64 || limit <= 0 {
		return false, errors.New("invalid managed reservation")
	}
	var previous uint64
	err := tx.QueryRowContext(ctx, "SELECT generation FROM managed_reservations WHERE kind=? AND token=?", kind, token).Scan(&previous)
	if err == nil {
		if previous != generation {
			return false, errors.New("reservation token belongs to another generation")
		}
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("read managed reservation: %w", err)
	}
	var used int64
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM managed_reservations WHERE kind=?", kind).Scan(&used); err != nil {
		return false, fmt.Errorf("count managed reservations: %w", err)
	}
	if used >= limit {
		return false, fmt.Errorf("managed %s budget exhausted", kind)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO managed_reservations(kind,token,generation) VALUES(?,?,?)", kind, token, generation); err != nil {
		return false, fmt.Errorf("reserve managed %s: %w", kind, err)
	}
	return true, nil
}

func (j *managedJournal) beginGeneration(ctx context.Context, token string, now time.Time) (generation uint64, fresh bool, err error) {
	err = j.mutate(ctx, "supervisor", token, func(tx *sql.Tx, state *managedSupervision) (bool, error) {
		if !managedIdentifier(token) || state.DesiredState == "stopped" || !now.Before(j.admission.HardStop) {
			return false, errors.New("supervisor incarnation is no longer admitted")
		}
		err := tx.QueryRowContext(ctx, "SELECT generation FROM managed_reservations WHERE kind='supervisor' AND token=?", token).Scan(&generation)
		if err == nil {
			if state.SupervisorToken != token {
				return false, errors.New("supervisor incarnation was superseded")
			}
			generation = state.Generation
			return false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Errorf("read supervisor incarnation: %w", err)
		}
		generation = state.Generation + 1
		fresh, err = reserveManagedToken(ctx, tx, "supervisor", token, generation, int64(j.admission.Recovery.MaxSupervisorStarts))
		if err != nil {
			return false, err
		}
		state.SupervisorToken = token
		fresh, err = j.advanceManagedEpoch(ctx, tx, state, contentHash([]byte("supervisor:"+token)))
		if err != nil {
			return false, err
		}
		if !fresh {
			return false, errors.New("new supervisor requires a fresh ownership epoch")
		}
		generation = state.Generation
		return true, nil
	})
	return generation, fresh && err == nil, err
}

func (j *managedJournal) advanceEpoch(ctx context.Context, supervisorToken, epochToken string, now time.Time) (generation uint64, fresh bool, err error) {
	err = j.mutate(ctx, "ownership-epoch", epochToken, func(tx *sql.Tx, state *managedSupervision) (bool, error) {
		if state.SupervisorToken != supervisorToken || !managedIdentifier(supervisorToken) {
			return false, errors.New("ownership epoch requires the current admitted supervisor")
		}
		if err := activeManagedRecovery(state, state.Generation, now, j.admission.HardStop); err != nil {
			return false, err
		}
		var err error
		fresh, err = j.advanceManagedEpoch(ctx, tx, state, epochToken)
		generation = state.Generation
		return fresh, err
	})
	return generation, fresh && err == nil, err
}

func (j *managedJournal) advanceManagedEpoch(ctx context.Context, tx *sql.Tx, state *managedSupervision, token string) (bool, error) {
	var previous uint64
	err := tx.QueryRowContext(ctx, "SELECT generation FROM managed_reservations WHERE kind='epoch' AND token=?", token).Scan(&previous)
	if err == nil {
		if previous != state.Generation {
			return false, errors.New("ownership epoch token was superseded")
		}
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("read ownership epoch reservation: %w", err)
	}
	bounds, err := managedRecordLimits(j.admission.Recovery, j.admission.Runtime)
	if err != nil {
		return false, err
	}
	if _, err := reserveManagedToken(ctx, tx, "epoch", token, state.Generation+1, bounds.Epochs); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE managed_scopes SET phase='revoked' WHERE phase IN ('reserved','granted')"); err != nil {
		return false, fmt.Errorf("revoke previous ownership epoch: %w", err)
	}
	state.Generation++
	return true, nil
}

func (j *managedJournal) budgetLimit(kind string) int {
	switch kind {
	case "controller":
		return j.admission.Recovery.MaxControllerStarts
	case "reconciliation":
		return j.admission.Recovery.MaxReconciliationInvocations
	case "host":
		return j.admission.Runtime.MaxHostInvocations
	case "model":
		return j.admission.Runtime.MaxModelCalls
	case "tool":
		return j.admission.Runtime.MaxToolCalls
	default:
		return 0
	}
}

func activeManagedGeneration(state *managedSupervision, generation uint64, now, deadline time.Time) error {
	if state.DesiredState != "running" {
		return errors.New("managed work requires running supervision")
	}
	return activeManagedRecovery(state, generation, now, deadline)
}

func activeManagedRecovery(state *managedSupervision, generation uint64, now, deadline time.Time) error {
	if generation == 0 || generation != state.Generation || state.DesiredState == "stopped" || !now.Before(deadline) {
		return errors.New("managed ownership epoch is inactive, stale or expired")
	}
	return nil
}

func (j *managedJournal) reserveBudget(ctx context.Context, generation uint64, kind, token string, now time.Time) (bool, error) {
	var fresh bool
	err := j.mutate(ctx, "reserve-"+kind, token, func(tx *sql.Tx, state *managedSupervision) (bool, error) {
		active := activeManagedGeneration(state, generation, now, j.admission.Deadline)
		if kind == "reconciliation" {
			active = activeManagedRecovery(state, generation, now, j.admission.HardStop)
		}
		if active != nil {
			return false, active
		}
		if err := requireNoManagedCleanup(ctx, tx); err != nil {
			return false, err
		}
		if kind == "controller" && now.Before(state.NextStart) {
			return false, errors.New("controller restart backoff has not elapsed")
		}
		if kind == "controller" || kind == "reconciliation" {
			var live int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM managed_scopes WHERE phase!='quiescent'").Scan(&live); err != nil {
				return false, fmt.Errorf("check replacement quiescence: %w", err)
			}
			if live != 0 {
				return false, errors.New("replacement requires positive scope quiescence")
			}
		}
		var err error
		fresh, err = reserveManagedToken(ctx, tx, kind, token, generation, int64(j.budgetLimit(kind)))
		return fresh, err
	})
	return fresh && err == nil, err
}

func validateManagedScope(scope ScopeHandle) error {
	hex := regexp.MustCompile(`^[0-9a-f]{32}$`)
	name := regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,254}$`)
	if !hex.MatchString(scope.ID) || !hex.MatchString(scope.Nonce) || !managedIdentifier(scope.Kind) ||
		!managedIdentifier(scope.Backend) || !name.MatchString(scope.Name) || scope.Generation == 0 || scope.Generation > math.MaxInt64 {
		return errors.New("invalid exact managed scope identity")
	}
	return nil
}

func loadManagedScope(ctx context.Context, reader managedRowReader, scope ScopeHandle) (managedScopeRecord, error) {
	var record managedScopeRecord
	var body []byte
	// Slice the BLOB before scanning so corrupt oversized storage cannot force
	// an unbounded JSON allocation. The extra byte makes overflow detectable.
	err := reader.QueryRowContext(ctx, "SELECT id,nonce,backend,name,substr(CAST(body AS BLOB),1,2049),phase,pid,pgid,cleanup FROM managed_scopes WHERE id=?", scope.ID).
		Scan(&record.ID, &record.Nonce, &record.Backend, &record.Name, &body, &record.Phase, &record.PID, &record.PGID, &record.CleanupEvidence)
	if err != nil {
		return managedScopeRecord{}, fmt.Errorf("read managed scope: %w", err)
	}
	record, err = decodeManagedScopeRecord(record, body)
	if err != nil {
		return managedScopeRecord{}, err
	}
	if record.ScopeHandle != scope {
		return managedScopeRecord{}, errors.New("managed scope identity or generation does not match")
	}
	return record, nil
}

func (j *managedJournal) reserveScope(ctx context.Context, scope ScopeHandle, deadline, now time.Time) (bool, error) {
	if scope.Kind == "controller" {
		return false, errors.New("controller scope requires atomic typed reservation")
	}
	if err := validateManagedScope(scope); err != nil {
		return false, err
	}
	var fresh bool
	err := j.mutate(ctx, "scope-reserved", scope.ID, func(tx *sql.Tx, state *managedSupervision) (bool, error) {
		if err := j.checkScopeAdmission(ctx, tx, state, scope, deadline, now); err != nil {
			return false, err
		}
		record, err := loadManagedScope(ctx, tx, scope)
		if err == nil {
			if !record.Deadline.Equal(deadline) {
				return false, errors.New("scope deadline is immutable")
			}
			return false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		fresh, err = j.insertScope(ctx, tx, scope, deadline)
		return fresh, err
	})
	return fresh && err == nil, err
}

func (j *managedJournal) checkScopeAdmission(ctx context.Context, tx *sql.Tx, state *managedSupervision, scope ScopeHandle, deadline, now time.Time) error {
	if scope.Kind == "cleanup" {
		return errors.New("cleanup scope requires an atomic typed permit reservation")
	}
	bound := managedScopeLifetime(j.admission, scope.Kind)
	active := activeManagedGeneration(state, scope.Generation, now, j.admission.Deadline)
	if scope.Kind == "observer" {
		active = activeManagedRecovery(state, scope.Generation, now, bound)
	}
	if active != nil {
		return active
	}
	if err := requireNoManagedCleanup(ctx, tx); err != nil {
		return err
	}
	if !deadline.After(now) || deadline.After(bound) {
		return errors.New("scope deadline exceeds its original admitted bound")
	}
	if scope.Kind != "observer" {
		return nil
	}
	var reserved uint64
	if err := tx.QueryRowContext(ctx, "SELECT generation FROM managed_reservations WHERE kind='reconciliation' AND token=?", scope.ID).Scan(&reserved); err != nil {
		return fmt.Errorf("observer requires a prior reconciliation reservation: %w", err)
	}
	if reserved != scope.Generation {
		return errors.New("observer reservation belongs to another generation")
	}
	return nil
}

func (j *managedJournal) insertScope(ctx context.Context, tx *sql.Tx, scope ScopeHandle, deadline time.Time) (bool, error) {
	var used int64
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM managed_scopes").Scan(&used); err != nil {
		return false, fmt.Errorf("count managed scopes: %w", err)
	}
	limit, err := journalRecordLimit(j.admission)
	if err != nil || used >= limit {
		return false, errors.New("managed scope budget exhausted")
	}
	body, err := json.Marshal(struct {
		ScopeHandle
		Deadline time.Time
	}{scope, deadline.UTC()})
	if err != nil {
		return false, fmt.Errorf("encode scope reservation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO managed_scopes(id,nonce,backend,name,body,phase) VALUES(?,?,?,?,?,'reserved')", scope.ID, scope.Nonce, scope.Backend, scope.Name, body); err != nil {
		return false, fmt.Errorf("persist scope reservation: %w", err)
	}
	return true, nil
}

// RecordHello persists a grant only after the backend authenticates the real
// peer PID/UID. Claimed subprocess JSON is not an authenticated identity.
func (j *managedJournal) RecordHello(ctx context.Context, scope ScopeHandle, pid, pgid int) error {
	if pid <= 1 || pgid <= 1 || (scope.Kind == "controller" && pid != pgid) {
		return errors.New("authenticated host PID and PGID must exceed one")
	}
	return j.mutate(ctx, "scope-granted", scope.ID, func(tx *sql.Tx, state *managedSupervision) (bool, error) {
		record, err := loadManagedScope(ctx, tx, scope)
		if err != nil {
			return false, err
		}
		if err := j.activeScope(state, record, time.Now()); err != nil {
			return false, err
		}
		if err := j.validateScopeHello(ctx, tx, state, record); err != nil {
			return false, err
		}
		if record.Phase == "granted" && record.PID == pid && record.PGID == pgid {
			return false, nil
		}
		if record.Phase != "reserved" {
			return false, errors.New("managed scope cannot grant this handshake phase")
		}
		if _, err := tx.ExecContext(ctx, "UPDATE managed_scopes SET phase='granted',pid=?,pgid=? WHERE id=?", pid, pgid, scope.ID); err != nil {
			return false, fmt.Errorf("record authenticated scope grant: %w", err)
		}
		return true, nil
	})
}

func (j *managedJournal) validateScopeHello(ctx context.Context, tx *sql.Tx, state *managedSupervision, record managedScopeRecord) error {
	switch record.Kind {
	case "controller":
		return validateManagedControllerGrant(ctx, tx, j, state, record)
	case "cleanup":
		_, err := j.loadCleanupPermit(ctx, tx, state, record.ScopeHandle)
		return err
	default:
		return nil
	}
}

// Granted checks the bound objective, full scope identity, epoch and live phase.
func (j *managedJournal) Granted(ctx context.Context, scope ScopeHandle) (bool, error) {
	var granted bool
	err := j.mutate(ctx, "", "", func(tx *sql.Tx, state *managedSupervision) (bool, error) {
		record, err := loadManagedScope(ctx, tx, scope)
		if err != nil {
			return false, err
		}
		granted = record.Phase == "granted" && j.activeScope(state, record, time.Now()) == nil
		if granted && scope.Kind == "controller" {
			if err := validateManagedControllerGrant(ctx, tx, j, state, record); err != nil {
				granted = false
				return false, err
			}
		}
		if granted && scope.Kind == "cleanup" {
			_, err := j.loadCleanupPermit(ctx, tx, state, scope)
			if err != nil {
				granted = false
				return false, err
			}
		}
		return false, nil
	})
	return granted, err
}

// MarkQuiescent records positive backend retirement proof, including old epochs.
func (j *managedJournal) MarkQuiescent(ctx context.Context, scope ScopeHandle) error {
	return j.mutate(ctx, "scope-quiescent", scope.ID, func(tx *sql.Tx, _ *managedSupervision) (bool, error) {
		record, err := loadManagedScope(ctx, tx, scope)
		if err != nil || record.Phase == "quiescent" {
			return false, err
		}
		// A revoked older epoch remains addressable for exact-scope cleanup.
		// Only the trusted backend calls this after positive retirement proof.
		if _, err := tx.ExecContext(ctx, "UPDATE managed_scopes SET phase='quiescent',cleanup=? WHERE id=?", "trusted backend confirmed exact-scope quiescence", scope.ID); err != nil {
			return false, fmt.Errorf("record scope cleanup evidence: %w", err)
		}
		return true, nil
	})
}

// Observe exposes persisted authenticated ownership for exact-scope recovery.
// It never grants execution and permits observing revoked older epochs.
func (j *managedJournal) Observe(ctx context.Context, scope ScopeHandle) (ScopeObservation, error) {
	var observation ScopeObservation
	err := j.mutate(ctx, "", "", func(tx *sql.Tx, _ *managedSupervision) (bool, error) {
		record, err := loadManagedScope(ctx, tx, scope)
		if err != nil {
			return false, err
		}
		observation = ScopeObservation{Phase: record.Phase, PID: record.PID, PGID: record.PGID, Deadline: record.Deadline}
		return false, nil
	})
	return observation, err
}

func (j *managedJournal) scopes(ctx context.Context) ([]managedScopeRecord, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	rows, err := j.conn.QueryContext(ctx, "SELECT id,nonce,backend,name,substr(CAST(body AS BLOB),1,2049),phase,pid,pgid,cleanup FROM managed_scopes ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("list managed scopes: %w", err)
	}
	defer rows.Close()
	var records []managedScopeRecord
	for rows.Next() {
		var record managedScopeRecord
		var body []byte
		if err := rows.Scan(&record.ID, &record.Nonce, &record.Backend, &record.Name, &body, &record.Phase, &record.PID, &record.PGID, &record.CleanupEvidence); err != nil {
			return nil, fmt.Errorf("read scope listing: %w", err)
		}
		record, err = decodeManagedScopeRecord(record, body)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("finish scope listing: %w", err)
	}
	return records, nil
}

func (j *managedJournal) setDesired(ctx context.Context, generation uint64, desired, reason string, next time.Time) error {
	if (desired != "running" && desired != "stopping" && desired != "stopped") || len(reason) > 4096 || strings.ContainsRune(reason, 0) || next.After(j.admission.HardStop) {
		return errors.New("invalid bounded supervision outcome or backoff")
	}
	return j.mutate(ctx, "desired-state", desired, func(tx *sql.Tx, state *managedSupervision) (bool, error) {
		if state.Generation != generation || (state.DesiredState != "running" && desired == "running") {
			return false, errors.New("stale generation or terminal supervision state")
		}
		if state.DesiredState == desired && state.Reason == reason && state.NextStart.Equal(next) {
			return false, nil
		}
		if err := j.prepareDesired(ctx, tx, state, desired); err != nil {
			return false, err
		}
		state.DesiredState, state.Reason, state.NextStart = desired, reason, next.UTC()
		return true, nil
	})
}

func (j *managedJournal) prepareDesired(ctx context.Context, tx *sql.Tx, state *managedSupervision, desired string) error {
	if state.DesiredState == "stopped" {
		return errors.New("stopped supervision is permanently sealed")
	}
	if desired == "stopped" {
		if state.DesiredState != "stopping" {
			return errors.New("final stop requires stopping supervision")
		}
		quiet, err := reservationScopesQuiescent(ctx, tx, j.admission, state.Generation)
		if err != nil || !quiet {
			return errors.Join(errors.New("final stop requires every exact scope quiescent"), err)
		}
	}
	if desired == "stopping" && state.DesiredState == "running" {
		if _, err := tx.ExecContext(ctx, "UPDATE managed_scopes SET phase='revoked' WHERE phase IN ('reserved','granted')"); err != nil {
			return fmt.Errorf("revoke stopping supervision: %w", err)
		}
	}
	return nil
}

func managedScopeLifetime(a managedJournalAdmission, kind string) time.Time {
	switch kind {
	case "observer", "keeper", "cleanup":
		return a.HardStop
	default:
		return a.Deadline
	}
}

func (j *managedJournal) activeScope(state *managedSupervision, record managedScopeRecord, now time.Time) error {
	if record.Kind == "observer" || record.Kind == "cleanup" {
		return activeManagedRecovery(state, record.Generation, now, record.Deadline)
	}
	if err := activeManagedGeneration(state, record.Generation, now, j.admission.Deadline); err != nil {
		return err
	}
	return activeManagedGeneration(state, record.Generation, now, record.Deadline)
}
