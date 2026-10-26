package factory

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

type supervisorRun struct {
	j               *managedJournal
	manifest        managedManifest
	backend         SupervisorBackend
	root, id, token string
	generation      uint64
	status          Status
}

// Supervise runs one counted controller incarnation and retires its scopes.
func Supervise(ctx context.Context, stateDir, id string, backend SupervisorBackend) (Status, error) {
	if backend == nil || (reflect.ValueOf(backend).Kind() == reflect.Ptr && reflect.ValueOf(backend).IsNil()) {
		return Status{}, errors.New("supervisor requires backend")
	}
	j, _, err := openManagedActivation(ctx, stateDir, id)
	if err != nil {
		return Status{}, err
	}
	_ = j.Close()
	release, err := lockManagedSupervisor(stateDir, id)
	if err != nil {
		return Status{}, err
	}
	defer func() { _ = release() }()
	j, manifest, err := openManagedActivation(ctx, stateDir, id)
	if err != nil {
		return Status{}, err
	}
	defer func() { _ = j.Close() }()
	run := supervisorRun{j: j, manifest: manifest, backend: backend, root: stateDir, id: id}
	return run.execute(ctx)
}

func (r *supervisorRun) read(ctx context.Context) (snapshot, error) {
	var result snapshot
	err := r.j.mutate(ctx, "", "", func(tx *sql.Tx, _ *managedSupervision) (bool, error) {
		a, e := loadManagedActivation(ctx, tx, r.j.identity, r.root, r.id)
		if e != nil {
			return false, e
		}
		if a.Phase != "active" {
			return false, errors.New("managed runtime is pending")
		}
		if e := verifyActivationEvent(ctx, tx, a.Runtime); e != nil {
			return false, e
		}
		result, e = validateActivationSnapshot(ctx, tx, r.manifest)
		return false, e
	})
	return result, err
}

func (r *supervisorRun) execute(ctx context.Context) (Status, error) {
	state, err := r.read(ctx)
	if err != nil {
		return Status{}, err
	}
	r.status = snapshotStatus(state)
	supervision, err := r.j.supervision(ctx)
	if err != nil {
		return r.status, err
	}
	if supervision.DesiredState == "stopped" {
		return r.status, nil
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return r.status, fmt.Errorf("allocate supervisor identity: %w", err)
	}
	r.token = hex.EncodeToString(random[:])
	r.generation, _, err = r.j.beginGeneration(ctx, r.token, time.Now())
	if err != nil {
		return r.status, err
	}
	work, cancel := context.WithDeadline(ctx, r.j.admission.Deadline)
	defer cancel()
	err = r.startAndMonitor(work, state, supervision)
	if err != nil {
		err = errors.Join(err, work.Err())
	}
	return r.finish(ctx, err)
}

func (r *supervisorRun) startAndMonitor(ctx context.Context, state snapshot, supervision managedSupervision) error {
	if supervision.DesiredState != "running" {
		return errors.New("supervision already stopping")
	}
	if state.State == "promoting" {
		return errors.New("promotion reconciliation unavailable")
	}
	if err := retireSupervisorScopes(ctx, r.j, r.backend); err != nil {
		return err
	}
	if state.State == "completed" || state.State == "failed" {
		return nil
	}
	if err := waitSupervisor(ctx, time.Until(supervision.NextStart)); err != nil {
		return err
	}
	scope, err := reserveManagedController(ctx, r.j, r.token, r.generation)
	if err != nil {
		return err
	}
	if err = r.backend.StartController(ctx, scope); err != nil {
		return fmt.Errorf("controller start uncertain: %w", err)
	}
	granted, err := r.j.Granted(ctx, scope)
	if err != nil || !granted {
		return errors.Join(errors.New("controller start lacked grant acknowledgement"), err)
	}
	return r.monitor(ctx, scope)
}

func (r *supervisorRun) monitor(ctx context.Context, scope ScopeHandle) error {
	for {
		supervision, err := r.j.supervision(ctx)
		if err != nil {
			return err
		}
		if supervision.Generation != r.generation || supervision.SupervisorToken != r.token {
			return errors.New("supervisor superseded")
		}
		if supervision.DesiredState != "running" {
			return errors.New("supervision interrupted")
		}
		state, err := r.read(ctx)
		if err != nil {
			return err
		}
		r.status = snapshotStatus(state)
		if state.State == "completed" || state.State == "failed" {
			return nil
		}
		if state.State == "promoting" {
			return errors.New("promotion reconciliation unavailable")
		}
		if err := r.observeController(ctx, scope); err != nil {
			return err
		}
		if err := waitSupervisor(ctx, 100*time.Millisecond); err != nil {
			return err
		}
	}
}

func (r *supervisorRun) observeController(ctx context.Context, scope ScopeHandle) error {
	quiet, err := r.backend.Quiescent(ctx, scope)
	if err != nil {
		return fmt.Errorf("observe controller: %w", err)
	}
	if !quiet {
		return nil
	}
	observation, err := r.j.Observe(ctx, scope)
	if err != nil {
		return err
	}
	if observation.Phase == "quiescent" {
		return errors.New("controller exited before delivery completed")
	}
	return nil
}

func waitSupervisor(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("supervisor wait: %w", err)
		}
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("supervisor wait: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}

func (r *supervisorRun) finish(ctx context.Context, cause error) (Status, error) {
	deadline := time.Now().Add(time.Duration(r.j.admission.Recovery.CleanupTimeoutSeconds) * time.Second)
	if r.j.admission.HardStop.Before(deadline) {
		deadline = r.j.admission.HardStop
	}
	c, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	defer cancel()
	reason := "supervision stopped"
	if cause != nil {
		reason = strings.ReplaceAll(cause.Error(), "\x00", "�")
		if len(reason) > 4096 {
			reason = reason[:4096]
		}
	}
	err := r.j.setDesired(c, r.generation, "stopping", reason, time.Time{})
	if err == nil {
		err = retireSupervisorScopes(c, r.j, r.backend)
	}
	if err == nil {
		err = r.j.setDesired(c, r.generation, "stopped", reason, time.Time{})
	}
	return r.status, errors.Join(cause, err)
}

func retireSupervisorScopes(ctx context.Context, j *managedJournal, backend SupervisorBackend) error {
	scopes, err := j.scopes(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, s := range scopes {
		if s.Phase == "quiescent" || s.Backend != "launchd" {
			continue
		}
		if err := retireSupervisorScope(ctx, j, backend, s.ScopeHandle); err != nil {
			failures = append(failures, err)
		}
	}
	for _, s := range scopes {
		if s.Phase == "quiescent" || s.Backend == "launchd" {
			continue
		}
		if s.Backend == "docker" {
			failures = append(failures, errors.New("docker reconciliation unavailable"))
		} else {
			failures = append(failures, errors.New("unsupported scope retirement"))
		}
	}
	return errors.Join(failures...)
}

func retireSupervisorScope(ctx context.Context, j *managedJournal, backend SupervisorBackend, scope ScopeHandle) error {
	if err := backend.Retire(ctx, scope); err != nil {
		return fmt.Errorf("retire scope %s: %w", scope.ID, err)
	}
	quiet, err := backend.Quiescent(ctx, scope)
	if err != nil {
		return fmt.Errorf("observe backend quiescence: %w", err)
	}
	if !quiet {
		return errors.New("scope retirement uncertain")
	}
	obs, err := j.Observe(ctx, scope)
	if err != nil {
		return err
	}
	if obs.Phase != "quiescent" {
		return errors.New("scope lacks durable quiescence")
	}
	return nil
}
