package factory

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"
)

type managedDockerSourceRecord struct {
	Version             int         `json:"version"`
	AdmissionSHA256     string      `json:"admission_sha256"`
	RuntimeConfigSHA256 string      `json:"runtime_config_sha256"`
	Volume              ScopeHandle `json:"volume"`
	Keeper              ScopeHandle `json:"keeper"`
	StartHost           ScopeHandle `json:"start_host"`
	SeedHost            ScopeHandle `json:"seed_host"`
	OperationDeadline   time.Time   `json:"operation_deadline"`
	SeedSHA256          string      `json:"seed_sha256"`
	SeedBytes           int64       `json:"seed_bytes"`
	Phase               string      `json:"phase"`
	StartedAt           string      `json:"started_at"`
}

func managedSourcePhase(phase string) int {
	return slices.Index([]string{"start_prepared", "start_dispatching", "start_acknowledged", "ready", "seed_prepared", "seed_dispatching", "seed_acknowledged", "seeded"}, phase)
}

// This preflight never initializes missing history on an admitted objective.
func requireManagedSourceSchema(ctx context.Context, j *managedJournal) error {
	return j.mutate(ctx, "", "", func(tx *sql.Tx, _ *managedSupervision) (bool, error) {
		rows, err := tx.QueryContext(ctx, "SELECT keeper_scope_id,volume_scope_id,start_scope_id,seed_scope_id,body FROM managed_docker_sources LIMIT 0")
		if err != nil {
			return false, fmt.Errorf("source preparation schema unavailable: %w", err)
		}
		return false, rows.Close()
	})
}

func encodeManagedDockerSource(j *managedJournal, source managedDockerSourceRecord) ([]byte, error) {
	if err := validateManagedDockerSource(j, source); err != nil {
		return nil, err
	}
	body, err := json.Marshal(source)
	if err != nil || len(body) > managedDockerIntentBytes {
		return nil, errors.Join(errors.New("source record exceeds canonical byte bound"), err)
	}
	return body, nil
}

func validateManagedDockerSource(j *managedJournal, s managedDockerSourceRecord) error {
	if s.Version != 1 || s.AdmissionSHA256 != contentHash(j.identity) || !managedKeeperID(s.RuntimeConfigSHA256) || !managedKeeperID(s.SeedSHA256) {
		return errors.New("source frozen identity differs")
	}
	if err := validateManagedSourceBounds(j.admission, s); err != nil {
		return err
	}
	phase := managedSourcePhase(s.Phase)
	if phase < 0 {
		return errors.New("unknown source preparation phase")
	}
	if (phase < 3 && s.StartedAt != "") || (phase >= 3 && s.StartedAt == "") {
		return errors.New("source phase lacks exact continuity state")
	}
	if err := validateCleanupCreatedAt(s.StartedAt); err != nil {
		return err
	}
	return validateManagedSourceHandles(j.admission, s, phase)
}

func validateManagedSourceBounds(a managedJournalAdmission, s managedDockerSourceRecord) error {
	// Same USTAR bound as ManagedHostRetention, from the immutable admission.
	var arithmetic hostRetentionArithmetic
	archiveBytes := arithmetic.sum(a.Runtime.SourceBytes, arithmetic.product(1024, int64(a.Runtime.MaxFiles)), 1536)
	if arithmetic.err != nil {
		return arithmetic.err
	}
	if s.SeedBytes <= 0 || s.SeedBytes > archiveBytes || s.OperationDeadline.IsZero() || s.OperationDeadline.Location() != time.UTC || s.OperationDeadline.After(a.Deadline) {
		return errors.New("source seed length or original deadline differs")
	}
	return nil
}

func validateManagedSourceHandles(a managedJournalAdmission, s managedDockerSourceRecord, phase int) error {
	// Reuse the exact volume/keeper/host naming and generation validator.
	if err := validateManagedKeeperHandles(a, managedDockerKeeperIntent{Volume: s.Volume, Keeper: s.Keeper, CreateHost: s.StartHost}); err != nil {
		return err
	}
	if phase < 4 {
		if s.SeedHost != (ScopeHandle{}) {
			return errors.New("source seed host exists before reservation")
		}
		return nil
	}
	if err := validateManagedKeeperHandles(a, managedDockerKeeperIntent{Volume: s.Volume, Keeper: s.Keeper, CreateHost: s.SeedHost}); err != nil {
		return err
	}
	if s.SeedHost.ID == s.StartHost.ID || s.SeedHost.Nonce == s.StartHost.Nonce {
		return errors.New("source host identities repeat")
	}
	return nil
}

func reserveManagedDockerSource(ctx context.Context, manifest managedManifest, j *managedJournal, generation uint64, keeper ScopeHandle, deadline time.Time, seedSHA256 string, seedBytes int64) (managedDockerSourceRecord, error) {
	if j == nil || ctx.Err() != nil || !reflect.DeepEqual(managedJournalIdentity(manifest), j.admission) {
		return managedDockerSourceRecord{}, errors.Join(errors.New("source requires its exact live admission"), ctx.Err())
	}
	if err := validateManagedManifest(manifest.Reservation.StateRoot, manifest.Spec.ID, manifest); err != nil {
		return managedDockerSourceRecord{}, err
	}
	limits, err := ManagedHostRetention(manifest.Spec)
	if err != nil {
		return managedDockerSourceRecord{}, err
	}
	var source managedDockerSourceRecord
	err = j.mutate(ctx, "docker-source-prepared", keeper.ID, func(tx *sql.Tx, state *managedSupervision) (bool, error) {
		bound, err := readManagedDockerKeeper(ctx, tx, j, keeper)
		if err != nil {
			return false, err
		}
		if generation != keeper.Generation || bound.Phase != "acknowledged" || bound.Binding.CreatedAt == "" || deadline.After(bound.CreateDeadline) {
			return false, errors.New("source requires its bound keeper and unchanged operation deadline")
		}
		host := newManagedDockerVolumeIntent(j, generation, deadline, bound.RuntimeConfigSHA256).CreateHost
		source = managedDockerSourceRecord{Version: 1, AdmissionSHA256: contentHash(j.identity), RuntimeConfigSHA256: bound.RuntimeConfigSHA256, Volume: bound.Volume, Keeper: keeper, StartHost: host, OperationDeadline: deadline.UTC(), SeedSHA256: seedSHA256, SeedBytes: seedBytes, Phase: "start_prepared"}
		body, err := encodeManagedDockerSource(j, source)
		if err != nil {
			return false, err
		}
		return insertManagedDockerSource(ctx, tx, j, state, source, body, limits.SourceVersions)
	})
	if err != nil {
		return managedDockerSourceRecord{}, err
	}
	return source, nil
}

func insertManagedDockerSource(ctx context.Context, tx *sql.Tx, j *managedJournal, state *managedSupervision, s managedDockerSourceRecord, body []byte, sourceVersions int64) (bool, error) {
	if err := activeManagedSource(ctx, tx, state, s, s.Keeper.Generation); err != nil {
		return false, err
	}
	var used int64
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM managed_docker_sources").Scan(&used); err != nil {
		return false, fmt.Errorf("count retained source records: %w", err)
	}
	if used >= sourceVersions {
		return false, errors.New("source version allowance exhausted")
	}
	if err := reserveManagedSourceHost(ctx, tx, j, state, s.StartHost, s.OperationDeadline); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO managed_docker_sources(keeper_scope_id,volume_scope_id,start_scope_id,seed_scope_id,body) VALUES(?,?,?,NULL,?)", s.Keeper.ID, s.Volume.ID, s.StartHost.ID, body); err != nil {
		return false, fmt.Errorf("persist source preparation: %w", err)
	}
	return true, nil
}

func reserveManagedSourceHost(ctx context.Context, tx *sql.Tx, j *managedJournal, state *managedSupervision, host ScopeHandle, deadline time.Time) error {
	if err := j.checkScopeAdmission(ctx, tx, state, host, deadline, time.Now()); err != nil {
		return err
	}
	if _, err := reserveManagedToken(ctx, tx, "host", host.ID, host.Generation, int64(j.admission.Runtime.MaxHostInvocations)); err != nil {
		return err
	}
	_, err := j.insertScope(ctx, tx, host, deadline)
	return err
}

func readManagedDockerSource(ctx context.Context, tx *sql.Tx, j *managedJournal, keeper ScopeHandle) (managedDockerSourceRecord, error) {
	var body []byte
	var volumeID, startID string
	var seedID sql.NullString
	var size int
	if err := tx.QueryRowContext(ctx, "SELECT volume_scope_id,start_scope_id,seed_scope_id,length(body),substr(body,1,16385) FROM managed_docker_sources WHERE keeper_scope_id=?", keeper.ID).Scan(&volumeID, &startID, &seedID, &size, &body); err != nil {
		return managedDockerSourceRecord{}, fmt.Errorf("read source preparation: %w", err)
	}
	var source managedDockerSourceRecord
	if size > managedDockerIntentBytes || json.Unmarshal(body, &source) != nil {
		return managedDockerSourceRecord{}, errors.New("invalid bounded source record")
	}
	canonical, err := encodeManagedDockerSource(j, source)
	if err != nil || !bytes.Equal(body, canonical) || source.Keeper != keeper || source.Volume.ID != volumeID || source.StartHost.ID != startID || source.SeedHost.ID != seedID.String || seedID.Valid != (source.SeedHost != (ScopeHandle{})) {
		return managedDockerSourceRecord{}, errors.Join(errors.New("source record differs from exact canonical identity"), err)
	}
	if err := validateManagedSourceReferences(ctx, tx, j, source); err != nil {
		return managedDockerSourceRecord{}, err
	}
	return source, nil
}

func validateManagedSourceReferences(ctx context.Context, tx *sql.Tx, j *managedJournal, s managedDockerSourceRecord) error {
	keeper, err := readManagedDockerKeeper(ctx, tx, j, s.Keeper)
	if err != nil {
		return err
	}
	if keeper.Phase != "acknowledged" || keeper.Binding.CreatedAt == "" || keeper.Volume != s.Volume || keeper.RuntimeConfigSHA256 != s.RuntimeConfigSHA256 || s.OperationDeadline.After(keeper.CreateDeadline) {
		return errors.New("source differs from acknowledged keeper lifetime")
	}
	for _, host := range []ScopeHandle{s.StartHost, s.SeedHost} {
		if host == (ScopeHandle{}) {
			continue
		}
		if host.ID == keeper.CreateHost.ID || host.Nonce == keeper.CreateHost.Nonce {
			return errors.New("source reuses keeper creation host")
		}
		if err := validateDockerIntentScope(ctx, tx, j.admission, host, s.OperationDeadline); err != nil {
			return err
		}
		var generation uint64
		if err := tx.QueryRowContext(ctx, "SELECT generation FROM managed_reservations WHERE kind='host' AND token=?", host.ID).Scan(&generation); err != nil || generation != s.Keeper.Generation {
			return errors.Join(errors.New("source lacks exact host reservation"), err)
		}
	}
	return nil
}

func loadManagedDockerSource(ctx context.Context, j *managedJournal, keeper ScopeHandle) (managedDockerSourceRecord, error) {
	if j == nil {
		return managedDockerSourceRecord{}, errors.New("source requires a journal")
	}
	var source managedDockerSourceRecord
	err := j.mutate(ctx, "", "", func(tx *sql.Tx, _ *managedSupervision) (bool, error) {
		var err error
		source, err = readManagedDockerSource(ctx, tx, j, keeper)
		return false, err
	})
	if err != nil {
		return managedDockerSourceRecord{}, err
	}
	return source, nil
}

func activeManagedSource(ctx context.Context, tx *sql.Tx, state *managedSupervision, s managedDockerSourceRecord, generation uint64) error {
	if generation != s.Keeper.Generation {
		return errors.New("source cannot reuse another ownership generation")
	}
	if err := activeManagedGeneration(state, generation, time.Now(), s.OperationDeadline); err != nil {
		return err
	}
	if err := requireNoManagedCleanup(ctx, tx); err != nil {
		return err
	}
	for _, scope := range []ScopeHandle{s.Volume, s.Keeper} {
		record, err := loadManagedScope(ctx, tx, scope)
		if err != nil {
			return err
		}
		if record.Phase != "reserved" || record.PID != 0 || record.PGID != 0 {
			return errors.New("source requires its exact live Docker scopes")
		}
	}
	return nil
}

func changeManagedDockerSource(ctx context.Context, j *managedJournal, keeper ScopeHandle, kind string, change func(*sql.Tx, *managedSupervision, *managedDockerSourceRecord) (bool, error)) error {
	if j == nil {
		return errors.New("source transition requires a journal")
	}
	return j.mutate(ctx, kind, keeper.ID, func(tx *sql.Tx, state *managedSupervision) (bool, error) {
		source, err := readManagedDockerSource(ctx, tx, j, keeper)
		if err != nil {
			return false, err
		}
		changed, err := change(tx, state, &source)
		if err != nil || !changed {
			return false, err
		}
		body, err := encodeManagedDockerSource(j, source)
		if err != nil {
			return false, err
		}
		var seedID any
		if source.SeedHost != (ScopeHandle{}) {
			seedID = source.SeedHost.ID
		}
		if _, err := tx.ExecContext(ctx, "UPDATE managed_docker_sources SET seed_scope_id=?,body=? WHERE keeper_scope_id=?", seedID, body, keeper.ID); err != nil {
			return false, fmt.Errorf("persist source transition: %w", err)
		}
		return true, nil
	})
}

func dispatchManagedDockerSourceStart(ctx context.Context, j *managedJournal, keeper ScopeHandle, runtimeSHA256 string) (managedDockerSourceRecord, error) {
	return dispatchManagedSource(ctx, j, keeper, "start", func(s managedDockerSourceRecord) bool { return s.RuntimeConfigSHA256 == runtimeSHA256 })
}

func dispatchManagedDockerSeed(ctx context.Context, j *managedJournal, keeper ScopeHandle, seedSHA256 string, seedBytes int64) (managedDockerSourceRecord, error) {
	return dispatchManagedSource(ctx, j, keeper, "seed", func(s managedDockerSourceRecord) bool { return s.SeedSHA256 == seedSHA256 && s.SeedBytes == seedBytes })
}

func dispatchManagedSource(ctx context.Context, j *managedJournal, keeper ScopeHandle, stage string, matches func(managedDockerSourceRecord) bool) (managedDockerSourceRecord, error) {
	var dispatched managedDockerSourceRecord
	err := changeManagedDockerSource(ctx, j, keeper, "docker-source-"+stage+"-dispatching", func(tx *sql.Tx, state *managedSupervision, s *managedDockerSourceRecord) (bool, error) {
		if err := activeManagedSource(ctx, tx, state, *s, keeper.Generation); err != nil {
			return false, err
		}
		if s.Phase != stage+"_prepared" || !matches(*s) {
			return false, errors.New("source already dispatched or frozen input changed")
		}
		host := s.StartHost
		if stage == "seed" {
			host = s.SeedHost
		}
		record, err := loadManagedScope(ctx, tx, host)
		if err != nil {
			return false, err
		}
		if record.Phase != "reserved" || record.PID != 0 || record.PGID != 0 {
			return false, errors.New("source dispatch requires unused original host")
		}
		s.Phase = stage + "_dispatching"
		dispatched = *s
		return true, nil
	})
	if err != nil {
		return managedDockerSourceRecord{}, err
	}
	return dispatched, nil
}

// Only the private adapter's own validated Invoke return reaches these acks.
func acknowledgeManagedDockerSourceStart(ctx context.Context, j *managedJournal, keeper, host ScopeHandle) error {
	return acknowledgeManagedSource(ctx, j, keeper, host, "start")
}

func acknowledgeManagedDockerSeed(ctx context.Context, j *managedJournal, keeper, host ScopeHandle) error {
	return acknowledgeManagedSource(ctx, j, keeper, host, "seed")
}

func acknowledgeManagedSource(ctx context.Context, j *managedJournal, keeper, host ScopeHandle, stage string) error {
	return changeManagedDockerSource(ctx, j, keeper, "docker-source-"+stage+"-acknowledged", func(tx *sql.Tx, state *managedSupervision, s *managedDockerSourceRecord) (bool, error) {
		if err := activeManagedSource(ctx, tx, state, *s, keeper.Generation); err != nil {
			return false, err
		}
		expected := s.StartHost
		if stage == "seed" {
			expected = s.SeedHost
		}
		if host != expected || managedSourcePhase(s.Phase) < managedSourcePhase(stage+"_dispatching") {
			return false, errors.New("source acknowledgment lacks exact dispatched host")
		}
		record, err := loadManagedScope(ctx, tx, host)
		if err != nil {
			return false, err
		}
		if record.Phase != "quiescent" {
			return false, errors.New("source acknowledgment requires positive host retirement")
		}
		if s.Phase != stage+"_dispatching" {
			return false, nil
		}
		s.Phase = stage + "_acknowledged"
		return true, nil
	})
}

func recordManagedDockerSourceReady(ctx context.Context, j *managedJournal, generation uint64, keeper ScopeHandle, startedAt string) error {
	return recordManagedSourceInspection(ctx, j, generation, keeper, startedAt, false)
}

func completeManagedDockerSeed(ctx context.Context, j *managedJournal, generation uint64, keeper ScopeHandle, startedAt string) error {
	return recordManagedSourceInspection(ctx, j, generation, keeper, startedAt, true)
}

func recordManagedSourceInspection(ctx context.Context, j *managedJournal, generation uint64, keeper ScopeHandle, startedAt string, seeded bool) error {
	return changeManagedDockerSource(ctx, j, keeper, "docker-source-inspected", func(tx *sql.Tx, state *managedSupervision, s *managedDockerSourceRecord) (bool, error) {
		if err := activeManagedSource(ctx, tx, state, *s, generation); err != nil {
			return false, err
		}
		if startedAt == "" {
			return false, errors.New("source inspection requires nonzero StartedAt")
		}
		if err := validateCleanupCreatedAt(startedAt); err != nil {
			return false, err
		}
		before, after := "start_acknowledged", "ready"
		if seeded {
			before, after = "seed_acknowledged", "seeded"
		}
		if managedSourcePhase(s.Phase) < managedSourcePhase(before) || (s.StartedAt != "" && s.StartedAt != startedAt) {
			return false, errors.New("source inspection lacks acknowledgment or continuity")
		}
		if s.Phase != before {
			return false, nil
		}
		s.Phase = after
		s.StartedAt = startedAt
		return true, nil
	})
}

func reserveManagedDockerSeed(ctx context.Context, j *managedJournal, generation uint64, keeper ScopeHandle) (managedDockerSourceRecord, error) {
	var prepared managedDockerSourceRecord
	err := changeManagedDockerSource(ctx, j, keeper, "docker-source-seed-prepared", func(tx *sql.Tx, state *managedSupervision, s *managedDockerSourceRecord) (bool, error) {
		if err := activeManagedSource(ctx, tx, state, *s, generation); err != nil {
			return false, err
		}
		if s.Phase != "ready" {
			return false, errors.New("source seed reservation requires readiness")
		}
		host := newManagedDockerVolumeIntent(j, generation, s.OperationDeadline, s.RuntimeConfigSHA256).CreateHost
		if err := reserveManagedSourceHost(ctx, tx, j, state, host, s.OperationDeadline); err != nil {
			return false, err
		}
		s.SeedHost = host
		s.Phase = "seed_prepared"
		prepared = *s
		return true, nil
	})
	if err != nil {
		return managedDockerSourceRecord{}, err
	}
	return prepared, nil
}
