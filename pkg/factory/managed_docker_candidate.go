package factory

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"
)

// Candidate metadata fits within the existing per-source 1 MiB metadata allowance;
// each candidate is unique per retained workspace and also consumes two bounded scopes.
const managedCandidateBytes = 16 << 10

type managedCandidateIntent struct {
	Version            int                       `json:"version"`
	AdmissionSHA256    string                    `json:"admission_sha256"`
	Runtime            ManagedRuntimeReference   `json:"runtime"`
	WorkspaceID        string                    `json:"workspace_id"`
	Workspace          managedWorkspace          `json:"workspace"`
	Source             managedDockerSourceRecord `json:"source"`
	KeeperSHA256       string                    `json:"keeper_sha256"`
	Container          ScopeHandle               `json:"container"`
	CreateHost         ScopeHandle               `json:"create_host"`
	StartHost          ScopeHandle               `json:"start_host"`
	ExecutionStartedAt time.Time                 `json:"execution_started_at"`
	ExecutionDeadline  time.Time                 `json:"execution_deadline"`
	GuardDeadline      time.Time                 `json:"guard_deadline"`
	Phase              string                    `json:"phase"`
	Binding            DockerCleanupBinding      `json:"binding"`
	ContainerStartedAt string                    `json:"container_started_at"`
}

func initManagedCandidateSchema(ctx context.Context, conn *sql.Conn) error {
	var exists, admissions int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='managed_docker_candidates'").Scan(&exists); err != nil {
		return fmt.Errorf("check candidate table: %w", err)
	}
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM managed_admissions").Scan(&admissions); err != nil {
		return fmt.Errorf("check candidate admission: %w", err)
	}
	if exists != 0 && admissions == 0 {
		var n int
		if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM managed_docker_candidates").Scan(&n); err != nil {
			return fmt.Errorf("check retained candidates: %w", err)
		}
		if n != 0 {
			return errors.New("missing admission retains candidate history")
		}
	}
	return initManagedDockerSchema(ctx, conn, `CREATE TABLE IF NOT EXISTS managed_docker_candidates (
 workspace_id TEXT PRIMARY KEY REFERENCES managed_workspaces(id),
 container_id TEXT NOT NULL UNIQUE REFERENCES managed_scopes(id),
 create_host_id TEXT NOT NULL UNIQUE REFERENCES managed_scopes(id),
 body BLOB NOT NULL CHECK(length(body)<=16384)
 );`)
}

func reserveManagedCandidate(ctx context.Context, owner *managedControllerOwner, workspaceID string) (managedCandidateIntent, error) {
	var intent managedCandidateIntent
	if owner == nil || owner.journal == nil {
		return intent, errors.New("candidate requires managed owner")
	}
	limits, err := ManagedHostRetention(owner.manifest.Spec)
	if err != nil {
		return intent, err
	}
	err = owner.journal.mutate(ctx, "candidate-prepared", workspaceID, func(tx *sql.Tx, state *managedSupervision) (bool, error) {
		w, source, keeper, err := prepareCandidateInputs(ctx, tx, owner, workspaceID, limits.SourceVersions)
		if err != nil {
			return false, err
		}
		intent, err = createManagedCandidate(ctx, tx, owner, state, w, source, keeper)
		if err != nil {
			return false, err
		}
		return true, nil
	})
	if err != nil {
		return managedCandidateIntent{}, err
	}
	return intent, nil
}

func requireCandidateSourceScopes(ctx context.Context, tx *sql.Tx, source managedDockerSourceRecord) error {
	for _, scope := range []ScopeHandle{source.Volume, source.Keeper} {
		record, err := loadManagedScope(ctx, tx, scope)
		if err != nil {
			return err
		}
		if record.Phase != "reserved" || record.PID != 0 || record.PGID != 0 {
			return errors.New("candidate requires live source scopes")
		}
	}
	return nil
}

func encodeManagedCandidate(j *managedJournal, intent managedCandidateIntent) ([]byte, error) {
	if err := validateManagedCandidate(j, intent); err != nil {
		return nil, err
	}
	body, err := json.Marshal(intent)
	if err != nil || len(body) > managedCandidateBytes {
		return nil, errors.Join(errors.New("candidate exceeds canonical bound"), err)
	}
	return body, nil
}

func validateManagedCandidate(j *managedJournal, i managedCandidateIntent) error {
	if !candidateFrozenIdentityValid(j, i) {
		return errors.New("candidate frozen identity differs")
	}
	if err := validateWorkspace(j, i.Workspace); err != nil {
		return err
	}
	if err := validateManagedDockerSource(j, i.Source); err != nil {
		return err
	}
	if !candidateSourceIdentityValid(i) {
		return errors.New("candidate source differs")
	}
	if !candidateDeadlineValid(j, i) {
		return errors.New("candidate deadline or runtime differs")
	}
	return validateCandidateDetails(j, i)
}

func validateCandidateDetails(j *managedJournal, i managedCandidateIntent) error {
	if i.Phase != "prepared" || i.StartHost != (ScopeHandle{}) || i.ContainerStartedAt != "" {
		return errors.New("invalid prepared candidate phase")
	}
	for _, scope := range []ScopeHandle{i.Container, i.CreateHost} {
		if err := validateManagedScope(scope); err != nil {
			return err
		}
		if scope.Generation != i.Workspace.Controller.Generation {
			return errors.New("candidate generation differs")
		}
	}
	object := cleanupObjectiveHash(j.admission)
	if !candidateHandlesValid(i, object) {
		return errors.New("candidate handles differ")
	}
	binding := managedDockerVolumeBinding(j.admission, i.Container)
	binding.Kind = "container"
	binding.ImageID = j.admission.Runtime.ImageDigest
	binding.VolumeDriver = ""
	binding.VolumeOptions = nil
	binding.Labels["dev.getoro.factory.kind"] = "container"
	if !reflect.DeepEqual(binding, i.Binding) {
		return errors.New("candidate binding differs")
	}
	return nil
}

func readManagedCandidate(ctx context.Context, tx *sql.Tx, owner *managedControllerOwner, workspaceID string) (managedCandidateIntent, error) {
	var i managedCandidateIntent
	var container, host string
	var size int
	var body []byte
	if owner == nil || owner.journal == nil {
		return i, errors.New("candidate requires owner")
	}
	if err := validateCandidateHistory(ctx, tx); err != nil {
		return i, err
	}
	if err := tx.QueryRowContext(ctx, "SELECT container_id,create_host_id,length(CAST(body AS BLOB)),substr(CAST(body AS BLOB),1,16385) FROM managed_docker_candidates WHERE workspace_id=?", workspaceID).Scan(&container, &host, &size, &body); err != nil {
		return i, fmt.Errorf("read candidate: %w", err)
	}
	if size <= 0 || size > managedCandidateBytes || size != len(body) || json.Unmarshal(body, &i) != nil {
		return managedCandidateIntent{}, errors.New("invalid candidate encoding")
	}
	encoded, err := encodeManagedCandidate(owner.journal, i)
	if err != nil || !bytes.Equal(body, encoded) || !candidateColumnsMatch(i, owner, workspaceID, container, host) {
		return managedCandidateIntent{}, errors.Join(errors.New("candidate canonical identity differs"), err)
	}
	if err := validateCandidateReferences(ctx, tx, owner, i); err != nil {
		return managedCandidateIntent{}, err
	}
	return i, nil
}

func prepareCandidateInputs(ctx context.Context, tx *sql.Tx, owner *managedControllerOwner, workspaceID string, limit int64) (managedWorkspace, managedDockerSourceRecord, managedDockerKeeperIntent, error) {
	if err := validateCandidateHistory(ctx, tx); err != nil {
		return managedWorkspace{}, managedDockerSourceRecord{}, managedDockerKeeperIntent{}, err
	}
	current, err := workspaceOwner(ctx, tx, owner)
	if err != nil {
		return managedWorkspace{}, managedDockerSourceRecord{}, managedDockerKeeperIntent{}, err
	}
	w, err := readManagedWorkspace(ctx, tx, owner, workspaceID)
	if err != nil {
		return managedWorkspace{}, managedDockerSourceRecord{}, managedDockerKeeperIntent{}, err
	}
	if w.Phase != "ready" || current.Attempts != w.Attempt || current.Worktree != w.ID || current.BaseSHA != w.SourceSHA {
		return managedWorkspace{}, managedDockerSourceRecord{}, managedDockerKeeperIntent{}, errors.New("candidate workspace differs from saved running attempt")
	}
	if err := requireNoManagedCleanup(ctx, tx); err != nil {
		return managedWorkspace{}, managedDockerSourceRecord{}, managedDockerKeeperIntent{}, err
	}
	if err := checkCandidateAvailability(ctx, tx, w.ID, limit); err != nil {
		return managedWorkspace{}, managedDockerSourceRecord{}, managedDockerKeeperIntent{}, err
	}
	source, err := readManagedDockerSource(ctx, tx, owner.journal, w.Keeper)
	if err != nil {
		return managedWorkspace{}, managedDockerSourceRecord{}, managedDockerKeeperIntent{}, err
	}
	keeper, err := readManagedDockerKeeper(ctx, tx, owner.journal, w.Keeper)
	if err != nil {
		return managedWorkspace{}, managedDockerSourceRecord{}, managedDockerKeeperIntent{}, err
	}
	if err := requireCandidateSourceScopes(ctx, tx, source); err != nil {
		return managedWorkspace{}, managedDockerSourceRecord{}, managedDockerKeeperIntent{}, err
	}
	return w, source, keeper, nil
}

func createManagedCandidate(ctx context.Context, tx *sql.Tx, owner *managedControllerOwner, state *managedSupervision, w managedWorkspace, source managedDockerSourceRecord, keeper managedDockerKeeperIntent) (managedCandidateIntent, error) {
	now := time.Now().UTC()
	deadline := now.Add(time.Duration(owner.manifest.Spec.StepTimeoutSeconds) * time.Second)
	if owner.journal.admission.Deadline.Before(deadline) {
		deadline = owner.journal.admission.Deadline
	}
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d.UTC()
	}
	if !deadline.After(now) {
		return managedCandidateIntent{}, errors.New("candidate execution deadline expired")
	}
	generated := newManagedDockerVolumeIntent(owner.journal, owner.scope.Generation, deadline, owner.runtime.SHA256)
	container := generated.Volume
	container.Kind = "container"
	container.Name = "oro-factory-" + cleanupObjectiveHash(owner.journal.admission) + "-container-" + container.ID + "-" + container.Nonce
	binding := managedDockerVolumeBinding(owner.journal.admission, container)
	binding.Kind = "container"
	binding.ImageID = owner.journal.admission.Runtime.ImageDigest
	binding.VolumeDriver = ""
	binding.VolumeOptions = nil
	binding.Labels["dev.getoro.factory.kind"] = "container"
	intent := managedCandidateIntent{Version: 1, AdmissionSHA256: contentHash(owner.journal.identity), Runtime: owner.runtime, WorkspaceID: w.ID, Workspace: w, Source: source, KeeperSHA256: candidateKeeperDigest(owner.journal, keeper), Container: container, CreateHost: generated.CreateHost, ExecutionStartedAt: now, ExecutionDeadline: deadline, GuardDeadline: deadline, Phase: "prepared", Binding: binding}
	body, err := encodeManagedCandidate(owner.journal, intent)
	if err != nil {
		return managedCandidateIntent{}, err
	}
	if err := owner.journal.checkScopeAdmission(ctx, tx, state, container, deadline, now); err != nil {
		return managedCandidateIntent{}, err
	}
	if err := reserveManagedSourceHost(ctx, tx, owner.journal, state, intent.CreateHost, deadline); err != nil {
		return managedCandidateIntent{}, err
	}
	if _, err := owner.journal.insertScope(ctx, tx, container, deadline); err != nil {
		return managedCandidateIntent{}, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO managed_docker_candidates(workspace_id,container_id,create_host_id,body) VALUES(?,?,?,?)", w.ID, container.ID, intent.CreateHost.ID, body); err != nil {
		return managedCandidateIntent{}, fmt.Errorf("persist candidate: %w", err)
	}
	return intent, nil
}

func candidateFrozenIdentityValid(j *managedJournal, i managedCandidateIntent) bool {
	return i.Version == 1 && i.AdmissionSHA256 == contentHash(j.identity) && i.Runtime.SHA256 == i.Workspace.RuntimeConfigSHA256 && i.WorkspaceID == i.Workspace.ID && i.Workspace.Phase == "ready" && i.Workspace.Keeper == i.Source.Keeper && managedKeeperID(i.KeeperSHA256)
}

func candidateSourceIdentityValid(i managedCandidateIntent) bool {
	return i.Source.Phase == "seeded" && i.Source.RuntimeConfigSHA256 == i.Runtime.SHA256 && i.Source.SeedSHA256 == i.Workspace.SeedSHA256 && i.Source.SeedBytes == i.Workspace.SeedBytes && i.Source.OperationDeadline.Equal(i.Workspace.OperationDeadline) && i.Source.StartedAt != ""
}

func candidateDeadlineValid(j *managedJournal, i managedCandidateIntent) bool {
	return validManagedRuntimeReference(j.admission.Reservation.StateRoot, j.admission.ObjectiveID, i.Runtime) && !i.ExecutionStartedAt.IsZero() && i.ExecutionStartedAt.Location() == time.UTC && !i.ExecutionDeadline.IsZero() && i.ExecutionDeadline.Location() == time.UTC && i.ExecutionDeadline.After(i.ExecutionStartedAt) && !i.ExecutionDeadline.After(j.admission.Deadline) && i.GuardDeadline.Equal(i.ExecutionDeadline) && i.GuardDeadline.Location() == time.UTC
}

func candidateHandlesValid(i managedCandidateIntent, object string) bool {
	return i.Container.Kind == "container" && i.Container.Backend == "docker" && i.Container.Name == "oro-factory-"+object+"-container-"+i.Container.ID+"-"+i.Container.Nonce && i.CreateHost.Kind == "host" && i.CreateHost.Backend == "launchd" && i.CreateHost.Name == "dev.getoro.factory."+object+".host."+i.CreateHost.ID+"."+i.CreateHost.Nonce && i.Container.ID != i.CreateHost.ID && i.Container.Nonce != i.CreateHost.Nonce
}

func validateCandidateReferences(ctx context.Context, tx *sql.Tx, owner *managedControllerOwner, i managedCandidateIntent) error {
	current, err := workspaceOwner(ctx, tx, owner)
	if err != nil {
		return err
	}
	w, err := readManagedWorkspace(ctx, tx, owner, i.WorkspaceID)
	if err != nil {
		return err
	}
	source, err := readManagedDockerSource(ctx, tx, owner.journal, w.Keeper)
	if err != nil {
		return err
	}
	keeper, err := readManagedDockerKeeper(ctx, tx, owner.journal, w.Keeper)
	if err != nil {
		return err
	}
	if current.Attempts != w.Attempt || current.Worktree != w.ID || current.BaseSHA != w.SourceSHA || !reflect.DeepEqual(i.Workspace, w) || !reflect.DeepEqual(i.Source, source) || candidateKeeperDigest(owner.journal, keeper) != i.KeeperSHA256 {
		return errors.New("candidate source identity changed")
	}
	for _, item := range []struct {
		scope    ScopeHandle
		deadline time.Time
	}{{i.Container, i.ExecutionDeadline}, {i.CreateHost, i.ExecutionDeadline}} {
		if err := validateDockerIntentScope(ctx, tx, owner.journal.admission, item.scope, item.deadline); err != nil {
			return err
		}
	}
	var generation uint64
	if err := tx.QueryRowContext(ctx, "SELECT generation FROM managed_reservations WHERE kind='host' AND token=?", i.CreateHost.ID).Scan(&generation); err != nil || generation != i.Container.Generation {
		return errors.Join(errors.New("candidate host reservation differs"), err)
	}
	return nil
}

func candidateColumnsMatch(i managedCandidateIntent, owner *managedControllerOwner, workspaceID, container, host string) bool {
	return i.WorkspaceID == workspaceID && i.Container.ID == container && i.CreateHost.ID == host && i.Workspace.Controller == owner.scope && i.Runtime == owner.runtime && !i.ExecutionDeadline.After(i.ExecutionStartedAt.Add(time.Duration(owner.manifest.Spec.StepTimeoutSeconds)*time.Second))
}

func validateCandidateHistory(ctx context.Context, tx *sql.Tx) error {
	state, err := loadManagedSupervision(ctx, tx)
	if err != nil {
		return err
	}
	var all, first, last int64
	if err := tx.QueryRowContext(ctx, "SELECT count(*),coalesce(min(revision),0),coalesce(max(revision),0) FROM managed_events").Scan(&all, &first, &last); err != nil {
		return fmt.Errorf("check managed event history: %w", err)
	}
	if all != state.Revision-1 || (all == 0 && (first != 0 || last != 0)) || (all > 0 && (first != 2 || last != state.Revision)) {
		return errors.New("managed event history is not contiguous")
	}
	var rows, events, orphaned int64
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM managed_docker_candidates").Scan(&rows); err != nil {
		return fmt.Errorf("count candidate history: %w", err)
	}
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM managed_events WHERE kind='candidate-prepared'").Scan(&events); err != nil {
		return fmt.Errorf("count candidate events: %w", err)
	}
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM managed_docker_candidates c WHERE (SELECT count(*) FROM managed_events e WHERE e.kind='candidate-prepared' AND e.ref=c.workspace_id) != 1").Scan(&orphaned); err != nil {
		return fmt.Errorf("check candidate events: %w", err)
	}
	if rows != events || orphaned != 0 {
		return errors.New("candidate history is missing or inconsistent")
	}
	return nil
}

func candidateKeeperDigest(j *managedJournal, keeper managedDockerKeeperIntent) string {
	body, err := encodeManagedDockerKeeper(j, keeper)
	if err != nil {
		return ""
	}
	return contentHash(body)
}

func checkCandidateAvailability(ctx context.Context, tx *sql.Tx, workspaceID string, limit int64) error {
	var count int64
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM managed_docker_candidates").Scan(&count); err != nil {
		return fmt.Errorf("count candidates: %w", err)
	}
	if count >= limit {
		return errors.New("candidate source allowance exhausted")
	}
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM managed_docker_candidates WHERE workspace_id=?", workspaceID).Scan(&exists); err != nil {
		return fmt.Errorf("check candidate workspace: %w", err)
	}
	if exists != 0 {
		return errors.New("candidate workspace already reserved")
	}
	return nil
}
