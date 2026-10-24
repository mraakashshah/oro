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
	"strconv"
	"time"
)

// Admission replay also initializes the base schema. Feature storage must never
// recreate missing retained intent history after an admission has committed.
func initManagedDockerVolumeSchema(ctx context.Context, conn *sql.Conn) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Docker intent schema: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var committed int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM managed_admissions").Scan(&committed); err != nil {
		return fmt.Errorf("check Docker intent admission: %w", err)
	}
	if committed != 0 {
		return nil
	}
	// The base schema can recreate a missing admissions table. A retained
	// snapshot or history still proves this is not an uncommitted new objective.
	for _, query := range []string{
		"SELECT count(*) FROM snapshots", "SELECT count(*) FROM events",
		"SELECT count(*) FROM managed_supervision", "SELECT count(*) FROM managed_reservations",
		"SELECT count(*) FROM managed_scopes", "SELECT count(*) FROM managed_events",
	} {
		var retained int
		if err := tx.QueryRowContext(ctx, query).Scan(&retained); err != nil {
			return fmt.Errorf("check uncommitted Docker intent schema: %w", err)
		}
		if retained != 0 {
			return errors.New("missing admission retains committed objective history")
		}
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS managed_docker_intents (
 volume_scope_id TEXT PRIMARY KEY REFERENCES managed_scopes(id),
 create_scope_id TEXT NOT NULL UNIQUE REFERENCES managed_scopes(id),
 body BLOB NOT NULL CHECK(length(body)<=16384)
);`); err != nil {
		return fmt.Errorf("initialize Docker intent storage: %w", err)
	}
	if err := commitManagedTransaction(conn, tx); err != nil {
		return fmt.Errorf("commit Docker intent schema: %w", err)
	}
	return nil
}

func managedDockerVolumeBinding(a managedJournalAdmission, volume ScopeHandle) DockerCleanupBinding {
	labels := map[string]string{}
	for key, value := range map[string]string{"objective": cleanupObjectiveHash(a), "scope": volume.ID, "nonce": volume.Nonce, "generation": strconv.FormatUint(volume.Generation, 10), "kind": "volume", "policy": a.ManifestHash} {
		labels["dev.getoro.factory."+key] = value
	}
	return DockerCleanupBinding{
		Kind: "volume", Context: a.Runtime.DockerContext, Name: volume.Name, VolumeDriver: "local", Labels: labels,
		VolumeOptions: map[string]string{"type": "tmpfs", "device": "tmpfs", "o": fmt.Sprintf("size=%d,nr_inodes=%d,uid=1000,gid=1000", a.Runtime.SourceBytes, a.Runtime.SourceInodes)},
	}
}

func encodeManagedDockerVolume(j *managedJournal, intent managedDockerVolumeIntent) ([]byte, error) {
	if err := validateManagedDockerVolume(j, intent); err != nil {
		return nil, err
	}
	body, err := json.Marshal(intent)
	if err != nil || len(body) > managedDockerIntentBytes {
		return nil, errors.Join(errors.New("managed Docker intent exceeds canonical bound"), err)
	}
	return body, nil
}

func validateManagedDockerVolume(j *managedJournal, intent managedDockerVolumeIntent) error {
	if intent.Version != 1 || intent.AdmissionSHA256 != contentHash(j.identity) || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(intent.RuntimeConfigSHA256) {
		return errors.New("managed Docker intent version or admission/config digest differs")
	}
	switch intent.Phase {
	case "prepared", "dispatching", "acknowledged":
	default:
		return errors.New("unknown Docker intent phase")
	}
	if err := validateDockerVolumeHandles(j.admission, intent); err != nil {
		return err
	}
	if !intent.WorkDeadline.Equal(j.admission.Deadline) || intent.WorkDeadline.Location() != time.UTC || intent.CreateDeadline.IsZero() || intent.CreateDeadline.Location() != time.UTC || intent.CreateDeadline.After(intent.WorkDeadline) {
		return errors.New("managed Docker intent deadline exceeds original work authority")
	}
	if err := validateCleanupCreatedAt(intent.Binding.CreatedAt); err != nil {
		return err
	}
	expected := managedDockerVolumeBinding(j.admission, intent.Volume)
	expected.CreatedAt = intent.Binding.CreatedAt
	if !reflect.DeepEqual(expected, intent.Binding) || (intent.Phase == "prepared" && intent.Binding.CreatedAt != "") {
		return errors.New("managed Docker intent differs from derived source ownership")
	}
	return nil
}

func validateDockerVolumeHandles(admission managedJournalAdmission, intent managedDockerVolumeIntent) error {
	for _, scope := range []ScopeHandle{intent.Volume, intent.CreateHost} {
		if err := validateManagedScope(scope); err != nil {
			return err
		}
	}
	volume, host := intent.Volume, intent.CreateHost
	objective := cleanupObjectiveHash(admission)
	if volume.Kind != "volume" || volume.Backend != "docker" || host.Kind != "host" || host.Backend != "launchd" || volume.Generation != host.Generation || volume.ID == host.ID || volume.Nonce == host.Nonce ||
		volume.Name != "oro-factory-"+objective+"-volume-"+volume.ID+"-"+volume.Nonce || host.Name != "dev.getoro.factory."+objective+".host."+host.ID+"."+host.Nonce {
		return errors.New("managed Docker intent scope identity differs")
	}
	return nil
}

func readManagedDockerVolume(ctx context.Context, tx *sql.Tx, j *managedJournal, volume ScopeHandle) (managedDockerVolumeIntent, error) {
	var body []byte
	var hostID string
	var size int
	err := tx.QueryRowContext(ctx, "SELECT create_scope_id,length(body),substr(body,1,16385) FROM managed_docker_intents WHERE volume_scope_id=?", volume.ID).Scan(&hostID, &size, &body)
	if err != nil {
		return managedDockerVolumeIntent{}, fmt.Errorf("read Docker volume intent: %w", err)
	}
	var intent managedDockerVolumeIntent
	if size > managedDockerIntentBytes || json.Unmarshal(body, &intent) != nil {
		return managedDockerVolumeIntent{}, errors.New("invalid bounded Docker intent encoding")
	}
	canonical, err := encodeManagedDockerVolume(j, intent)
	if err != nil || !bytes.Equal(canonical, body) || intent.Volume != volume || intent.CreateHost.ID != hostID {
		return managedDockerVolumeIntent{}, errors.Join(errors.New("managed Docker intent differs from canonical exact identity"), err)
	}
	for _, scope := range []ScopeHandle{intent.Volume, intent.CreateHost} {
		deadline := intent.WorkDeadline
		if scope == intent.CreateHost {
			deadline = intent.CreateDeadline
		}
		if err := validateDockerIntentScope(ctx, tx, j.admission, scope, deadline); err != nil {
			return managedDockerVolumeIntent{}, err
		}
	}
	var generation uint64
	if err := tx.QueryRowContext(ctx, "SELECT generation FROM managed_reservations WHERE kind='host' AND token=?", hostID).Scan(&generation); err != nil || generation != volume.Generation {
		return managedDockerVolumeIntent{}, errors.Join(errors.New("managed Docker intent lacks its exact H reservation"), err)
	}
	return intent, nil
}

func validateDockerIntentScope(ctx context.Context, tx *sql.Tx, admission managedJournalAdmission, scope ScopeHandle, deadline time.Time) error {
	var columns managedScopeRecord
	var body []byte
	err := tx.QueryRowContext(ctx, "SELECT id,nonce,backend,name,body,phase,pid,pgid FROM managed_scopes WHERE id=?", scope.ID).
		Scan(&columns.ID, &columns.Nonce, &columns.Backend, &columns.Name, &body, &columns.Phase, &columns.PID, &columns.PGID)
	if err != nil {
		return fmt.Errorf("read exact Docker intent scope: %w", err)
	}
	stored, err := decodeDockerPriorScope(admission, columns, body)
	if err != nil {
		return err
	}
	if stored != scope {
		return errors.New("managed Docker scope body differs from exact handle")
	}
	// Canonical body validation above excludes lifecycle fields; the existing
	// loader can no longer overwrite the authoritative phase/PID/PGID columns.
	record, err := loadManagedScope(ctx, tx, scope)
	if err != nil || !record.Deadline.Equal(deadline) {
		return errors.Join(errors.New("managed Docker scope deadline differs"), err)
	}
	return nil
}

func changeManagedDockerVolume(ctx context.Context, journal *managedJournal, volume ScopeHandle, kind string, change func(*sql.Tx, *managedSupervision, *managedDockerVolumeIntent) (bool, error)) error {
	if journal == nil {
		return errors.New("managed Docker intent transition requires a journal")
	}
	return journal.mutate(ctx, kind, volume.ID, func(tx *sql.Tx, state *managedSupervision) (bool, error) {
		intent, err := readManagedDockerVolume(ctx, tx, journal, volume)
		if err != nil {
			return false, err
		}
		changed, err := change(tx, state, &intent)
		if err != nil || !changed {
			return false, err
		}
		body, err := encodeManagedDockerVolume(journal, intent)
		if err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE managed_docker_intents SET body=? WHERE volume_scope_id=?", body, volume.ID); err != nil {
			return false, fmt.Errorf("persist Docker intent transition: %w", err)
		}
		return true, nil
	})
}

func activeManagedDockerCreate(ctx context.Context, tx *sql.Tx, state *managedSupervision, intent managedDockerVolumeIntent) error {
	if err := activeManagedGeneration(state, intent.Volume.Generation, time.Now(), intent.CreateDeadline); err != nil {
		return err
	}
	return requireNoManagedCleanup(ctx, tx)
}

func decodeDockerPriorScope(admission managedJournalAdmission, columns managedScopeRecord, body []byte) (ScopeHandle, error) {
	var stored struct {
		ScopeHandle
		Deadline time.Time
	}
	if len(body) > 2048 || json.Unmarshal(body, &stored) != nil {
		return ScopeHandle{}, errors.New("invalid prior Docker host scope")
	}
	if err := validateManagedScope(stored.ScopeHandle); err != nil {
		return ScopeHandle{}, err
	}
	canonical, err := json.Marshal(stored)
	if err != nil || !bytes.Equal(canonical, body) || stored.Deadline.IsZero() || stored.Deadline.Location() != time.UTC || stored.Deadline.After(managedScopeLifetime(admission, stored.Kind)) {
		return ScopeHandle{}, errors.Join(errors.New("prior scope has noncanonical or invalid lifetime metadata"), err)
	}
	if stored.ID != columns.ID || stored.Nonce != columns.Nonce || stored.Backend != columns.Backend || stored.Name != columns.Name {
		return ScopeHandle{}, errors.New("prior scope columns differ from complete ownership")
	}
	if err := validateDockerScopePhase(columns); err != nil {
		return ScopeHandle{}, err
	}
	return stored.ScopeHandle, nil
}

func validateDockerScopePhase(record managedScopeRecord) error {
	if record.PID < 0 || record.PGID < 0 {
		return errors.New("invalid prior scope process identity")
	}
	switch record.Phase {
	case "reserved":
		if record.PID != 0 || record.PGID != 0 {
			return errors.New("reserved scope contains an authenticated process")
		}
	case "granted":
		if record.PID <= 1 || record.PGID <= 1 {
			return errors.New("granted scope lacks authenticated process identity")
		}
	case "revoked", "quiescent":
	default:
		return errors.New("invalid prior scope phase")
	}
	return nil
}
