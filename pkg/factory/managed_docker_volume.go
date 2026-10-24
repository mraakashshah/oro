package factory

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"time"
)

const managedDockerIntentBytes = 16384

type managedDockerVolumeIntent struct {
	Version             int                  `json:"version"`
	AdmissionSHA256     string               `json:"admission_sha256"`
	RuntimeConfigSHA256 string               `json:"runtime_config_sha256"`
	Volume              ScopeHandle          `json:"volume"`
	CreateHost          ScopeHandle          `json:"create_host"`
	WorkDeadline        time.Time            `json:"work_deadline"`
	CreateDeadline      time.Time            `json:"create_deadline"`
	Phase               string               `json:"phase"`
	Binding             DockerCleanupBinding `json:"binding"`
}

// The H reservation, both scopes and retained intent share one FULL transaction.
// No external request is permitted until dispatch wins its separate durable CAS.
func reserveManagedDockerVolume(ctx context.Context, manifest managedManifest, journal *managedJournal, generation uint64, createDeadline time.Time, runtimeConfigSHA256 string) (managedDockerVolumeIntent, error) {
	if journal == nil || ctx.Err() != nil || !reflect.DeepEqual(managedJournalIdentity(manifest), journal.admission) {
		return managedDockerVolumeIntent{}, errors.Join(errors.New("managed Docker volume requires its exact live admission"), ctx.Err())
	}
	if err := validateManagedManifest(manifest.Reservation.StateRoot, manifest.Spec.ID, manifest); err != nil {
		return managedDockerVolumeIntent{}, err
	}
	limits, err := ManagedHostRetention(manifest.Spec)
	if err != nil {
		return managedDockerVolumeIntent{}, err
	}
	intent := newManagedDockerVolumeIntent(journal, generation, createDeadline, runtimeConfigSHA256)
	body, err := encodeManagedDockerVolume(journal, intent)
	if err != nil {
		return managedDockerVolumeIntent{}, err
	}
	err = journal.mutate(ctx, "docker-volume-prepared", intent.Volume.ID, func(tx *sql.Tx, state *managedSupervision) (bool, error) {
		return insertManagedDockerVolume(ctx, tx, journal, state, intent, body, limits.SourceVersions)
	})
	if err != nil {
		return managedDockerVolumeIntent{}, err
	}
	return intent, nil
}

func insertManagedDockerVolume(ctx context.Context, tx *sql.Tx, journal *managedJournal, state *managedSupervision, intent managedDockerVolumeIntent, body []byte, sourceVersions int64) (bool, error) {
	now := time.Now()
	for _, scope := range []ScopeHandle{intent.CreateHost, intent.Volume} {
		deadline := intent.WorkDeadline
		if scope == intent.CreateHost {
			deadline = intent.CreateDeadline
		}
		if err := journal.checkScopeAdmission(ctx, tx, state, scope, deadline, now); err != nil {
			return false, err
		}
	}
	var used int64
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM managed_docker_intents").Scan(&used); err != nil {
		return false, fmt.Errorf("count retained Docker intents: %w", err)
	}
	if used >= sourceVersions {
		return false, errors.New("managed Docker source version budget exhausted")
	}
	if _, err := reserveManagedToken(ctx, tx, "host", intent.CreateHost.ID, intent.Volume.Generation, int64(journal.admission.Runtime.MaxHostInvocations)); err != nil {
		return false, err
	}
	if _, err := journal.insertScope(ctx, tx, intent.CreateHost, intent.CreateDeadline); err != nil {
		return false, err
	}
	if _, err := journal.insertScope(ctx, tx, intent.Volume, intent.WorkDeadline); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO managed_docker_intents(volume_scope_id,create_scope_id,body) VALUES(?,?,?)", intent.Volume.ID, intent.CreateHost.ID, body); err != nil {
		return false, fmt.Errorf("persist Docker volume intent: %w", err)
	}
	return true, nil
}

func newManagedDockerVolumeIntent(j *managedJournal, generation uint64, deadline time.Time, digest string) managedDockerVolumeIntent {
	// crypto/rand.Read is infallible on supported Go platforms and terminates on
	// entropy failure. The two complete handles are independent random identities.
	var random [64]byte
	_, _ = rand.Read(random[:])
	volume := ScopeHandle{ID: hex.EncodeToString(random[:16]), Nonce: hex.EncodeToString(random[16:32]), Generation: generation, Kind: "volume", Backend: "docker"}
	host := ScopeHandle{ID: hex.EncodeToString(random[32:48]), Nonce: hex.EncodeToString(random[48:]), Generation: generation, Kind: "host", Backend: "launchd"}
	objective := cleanupObjectiveHash(j.admission)
	volume.Name = "oro-factory-" + objective + "-volume-" + volume.ID + "-" + volume.Nonce
	host.Name = "dev.getoro.factory." + objective + ".host." + host.ID + "." + host.Nonce
	return managedDockerVolumeIntent{
		Version: 1, AdmissionSHA256: contentHash(j.identity), RuntimeConfigSHA256: digest,
		Volume: volume, CreateHost: host, WorkDeadline: j.admission.Deadline, CreateDeadline: deadline.UTC(), Phase: "prepared", Binding: managedDockerVolumeBinding(j.admission, volume),
	}
}

func loadManagedDockerVolume(ctx context.Context, journal *managedJournal, volume ScopeHandle) (managedDockerVolumeIntent, error) {
	if journal == nil {
		return managedDockerVolumeIntent{}, errors.New("managed Docker intent requires a journal")
	}
	var intent managedDockerVolumeIntent
	err := journal.mutate(ctx, "", "", func(tx *sql.Tx, _ *managedSupervision) (bool, error) {
		var err error
		intent, err = readManagedDockerVolume(ctx, tx, journal, volume)
		return false, err
	})
	if err != nil {
		return managedDockerVolumeIntent{}, err
	}
	return intent, nil
}

func dispatchManagedDockerVolume(ctx context.Context, journal *managedJournal, volume ScopeHandle, runtimeConfigSHA256 string) (managedDockerVolumeIntent, error) {
	var dispatched managedDockerVolumeIntent
	err := changeManagedDockerVolume(ctx, journal, volume, "docker-volume-dispatching", func(tx *sql.Tx, state *managedSupervision, intent *managedDockerVolumeIntent) (bool, error) {
		if err := activeManagedDockerCreate(ctx, tx, state, *intent); err != nil {
			return false, err
		}
		if intent.Phase != "prepared" || intent.RuntimeConfigSHA256 != runtimeConfigSHA256 {
			return false, errors.New("managed Docker intent is already dispatched or its runtime changed")
		}
		for _, scope := range []ScopeHandle{intent.Volume, intent.CreateHost} {
			record, err := loadManagedScope(ctx, tx, scope)
			if err != nil {
				return false, err
			}
			if record.Phase != "reserved" || record.PID != 0 || record.PGID != 0 {
				return false, errors.New("managed Docker dispatch requires both exact unused scopes")
			}
		}
		intent.Phase = "dispatching"
		dispatched = *intent
		return true, nil
	})
	if err != nil {
		return managedDockerVolumeIntent{}, err
	}
	return dispatched, nil
}

// Only the private live-call adapter may acknowledge, immediately after checking
// its own exact Invoke return. A generic HostResult is never an input here.
func acknowledgeManagedDockerVolume(ctx context.Context, journal *managedJournal, volume, createHost ScopeHandle) error {
	return changeManagedDockerVolume(ctx, journal, volume, "docker-volume-acknowledged", func(tx *sql.Tx, state *managedSupervision, intent *managedDockerVolumeIntent) (bool, error) {
		if err := activeManagedDockerCreate(ctx, tx, state, *intent); err != nil {
			return false, err
		}
		if intent.CreateHost != createHost || intent.Phase == "prepared" {
			return false, errors.New("managed Docker acknowledgment requires its dispatched host")
		}
		record, err := loadManagedScope(ctx, tx, createHost)
		if err != nil {
			return false, err
		}
		if record.Phase != "quiescent" {
			return false, errors.New("managed Docker acknowledgment requires positive host retirement")
		}
		if intent.Phase == "acknowledged" {
			return false, nil
		}
		intent.Phase = "acknowledged"
		return true, nil
	})
}

func bindManagedDockerVolume(ctx context.Context, journal *managedJournal, ownerGeneration uint64, volume ScopeHandle, observed DockerCleanupBinding) error {
	return changeManagedDockerVolume(ctx, journal, volume, "docker-volume-bound", func(tx *sql.Tx, state *managedSupervision, intent *managedDockerVolumeIntent) (bool, error) {
		if err := activeManagedRecovery(state, ownerGeneration, time.Now(), journal.admission.HardStop); err != nil {
			return false, err
		}
		if ownerGeneration < volume.Generation || intent.Phase == "prepared" {
			return false, errors.New("managed Docker lifetime requires a dispatched owned intent")
		}
		if err := requireNoManagedCleanup(ctx, tx); err != nil {
			return false, err
		}
		if observed.CreatedAt == "" {
			return false, errors.New("managed Docker lifetime requires a positive creation identity")
		}
		if err := validateCleanupCreatedAt(observed.CreatedAt); err != nil {
			return false, err
		}
		expected := intent.Binding
		expected.CreatedAt = observed.CreatedAt
		if !reflect.DeepEqual(expected, observed) || (intent.Binding.CreatedAt != "" && intent.Binding.CreatedAt != observed.CreatedAt) {
			return false, errors.New("managed Docker lifetime differs from exact immutable binding")
		}
		if intent.Binding.CreatedAt != "" {
			return false, nil
		}
		intent.Binding.CreatedAt = observed.CreatedAt
		return true, nil
	})
}

// Only never-dispatched prepared intents have a no-daemon-effect proof. Retiring
// every old host and changing generation prevents a late old-owner dispatch.
func retirePreparedManagedDockerVolume(ctx context.Context, journal *managedJournal, ownerGeneration uint64, volume ScopeHandle) error {
	if journal == nil {
		return errors.New("managed Docker retirement requires a journal")
	}
	return journal.mutate(ctx, "docker-volume-unused", volume.ID, func(tx *sql.Tx, state *managedSupervision) (bool, error) {
		intent, err := readManagedDockerVolume(ctx, tx, journal, volume)
		if err != nil {
			return false, err
		}
		if err := activeManagedRecovery(state, ownerGeneration, time.Now(), journal.admission.HardStop); err != nil {
			return false, err
		}
		if ownerGeneration <= volume.Generation || intent.Phase != "prepared" {
			return false, errors.New("managed Docker unused proof requires a revoked prepared generation")
		}
		if err := requireNoManagedCleanup(ctx, tx); err != nil {
			return false, err
		}
		if err := requireOldDockerHostsQuiescent(ctx, tx, journal.admission, ownerGeneration); err != nil {
			return false, err
		}
		return retireUnusedDockerScope(ctx, tx, volume)
	})
}

func retireUnusedDockerScope(ctx context.Context, tx *sql.Tx, volume ScopeHandle) (bool, error) {
	record, err := loadManagedScope(ctx, tx, volume)
	if err != nil {
		return false, err
	}
	if record.Phase == "quiescent" {
		return false, nil
	}
	if record.Phase != "revoked" || record.PID != 0 || record.PGID != 0 {
		return false, errors.New("managed Docker unused scope is not revoked and unstarted")
	}
	if _, err := tx.ExecContext(ctx, "UPDATE managed_scopes SET phase='quiescent',cleanup=? WHERE id=?", "never dispatched; prior host generations positively retired", volume.ID); err != nil {
		return false, fmt.Errorf("retire unused Docker volume: %w", err)
	}
	return true, nil
}

func requireOldDockerHostsQuiescent(ctx context.Context, tx *sql.Tx, admission managedJournalAdmission, generation uint64) error {
	// Filtering by a redundant column first could hide a live host whose column
	// disagrees with its body. Validate every complete record before filtering.
	rows, err := tx.QueryContext(ctx, "SELECT id,nonce,backend,name,body,phase,pid,pgid FROM managed_scopes")
	if err != nil {
		return fmt.Errorf("read prior Docker host scopes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var body []byte
		var record managedScopeRecord
		if err := rows.Scan(&record.ID, &record.Nonce, &record.Backend, &record.Name, &body, &record.Phase, &record.PID, &record.PGID); err != nil {
			return fmt.Errorf("read prior Docker host: %w", err)
		}
		scope, err := decodeDockerPriorScope(admission, record, body)
		if err != nil {
			return err
		}
		if scope.Backend != "docker" && scope.Generation < generation && record.Phase != "quiescent" {
			return errors.New("unused Docker volume requires every prior host retired")
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("finish prior Docker hosts: %w", err)
	}
	return nil
}
