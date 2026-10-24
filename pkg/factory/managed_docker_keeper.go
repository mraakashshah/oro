package factory

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"time"
)

type managedDockerKeeperIntent struct {
	Version             int                  `json:"version"`
	AdmissionSHA256     string               `json:"admission_sha256"`
	RuntimeConfigSHA256 string               `json:"runtime_config_sha256"`
	Volume              ScopeHandle          `json:"volume"`
	Keeper              ScopeHandle          `json:"keeper"`
	CreateHost          ScopeHandle          `json:"create_host"`
	WorkDeadline        time.Time            `json:"work_deadline"`
	CreateDeadline      time.Time            `json:"create_deadline"`
	GuardDeadline       time.Time            `json:"guard_deadline"`
	Phase               string               `json:"phase"`
	Binding             DockerCleanupBinding `json:"binding"`
}

func managedKeeperImage(image string) bool {
	return regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(image)
}
func managedKeeperID(id string) bool { return regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(id) }

func newManagedDockerKeeperIntent(j *managedJournal, generation uint64, volume ScopeHandle, deadline time.Time, digest string) managedDockerKeeperIntent {
	// Reuse the existing cryptographic handle generator; no volume is reserved.
	generated := newManagedDockerVolumeIntent(j, generation, deadline, digest)
	keeper := generated.Volume
	keeper.Kind = "keeper"
	keeper.Name = "oro-factory-" + cleanupObjectiveHash(j.admission) + "-keeper-" + keeper.ID + "-" + keeper.Nonce
	intent := managedDockerKeeperIntent{Version: 1, AdmissionSHA256: contentHash(j.identity), RuntimeConfigSHA256: digest, Volume: volume, Keeper: keeper, CreateHost: generated.CreateHost, WorkDeadline: j.admission.Deadline, CreateDeadline: deadline.UTC(), GuardDeadline: j.admission.HardStop, Phase: "prepared"}
	intent.Binding = managedDockerKeeperBinding(j.admission, keeper)
	return intent
}

func managedDockerKeeperBinding(a managedJournalAdmission, keeper ScopeHandle) DockerCleanupBinding {
	labels := managedDockerVolumeBinding(a, keeper).Labels
	labels["dev.getoro.factory.kind"] = "keeper"
	return DockerCleanupBinding{Kind: "container", Context: a.Runtime.DockerContext, Name: keeper.Name, ImageID: a.Runtime.ImageDigest, Labels: labels}
}

func reserveManagedDockerKeeper(ctx context.Context, manifest managedManifest, journal *managedJournal, generation uint64, volume ScopeHandle, createDeadline time.Time, runtimeConfigSHA256 string) (managedDockerKeeperIntent, error) {
	if journal == nil || ctx.Err() != nil || !reflect.DeepEqual(managedJournalIdentity(manifest), journal.admission) {
		return managedDockerKeeperIntent{}, errors.Join(errors.New("keeper requires its exact live admission"), ctx.Err())
	}
	if err := validateManagedManifest(manifest.Reservation.StateRoot, manifest.Spec.ID, manifest); err != nil {
		return managedDockerKeeperIntent{}, err
	}
	limits, err := ManagedHostRetention(manifest.Spec)
	if err != nil {
		return managedDockerKeeperIntent{}, err
	}
	intent := newManagedDockerKeeperIntent(journal, generation, volume, createDeadline, runtimeConfigSHA256)
	body, err := encodeManagedDockerKeeper(journal, intent)
	if err != nil {
		return managedDockerKeeperIntent{}, err
	}
	err = journal.mutate(ctx, "docker-keeper-prepared", intent.Keeper.ID, func(tx *sql.Tx, state *managedSupervision) (bool, error) {
		return insertManagedDockerKeeper(ctx, tx, journal, state, intent, body, limits.SourceVersions)
	})
	if err != nil {
		return managedDockerKeeperIntent{}, err
	}
	return intent, nil
}

func insertManagedDockerKeeper(ctx context.Context, tx *sql.Tx, j *managedJournal, state *managedSupervision, intent managedDockerKeeperIntent, body []byte, sourceVersions int64) (bool, error) {
	if err := requireManagedKeeperVolume(ctx, tx, j, intent); err != nil {
		return false, err
	}
	record, err := loadManagedScope(ctx, tx, intent.Volume)
	if err != nil {
		return false, err
	}
	if record.Phase != "reserved" || record.PID != 0 || record.PGID != 0 {
		return false, errors.New("keeper source volume is no longer live")
	}
	for _, scope := range []ScopeHandle{intent.CreateHost, intent.Keeper} {
		deadline := intent.GuardDeadline
		if scope == intent.CreateHost {
			deadline = intent.CreateDeadline
		}
		if err := j.checkScopeAdmission(ctx, tx, state, scope, deadline, time.Now()); err != nil {
			return false, err
		}
	}
	var used int64
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM managed_docker_keepers").Scan(&used); err != nil {
		return false, fmt.Errorf("count retained keepers: %w", err)
	}
	if used >= sourceVersions {
		return false, errors.New("keeper source version allowance exhausted")
	}
	if _, err := reserveManagedToken(ctx, tx, "host", intent.CreateHost.ID, intent.Keeper.Generation, int64(j.admission.Runtime.MaxHostInvocations)); err != nil {
		return false, err
	}
	if _, err := j.insertScope(ctx, tx, intent.CreateHost, intent.CreateDeadline); err != nil {
		return false, err
	}
	if _, err := j.insertScope(ctx, tx, intent.Keeper, intent.GuardDeadline); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO managed_docker_keepers(keeper_scope_id,volume_scope_id,create_scope_id,body) VALUES(?,?,?,?)", intent.Keeper.ID, intent.Volume.ID, intent.CreateHost.ID, body); err != nil {
		return false, fmt.Errorf("persist keeper intent: %w", err)
	}
	return true, nil
}

func encodeManagedDockerKeeper(j *managedJournal, intent managedDockerKeeperIntent) ([]byte, error) {
	if err := validateManagedDockerKeeper(j, intent); err != nil {
		return nil, err
	}
	body, err := json.Marshal(intent)
	if err != nil || len(body) > managedDockerIntentBytes {
		return nil, errors.Join(errors.New("keeper intent exceeds canonical byte bound"), err)
	}
	return body, nil
}

func validateManagedDockerKeeper(j *managedJournal, intent managedDockerKeeperIntent) error {
	if intent.Version != 1 || intent.AdmissionSHA256 != contentHash(j.identity) || !managedKeeperID(intent.RuntimeConfigSHA256) || !managedKeeperImage(j.admission.Runtime.ImageDigest) {
		return errors.New("keeper version or frozen identity differs")
	}
	if err := validateManagedKeeperHandles(j.admission, intent); err != nil {
		return err
	}
	if err := validateManagedKeeperPhase(j.admission, intent); err != nil {
		return err
	}
	if err := validateCleanupCreatedAt(intent.Binding.CreatedAt); err != nil {
		return err
	}
	expected := managedDockerKeeperBinding(j.admission, intent.Keeper)
	expected.ContainerID = intent.Binding.ContainerID
	expected.CreatedAt = intent.Binding.CreatedAt
	if !reflect.DeepEqual(expected, intent.Binding) {
		return errors.New("keeper binding differs from derived ownership")
	}
	return nil
}

func validateManagedKeeperPhase(admission managedJournalAdmission, intent managedDockerKeeperIntent) error {
	if !intent.WorkDeadline.Equal(admission.Deadline) || !intent.GuardDeadline.Equal(admission.HardStop) || intent.WorkDeadline.Location() != time.UTC || intent.GuardDeadline.Location() != time.UTC || intent.CreateDeadline.IsZero() || intent.CreateDeadline.Location() != time.UTC || intent.CreateDeadline.After(intent.WorkDeadline) {
		return errors.New("keeper original deadlines differ")
	}
	switch intent.Phase {
	case "prepared", "dispatching":
		if intent.Binding.ContainerID != "" || intent.Binding.CreatedAt != "" {
			return errors.New("unacknowledged keeper cannot assert a returned lifetime")
		}
	case "acknowledged":
		if !managedKeeperID(intent.Binding.ContainerID) {
			return errors.New("acknowledged keeper requires full container ID")
		}
	default:
		return errors.New("unknown keeper intent phase")
	}
	return nil
}

func validateManagedKeeperHandles(a managedJournalAdmission, intent managedDockerKeeperIntent) error {
	ids := map[string]bool{}
	nonces := map[string]bool{}
	for _, scope := range []ScopeHandle{intent.Volume, intent.Keeper, intent.CreateHost} {
		if err := validateManagedScope(scope); err != nil {
			return err
		}
		if ids[scope.ID] || nonces[scope.Nonce] || scope.Generation != intent.Keeper.Generation {
			return errors.New("keeper handles repeat or differ in generation")
		}
		ids[scope.ID] = true
		nonces[scope.Nonce] = true
	}
	object := cleanupObjectiveHash(a)
	for _, scope := range []ScopeHandle{intent.Volume, intent.Keeper} {
		if scope.Backend != "docker" || scope.Name != "oro-factory-"+object+"-"+scope.Kind+"-"+scope.ID+"-"+scope.Nonce {
			return errors.New("keeper Docker scope name differs")
		}
	}
	host := intent.CreateHost
	if intent.Volume.Kind != "volume" || intent.Keeper.Kind != "keeper" || host.Kind != "host" || host.Backend != "launchd" || host.Name != "dev.getoro.factory."+object+".host."+host.ID+"."+host.Nonce {
		return errors.New("keeper scope kinds differ")
	}
	return nil
}

func requireManagedKeeperVolume(ctx context.Context, tx *sql.Tx, j *managedJournal, intent managedDockerKeeperIntent) error {
	volume, err := readManagedDockerVolume(ctx, tx, j, intent.Volume)
	if err != nil {
		return err
	}
	if volume.Phase != "acknowledged" || volume.Binding.CreatedAt == "" || volume.RuntimeConfigSHA256 != intent.RuntimeConfigSHA256 || !volume.WorkDeadline.Equal(intent.WorkDeadline) {
		return errors.New("keeper requires an acknowledged bound volume with the same frozen configuration")
	}
	return nil
}

func readManagedDockerKeeper(ctx context.Context, tx *sql.Tx, j *managedJournal, keeper ScopeHandle) (managedDockerKeeperIntent, error) {
	var body []byte
	var volumeID, hostID string
	var size int
	if err := tx.QueryRowContext(ctx, "SELECT volume_scope_id,create_scope_id,length(body),substr(body,1,16385) FROM managed_docker_keepers WHERE keeper_scope_id=?", keeper.ID).Scan(&volumeID, &hostID, &size, &body); err != nil {
		return managedDockerKeeperIntent{}, fmt.Errorf("read keeper intent: %w", err)
	}
	var intent managedDockerKeeperIntent
	if size > managedDockerIntentBytes || json.Unmarshal(body, &intent) != nil {
		return managedDockerKeeperIntent{}, errors.New("invalid bounded keeper record")
	}
	canonical, err := encodeManagedDockerKeeper(j, intent)
	if err != nil || !bytes.Equal(body, canonical) || intent.Keeper != keeper || intent.Volume.ID != volumeID || intent.CreateHost.ID != hostID {
		return managedDockerKeeperIntent{}, errors.Join(errors.New("keeper record differs from exact canonical identity"), err)
	}
	if err := requireManagedKeeperVolume(ctx, tx, j, intent); err != nil {
		return managedDockerKeeperIntent{}, err
	}
	for _, scope := range []ScopeHandle{intent.Keeper, intent.CreateHost} {
		deadline := intent.GuardDeadline
		if scope == intent.CreateHost {
			deadline = intent.CreateDeadline
		}
		if err := validateDockerIntentScope(ctx, tx, j.admission, scope, deadline); err != nil {
			return managedDockerKeeperIntent{}, err
		}
	}
	var generation uint64
	if err := tx.QueryRowContext(ctx, "SELECT generation FROM managed_reservations WHERE kind='host' AND token=?", hostID).Scan(&generation); err != nil || generation != keeper.Generation {
		return managedDockerKeeperIntent{}, errors.Join(errors.New("keeper lacks exact host reservation"), err)
	}
	return intent, nil
}

func loadManagedDockerKeeper(ctx context.Context, journal *managedJournal, keeper ScopeHandle) (managedDockerKeeperIntent, error) {
	if journal == nil {
		return managedDockerKeeperIntent{}, errors.New("keeper requires a journal")
	}
	var intent managedDockerKeeperIntent
	err := journal.mutate(ctx, "", "", func(tx *sql.Tx, _ *managedSupervision) (bool, error) {
		var err error
		intent, err = readManagedDockerKeeper(ctx, tx, journal, keeper)
		return false, err
	})
	if err != nil {
		return managedDockerKeeperIntent{}, err
	}
	return intent, nil
}

func changeManagedDockerKeeper(ctx context.Context, j *managedJournal, keeper ScopeHandle, kind string, change func(*sql.Tx, *managedSupervision, *managedDockerKeeperIntent) (bool, error)) error {
	if j == nil {
		return errors.New("keeper transition requires a journal")
	}
	return j.mutate(ctx, kind, keeper.ID, func(tx *sql.Tx, state *managedSupervision) (bool, error) {
		intent, err := readManagedDockerKeeper(ctx, tx, j, keeper)
		if err != nil {
			return false, err
		}
		changed, err := change(tx, state, &intent)
		if err != nil || !changed {
			return false, err
		}
		body, err := encodeManagedDockerKeeper(j, intent)
		if err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE managed_docker_keepers SET body=? WHERE keeper_scope_id=?", body, keeper.ID); err != nil {
			return false, fmt.Errorf("persist keeper transition: %w", err)
		}
		return true, nil
	})
}

func activeManagedKeeperCreate(ctx context.Context, tx *sql.Tx, state *managedSupervision, intent managedDockerKeeperIntent) error {
	if err := activeManagedGeneration(state, intent.Keeper.Generation, time.Now(), intent.CreateDeadline); err != nil {
		return err
	}
	return requireNoManagedCleanup(ctx, tx)
}

func dispatchManagedDockerKeeper(ctx context.Context, journal *managedJournal, keeper ScopeHandle, runtimeConfigSHA256 string) (managedDockerKeeperIntent, error) {
	var dispatched managedDockerKeeperIntent
	err := changeManagedDockerKeeper(ctx, journal, keeper, "docker-keeper-dispatching", func(tx *sql.Tx, state *managedSupervision, intent *managedDockerKeeperIntent) (bool, error) {
		if err := activeManagedKeeperCreate(ctx, tx, state, *intent); err != nil {
			return false, err
		}
		if intent.Phase != "prepared" || intent.RuntimeConfigSHA256 != runtimeConfigSHA256 {
			return false, errors.New("keeper already dispatched or runtime changed")
		}
		for _, scope := range []ScopeHandle{intent.Volume, intent.Keeper, intent.CreateHost} {
			record, err := loadManagedScope(ctx, tx, scope)
			if err != nil {
				return false, err
			}
			if record.Phase != "reserved" || record.PID != 0 || record.PGID != 0 {
				return false, errors.New("keeper dispatch requires its exact live unused scopes")
			}
		}
		intent.Phase = "dispatching"
		dispatched = *intent
		return true, nil
	})
	if err != nil {
		return managedDockerKeeperIntent{}, err
	}
	return dispatched, nil
}

// The private adapter acknowledges only its own exact successful Invoke return.
func acknowledgeManagedDockerKeeper(ctx context.Context, journal *managedJournal, keeper, createHost ScopeHandle, returnedID string) error {
	return changeManagedDockerKeeper(ctx, journal, keeper, "docker-keeper-acknowledged", func(tx *sql.Tx, state *managedSupervision, intent *managedDockerKeeperIntent) (bool, error) {
		if err := activeManagedKeeperCreate(ctx, tx, state, *intent); err != nil {
			return false, err
		}
		if intent.CreateHost != createHost || intent.Phase == "prepared" || !managedKeeperID(returnedID) {
			return false, errors.New("keeper acknowledgment lacks its dispatched host and full ID")
		}
		record, err := loadManagedScope(ctx, tx, createHost)
		if err != nil {
			return false, err
		}
		if record.Phase != "quiescent" {
			return false, errors.New("keeper acknowledgment requires positive host retirement")
		}
		if intent.Phase == "acknowledged" {
			if intent.Binding.ContainerID != returnedID {
				return false, errors.New("keeper returned ID changed")
			}
			return false, nil
		}
		intent.Phase = "acknowledged"
		intent.Binding.ContainerID = returnedID
		return true, nil
	})
}

func bindManagedDockerKeeper(ctx context.Context, journal *managedJournal, ownerGeneration uint64, keeper ScopeHandle, observed DockerCleanupBinding) error {
	return changeManagedDockerKeeper(ctx, journal, keeper, "docker-keeper-bound", func(tx *sql.Tx, state *managedSupervision, intent *managedDockerKeeperIntent) (bool, error) {
		if err := activeManagedRecovery(state, ownerGeneration, time.Now(), journal.admission.HardStop); err != nil {
			return false, err
		}
		if ownerGeneration < keeper.Generation || intent.Phase != "acknowledged" || observed.CreatedAt == "" {
			return false, errors.New("keeper lifetime requires its acknowledged owned container")
		}
		if err := requireNoManagedCleanup(ctx, tx); err != nil {
			return false, err
		}
		if err := validateCleanupCreatedAt(observed.CreatedAt); err != nil {
			return false, err
		}
		expected := cloneManagedDockerBinding(intent.Binding)
		expected.CreatedAt = observed.CreatedAt
		if !reflect.DeepEqual(expected, observed) || (intent.Binding.CreatedAt != "" && intent.Binding.CreatedAt != observed.CreatedAt) {
			return false, errors.New("keeper lifetime differs from immutable ownership")
		}
		if intent.Binding.CreatedAt != "" {
			return false, nil
		}
		intent.Binding.CreatedAt = observed.CreatedAt
		return true, nil
	})
}
