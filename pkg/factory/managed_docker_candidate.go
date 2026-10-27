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

// Candidate metadata fits within the existing per-source 1 MiB metadata allowance;
// each candidate is unique per retained workspace and also consumes two bounded scopes.
const managedCandidateBytes = 16 << 10

type managedCandidateIntent struct {
	Version            int                         `json:"version"`
	AdmissionSHA256    string                      `json:"admission_sha256"`
	Runtime            ManagedRuntimeReference     `json:"runtime"`
	WorkspaceID        string                      `json:"workspace_id"`
	Workspace          managedWorkspace            `json:"workspace"`
	Source             managedDockerSourceRecord   `json:"source"`
	KeeperSHA256       string                      `json:"keeper_sha256"`
	Container          ScopeHandle                 `json:"container"`
	CreateHost         ScopeHandle                 `json:"create_host"`
	StartHost          ScopeHandle                 `json:"start_host"`
	ExecutionStartedAt time.Time                   `json:"execution_started_at"`
	ExecutionDeadline  time.Time                   `json:"execution_deadline"`
	GuardDeadline      time.Time                   `json:"guard_deadline"`
	Phase              string                      `json:"phase"`
	Binding            DockerCleanupBinding        `json:"binding"`
	ContainerStartedAt string                      `json:"container_started_at"`
	Retirement         *managedCandidateRetirement `json:"retirement,omitempty"`
}

type managedCandidateRetirement struct {
	StartedAt time.Time   `json:"started_at"`
	Scope     ScopeHandle `json:"scope"`
	Deadline  time.Time   `json:"deadline"`
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
	if err := validateCandidatePhase(j, i); err != nil {
		return err
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
	binding.ContainerID, binding.CreatedAt = i.Binding.ContainerID, i.Binding.CreatedAt
	if !reflect.DeepEqual(binding, i.Binding) {
		return errors.New("candidate binding differs")
	}
	return nil
}

func readManagedCandidate(ctx context.Context, tx *sql.Tx, owner *managedControllerOwner, workspaceID string) (managedCandidateIntent, error) {
	if owner == nil || owner.journal == nil {
		return managedCandidateIntent{}, errors.New("candidate requires owner")
	}
	i, err := readManagedCandidateStructural(ctx, tx, owner.journal, workspaceID)
	if err != nil {
		return managedCandidateIntent{}, err
	}
	if !candidateColumnsMatch(i, owner, workspaceID, i.Container.ID, i.CreateHost.ID) {
		return managedCandidateIntent{}, errors.New("candidate owner differs")
	}
	if err := validateCandidateReferences(ctx, tx, owner, i); err != nil {
		return managedCandidateIntent{}, err
	}
	return i, nil
}

func readManagedCandidateStructural(ctx context.Context, tx *sql.Tx, j *managedJournal, workspaceID string) (managedCandidateIntent, error) {
	var i managedCandidateIntent
	var container, host string
	var size int
	var body []byte
	if j == nil {
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
	encoded, err := encodeManagedCandidate(j, i)
	if err != nil || !bytes.Equal(body, encoded) || (i.WorkspaceID != workspaceID || i.Container.ID != container || i.CreateHost.ID != host) {
		return managedCandidateIntent{}, errors.Join(errors.New("candidate canonical identity differs"), err)
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
	return validateCandidateScopeReferences(ctx, tx, owner, i)
}

func validateCandidateScopeReferences(ctx context.Context, tx *sql.Tx, owner *managedControllerOwner, i managedCandidateIntent) error {
	for _, item := range []struct {
		scope    ScopeHandle
		deadline time.Time
	}{{i.Container, i.ExecutionDeadline}, {i.CreateHost, i.ExecutionDeadline}} {
		if err := validateDockerIntentScope(ctx, tx, owner.journal.admission, item.scope, item.deadline); err != nil {
			return err
		}
	}
	if i.StartHost != (ScopeHandle{}) {
		if err := validateDockerIntentScope(ctx, tx, owner.journal.admission, i.StartHost, i.ExecutionDeadline); err != nil {
			return err
		}
		var generation uint64
		if err := tx.QueryRowContext(ctx, "SELECT generation FROM managed_reservations WHERE kind='host' AND token=?", i.StartHost.ID).Scan(&generation); err != nil || generation != i.Container.Generation {
			return errors.Join(errors.New("candidate start host reservation differs"), err)
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
	return validateCandidateRetirementHistory(ctx, tx)
}

func validateCandidateRetirementHistory(ctx context.Context, tx *sql.Tx) error {
	var bad, retiringEvents, retiredEvents, retiringRows, retiredRows int64
	query := `SELECT count(*) FROM managed_docker_candidates c WHERE
	 json_valid(c.body)!=1 OR coalesce(json_extract(c.body,'$.phase'),'') NOT IN ('prepared','create_dispatching','created','inspected','start_prepared','start_dispatching','start_acknowledged','ready','retiring','retired') OR
	 ((json_type(c.body,'$.retirement') IS NOT NULL) != (json_extract(c.body,'$.phase') IN ('retiring','retired'))) OR
	 (SELECT count(*) FROM managed_events e WHERE e.kind='candidate-retiring' AND e.ref=c.workspace_id) != CASE WHEN json_extract(c.body,'$.phase') IN ('retiring','retired') THEN 1 ELSE 0 END OR
	 (SELECT count(*) FROM managed_events e WHERE e.kind='candidate-retired' AND e.ref=c.workspace_id) != CASE WHEN json_extract(c.body,'$.phase')='retired' THEN 1 ELSE 0 END OR
	 (json_extract(c.body,'$.phase')='retired' AND NOT EXISTS (SELECT 1 FROM managed_scopes s WHERE s.id=c.container_id AND s.nonce=json_extract(c.body,'$.container.nonce') AND s.backend=json_extract(c.body,'$.container.backend') AND s.name=json_extract(c.body,'$.container.name') AND json_extract(s.body,'$.kind')=json_extract(c.body,'$.container.kind') AND json_extract(s.body,'$.generation')=json_extract(c.body,'$.container.generation') AND json_extract(s.body,'$.Deadline')=json_extract(c.body,'$.execution_deadline') AND s.phase='quiescent' AND s.cleanup!='')) OR
	 (json_extract(c.body,'$.phase')='retired' AND NOT EXISTS (SELECT 1 FROM managed_scopes h WHERE h.id=json_extract(c.body,'$.retirement.scope.id') AND h.nonce=json_extract(c.body,'$.retirement.scope.nonce') AND h.backend=json_extract(c.body,'$.retirement.scope.backend') AND h.name=json_extract(c.body,'$.retirement.scope.name') AND json_extract(h.body,'$.kind')=json_extract(c.body,'$.retirement.scope.kind') AND json_extract(h.body,'$.generation')=json_extract(c.body,'$.retirement.scope.generation') AND json_extract(h.body,'$.Deadline')=json_extract(c.body,'$.retirement.deadline') AND h.phase='quiescent' AND h.cleanup!='')) OR
	 (json_extract(c.body,'$.phase')='retired' AND NOT EXISTS (SELECT 1 FROM managed_events a JOIN managed_events b ON a.ref=b.ref AND a.revision<b.revision WHERE a.ref=c.workspace_id AND a.kind='candidate-retiring' AND b.kind='candidate-retired'))`
	if err := tx.QueryRowContext(ctx, query).Scan(&bad); err != nil {
		return fmt.Errorf("check candidate retirement history: %w", err)
	}
	query = `SELECT (SELECT count(*) FROM managed_events WHERE kind='candidate-retiring'), (SELECT count(*) FROM managed_events WHERE kind='candidate-retired'), (SELECT count(*) FROM managed_docker_candidates WHERE json_extract(body,'$.phase') IN ('retiring','retired')), (SELECT count(*) FROM managed_docker_candidates WHERE json_extract(body,'$.phase')='retired')`
	if err := tx.QueryRowContext(ctx, query).Scan(&retiringEvents, &retiredEvents, &retiringRows, &retiredRows); err != nil {
		return fmt.Errorf("count candidate retirement history: %w", err)
	}
	if bad != 0 || retiringEvents != retiringRows || retiredEvents != retiredRows {
		return errors.New("candidate retirement history differs")
	}
	return validateCandidateRetirementRows(ctx, tx)
}

func validateCandidateRetirementRows(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "SELECT workspace_id,container_id,length(CAST(body AS BLOB)),substr(CAST(body AS BLOB),1,16385) FROM managed_docker_candidates")
	if err != nil {
		return fmt.Errorf("read retirement fence rows: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var size int
		var workspace, container string
		var body []byte
		var candidate managedCandidateIntent
		if err := rows.Scan(&workspace, &container, &size, &body); err != nil {
			return fmt.Errorf("scan retirement fence row: %w", err)
		}
		if size <= 0 || size > managedCandidateBytes || size != len(body) || json.Unmarshal(body, &candidate) != nil {
			return errors.New("invalid candidate retirement fence row")
		}
		canonical, err := json.Marshal(candidate)
		if err != nil || !bytes.Equal(canonical, body) || candidate.WorkspaceID != workspace || candidate.Container.ID != container || ((candidate.Phase == "retiring" || candidate.Phase == "retired") != (candidate.Retirement != nil)) {
			return errors.New("candidate retirement fence is not canonical")
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("finish retirement fence rows: %w", err)
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

func validateCandidatePhase(j *managedJournal, i managedCandidateIntent) error {
	phase := slices.Index([]string{"prepared", "create_dispatching", "created", "inspected", "start_prepared", "start_dispatching", "start_acknowledged", "ready", "retiring", "retired"}, i.Phase)
	if phase < 0 {
		return errors.New("invalid candidate phase")
	}
	if phase < 2 {
		if i.Binding.ContainerID != "" || i.Binding.CreatedAt != "" {
			return errors.New("unacknowledged candidate has binding")
		}
	} else if !managedKeeperID(i.Binding.ContainerID) {
		return errors.New("candidate requires full container ID")
	}
	if phase < 3 && i.Binding.CreatedAt != "" {
		return errors.New("candidate creation lifetime precedes inspection")
	}
	if phase >= 3 && i.Binding.CreatedAt == "" {
		return errors.New("candidate missing creation lifetime")
	}
	if err := validateCleanupCreatedAt(i.Binding.CreatedAt); err != nil {
		return err
	}
	if (phase < 4) != (i.StartHost == (ScopeHandle{})) {
		return errors.New("candidate start scope differs from phase")
	}
	if err := validateCandidateStartHost(j, i, phase); err != nil {
		return err
	}
	if (phase < 7) != (i.ContainerStartedAt == "") {
		return errors.New("candidate running lifetime differs from phase")
	}
	if err := validateCandidateRetirement(j, i, phase); err != nil {
		return err
	}
	return validateCleanupCreatedAt(i.ContainerStartedAt)
}

func validateCandidateRetirement(j *managedJournal, i managedCandidateIntent, phase int) error {
	if phase < 8 {
		if i.Retirement != nil {
			return errors.New("candidate retirement precedes ready")
		}
		return nil
	}
	if i.Retirement == nil {
		return errors.New("candidate retirement identity missing")
	}
	r := i.Retirement
	if err := validateManagedScope(r.Scope); err != nil {
		return err
	}
	name := "dev.getoro.factory." + cleanupObjectiveHash(j.admission) + ".cleanup." + r.Scope.ID + "." + r.Scope.Nonce
	if !retirementScopeValid(i, r, name) || !retirementDeadlineValid(j, r) {
		return errors.New("candidate retirement identity differs")
	}
	return nil
}

func retirementScopeValid(i managedCandidateIntent, r *managedCandidateRetirement, name string) bool {
	return r.Scope.Kind == "cleanup" && r.Scope.Backend == "launchd" && r.Scope.Generation == i.Container.Generation && r.Scope.Name == name
}

func retirementDeadlineValid(j *managedJournal, r *managedCandidateRetirement) bool {
	return !r.StartedAt.IsZero() && r.StartedAt.Location() == time.UTC && !r.Deadline.IsZero() && r.Deadline.Location() == time.UTC && r.Deadline.After(r.StartedAt) && !r.Deadline.After(r.StartedAt.Add(time.Duration(j.admission.Recovery.CleanupTimeoutSeconds)*time.Second)) && !r.Deadline.After(j.admission.HardStop)
}

func validateCandidateStartHost(j *managedJournal, i managedCandidateIntent, phase int) error {
	if phase >= 4 {
		object := cleanupObjectiveHash(j.admission)
		if i.StartHost.Name != "dev.getoro.factory."+object+".host."+i.StartHost.ID+"."+i.StartHost.Nonce || i.StartHost.ID == i.Container.ID || i.StartHost.Nonce == i.Container.Nonce || i.StartHost.ID == i.Source.StartHost.ID || i.StartHost.Nonce == i.Source.StartHost.Nonce || i.StartHost.ID == i.Source.SeedHost.ID || i.StartHost.Nonce == i.Source.SeedHost.Nonce {
			return errors.New("candidate start host identity differs")
		}
		if err := validateManagedScope(i.StartHost); err != nil {
			return err
		}
		if i.StartHost.Kind != "host" || i.StartHost.Backend != "launchd" || i.StartHost.Generation != i.Container.Generation || i.StartHost.ID == i.CreateHost.ID || i.StartHost.Nonce == i.CreateHost.Nonce {
			return errors.New("candidate start host differs")
		}
	}
	return nil
}

func transitionManagedCandidate(ctx context.Context, owner *managedControllerOwner, expected managedCandidateIntent, from, to, value string) (managedCandidateIntent, error) {
	var updated managedCandidateIntent
	err := owner.journal.mutate(ctx, "candidate-"+to, expected.WorkspaceID, func(tx *sql.Tx, state *managedSupervision) (bool, error) {
		current, err := readManagedCandidate(ctx, tx, owner, expected.WorkspaceID)
		if err != nil {
			return false, err
		}
		if !reflect.DeepEqual(current, expected) || current.Phase != from {
			return false, errors.New("candidate phase or identity changed")
		}
		if err := activeManagedGeneration(state, owner.scope.Generation, time.Now(), current.ExecutionDeadline); err != nil {
			return false, err
		}
		if err := requireNoManagedCleanup(ctx, tx); err != nil {
			return false, err
		}
		if err := requireCandidateSourceScopes(ctx, tx, current.Source); err != nil {
			return false, err
		}
		if err := applyCandidateTransition(ctx, tx, owner, state, &current, from, to, value); err != nil {
			return false, err
		}
		body, err := encodeManagedCandidate(owner.journal, current)
		if err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE managed_docker_candidates SET body=? WHERE workspace_id=?", body, current.WorkspaceID); err != nil {
			return false, fmt.Errorf("persist candidate transition: %w", err)
		}
		updated = current
		return true, nil
	})
	if err != nil {
		return managedCandidateIntent{}, err
	}
	return updated, nil
}

func applyCandidateTransition(ctx context.Context, tx *sql.Tx, owner *managedControllerOwner, state *managedSupervision, i *managedCandidateIntent, from, to, value string) error {
	switch from + "/" + to {
	case "prepared/create_dispatching":
	case "create_dispatching/created":
		if !managedKeeperID(value) {
			return errors.New("candidate creation lacks exact ID")
		}
		if err := requireCandidateHostQuiescent(ctx, tx, i.CreateHost); err != nil {
			return err
		}
		i.Binding.ContainerID = value
	case "created/inspected":
		if err := validateCandidateLifetime(value); err != nil {
			return err
		}
		i.Binding.CreatedAt = value
	case "inspected/start_prepared":
		generated := newManagedDockerVolumeIntent(owner.journal, i.Container.Generation, i.ExecutionDeadline, owner.runtime.SHA256)
		i.StartHost = generated.CreateHost
		if err := reserveManagedSourceHost(ctx, tx, owner.journal, state, i.StartHost, i.ExecutionDeadline); err != nil {
			return err
		}
	case "start_prepared/start_dispatching":
	case "start_dispatching/start_acknowledged":
		if err := requireCandidateHostQuiescent(ctx, tx, i.StartHost); err != nil {
			return err
		}
	case "start_acknowledged/ready":
		if err := validateCandidateLifetime(value); err != nil {
			return err
		}
		i.ContainerStartedAt = value
	default:
		return errors.New("invalid candidate transition")
	}
	i.Phase = to
	return nil
}

func validateCandidateLifetime(value string) error {
	if err := validateCleanupCreatedAt(value); err != nil || value == "" {
		return errors.New("candidate lifetime invalid")
	}
	return nil
}

func requireCandidateHostQuiescent(ctx context.Context, tx *sql.Tx, scope ScopeHandle) error {
	record, err := loadManagedScope(ctx, tx, scope)
	if err != nil {
		return err
	}
	if record.Phase != "quiescent" {
		return errors.New("candidate host scope not retired")
	}
	return nil
}
