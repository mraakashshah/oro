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
	"time"
)

// ManagedAdmissionView contains immutable producer inputs, never execution authority.
type ManagedAdmissionView struct {
	StateRoot, ObjectiveID, PolicyHash string
	GitPath, GitSHA256                 string
	ModelPath, ModelSHA256             string
	Repository, TargetRef              string
	Deadline, HardStop                 time.Time
	CleanupTimeoutSeconds              int
	DockerContext, ImageDigest         string
}

// ReadManagedAdmission verifies existing frozen artifacts and committed admission
// using one overall 30-second context. The root must already be canonical. Reads
// remain valid after expiry or stop, and never authorize startup or bypass the
// activation fence. Original specification and executable paths are not read.
func ReadManagedAdmission(ctx context.Context, stateDir, id string) (ManagedAdmissionView, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return ManagedAdmissionView{}, fmt.Errorf("read managed admission canceled: %w", err)
	}
	if !managedAbsolutePath(stateDir) || !managedIdentifier(id) {
		return ManagedAdmissionView{}, errors.New("invalid managed admission root or objective")
	}
	root, err := resolveAncestors(stateDir)
	if err != nil || root != stateDir {
		return ManagedAdmissionView{}, errors.New("managed admission root must be canonical")
	}
	manifest, err := loadManagedManifest(ctx, root, id)
	if err != nil {
		return ManagedAdmissionView{}, err
	}
	journal, err := openManagedJournal(ctx, root, id)
	if err != nil {
		return ManagedAdmissionView{}, err
	}
	defer func() { _ = journal.Close() }()
	if err := verifyManagedAdmissionIdentity(ctx, journal, manifest); err != nil {
		return ManagedAdmissionView{}, err
	}
	if err := ctx.Err(); err != nil {
		return ManagedAdmissionView{}, fmt.Errorf("read managed admission canceled: %w", err)
	}
	return ManagedAdmissionView{
		StateRoot: root, ObjectiveID: id, PolicyHash: manifest.PolicyHash,
		GitPath: filepath.Join(root, id, "git"), GitSHA256: manifest.Programs["git"].Digest,
		ModelPath: filepath.Join(root, id, "model"), ModelSHA256: manifest.Programs["model"].Digest,
		Repository: manifest.Spec.Repository, TargetRef: "refs/heads/" + manifest.Spec.TargetBranch,
		Deadline: manifest.Deadline, HardStop: manifest.HardStop,
		CleanupTimeoutSeconds: manifest.Spec.Recovery.CleanupTimeoutSeconds,
		DockerContext:         manifest.Spec.Runtime.DockerContext, ImageDigest: manifest.Spec.Runtime.ImageDigest,
	}, nil
}

func verifyManagedAdmissionIdentity(ctx context.Context, journal *managedJournal, manifest managedManifest) error {
	expected, err := json.Marshal(managedJournalIdentity(manifest))
	if err != nil {
		return fmt.Errorf("encode managed admission identity: %w", err)
	}
	return journal.mutate(ctx, "", "", func(tx *sql.Tx, _ *managedSupervision) (bool, error) {
		// mutate rereads the exact journal identity inside this transaction.
		if !bytes.Equal(expected, journal.identity) {
			return false, errors.New("managed journal identity differs from frozen admission")
		}
		state, err := decodeSnapshot(tx.QueryRowContext(ctx, "SELECT body FROM snapshots WHERE id=1"))
		if err != nil {
			return false, err
		}
		if state.PolicyHash != manifest.PolicyHash || !reflect.DeepEqual(state.Spec, managedSnapshotSpec(manifest.Spec)) ||
			!state.CreatedAt.Equal(manifest.CreatedAt) || !state.Deadline.Equal(manifest.Deadline) {
			return false, errors.New("managed snapshot identity differs from frozen admission")
		}
		return false, nil // Read-only ownership transaction: no revision or event.
	})
}
