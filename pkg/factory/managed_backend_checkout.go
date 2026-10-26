package factory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func (b *managedDeliveryBackend) checkout(ctx context.Context, stage string, attempt int, sha string, deadline time.Time) (string, error) {
	id, err := b.checkoutWithin(ctx, stage, attempt, sha, deadline)
	if err != nil {
		return "", interrupt("checkout", err)
	}
	return id, nil
}

func (b *managedDeliveryBackend) checkoutWithin(ctx context.Context, stage string, attempt int, sha string, deadline time.Time) (string, error) {
	if stage != "worker" || attempt <= 0 || !sourceOID(sha) || !deadline.Equal(b.owner.manifest.Deadline) {
		return "", errors.New("invalid checkout identity")
	}
	w, err := b.findWorkspace(ctx, stage, attempt, sha)
	if err != nil {
		return "", err
	}
	if w.ID != "" && w.Phase != "ready" {
		return "", errors.New("uncertain workspace requires reconciliation")
	}
	operationDeadline := b.workspaceDeadline(ctx, deadline, w)
	ctx, cancel := context.WithDeadline(ctx, operationDeadline)
	defer cancel()
	if w.ID == "" {
		w, err = reserveManagedWorkspace(ctx, b.owner, attempt, stage, sha, operationDeadline)
		if err != nil {
			return "", err
		}
	}
	if err := b.checkOwner(ctx); err != nil {
		return "", err
	}
	entries, err := b.git.export(ctx, sha)
	if err != nil {
		return "", err
	}
	if err := b.prepareWorkspace(ctx, w, entries); err != nil {
		return "", err
	}
	if err := b.checkReadyWorkspace(ctx, w.ID, sha); err != nil {
		return "", err
	}
	return w.ID, nil
}

func (b *managedDeliveryBackend) checkReadyWorkspace(ctx context.Context, id, sha string) error {
	return b.owner.journal.mutate(ctx, "", "", func(tx *sql.Tx, _ *managedSupervision) (bool, error) {
		current, e := workspaceCurrent(ctx, tx, b.owner, id)
		if e != nil {
			return false, e
		}
		if current.Phase != "ready" || current.SourceSHA != sha {
			return false, errors.New("workspace continuity changed")
		}
		return false, nil
	})
}

func (b *managedDeliveryBackend) workspaceDeadline(ctx context.Context, deadline time.Time, w managedWorkspace) time.Time {
	if w.ID != "" {
		return w.OperationDeadline
	}
	operationDeadline := time.Now().UTC().Add(time.Duration(b.owner.manifest.Spec.StepTimeoutSeconds) * time.Second)
	if deadline.Before(operationDeadline) {
		operationDeadline = deadline
	}
	if d, ok := ctx.Deadline(); ok && d.Before(operationDeadline) {
		operationDeadline = d.UTC()
	}
	return operationDeadline
}

func (b *managedDeliveryBackend) findWorkspace(ctx context.Context, stage string, attempt int, sha string) (managedWorkspace, error) {
	var w managedWorkspace
	err := b.owner.journal.mutate(ctx, "", "", func(tx *sql.Tx, _ *managedSupervision) (bool, error) {
		s, e := workspaceOwner(ctx, tx, b.owner)
		if e != nil {
			return false, e
		}
		if s.Attempts != attempt {
			return false, errors.New("checkout attempt changed")
		}
		var id string
		e = tx.QueryRowContext(ctx, "SELECT id FROM managed_workspaces WHERE controller_id=? AND attempt=? AND stage=?", b.owner.scope.ID, attempt, stage).Scan(&id)
		if errors.Is(e, sql.ErrNoRows) {
			return false, nil
		}
		if e != nil {
			return false, fmt.Errorf("lookup workspace: %w", e)
		}
		w, e = readManagedWorkspace(ctx, tx, b.owner, id)
		if e != nil {
			return false, e
		}
		if w.SourceSHA != sha {
			return false, errors.New("checkout source changed")
		}
		return false, nil
	})
	return w, err
}

func (b *managedDeliveryBackend) prepareWorkspace(ctx context.Context, w managedWorkspace, entries []sourceEntry) error {
	if w.Phase == "ready" {
		_, err := b.docker.resumeSource(ctx, w.Keeper, entries)
		return err
	}
	seed, err := b.docker.encodeSource(ctx, entries)
	if err != nil {
		return err
	}
	if err := beginManagedWorkspacePreparation(ctx, b.owner, w.ID, seed); err != nil {
		return err
	}
	source, err := b.docker.prepareSource(ctx, entries)
	if err != nil {
		return err
	}
	return bindManagedWorkspaceSource(ctx, b.owner, w.ID, source.Keeper)
}
