package factory

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const managedWorkspaceBytes = 2048

type managedWorkspace struct {
	Version             int         `json:"version"`
	AdmissionSHA256     string      `json:"admission_sha256"`
	RuntimeConfigSHA256 string      `json:"runtime_config_sha256"`
	ID                  string      `json:"id"`
	Controller          ScopeHandle `json:"controller"`
	Attempt             int         `json:"attempt"`
	Stage               string      `json:"stage"`
	SourceSHA           string      `json:"source_sha"`
	OperationDeadline   time.Time   `json:"operation_deadline"`
	Phase               string      `json:"phase"`
	SeedSHA256          string      `json:"seed_sha256"`
	SeedBytes           int64       `json:"seed_bytes"`
	Keeper              ScopeHandle `json:"keeper"`
}

func initManagedWorkspaceSchema(ctx context.Context, conn *sql.Conn) error {
	var exists, admissions int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='managed_workspaces'").Scan(&exists); err != nil {
		return fmt.Errorf("check workspace table: %w", err)
	}
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM managed_admissions").Scan(&admissions); err != nil {
		return fmt.Errorf("check workspace admission: %w", err)
	}
	if exists != 0 && admissions == 0 {
		var count int
		if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM managed_workspaces").Scan(&count); err != nil {
			return fmt.Errorf("check retained workspaces: %w", err)
		}
		if count != 0 {
			return errors.New("missing admission retains workspace history")
		}
	}
	return initManagedDockerSchema(ctx, conn, `CREATE TABLE IF NOT EXISTS managed_workspaces (
 id TEXT PRIMARY KEY, controller_id TEXT NOT NULL, attempt INTEGER NOT NULL, stage TEXT NOT NULL,
 keeper_id TEXT UNIQUE, body BLOB NOT NULL CHECK(length(body)<=2048), UNIQUE(controller_id,attempt,stage)
 );`)
}

func workspaceOwner(ctx context.Context, tx *sql.Tx, owner *managedControllerOwner) (snapshot, error) {
	if owner == nil || owner.journal == nil {
		return snapshot{}, errors.New("workspace requires managed owner")
	}
	current, err := validateActivationSnapshot(ctx, tx, owner.manifest)
	if err != nil {
		return snapshot{}, err
	}
	if err := owner.validateNext(ctx, tx, current); err != nil {
		return snapshot{}, err
	}
	if current.State != "running" || current.Attempts <= 0 {
		return snapshot{}, errors.New("workspace requires running attempt")
	}
	return current, nil
}

func workspaceIdentityValid(j *managedJournal, w managedWorkspace) bool {
	raw, err := hex.DecodeString(w.ID)
	return err == nil && len(raw) == 16 && hex.EncodeToString(raw) == w.ID && w.Version == 1 && w.AdmissionSHA256 == contentHash(j.identity) && managedKeeperID(w.RuntimeConfigSHA256) && w.Controller.Kind == "controller" && w.Attempt > 0 && w.Stage == "worker" && sourceOID(w.SourceSHA) && !w.OperationDeadline.IsZero() && w.OperationDeadline.Location() == time.UTC && !w.OperationDeadline.After(j.admission.Deadline)
}

func validateWorkspace(j *managedJournal, w managedWorkspace) error {
	if !workspaceIdentityValid(j, w) {
		return errors.New("invalid workspace identity")
	}
	switch w.Phase {
	case "reserved":
		if w.SeedSHA256 != "" || w.SeedBytes != 0 || w.Keeper != (ScopeHandle{}) {
			return errors.New("invalid reserved workspace")
		}
	case "preparing", "ready":
		var a hostRetentionArithmetic
		limit := a.sum(j.admission.Runtime.SourceBytes, a.product(1024, int64(j.admission.Runtime.MaxFiles)), 1536)
		if a.err != nil || !managedKeeperID(w.SeedSHA256) || w.SeedBytes <= 0 || w.SeedBytes > limit || (w.Phase == "preparing" && w.Keeper != (ScopeHandle{})) || (w.Phase == "ready" && w.Keeper == (ScopeHandle{})) {
			return errors.New("invalid prepared workspace")
		}
	default:
		return errors.New("invalid workspace phase")
	}
	return nil
}

func encodeManagedWorkspace(j *managedJournal, w managedWorkspace) ([]byte, error) {
	if err := validateWorkspace(j, w); err != nil {
		return nil, err
	}
	body, err := json.Marshal(w)
	if err != nil || len(body) > managedWorkspaceBytes {
		return nil, errors.Join(errors.New("workspace exceeds canonical bound"), err)
	}
	return body, nil
}

func readManagedWorkspace(ctx context.Context, tx *sql.Tx, owner *managedControllerOwner, id string) (managedWorkspace, error) {
	var w managedWorkspace
	if owner == nil || owner.journal == nil {
		return w, errors.New("workspace requires owner")
	}
	var controller, stage string
	var attempt, size int
	var keeper sql.NullString
	var body []byte
	if err := tx.QueryRowContext(ctx, "SELECT controller_id,attempt,stage,keeper_id,length(CAST(body AS BLOB)),substr(CAST(body AS BLOB),1,2049) FROM managed_workspaces WHERE id=?", id).Scan(&controller, &attempt, &stage, &keeper, &size, &body); err != nil {
		return w, fmt.Errorf("read workspace: %w", err)
	}
	if size <= 0 || size > managedWorkspaceBytes || size != len(body) || json.Unmarshal(body, &w) != nil {
		return managedWorkspace{}, errors.New("invalid workspace encoding")
	}
	canonical, err := encodeManagedWorkspace(owner.journal, w)
	if err != nil || !bytes.Equal(canonical, body) || !workspaceColumnsMatch(w, id, controller, attempt, stage, keeper, owner) {
		return managedWorkspace{}, errors.Join(errors.New("workspace identity differs"), err)
	}
	if w.Phase == "ready" {
		if err := validateWorkspaceSource(ctx, tx, owner, w, w.Keeper); err != nil {
			return managedWorkspace{}, err
		}
	}
	return w, nil
}

func workspaceColumnsMatch(w managedWorkspace, id, controller string, attempt int, stage string, keeper sql.NullString, owner *managedControllerOwner) bool {
	return w.ID == id && w.Controller.ID == controller && w.Attempt == attempt && w.Stage == stage && keeper.Valid == (w.Keeper != (ScopeHandle{})) && keeper.String == w.Keeper.ID && w.Controller == owner.scope && w.RuntimeConfigSHA256 == owner.runtime.SHA256
}

func workspaceDeadlineValid(hasDeadline bool, deadline, contextDeadline, now time.Time, owner *managedControllerOwner) bool {
	return hasDeadline && !deadline.IsZero() && deadline.Location() == time.UTC && deadline.After(now) && !deadline.After(contextDeadline) && !deadline.After(owner.journal.admission.Deadline) && !deadline.After(now.Add(time.Duration(owner.manifest.Spec.StepTimeoutSeconds)*time.Second))
}

func reserveManagedWorkspace(ctx context.Context, owner *managedControllerOwner, attempt int, stage, sourceSHA string, deadline time.Time) (managedWorkspace, error) {
	var w managedWorkspace
	if owner == nil || owner.journal == nil {
		return w, errors.New("workspace requires owner")
	}
	contextDeadline, ok := ctx.Deadline()
	now := time.Now()
	if !workspaceDeadlineValid(ok, deadline, contextDeadline, now, owner) {
		return w, errors.New("workspace deadline exceeds original bounds")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return w, fmt.Errorf("allocate workspace ID: %w", err)
	}
	w = managedWorkspace{Version: 1, AdmissionSHA256: contentHash(owner.journal.identity), RuntimeConfigSHA256: owner.runtime.SHA256, ID: hex.EncodeToString(random[:]), Controller: owner.scope, Attempt: attempt, Stage: stage, SourceSHA: sourceSHA, OperationDeadline: deadline, Phase: "reserved"}
	body, err := encodeManagedWorkspace(owner.journal, w)
	if err != nil {
		return managedWorkspace{}, err
	}
	limits, err := ManagedHostRetention(owner.manifest.Spec)
	if err != nil {
		return managedWorkspace{}, err
	}
	err = owner.journal.mutate(ctx, "workspace-reserved", w.ID, func(tx *sql.Tx, _ *managedSupervision) (bool, error) {
		state, e := workspaceOwner(ctx, tx, owner)
		if e != nil {
			return false, e
		}
		if state.Attempts != attempt || !time.Now().Before(deadline) {
			return false, errors.New("workspace attempt or deadline changed")
		}
		var count int64
		if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM managed_workspaces").Scan(&count); e != nil {
			return false, fmt.Errorf("count workspaces: %w", e)
		}
		if count >= limits.SourceVersions {
			return false, errors.New("workspace source version allowance exhausted")
		}
		_, e = tx.ExecContext(ctx, "INSERT INTO managed_workspaces(id,controller_id,attempt,stage,keeper_id,body) VALUES(?,?,?,?,NULL,?)", w.ID, w.Controller.ID, w.Attempt, w.Stage, body)
		if e != nil {
			return false, fmt.Errorf("reserve workspace: %w", e)
		}
		return true, nil
	})
	if err != nil {
		return managedWorkspace{}, err
	}
	return w, nil
}

func workspaceCurrent(ctx context.Context, tx *sql.Tx, owner *managedControllerOwner, id string) (managedWorkspace, error) {
	state, err := workspaceOwner(ctx, tx, owner)
	if err != nil {
		return managedWorkspace{}, err
	}
	w, err := readManagedWorkspace(ctx, tx, owner, id)
	if err != nil {
		return w, err
	}
	if state.Attempts != w.Attempt || !time.Now().Before(w.OperationDeadline) {
		return managedWorkspace{}, errors.New("workspace attempt or deadline changed")
	}
	return w, nil
}

func beginManagedWorkspacePreparation(ctx context.Context, owner *managedControllerOwner, id string, seed []byte) error {
	if owner == nil || owner.journal == nil {
		return errors.New("workspace requires owner")
	}
	return owner.journal.mutate(ctx, "workspace-preparing", id, func(tx *sql.Tx, _ *managedSupervision) (bool, error) {
		w, err := workspaceCurrent(ctx, tx, owner, id)
		if err != nil {
			return false, err
		}
		if w.Phase != "reserved" {
			return false, errors.New("workspace preparation already reserved")
		}
		var arithmetic hostRetentionArithmetic
		limit := arithmetic.sum(owner.journal.admission.Runtime.SourceBytes, arithmetic.product(1024, int64(owner.journal.admission.Runtime.MaxFiles)), 1536)
		if arithmetic.err != nil || len(seed) == 0 || int64(len(seed)) > limit {
			return false, errors.New("workspace seed exceeds archive bound")
		}
		w.Phase = "preparing"
		w.SeedSHA256 = contentHash(seed)
		w.SeedBytes = int64(len(seed))
		body, err := encodeManagedWorkspace(owner.journal, w)
		if err != nil {
			return false, err
		}
		result, err := tx.ExecContext(ctx, "UPDATE managed_workspaces SET body=? WHERE id=?", body, id)
		if err != nil {
			return false, fmt.Errorf("begin workspace preparation: %w", err)
		}
		n, err := result.RowsAffected()
		if err != nil || n != 1 {
			return false, errors.Join(errors.New("workspace preparation lost"), err)
		}
		return true, nil
	})
}

func bindManagedWorkspaceSource(ctx context.Context, owner *managedControllerOwner, id string, keeper ScopeHandle) error {
	if owner == nil || owner.journal == nil {
		return errors.New("workspace requires owner")
	}
	return owner.journal.mutate(ctx, "workspace-ready", id, func(tx *sql.Tx, _ *managedSupervision) (bool, error) {
		w, err := workspaceCurrent(ctx, tx, owner, id)
		if err != nil {
			return false, err
		}
		if w.Phase == "ready" && w.Keeper != keeper {
			return false, errors.New("workspace keeper changed")
		}
		if w.Phase != "preparing" && w.Phase != "ready" {
			return false, errors.New("workspace has no prepared seed")
		}
		if err := validateWorkspaceSource(ctx, tx, owner, w, keeper); err != nil {
			return false, err
		}
		if w.Phase == "ready" {
			return false, nil
		}
		w.Phase = "ready"
		w.Keeper = keeper
		body, err := encodeManagedWorkspace(owner.journal, w)
		if err != nil {
			return false, err
		}
		_, err = tx.ExecContext(ctx, "UPDATE managed_workspaces SET keeper_id=?,body=? WHERE id=?", keeper.ID, body, id)
		if err != nil {
			return false, fmt.Errorf("bind workspace source: %w", err)
		}
		return true, nil
	})
}

func validateWorkspaceSource(ctx context.Context, tx *sql.Tx, owner *managedControllerOwner, w managedWorkspace, keeper ScopeHandle) error {
	j := owner.journal
	source, err := readManagedDockerSource(ctx, tx, j, keeper)
	if err != nil {
		return err
	}
	if source.Phase != "seeded" || source.Keeper.Generation != owner.scope.Generation || source.RuntimeConfigSHA256 != w.RuntimeConfigSHA256 || !source.OperationDeadline.Equal(w.OperationDeadline) || source.SeedSHA256 != w.SeedSHA256 || source.SeedBytes != w.SeedBytes || source.StartedAt == "" {
		return errors.New("workspace requires exact seeded source")
	}
	bound, err := readManagedDockerKeeper(ctx, tx, j, keeper)
	if err != nil {
		return err
	}
	volume, err := readManagedDockerVolume(ctx, tx, j, source.Volume)
	if err != nil {
		return err
	}
	for _, scope := range []ScopeHandle{volume.CreateHost, bound.CreateHost, source.StartHost, source.SeedHost} {
		record, e := loadManagedScope(ctx, tx, scope)
		if e != nil {
			return e
		}
		if record.Phase != "quiescent" {
			return errors.New("workspace requires retired source hosts")
		}
	}
	return nil
}
