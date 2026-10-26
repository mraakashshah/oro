package factory

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"syscall"
	"time"
)

type managedControllerOwner struct {
	journal  *managedJournal
	manifest managedManifest
	scope    ScopeHandle
	runtime  ManagedRuntimeReference
}

func (o *managedControllerOwner) validateNext(ctx context.Context, tx *sql.Tx, next snapshot) error {
	if o.journal == nil {
		return errors.New("managed owner requires journal")
	}
	expected, err := o.validateAdmission(ctx, tx)
	if err != nil {
		return err
	}
	state, err := loadManagedSupervision(ctx, tx)
	if err != nil {
		return err
	}
	record, err := loadManagedScope(ctx, tx, o.scope)
	if err != nil {
		return err
	}
	if record.Phase != "granted" {
		return errors.New("managed owner requires granted controller")
	}
	if err := validateManagedControllerGrant(ctx, tx, o.journal, &state, record); err != nil {
		return err
	}
	pgid, err := syscall.Getpgid(0)
	if err != nil {
		return fmt.Errorf("read process group: %w", err)
	}
	if os.Getpid() != record.PID || pgid != record.PGID {
		return errors.New("managed controller does not own this process")
	}
	activation, err := loadManagedActivation(ctx, tx, expected, o.journal.admission.Reservation.StateRoot, o.journal.admission.ObjectiveID)
	if err != nil {
		return err
	}
	if activation.Runtime != o.runtime {
		return errors.New("managed owner runtime changed")
	}
	current, err := validateActivationSnapshot(ctx, tx, o.manifest)
	if err != nil {
		return err
	}
	expectedSnapshot := snapshotForManagedActivation(o.manifest)
	if err := validateManagedOwnerSnapshot(current, expectedSnapshot); err != nil {
		return err
	}
	return validateManagedOwnerSnapshot(next, expectedSnapshot)
}

func validateManagedOwnerSnapshot(current, expected snapshot) error {
	if !reflect.DeepEqual(current.Spec, expected.Spec) || current.PolicyHash != expected.PolicyHash || current.PolicyDigests != nil || !current.CreatedAt.Equal(expected.CreatedAt) || !current.Deadline.Equal(expected.Deadline) || current.CreatedAt.Location() != time.UTC || current.Deadline.Location() != time.UTC {
		return errors.New("managed owner snapshot identity changed")
	}
	return nil
}

func (o *managedControllerOwner) validateAdmission(ctx context.Context, tx *sql.Tx) ([]byte, error) {
	expected, err := json.Marshal(managedJournalIdentity(o.manifest))
	if err != nil {
		return nil, fmt.Errorf("encode owner admission: %w", err)
	}
	var persisted []byte
	if err := tx.QueryRowContext(ctx, "SELECT body FROM managed_admissions WHERE id=1").Scan(&persisted); err != nil {
		return nil, fmt.Errorf("read owner admission: %w", err)
	}
	if !bytes.Equal(expected, o.journal.identity) || !bytes.Equal(expected, persisted) {
		return nil, errors.New("managed owner admission identity changed")
	}
	return expected, nil
}
