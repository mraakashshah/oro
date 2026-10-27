package factory

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// reserveManagedController commits the counted startup intent before any launch.
func reserveManagedController(ctx context.Context, j *managedJournal, supervisorToken string, generation uint64) (ScopeHandle, error) {
	if j == nil || ctx.Err() != nil {
		return ScopeHandle{}, errors.Join(errors.New("controller requires live managed authority"), ctx.Err())
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return ScopeHandle{}, fmt.Errorf("allocate controller identity: %w", err)
	}
	scope := ScopeHandle{ID: hex.EncodeToString(random[:16]), Nonce: hex.EncodeToString(random[16:]), Kind: "controller", Backend: "launchd", Generation: generation}
	scope.Name = "dev.getoro.factory." + cleanupObjectiveHash(j.admission) + ".controller." + scope.ID + "." + scope.Nonce
	err := j.mutate(ctx, "controller-reserved", scope.ID, func(tx *sql.Tx, state *managedSupervision) (bool, error) {
		if err := checkManagedControllerAuthority(ctx, tx, j, state, supervisorToken, generation); err != nil {
			return false, err
		}
		fresh, err := reserveManagedToken(ctx, tx, "controller", scope.ID, generation, int64(j.admission.Recovery.MaxControllerStarts))
		if err != nil {
			return false, err
		}
		if !fresh {
			return false, errors.New("controller token already reserved")
		}
		return j.insertScope(ctx, tx, scope, j.admission.Deadline)
	})
	if err != nil {
		return ScopeHandle{}, err
	}
	return scope, nil
}

func checkManagedControllerAuthority(ctx context.Context, tx *sql.Tx, j *managedJournal, state *managedSupervision, supervisorToken string, generation uint64) error {
	activation, err := loadManagedActivation(ctx, tx, j.identity, j.admission.Reservation.StateRoot, j.admission.ObjectiveID)
	if err != nil {
		return err
	}
	if activation.Phase != "active" {
		return errors.New("controller requires active runtime")
	}
	if err := verifyActivationEvent(ctx, tx, activation.Runtime); err != nil {
		return err
	}
	now := time.Now()
	if err := activeManagedGeneration(state, generation, now, j.admission.Deadline); err != nil {
		return err
	}
	if state.SupervisorToken != supervisorToken || !managedIdentifier(supervisorToken) {
		return errors.New("controller requires current supervisor")
	}
	var reserved uint64
	if err := tx.QueryRowContext(ctx, "SELECT generation FROM managed_reservations WHERE kind='supervisor' AND token=?", supervisorToken).Scan(&reserved); err != nil || reserved == 0 || reserved > generation {
		return errors.Join(errors.New("controller requires supervisor reservation"), err)
	}
	if now.Before(state.NextStart) {
		return errors.New("controller restart backoff has not elapsed")
	}
	if err := requireNoManagedCleanup(ctx, tx); err != nil {
		return err
	}
	return scanManagedCleanupScopes(ctx, tx, func(record managedScopeRecord) error {
		if record.Phase != "quiescent" {
			return errors.New("controller requires positive scope quiescence")
		}
		return nil
	})
}

// validateManagedControllerGrant checks typed authority in the handshake transaction.
func validateManagedControllerGrant(ctx context.Context, tx *sql.Tx, j *managedJournal, state *managedSupervision, record managedScopeRecord) error {
	if err := validateManagedControllerGrantIdentity(ctx, tx, j, state, record); err != nil {
		return err
	}
	if err := activeManagedGeneration(state, record.Generation, time.Now(), j.admission.Deadline); err != nil {
		return err
	}
	return requireNoManagedCleanup(ctx, tx)
}

// validateManagedControllerGrantIdentity does not grant work or waive cleanup gates.
func validateManagedControllerGrantIdentity(ctx context.Context, tx *sql.Tx, j *managedJournal, state *managedSupervision, record managedScopeRecord) error {
	scope := record.ScopeHandle
	expected := "dev.getoro.factory." + cleanupObjectiveHash(j.admission) + ".controller." + scope.ID + "." + scope.Nonce
	if scope.Kind != "controller" || scope.Backend != "launchd" || scope.Name != expected || !record.Deadline.Equal(j.admission.Deadline) {
		return errors.New("controller scope differs from admitted identity")
	}
	if record.Phase == "granted" && record.PID != record.PGID {
		return errors.New("controller grant requires its authenticated process leader")
	}
	if err := validateManagedControllerActivation(ctx, tx, j); err != nil {
		return err
	}
	if !managedIdentifier(state.SupervisorToken) {
		return errors.New("controller requires current supervisor")
	}
	var reserved uint64
	if err := tx.QueryRowContext(ctx, "SELECT generation FROM managed_reservations WHERE kind='supervisor' AND token=?", state.SupervisorToken).Scan(&reserved); err != nil || reserved == 0 || reserved > scope.Generation {
		return errors.Join(errors.New("controller requires supervisor reservation"), err)
	}
	if err := tx.QueryRowContext(ctx, "SELECT generation FROM managed_reservations WHERE kind='controller' AND token=?", scope.ID).Scan(&reserved); err != nil || reserved != scope.Generation {
		return errors.Join(errors.New("controller requires exact counted reservation"), err)
	}
	return nil
}

func validateManagedControllerActivation(ctx context.Context, tx *sql.Tx, j *managedJournal) error {
	activation, err := loadManagedActivation(ctx, tx, j.identity, j.admission.Reservation.StateRoot, j.admission.ObjectiveID)
	if err != nil {
		return err
	}
	if activation.Phase != "active" {
		return errors.New("controller requires active runtime")
	}
	return verifyActivationEvent(ctx, tx, activation.Runtime)
}
