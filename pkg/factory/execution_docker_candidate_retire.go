package factory

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"reflect"
	"syscall"
	"time"
)

// candidateCleanupContext retains explicit cancellation and earlier caller
// bounds while permitting only the objective deadline to elapse during cleanup.
func candidateCleanupContext(parent context.Context, manifest managedManifest) (context.Context, func(), error) {
	callerDeadline, hasDeadline := parent.Deadline()
	objectiveBound := hasDeadline && callerDeadline.Equal(manifest.Deadline)
	rejected := func() error {
		err := parent.Err()
		if err != nil && (!objectiveBound || !errors.Is(err, context.DeadlineExceeded)) {
			return fmt.Errorf("parent context: %w", err)
		}
		return nil
	}
	if err := rejected(); err != nil {
		return nil, nil, fmt.Errorf("candidate cleanup canceled: %w", err)
	}
	now := time.Now()
	deadline := now.Add(time.Duration(manifest.Spec.Recovery.CleanupTimeoutSeconds) * time.Second)
	if manifest.HardStop.Before(deadline) {
		deadline = manifest.HardStop
	}
	if hasDeadline && !objectiveBound && callerDeadline.Before(deadline) {
		deadline = callerDeadline
	}
	if !deadline.After(now) {
		return nil, nil, errors.New("candidate cleanup bound expired")
	}
	ctx, cancel := context.WithDeadline(context.WithoutCancel(parent), deadline)
	stop := context.AfterFunc(parent, func() {
		err := parent.Err()
		if errors.Is(err, context.Canceled) || (errors.Is(err, context.DeadlineExceeded) && !hasDeadline) {
			cancel()
		}
	})
	release := func() { stop(); cancel() }
	if err := rejected(); err != nil {
		release()
		return nil, nil, fmt.Errorf("candidate cleanup canceled: %w", err)
	}
	return ctx, release, nil
}

func (d *managedDockerVolumes) retireCandidate(parent context.Context, owner *managedControllerOwner, expected managedCandidateIntent) (managedCandidateIntent, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.ready(parent); err != nil {
		return managedCandidateIntent{}, err
	}
	retiring, permit, call, err := reserveCandidateRetirement(parent, owner, expected, d.config, d.limits)
	if err != nil {
		return managedCandidateIntent{}, err
	}
	ctx, cancel := context.WithDeadline(parent, permit.Deadline)
	defer cancel()
	authenticated, err := owner.journal.CleanupPermit(ctx, permit.Scope)
	if err != nil || !reflect.DeepEqual(authenticated, permit) {
		return managedCandidateIntent{}, errors.Join(errors.New("candidate cleanup permit differs"), err)
	}

	result, err := d.invoke(ctx, call)
	if err != nil {
		return managedCandidateIntent{}, err
	}
	if result.ExitCode != 0 || len(result.Stderr) != 0 {
		return managedCandidateIntent{}, errors.New("candidate cleanup returned uncertain result")
	}
	receipt, err := DecodeDockerCleanupResult(result.Stdout, permit)
	if err != nil {
		return managedCandidateIntent{}, err
	}
	if receipt.Observation != "stopped" || !receipt.MutationAcknowledged {
		return managedCandidateIntent{}, errors.New("candidate stop lacks positive acknowledgment")
	}
	return finishCandidateRetirement(ctx, owner, retiring, permit)
}

func reserveCandidateRetirement(ctx context.Context, owner *managedControllerOwner, expected managedCandidateIntent, config ManagedDockerConfig, limits HostRetentionLimits) (managedCandidateIntent, DockerCleanupPermit, HostInvocation, error) {
	var updated managedCandidateIntent
	var permit DockerCleanupPermit
	var call HostInvocation
	if owner == nil || owner.journal == nil {
		return updated, permit, call, errors.New("retirement requires owner")
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return updated, permit, call, fmt.Errorf("allocate candidate cleanup identity: %w", err)
	}
	scope := ScopeHandle{ID: hex.EncodeToString(random[:16]), Nonce: hex.EncodeToString(random[16:]), Kind: "cleanup", Backend: "launchd", Generation: owner.scope.Generation}
	scope.Name = "dev.getoro.factory." + cleanupObjectiveHash(owner.journal.admission) + ".cleanup." + scope.ID + "." + scope.Nonce
	err := owner.journal.mutate(ctx, "candidate-retiring", expected.WorkspaceID, func(tx *sql.Tx, state *managedSupervision) (bool, error) {
		current, p, body, started, err := prepareCandidateRetirement(ctx, tx, owner, state, expected, scope)
		if err != nil {
			return false, err
		}
		permit = p
		if config.StateRoot != owner.journal.admission.Reservation.StateRoot || config.ObjectiveID != owner.journal.admission.ObjectiveID || config.PolicyHash != owner.journal.admission.ManifestHash || config.RuntimeConfigPath != current.Runtime.Path || config.RuntimeConfigSHA256 != current.Runtime.SHA256 {
			return false, errors.New("candidate cleanup runtime differs from admission")
		}
		call, err = candidateCleanupInvocation(config, limits, permit, body)
		if err != nil {
			return false, err
		}
		if _, err := reserveManagedCleanupRecord(ctx, tx, owner.journal, permit, body); err != nil {
			return false, err
		}
		current.Phase = "retiring"
		current.Retirement = &managedCandidateRetirement{StartedAt: started, Scope: scope, Deadline: permit.Deadline}
		encoded, err := encodeManagedCandidate(owner.journal, current)
		if err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE managed_docker_candidates SET body=? WHERE workspace_id=?", encoded, current.WorkspaceID); err != nil {
			return false, fmt.Errorf("persist candidate retirement: %w", err)
		}
		updated = current
		return true, nil
	})
	if err != nil {
		return managedCandidateIntent{}, DockerCleanupPermit{}, HostInvocation{}, err
	}
	return updated, permit, call, nil
}

func candidateCleanupInvocation(config ManagedDockerConfig, limits HostRetentionLimits, permit DockerCleanupPermit, body []byte) (HostInvocation, error) {
	canonical, err := json.Marshal(permit)
	if err != nil || len(body) > ManagedCleanupPermitBytes || !bytes.Equal(canonical, body) {
		return HostInvocation{}, errors.Join(errors.New("candidate cleanup permit is not bounded canonical JSON"), err)
	}
	call := HostInvocation{Scope: permit.Scope, Path: config.OroPath, Args: []string{"factory", "_docker-cleanup", "--runtime-config", config.RuntimeConfigPath, "--runtime-config-sha256", config.RuntimeConfigSHA256}, Env: []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "LANG=C"}, Dir: config.StateRoot, Stdin: body, Deadline: permit.Deadline, OutputLimit: 1 << 20}
	cleanupLimits := limits
	cleanupLimits.ArchiveBytes = ManagedCleanupPermitBytes
	if err := validateManagedHostRequest(call, cleanupLimits); err != nil {
		return HostInvocation{}, err
	}
	return call, nil
}

func prepareCandidateRetirement(ctx context.Context, tx *sql.Tx, owner *managedControllerOwner, state *managedSupervision, expected managedCandidateIntent, scope ScopeHandle) (managedCandidateIntent, DockerCleanupPermit, []byte, time.Time, error) {
	current, err := readReadyCandidateForRetirement(ctx, tx, owner, expected)
	if err != nil {
		return managedCandidateIntent{}, DockerCleanupPermit{}, nil, time.Time{}, err
	}
	now := time.Now().UTC()
	deadline, err := candidateRetirementDeadline(ctx, owner, now)
	if err != nil {
		return managedCandidateIntent{}, DockerCleanupPermit{}, nil, time.Time{}, err
	}
	keeper, err := readManagedDockerKeeper(ctx, tx, owner.journal, current.Source.Keeper)
	if err != nil || candidateKeeperDigest(owner.journal, keeper) != current.KeeperSHA256 {
		return managedCandidateIntent{}, DockerCleanupPermit{}, nil, time.Time{}, errors.Join(errors.New("candidate keeper differs"), err)
	}
	binding := current.Binding
	binding.Labels = maps.Clone(keeper.Binding.Labels)
	for k, v := range current.Binding.Labels {
		binding.Labels[k] = v
	}
	permit := DockerCleanupPermit{Scope: scope, Target: current.Container, Operation: DockerCleanupStop, Binding: binding, Deadline: deadline}
	body, err := encodeCleanupPermit(owner.journal.admission, permit)
	if err != nil {
		return managedCandidateIntent{}, DockerCleanupPermit{}, nil, time.Time{}, err
	}
	if err := requireRetirementParent(ctx, tx, owner.journal, state, current, permit); err != nil {
		return managedCandidateIntent{}, DockerCleanupPermit{}, nil, time.Time{}, err
	}
	if err := validateRetiringReferences(ctx, tx, owner.journal, current, current.WorkspaceID, permit); err != nil {
		return managedCandidateIntent{}, DockerCleanupPermit{}, nil, time.Time{}, err
	}
	parentRecord, err := loadManagedScope(ctx, tx, current.Workspace.Controller)
	if err != nil {
		return managedCandidateIntent{}, DockerCleanupPermit{}, nil, time.Time{}, err
	}
	pgid, err := syscall.Getpgid(0)
	if err != nil || parentRecord.PID != os.Getpid() || parentRecord.PGID != pgid {
		return managedCandidateIntent{}, DockerCleanupPermit{}, nil, time.Time{}, errors.Join(errors.New("retirement controller does not own this process"), err)
	}
	return current, permit, body, now, nil
}

func candidateRetirementDeadline(ctx context.Context, owner *managedControllerOwner, now time.Time) (time.Time, error) {
	deadline := now.Add(time.Duration(owner.manifest.Spec.Recovery.CleanupTimeoutSeconds) * time.Second)
	if owner.journal.admission.HardStop.Before(deadline) {
		deadline = owner.journal.admission.HardStop
	}
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d.UTC()
	}
	if !deadline.After(now) {
		return time.Time{}, errors.New("candidate retirement deadline expired")
	}
	return deadline, nil
}

func readReadyCandidateForRetirement(ctx context.Context, tx *sql.Tx, owner *managedControllerOwner, expected managedCandidateIntent) (managedCandidateIntent, error) {
	current, err := readManagedCandidateStructural(ctx, tx, owner.journal, expected.WorkspaceID)
	if err != nil {
		return managedCandidateIntent{}, err
	}
	if current.Phase != "ready" || !reflect.DeepEqual(current, expected) {
		return managedCandidateIntent{}, errors.New("candidate retirement requires exact ready identity")
	}
	if !candidateColumnsMatch(current, owner, current.WorkspaceID, current.Container.ID, current.CreateHost.ID) {
		return managedCandidateIntent{}, errors.New("candidate retirement owner differs")
	}
	if _, err := owner.validateAdmission(ctx, tx); err != nil {
		return managedCandidateIntent{}, err
	}
	if err := requireNoManagedCleanup(ctx, tx); err != nil {
		return managedCandidateIntent{}, err
	}
	return current, nil
}

func requireRetirementParent(ctx context.Context, tx *sql.Tx, j *managedJournal, state *managedSupervision, i managedCandidateIntent, permit DockerCleanupPermit) error {
	if err := activeManagedRecovery(state, i.Container.Generation, time.Now(), permit.Deadline); err != nil {
		return err
	}
	if state.DesiredState != "running" || i.Container.Generation != state.Generation {
		return errors.New("retirement requires running generation")
	}
	parent, err := loadManagedScope(ctx, tx, i.Workspace.Controller)
	if err != nil {
		return err
	}
	if parent.Phase != "granted" || parent.PID <= 1 || parent.PID != parent.PGID {
		return errors.New("retirement requires exact granted parent")
	}
	if err := validateManagedControllerGrantIdentity(ctx, tx, j, state, parent); err != nil {
		return err
	}
	activation, err := loadManagedActivation(ctx, tx, j.identity, j.admission.Reservation.StateRoot, j.admission.ObjectiveID)
	if err != nil {
		return err
	}
	if activation.Runtime != i.Runtime {
		return errors.New("retirement runtime differs")
	}
	return scanManagedCleanupScopes(ctx, tx, func(record managedScopeRecord) error {
		if record.Backend != "docker" && record.Phase != "quiescent" && record.ScopeHandle != parent.ScopeHandle && record.ScopeHandle != permit.Scope {
			return errors.New("retirement requires other host scopes quiescent")
		}
		return nil
	})
}

func finishCandidateRetirement(ctx context.Context, owner *managedControllerOwner, expected managedCandidateIntent, permit DockerCleanupPermit) (managedCandidateIntent, error) {
	var done managedCandidateIntent
	err := owner.journal.mutate(ctx, "candidate-retired", expected.WorkspaceID, func(tx *sql.Tx, state *managedSupervision) (bool, error) {
		current, err := validateRetiringPermit(ctx, tx, owner.journal, state, permit)
		if err != nil {
			return false, err
		}
		if !reflect.DeepEqual(current, expected) {
			return false, errors.New("retirement identity changed")
		}
		helper, err := loadManagedScope(ctx, tx, permit.Scope)
		if err != nil || helper.Phase != "quiescent" {
			return false, errors.Join(errors.New("cleanup helper not quiescent"), err)
		}
		target, err := loadManagedScope(ctx, tx, permit.Target)
		if err != nil || target.Phase != "reserved" {
			return false, errors.Join(errors.New("candidate target not live"), err)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE managed_scopes SET phase='quiescent',cleanup=? WHERE id=?", "exact candidate stopped with acknowledged cleanup", target.ID); err != nil {
			return false, fmt.Errorf("retire candidate scope: %w", err)
		}
		current.Phase = "retired"
		body, err := encodeManagedCandidate(owner.journal, current)
		if err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE managed_docker_candidates SET body=? WHERE workspace_id=?", body, current.WorkspaceID); err != nil {
			return false, fmt.Errorf("persist retired candidate: %w", err)
		}
		done = current
		return true, nil
	})
	return done, err
}

// validateRetiringPermit reopens only the exact durable candidate parent and
// cleanup identity. It never grants ordinary controller or host work.
func validateRetiringReferences(ctx context.Context, tx *sql.Tx, j *managedJournal, i managedCandidateIntent, workspaceID string, permit DockerCleanupPermit) error {
	if err := validateRetirementTarget(ctx, tx, i); err != nil {
		return err
	}
	manifest, err := loadManagedManifest(ctx, j.admission.Reservation.StateRoot, j.admission.ObjectiveID)
	if err != nil || !reflect.DeepEqual(managedJournalIdentity(manifest), j.admission) {
		return errors.Join(errors.New("retirement admission differs"), err)
	}
	owner := &managedControllerOwner{journal: j, manifest: manifest, scope: i.Workspace.Controller, runtime: i.Runtime}
	if !candidateColumnsMatch(i, owner, workspaceID, i.Container.ID, i.CreateHost.ID) {
		return errors.New("retirement candidate owner differs")
	}
	w, err := readManagedWorkspace(ctx, tx, owner, workspaceID)
	if err != nil || !reflect.DeepEqual(w, i.Workspace) {
		return errors.Join(errors.New("retirement workspace differs"), err)
	}
	snapshot, err := validateActivationSnapshot(ctx, tx, manifest)
	if err != nil || snapshot.State != "running" || snapshot.Attempts != w.Attempt || snapshot.Worktree != w.ID || snapshot.BaseSHA != w.SourceSHA {
		return errors.Join(errors.New("retirement snapshot differs"), err)
	}
	if err := validateManagedOwnerSnapshot(snapshot, snapshotForManagedActivation(manifest)); err != nil {
		return err
	}
	return validateRetiringSource(ctx, tx, j, owner, i, w, permit)
}

func validateRetirementTarget(ctx context.Context, tx *sql.Tx, i managedCandidateIntent) error {
	target, err := loadManagedScope(ctx, tx, i.Container)
	if err != nil || target.Phase != "reserved" || target.PID != 0 || target.PGID != 0 {
		return errors.Join(errors.New("retirement target is not live exact scope"), err)
	}
	return nil
}

func validateRetiringSource(ctx context.Context, tx *sql.Tx, j *managedJournal, owner *managedControllerOwner, i managedCandidateIntent, w managedWorkspace, permit DockerCleanupPermit) error {
	source, err := readManagedDockerSource(ctx, tx, j, w.Keeper)
	if err != nil || !reflect.DeepEqual(source, i.Source) {
		return errors.Join(errors.New("retirement source differs"), err)
	}
	keeper, err := readManagedDockerKeeper(ctx, tx, j, w.Keeper)
	if err != nil || candidateKeeperDigest(j, keeper) != i.KeeperSHA256 {
		return errors.Join(errors.New("retirement keeper differs"), err)
	}
	want := i.Binding
	want.Labels = maps.Clone(keeper.Binding.Labels)
	for k, v := range i.Binding.Labels {
		want.Labels[k] = v
	}
	if !reflect.DeepEqual(want, permit.Binding) {
		return errors.New("retirement full binding differs")
	}
	if err := validateCandidateScopeReferences(ctx, tx, owner, i); err != nil {
		return err
	}
	return requireCandidateSourceScopes(ctx, tx, i.Source)
}

func validateRetirementPermitRecord(ctx context.Context, tx *sql.Tx, j *managedJournal, permit DockerCleanupPermit) error {
	body, err := encodeCleanupPermit(j.admission, permit)
	if err != nil {
		return err
	}
	var stored []byte
	if err := tx.QueryRowContext(ctx, "SELECT body FROM managed_cleanup_permits WHERE scope_id=?", permit.Scope.ID).Scan(&stored); err != nil || !reflect.DeepEqual(stored, body) {
		return errors.Join(errors.New("retirement permit identity differs"), err)
	}
	var generation uint64
	if err := tx.QueryRowContext(ctx, "SELECT generation FROM managed_reservations WHERE kind='cleanup' AND token=?", permit.Scope.ID).Scan(&generation); err != nil || generation != permit.Scope.Generation {
		return errors.Join(errors.New("retirement permit lacks reservation"), err)
	}
	helper, err := loadManagedScope(ctx, tx, permit.Scope)
	if err != nil || !helper.Deadline.Equal(permit.Deadline) {
		return errors.Join(errors.New("retirement scope differs"), err)
	}
	return nil
}

func validateRetiringPermit(ctx context.Context, tx *sql.Tx, j *managedJournal, state *managedSupervision, permit DockerCleanupPermit) (managedCandidateIntent, error) {
	var workspaceID string
	if err := tx.QueryRowContext(ctx, "SELECT workspace_id FROM managed_docker_candidates WHERE container_id=?", permit.Target.ID).Scan(&workspaceID); err != nil {
		return managedCandidateIntent{}, fmt.Errorf("read retirement candidate: %w", err)
	}
	i, err := readManagedCandidateStructural(ctx, tx, j, workspaceID)
	if err != nil {
		return managedCandidateIntent{}, err
	}
	if i.Phase != "retiring" || i.Retirement == nil || i.Retirement.Scope != permit.Scope || !i.Retirement.Deadline.Equal(permit.Deadline) || i.Container != permit.Target || permit.Operation != DockerCleanupStop {
		return managedCandidateIntent{}, errors.New("cleanup differs from retiring candidate")
	}
	if err := validateRetirementPermitRecord(ctx, tx, j, permit); err != nil {
		return managedCandidateIntent{}, err
	}
	if err := requireRetirementParent(ctx, tx, j, state, i, permit); err != nil {
		return managedCandidateIntent{}, err
	}
	if err := validateRetiringReferences(ctx, tx, j, i, workspaceID, permit); err != nil {
		return managedCandidateIntent{}, err
	}
	return i, nil
}
