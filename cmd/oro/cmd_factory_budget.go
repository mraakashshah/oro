package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"oro/pkg/factory"
	"oro/pkg/storage"
)

// Admission reserves capacity before any objective or execution scope exists.
// Uncertain or failed subsequent admission deliberately retains that reservation;
// CLI initialization/abort must provide separate durable no-execution proof.
func admitFactoryHostBudget(ctx context.Context, stateDir string, spec factory.ManagedSpec, probe storage.DiskProbe) (factory.HostReservation, error) {
	return admitFactoryHostBudgetWithHooks(ctx, stateDir, spec, probe, factoryBudgetHooks{})
}

func admitFactoryHostBudgetWithHooks(ctx context.Context, stateDir string, spec factory.ManagedSpec, probe storage.DiskProbe, hooks factoryBudgetHooks) (factory.HostReservation, error) {
	specHash, err := prepareFactoryBudgetSpec(&spec)
	if err != nil {
		return factory.HostReservation{}, err
	}
	access, err := lockFactoryBudget(ctx, stateDir)
	if err != nil {
		return factory.HostReservation{}, err
	}
	defer access.close()
	if err := sameFactoryBudgetFilesystem(access, spec.Repository); err != nil {
		return factory.HostReservation{}, err
	}
	ledger, initialized, err := access.load()
	if err != nil {
		return factory.HostReservation{}, err
	}
	if previous, found := ledger.objective(spec.ID); found {
		if previous.Released || previous.SpecSHA256 != specHash {
			return factory.HostReservation{}, errors.New("host reservation objective identity cannot be reset or reused")
		}
		return previous.reservation(access.rootPath), nil
	}
	if len(ledger.Entries) >= factoryBudgetMaxEntries {
		return factory.HostReservation{}, errors.New("host reservation lifetime entry limit exhausted")
	}
	if err := checkFactoryBudget(ctx, access, ledger, spec, probe); err != nil {
		return factory.HostReservation{}, err
	}
	if err := ctx.Err(); err != nil {
		return factory.HostReservation{}, fmt.Errorf("host budget probe context: %w", err)
	}
	entry := factoryBudgetEntry{ID: randomFactoryBudgetID(), ObjectiveID: spec.ID, Bytes: spec.Runtime.HostRequiredBytes,
		ReserveBytes: spec.Runtime.HostReserveBytes, SpecSHA256: specHash}
	ledger.Entries = append(ledger.Entries, entry)
	slices.SortFunc(ledger.Entries, func(a, b factoryBudgetEntry) int { return strings.Compare(a.ObjectiveID, b.ObjectiveID) })
	ledger.Revision++
	if !initialized {
		if err := access.initialize(); err != nil {
			return factory.HostReservation{}, err
		}
	}
	if err := access.publish(ctx, ledger, hooks); err != nil {
		return factory.HostReservation{}, err
	}
	return entry.reservation(access.rootPath), nil
}

func prepareFactoryBudgetSpec(spec *factory.ManagedSpec) (string, error) {
	limits, err := factory.ManagedHostRetention(*spec)
	if err != nil {
		return "", fmt.Errorf("validate host retention: %w", err)
	}
	if spec.Runtime.HostRequiredBytes < limits.MinimumBytes {
		return "", fmt.Errorf("host required bytes must cover retained-byte bound %d", limits.MinimumBytes)
	}
	repository, err := filepath.EvalSymlinks(spec.Repository)
	if err != nil {
		return "", fmt.Errorf("resolve budget repository: %w", err)
	}
	spec.Repository = repository
	// Revalidate containment after canonicalization, before encoding the bounded
	// complete identity. A path alias cannot create a second logical reservation.
	if _, err := factory.ManagedHostRetention(*spec); err != nil {
		return "", fmt.Errorf("validate canonical host retention: %w", err)
	}
	data, err := json.Marshal(spec)
	if err != nil {
		return "", fmt.Errorf("encode host budget specification: %w", err)
	}
	return factoryBudgetHash(data), nil
}

func checkFactoryBudget(ctx context.Context, access *factoryBudgetAccess, ledger factoryBudgetSnapshot, spec factory.ManagedSpec, probe storage.DiskProbe) error {
	required, margin := spec.Runtime.HostRequiredBytes, spec.Runtime.HostReserveBytes
	for _, entry := range ledger.Entries {
		if entry.Released {
			continue
		}
		if entry.Bytes > math.MaxInt64-required {
			return errors.New("active host reservations overflow")
		}
		required += entry.Bytes
		margin = max(margin, entry.ReserveBytes)
	}
	root := access.rootPath
	if _, err := access.parent.Lstat(access.rootName); err == nil {
		root = filepath.Join(root, spec.ID)
	}
	// Charging existing reservations to RequiredBytes is equivalent to subtracting
	// them from observed free space, while preserving raw probe validation.
	decision, err := storage.CheckRuntimeBudget(ctx, probe, storage.RuntimeBudgetRequest{
		Root: root, RequiredBytes: required, MinFreeBytes: margin, Fresh: true,
	})
	if err != nil {
		return fmt.Errorf("check fresh factory host budget: %w", err)
	}
	if decision.Mode != storage.BudgetFresh {
		return fmt.Errorf("fresh factory host budget: %w", storage.ErrRuntimeBudgetDenied)
	}
	return nil
}

func sameFactoryBudgetFilesystem(access *factoryBudgetAccess, repository string) error {
	relative, err := filepath.Rel(repository, access.rootPath)
	if err != nil || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return errors.New("factory budget state root must be outside source")
	}
	repo, err := os.Stat(repository)
	if err != nil || !repo.IsDir() {
		return errors.New("factory budget repository is not a directory")
	}
	state, err := access.parent.Stat(access.rootName)
	if errors.Is(err, os.ErrNotExist) {
		state, err = access.parent.Stat(".")
	}
	if err != nil {
		return fmt.Errorf("inspect factory budget filesystem: %w", err)
	}
	repoStat, repoOK := repo.Sys().(*syscall.Stat_t)
	stateStat, stateOK := state.Sys().(*syscall.Stat_t)
	if !repoOK || !stateOK || repoStat.Dev != stateStat.Dev {
		return errors.New("repository and factory state must share one filesystem")
	}
	return nil
}

func randomFactoryBudgetID() string {
	var value [16]byte
	// crypto/rand.Read either fills the buffer or terminates on entropy failure.
	_, _ = rand.Read(value[:])
	return hex.EncodeToString(value[:])
}

func factoryBudgetHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type factoryBudgetReleaseQuery func(context.Context, string, string) (factory.HostReservation, bool, error)

func releaseFactoryHostReservation(ctx context.Context, stateDir, reservationID string) error {
	return releaseFactoryHostReservationWithQuery(ctx, stateDir, reservationID, factory.ReleasableHostReservation)
}

func releaseFactoryHostReservationWithQuery(ctx context.Context, stateDir, reservationID string, query factoryBudgetReleaseQuery) error {
	access, err := lockFactoryBudget(ctx, stateDir)
	if err != nil {
		return err
	}
	defer access.close()
	ledger, initialized, err := access.load()
	if err != nil {
		return err
	}
	if !initialized {
		return errors.New("host reservation authority is missing")
	}
	for i, entry := range ledger.Entries {
		if entry.ID != reservationID {
			continue
		}
		if entry.Released {
			return nil
		}
		exact, eligible, err := query(ctx, access.rootPath, entry.ObjectiveID)
		if err != nil {
			return fmt.Errorf("query host reservation release authority: %w", err)
		}
		if !eligible || exact != entry.reservation(access.rootPath) {
			return errors.New("host reservation release lacks exact stopped and quiescent authority")
		}
		ledger.Entries[i].Released = true
		ledger.Revision++
		return access.publish(ctx, ledger, factoryBudgetHooks{})
	}
	return errors.New("host reservation identity is unknown")
}
