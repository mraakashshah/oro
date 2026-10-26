package factory

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Every retry consumes a fresh C token. Scope, token and immutable permit commit
// together before the host runtime can publish a request or grant execution.
func reserveManagedCleanupInvocation(ctx context.Context, j *managedJournal, generation uint64, target ScopeHandle, operation DockerCleanupOp, binding DockerCleanupBinding, deadline time.Time) (ScopeHandle, error) {
	if j == nil || ctx.Err() != nil {
		return ScopeHandle{}, errors.Join(errors.New("cleanup requires live managed authority"), ctx.Err())
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return ScopeHandle{}, fmt.Errorf("allocate cleanup identity: %w", err)
	}
	scope := ScopeHandle{ID: hex.EncodeToString(random[:16]), Nonce: hex.EncodeToString(random[16:]), Generation: generation, Kind: "cleanup", Backend: "launchd"}
	scope.Name = "dev.getoro.factory." + cleanupObjectiveHash(j.admission) + ".cleanup." + scope.ID + "." + scope.Nonce
	permit := DockerCleanupPermit{Scope: scope, Target: target, Operation: operation, Binding: binding, Deadline: deadline.UTC()}
	body, err := encodeCleanupPermit(j.admission, permit)
	if err != nil {
		return ScopeHandle{}, err
	}
	err = j.mutate(ctx, "cleanup-reserved", scope.ID, func(tx *sql.Tx, state *managedSupervision) (bool, error) {
		if err := activeManagedRecovery(state, generation, time.Now(), deadline); err != nil {
			return false, err
		}
		if err := requireCleanupTarget(ctx, tx, target); err != nil {
			return false, err
		}
		if err := requireCleanupKeeperBinding(ctx, tx, j, permit); err != nil {
			return false, err
		}
		if err := requireCleanupHostQuiescence(ctx, tx, scope.ID); err != nil {
			return false, err
		}
		if _, err := reserveManagedToken(ctx, tx, "cleanup", scope.ID, generation, int64(j.admission.Recovery.MaxCleanupInvocations)); err != nil {
			return false, err
		}
		if _, err := j.insertScope(ctx, tx, scope, deadline); err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO managed_cleanup_permits(scope_id,body) VALUES(?,?)", scope.ID, body); err != nil {
			return false, fmt.Errorf("persist cleanup permit: %w", err)
		}
		return true, nil
	})
	if err != nil {
		return ScopeHandle{}, err
	}
	return scope, nil
}

// CleanupPermit reads a current typed permit without granting execution.
func (j *managedJournal) CleanupPermit(ctx context.Context, scope ScopeHandle) (DockerCleanupPermit, error) {
	var permit DockerCleanupPermit
	err := j.mutate(ctx, "", "", func(tx *sql.Tx, state *managedSupervision) (bool, error) {
		var err error
		permit, err = j.loadCleanupPermit(ctx, tx, state, scope)
		return false, err
	})
	if err != nil {
		return DockerCleanupPermit{}, err
	}
	return permit, nil
}

func (j *managedJournal) loadCleanupPermit(ctx context.Context, tx *sql.Tx, state *managedSupervision, scope ScopeHandle) (DockerCleanupPermit, error) {
	var body []byte
	if err := tx.QueryRowContext(ctx, "SELECT body FROM managed_cleanup_permits WHERE scope_id=?", scope.ID).Scan(&body); err != nil {
		return DockerCleanupPermit{}, fmt.Errorf("read cleanup permit: %w", err)
	}
	permit, err := decodeCleanupPermit(j.admission, scope, body)
	if err != nil {
		return DockerCleanupPermit{}, err
	}
	record, err := loadManagedScope(ctx, tx, scope)
	if err != nil {
		return DockerCleanupPermit{}, err
	}
	if (record.Phase != "reserved" && record.Phase != "granted") || !record.Deadline.Equal(permit.Deadline) {
		return DockerCleanupPermit{}, errors.New("cleanup permit scope is retired or has a different deadline")
	}
	if err := activeManagedRecovery(state, scope.Generation, time.Now(), permit.Deadline); err != nil {
		return DockerCleanupPermit{}, err
	}
	if err := requireCleanupTarget(ctx, tx, permit.Target); err != nil {
		return DockerCleanupPermit{}, err
	}
	if err := requireCleanupKeeperBinding(ctx, tx, j, permit); err != nil {
		return DockerCleanupPermit{}, err
	}
	if err := requireCleanupHostQuiescence(ctx, tx, scope.ID); err != nil {
		return DockerCleanupPermit{}, err
	}
	var generation uint64
	if err := tx.QueryRowContext(ctx, "SELECT generation FROM managed_reservations WHERE kind='cleanup' AND token=?", scope.ID).Scan(&generation); err != nil || generation != scope.Generation {
		return DockerCleanupPermit{}, errors.Join(errors.New("cleanup permit lacks its exact budget reservation"), err)
	}
	return permit, nil
}

func requireCleanupKeeperBinding(ctx context.Context, tx *sql.Tx, j *managedJournal, permit DockerCleanupPermit) error {
	if permit.Target.Kind != "keeper" {
		return nil
	}
	keeper, err := readManagedDockerKeeper(ctx, tx, j, permit.Target)
	if err != nil {
		return err
	}
	want := keeper.Binding
	if want.CreatedAt == "" {
		if permit.Operation != DockerCleanupInspect {
			return errors.New("unbound keeper permits inspection only")
		}
		want.ContainerID, want.CreatedAt = "", ""
	}
	if !reflect.DeepEqual(permit.Binding, want) {
		return errors.New("cleanup keeper binding differs from durable identity")
	}
	return nil
}

func decodeCleanupPermit(a managedJournalAdmission, scope ScopeHandle, body []byte) (DockerCleanupPermit, error) {
	var permit DockerCleanupPermit
	if len(body) > ManagedCleanupPermitBytes || json.Unmarshal(body, &permit) != nil {
		return DockerCleanupPermit{}, errors.New("invalid bounded cleanup permit encoding")
	}
	canonical, err := encodeCleanupPermit(a, permit)
	if err != nil || !bytes.Equal(canonical, body) || permit.Scope != scope {
		return DockerCleanupPermit{}, errors.Join(errors.New("cleanup permit differs from its canonical exact identity"), err)
	}
	return permit, nil
}

func requireCleanupTarget(ctx context.Context, tx *sql.Tx, target ScopeHandle) error {
	_, err := loadManagedScope(ctx, tx, target)
	if err != nil {
		return err
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM managed_scopes WHERE id=? AND nonce=? AND backend=? AND name=?", target.ID, target.Nonce, target.Backend, target.Name).Scan(&count); err != nil || count != 1 {
		return errors.Join(errors.New("cleanup target columns differ from exact ownership"), err)
	}
	return nil
}

func requireCleanupHostQuiescence(ctx context.Context, tx *sql.Tx, except string) error {
	return scanManagedCleanupScopes(ctx, tx, func(record managedScopeRecord) error {
		if record.Backend != "docker" && record.Phase != "quiescent" && record.ID != except {
			return errors.New("cleanup requires positive prior host-scope retirement")
		}
		return nil
	})
}

func requireNoManagedCleanup(ctx context.Context, tx *sql.Tx) error {
	return scanManagedCleanupScopes(ctx, tx, func(record managedScopeRecord) error {
		if record.Phase != "quiescent" && record.Kind == "cleanup" {
			return errors.New("ordinary work requires positive cleanup-helper retirement")
		}
		return nil
	})
}

// Validate complete rows before any classification, including excluded Docker,
// retired and self scopes. Raw columns or JSON fields cannot hide corruption.
func scanManagedCleanupScopes(ctx context.Context, tx *sql.Tx, check func(managedScopeRecord) error) error {
	rows, err := tx.QueryContext(ctx, "SELECT id,nonce,backend,name,substr(CAST(body AS BLOB),1,2049),phase,pid,pgid,cleanup FROM managed_scopes")
	if err != nil {
		return fmt.Errorf("list cleanup exclusion scopes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var record managedScopeRecord
		var body []byte
		if err := rows.Scan(&record.ID, &record.Nonce, &record.Backend, &record.Name, &body, &record.Phase, &record.PID, &record.PGID, &record.CleanupEvidence); err != nil {
			return fmt.Errorf("read cleanup exclusion scope: %w", err)
		}
		record, err = decodeManagedScopeRecord(record, body)
		if err != nil {
			return err
		}
		if err := check(record); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("finish cleanup exclusion scopes: %w", err)
	}
	return nil
}

func cleanupObjectiveHash(a managedJournalAdmission) string {
	digest := sha256.Sum256([]byte(a.Reservation.StateRoot + "\x00" + a.ObjectiveID))
	return hex.EncodeToString(digest[:8])
}

func encodeCleanupPermit(a managedJournalAdmission, permit DockerCleanupPermit) ([]byte, error) {
	if err := validateManagedScope(permit.Scope); err != nil {
		return nil, err
	}
	if err := validateManagedScope(permit.Target); err != nil {
		return nil, err
	}
	name := "dev.getoro.factory." + cleanupObjectiveHash(a) + ".cleanup." + permit.Scope.ID + "." + permit.Scope.Nonce
	if permit.Scope.Kind != "cleanup" || permit.Scope.Backend != "launchd" || permit.Scope.Name != name || permit.Target.Generation > permit.Scope.Generation ||
		permit.Deadline.IsZero() || permit.Deadline.After(a.HardStop) {
		return nil, errors.New("cleanup scope or lifetime differs from admitted authority")
	}
	if err := validateCleanupBinding(a, permit); err != nil {
		return nil, err
	}
	body, err := json.Marshal(permit)
	if err != nil || len(body) > ManagedCleanupPermitBytes {
		return nil, errors.Join(errors.New("cleanup permit exceeds its canonical byte bound"), err)
	}
	return body, nil
}

func validateCleanupBinding(a managedJournalAdmission, permit DockerCleanupPermit) error {
	target, binding := permit.Target, permit.Binding
	if target.Backend != "docker" || (target.Kind != "keeper" && target.Kind != "container" && target.Kind != "volume") {
		return errors.New("cleanup target must be an admitted Docker resource kind")
	}
	name := "oro-factory-" + cleanupObjectiveHash(a) + "-" + target.Kind + "-" + target.ID + "-" + target.Nonce
	if target.Name != name || binding.Name != name || binding.Context != a.Runtime.DockerContext {
		return errors.New("cleanup target name or context differs from admission")
	}
	if permit.Operation != DockerCleanupInspect && permit.Operation != DockerCleanupStop && permit.Operation != DockerCleanupRemove {
		return errors.New("cleanup operation is not inspection or retirement")
	}
	if err := validateCleanupLabels(a, target, binding.Labels); err != nil {
		return err
	}
	if !boundedCleanupMap(binding.VolumeOptions) {
		return errors.New("cleanup volume options exceed their bounded descriptor")
	}
	if err := validateCleanupCreatedAt(binding.CreatedAt); err != nil {
		return err
	}
	if target.Kind == "volume" {
		return validateCleanupVolume(binding, permit.Operation)
	}
	return validateCleanupContainer(binding, permit.Operation)
}

func validateCleanupCreatedAt(value string) error {
	if value == "" {
		return nil
	}
	created, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || created.IsZero() || created.UTC().Format(time.RFC3339Nano) != value {
		return errors.New("cleanup creation identity is not canonical UTC")
	}
	return nil
}

func validateCleanupVolume(binding DockerCleanupBinding, operation DockerCleanupOp) error {
	if binding.Kind != "volume" || binding.ContainerID != "" || binding.ImageID != "" || binding.VolumeDriver != "local" ||
		binding.VolumeOptions["type"] != "tmpfs" || binding.VolumeOptions["device"] != "tmpfs" || binding.VolumeOptions["o"] == "" ||
		len(binding.VolumeOptions) != 3 || operation == DockerCleanupStop || (operation == DockerCleanupRemove && binding.CreatedAt == "") {
		return errors.New("cleanup volume descriptor or operation is invalid")
	}
	return nil
}

func validateCleanupContainer(binding DockerCleanupBinding, operation DockerCleanupOp) error {
	hexID := regexp.MustCompile(`^[0-9a-f]{64}$`)
	if binding.Kind != "container" || binding.VolumeDriver != "" || len(binding.VolumeOptions) != 0 ||
		!strings.HasPrefix(binding.ImageID, "sha256:") || !hexID.MatchString(strings.TrimPrefix(binding.ImageID, "sha256:")) {
		return errors.New("cleanup container descriptor is invalid")
	}
	if binding.ContainerID == "" && binding.CreatedAt == "" && operation == DockerCleanupInspect {
		return nil
	}
	if !hexID.MatchString(binding.ContainerID) || binding.CreatedAt == "" {
		return errors.New("cleanup mutation requires the full container and creation identities")
	}
	return nil
}

func validateCleanupLabels(a managedJournalAdmission, target ScopeHandle, labels map[string]string) error {
	if !boundedCleanupMap(labels) {
		return errors.New("cleanup labels exceed their bounded descriptor")
	}
	want := map[string]string{
		"objective": cleanupObjectiveHash(a), "scope": target.ID, "nonce": target.Nonce,
		"generation": strconv.FormatUint(target.Generation, 10), "kind": target.Kind, "policy": a.ManifestHash,
	}
	for key, value := range want {
		if labels["dev.getoro.factory."+key] != value {
			return errors.New("cleanup labels differ from exact admitted ownership")
		}
	}
	return nil
}

func boundedCleanupMap(values map[string]string) bool {
	if len(values) > 16 {
		return false
	}
	for key, value := range values {
		if key == "" || len(key) > 128 || len(value) > 1024 || strings.ContainsRune(key, 0) || strings.ContainsRune(value, 0) {
			return false
		}
	}
	return true
}
