package factory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

// RunManagedController drives an already activated and granted controller.
func RunManagedController(ctx context.Context, stateDir, id string, generation uint64, token string, host HostRuntime, reader ManagedDockerConfigReader) (Status, error) {
	j, manifest, err := openManagedActivation(ctx, stateDir, id)
	if err != nil {
		return Status{}, err
	}
	defer func() { _ = j.Close() }()
	unlock, err := lockObjective(filepath.Join(stateDir, id))
	if err != nil {
		return Status{}, err
	}
	defer func() { _ = unlock() }()
	store, _, err := openManagedConnection(ctx, filepath.Join(stateDir, id), false)
	if err != nil {
		return Status{}, err
	}
	defer func() { _ = store.close() }()
	owner, current, err := resolveManagedController(ctx, j, manifest, stateDir, id, generation, token)
	if err != nil {
		return Status{}, err
	}
	store.owner = owner
	if current.State != "pending" {
		return snapshotStatus(current), &managedInterruptionError{stage: "startup", cause: fmt.Errorf("state %s requires reconciliation", current.State)}
	}
	backend, err := newManagedDeliveryBackend(ctx, owner, host, reader)
	if err != nil {
		return snapshotStatus(current), err
	}
	c := controller{store: store, backend: backend, state: current}
	err = c.drive(ctx)
	return snapshotStatus(c.state), err
}

func resolveManagedController(ctx context.Context, j *managedJournal, manifest managedManifest, stateDir, id string, generation uint64, token string) (*managedControllerOwner, snapshot, error) {
	var owner *managedControllerOwner
	var current snapshot
	err := j.mutate(ctx, "", "", func(tx *sql.Tx, _ *managedSupervision) (bool, error) {
		var body []byte
		if e := tx.QueryRowContext(ctx, "SELECT substr(CAST(body AS BLOB),1,2049) FROM managed_scopes WHERE id=?", token).Scan(&body); e != nil {
			return false, fmt.Errorf("read controller identity: %w", e)
		}
		var identity struct {
			ScopeHandle
			Deadline time.Time
		}
		if e := json.Unmarshal(body, &identity); e != nil {
			return false, fmt.Errorf("decode controller identity: %w", e)
		}
		if identity.ID != token || identity.Kind != "controller" || identity.Generation != generation {
			return false, errors.New("invalid managed controller token")
		}
		record, e := loadManagedScope(ctx, tx, identity.ScopeHandle)
		if e != nil {
			return false, e
		}
		activation, e := loadManagedActivation(ctx, tx, j.identity, stateDir, id)
		if e != nil {
			return false, e
		}
		owner = &managedControllerOwner{journal: j, manifest: manifest, scope: record.ScopeHandle, runtime: activation.Runtime}
		current, e = validateActivationSnapshot(ctx, tx, manifest)
		if e != nil {
			return false, e
		}
		return false, owner.validateNext(ctx, tx, current)
	})
	return owner, current, err
}
