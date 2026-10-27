package factory

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"time"
)

// ErrManagedRuntimePending identifies verified historical pending metadata; it does not authorize activation or execution.
var ErrManagedRuntimePending = errors.New("managed runtime is pending")

// ManagedRuntimeReference identifies a frozen runtime artifact; it grants no execution authority.
type (
	ManagedRuntimeReference struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	}
	managedActivation struct {
		Version         int                     `json:"version"`
		AdmissionSHA256 string                  `json:"admission_sha256"`
		Phase           string                  `json:"phase"`
		Runtime         ManagedRuntimeReference `json:"runtime"`
	}
)

func loadManagedActivation(ctx context.Context, reader managedRowReader, identity []byte, root, id string) (managedActivation, error) {
	var a managedActivation
	var body []byte
	var length int
	if err := reader.QueryRowContext(ctx, "SELECT length(CAST(body AS BLOB)),substr(CAST(body AS BLOB),1,2049) FROM managed_activation WHERE id=1").Scan(&length, &body); err != nil {
		return a, fmt.Errorf("read managed activation: %w", err)
	}
	if length > 2048 || length != len(body) || json.Unmarshal(body, &a) != nil {
		return a, errors.New("invalid managed activation encoding")
	}
	canonical, err := json.Marshal(a)
	if err != nil || !bytes.Equal(body, canonical) || a.Version != 1 || a.AdmissionSHA256 != contentHash(identity) {
		return a, errors.New("invalid managed activation identity")
	}
	switch a.Phase {
	case "pending":
		if a.Runtime != (ManagedRuntimeReference{}) {
			return a, errors.New("invalid pending runtime")
		}
	case "active":
		if !validManagedRuntimeReference(root, id, a.Runtime) {
			return a, errors.New("invalid active runtime")
		}
	default:
		return a, errors.New("invalid managed activation phase")
	}
	return a, nil
}

func validManagedRuntimeReference(root, id string, ref ManagedRuntimeReference) bool {
	return ref.Path == filepath.Join(root, id, "runtime", "runtime.json") && regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(ref.SHA256)
}

func openManagedActivation(ctx context.Context, root, id string) (*managedJournal, managedManifest, error) {
	if !managedAbsolutePath(root) || !managedIdentifier(id) {
		return nil, managedManifest{}, errors.New("invalid managed activation root or objective")
	}
	canonical, err := resolveAncestors(root)
	if err != nil || canonical != root {
		return nil, managedManifest{}, errors.New("managed activation root must be canonical")
	}
	manifest, err := loadManagedManifest(ctx, root, id)
	if err != nil {
		return nil, managedManifest{}, err
	}
	j, err := openManagedJournal(ctx, root, id)
	if err != nil {
		return nil, managedManifest{}, err
	}
	if err := verifyManagedAdmissionIdentity(ctx, j, manifest); err != nil {
		_ = j.Close()
		return nil, managedManifest{}, err
	}
	return j, manifest, nil
}

// ActivateManaged binds an immutable runtime reference to a pristine admission.
func ActivateManaged(ctx context.Context, stateDir, id string, ref ManagedRuntimeReference) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !validManagedRuntimeReference(stateDir, id, ref) {
		return errors.New("invalid managed runtime reference")
	}
	j, manifest, err := openManagedActivation(ctx, stateDir, id)
	if err != nil {
		return err
	}
	defer func() { _ = j.Close() }()
	return j.mutate(ctx, "runtime-activated", ref.SHA256, func(tx *sql.Tx, state *managedSupervision) (bool, error) {
		return activateManagedTransaction(ctx, tx, j, manifest, state, ref)
	})
}

// ReadManagedRuntimeReference returns historical active identity without granting execution.
func ReadManagedRuntimeReference(ctx context.Context, stateDir, id string) (ManagedRuntimeReference, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	j, manifest, err := openManagedActivation(ctx, stateDir, id)
	if err != nil {
		return ManagedRuntimeReference{}, err
	}
	defer func() { _ = j.Close() }()
	var ref ManagedRuntimeReference
	err = j.mutate(ctx, "", "", func(tx *sql.Tx, _ *managedSupervision) (bool, error) {
		a, err := loadManagedActivation(ctx, tx, j.identity, stateDir, id)
		if err != nil {
			return false, err
		}
		if _, err := validateActivationSnapshot(ctx, tx, manifest); err != nil {
			return false, err
		}
		if err := verifyActivationHistory(ctx, tx, manifest, false); err != nil {
			return false, err
		}
		if a.Phase != "active" {
			return false, ErrManagedRuntimePending
		}
		if err := verifyActivationEvent(ctx, tx, a.Runtime); err != nil {
			return false, err
		}
		ref = a.Runtime
		return false, nil
	})
	return ref, err
}

func snapshotForManagedActivation(manifest managedManifest) snapshot {
	return snapshot{Spec: managedSnapshotSpec(manifest.Spec), PolicyHash: manifest.PolicyHash, CreatedAt: manifest.CreatedAt, Deadline: manifest.Deadline, Revision: 1, State: "pending"}
}

func verifyActivationHistory(ctx context.Context, tx *sql.Tx, manifest managedManifest, pristine bool) error {
	expected, err := json.Marshal(snapshotForManagedActivation(manifest))
	if err != nil {
		return fmt.Errorf("encode initial delivery event: %w", err)
	}
	var state string
	var body []byte
	var length int
	if err := tx.QueryRowContext(ctx, "SELECT state,length(CAST(body AS BLOB)),substr(CAST(body AS BLOB),1,?) FROM events WHERE revision=1", len(expected)+1).Scan(&state, &length, &body); err != nil {
		return fmt.Errorf("read initial delivery event: %w", err)
	}
	if state != "pending" || length != len(expected) || !bytes.Equal(body, expected) {
		return errors.New("invalid initial delivery event")
	}
	if pristine {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM events").Scan(&n); err != nil || n != 1 {
			return errors.New("managed activation requires original delivery history")
		}
	}
	for _, table := range []string{"managed_reservations", "managed_scopes", "managed_events", "managed_cleanup_permits", "managed_docker_intents", "managed_docker_keepers", "managed_docker_sources", "managed_workspaces", "managed_docker_candidates"} {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			return fmt.Errorf("count %s: %w", table, err)
		}
		if pristine && n != 0 {
			return errors.New("managed activation requires empty execution history")
		}
	}
	return validateCandidateHistory(ctx, tx)
}

func activateManagedTransaction(ctx context.Context, tx *sql.Tx, j *managedJournal, manifest managedManifest, state *managedSupervision, ref ManagedRuntimeReference) (bool, error) {
	a, err := loadManagedActivation(ctx, tx, j.identity, j.admission.Reservation.StateRoot, j.admission.ObjectiveID)
	if err != nil {
		return false, err
	}
	current, err := validateActivationSnapshot(ctx, tx, manifest)
	if err != nil {
		return false, err
	}
	if err := verifyActivationHistory(ctx, tx, manifest, a.Phase == "pending"); err != nil {
		return false, err
	}
	if a.Phase == "active" {
		if err := verifyActivationEvent(ctx, tx, a.Runtime); err != nil {
			return false, err
		}
		if a.Runtime != ref {
			return false, errors.New("managed runtime reference is immutable")
		}
		return false, nil
	}
	if !time.Now().Before(j.admission.Deadline) || *state != (managedSupervision{Revision: 1, DesiredState: "running"}) {
		return false, errors.New("managed activation requires pristine live supervision")
	}
	if !reflect.DeepEqual(current, snapshotForManagedActivation(manifest)) {
		return false, errors.New("managed activation requires pristine delivery")
	}
	a.Phase = "active"
	a.Runtime = ref
	body, err := json.Marshal(a)
	if err != nil || len(body) > 2048 {
		return false, errors.New("managed activation exceeds bound")
	}
	if _, err := tx.ExecContext(ctx, "UPDATE managed_activation SET body=? WHERE id=1", body); err != nil {
		return false, fmt.Errorf("persist activation: %w", err)
	}
	return true, nil
}

func validateActivationSnapshot(ctx context.Context, tx *sql.Tx, manifest managedManifest) (snapshot, error) {
	var current snapshot
	var revision, length int64
	var body []byte
	if err := tx.QueryRowContext(ctx, "SELECT revision,length(CAST(body AS BLOB)),substr(CAST(body AS BLOB),1,?) FROM snapshots WHERE id=1", managedJournalMaxDatabaseBytes+1).Scan(&revision, &length, &body); err != nil {
		return current, fmt.Errorf("read activation snapshot: %w", err)
	}
	if length <= 0 || length > managedJournalMaxDatabaseBytes || int64(len(body)) != length || json.Unmarshal(body, &current) != nil {
		return current, errors.New("invalid activation snapshot encoding")
	}
	canonical, err := json.Marshal(current)
	if err != nil || !bytes.Equal(body, canonical) || revision <= 0 || current.Revision != revision {
		return current, errors.New("invalid activation snapshot revision or encoding")
	}
	expected := snapshotForManagedActivation(manifest)
	if current.PolicyHash != expected.PolicyHash || !reflect.DeepEqual(current.Spec, expected.Spec) || !current.CreatedAt.Equal(expected.CreatedAt) || !current.Deadline.Equal(expected.Deadline) {
		return current, errors.New("activation snapshot identity differs from admission")
	}
	return current, nil
}

func verifyActivationEvent(ctx context.Context, tx *sql.Tx, ref ManagedRuntimeReference) error {
	expected, err := json.Marshal(managedSupervision{Revision: 2, DesiredState: "running"})
	if err != nil {
		return fmt.Errorf("encode activation event: %w", err)
	}
	var kind, digest string
	var length int
	var body []byte
	if err := tx.QueryRowContext(ctx, "SELECT kind,ref,length(CAST(body AS BLOB)),substr(CAST(body AS BLOB),1,?) FROM managed_events WHERE revision=2", len(expected)+1).Scan(&kind, &digest, &length, &body); err != nil {
		return fmt.Errorf("read activation event: %w", err)
	}
	if kind != "runtime-activated" || digest != ref.SHA256 || length != len(expected) || !bytes.Equal(body, expected) {
		return errors.New("invalid managed activation event")
	}
	return nil
}
