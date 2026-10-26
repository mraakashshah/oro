//go:build darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"time"

	"oro/pkg/factory"
)

type (
	factoryControllerCall struct {
		scope    factory.ScopeHandle
		cancel   context.CancelFunc
		done     chan struct{}
		retiring bool
	}
	factorySupervisorBackend struct {
		gate        chan struct{}
		controllers map[string]*factoryControllerCall
		host        *factoryLaunchdRuntime
		view        factory.ManagedAdmissionView
		ref         factory.ManagedRuntimeReference
	}
)

func (backend *factorySupervisorBackend) lockController(ctx context.Context) error {
	select {
	case backend.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-backend.gate
			return fmt.Errorf("controller gate: %w", err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("controller gate: %w", ctx.Err())
	}
}

func (backend *factorySupervisorBackend) registerController(ctx context.Context, call *factoryControllerCall) error {
	if err := backend.lockController(ctx); err != nil {
		return err
	}
	defer func() { <-backend.gate }()
	if _, exists := backend.controllers[call.scope.ID]; exists {
		return errors.New("controller scope already started or retired")
	}
	backend.controllers[call.scope.ID] = call
	return nil
}

func (backend *factorySupervisorBackend) stopController(ctx context.Context, scope factory.ScopeHandle) error {
	if err := backend.lockController(ctx); err != nil {
		return err
	}
	call := backend.controllers[scope.ID]
	if call != nil && call.scope != scope {
		<-backend.gate
		return errors.New("controller retirement handle mismatch")
	}
	if call == nil {
		call = &factoryControllerCall{scope: scope, retiring: true}
		backend.controllers[scope.ID] = call
	} else {
		call.retiring = true
		if call.cancel != nil {
			call.cancel()
		}
	}
	<-backend.gate
	if call.done != nil {
		select {
		case <-call.done:
		case <-ctx.Done():
			return fmt.Errorf("wait for controller startup: %w", ctx.Err())
		}
	}
	return nil
}

func openFactorySupervisorBackend(ctx context.Context, root, id string, ref factory.ManagedRuntimeReference) (*factorySupervisorBackend, error) {
	view, err := factory.ReadManagedAdmission(ctx, root, id)
	if err != nil {
		return nil, fmt.Errorf("read controller admission: %w", err)
	}
	active, err := factory.ReadManagedRuntimeReference(ctx, root, id)
	if err != nil {
		return nil, fmt.Errorf("read controller activation: %w", err)
	}
	if active != ref {
		return nil, errors.New("controller runtime reference differs from activation")
	}
	artifact, err := readFactoryRuntimeConfig(ctx, ref.Path, ref.SHA256)
	if err != nil {
		return nil, err
	}
	if !factoryDockerStorageIdentityMatches(view, artifact.factoryRuntimeConfig) {
		return nil, errors.New("controller runtime differs from admission")
	}
	config := factoryLaunchdConfig{StateRoot: root, ObjectiveID: id, OroPath: artifact.OroPath, OroSHA256: artifact.OroSHA256, GitPath: view.GitPath, GitSHA256: view.GitSHA256, Repository: view.Repository, TargetRef: view.TargetRef, RuntimeConfigPath: ref.Path, RuntimeConfigSHA256: ref.SHA256, HardStop: view.HardStop, CleanupTimeout: time.Duration(view.CleanupTimeoutSeconds) * time.Second}
	host, err := openFactoryLaunchdRuntime(ctx, config)
	if err != nil {
		return nil, err
	}
	return &factorySupervisorBackend{host: host, view: view, ref: ref, controllers: make(map[string]*factoryControllerCall), gate: make(chan struct{}, 1)}, nil
}

func (backend *factorySupervisorBackend) Close() error { return backend.host.Close() }
func (backend *factorySupervisorBackend) Invoke(ctx context.Context, invocation factory.HostInvocation) (factory.HostResult, error) {
	return backend.host.Invoke(ctx, invocation)
}

func (backend *factorySupervisorBackend) ReleaseHostReservation(ctx context.Context, id string) error {
	return releaseFactoryHostReservation(ctx, backend.view.StateRoot, id)
}

func (backend *factorySupervisorBackend) Retire(ctx context.Context, scope factory.ScopeHandle) error {
	if scope.Kind != "controller" {
		return backend.host.Retire(ctx, scope)
	}
	if err := backend.validateController(scope); err != nil {
		return err
	}
	deadline, err := backend.host.retirementDeadline()
	if err != nil {
		return err
	}
	if parentDeadline, ok := ctx.Deadline(); ok && parentDeadline.Before(deadline) {
		deadline = parentDeadline
	}
	cleanup, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	defer cancel()
	if err := backend.stopController(cleanup, scope); err != nil {
		return err
	}
	observation, err := backend.host.authority.Observe(cleanup, scope)
	if err != nil {
		return fmt.Errorf("observe controller retirement: %w", err)
	}
	if observation.Phase == "quiescent" {
		return nil
	}
	code, err := backend.host.launchctl(cleanup, "bootout", backend.host.serviceTarget(scope))
	if err != nil || (code != 0 && code != int(syscall.ESRCH)) {
		return fmt.Errorf("controller bootout unconfirmed (exit %d): %w", code, errors.Join(err, errors.New("bootout not confirmed")))
	}
	return backend.finishControllerRetirement(cleanup, scope, observation)
}

func (backend *factorySupervisorBackend) finishControllerRetirement(cleanup context.Context, scope factory.ScopeHandle, observation factory.ScopeObservation) error {
	if observation.PID != 0 || observation.PGID != 0 {
		if observation.PID <= 1 || observation.PGID != observation.PID {
			return errors.New("controller group identity invalid")
		}
		if err := waitFactoryGroupEmpty(cleanup, observation.PGID, backend.host.probe); err != nil {
			return err
		}
	} else if observation.Phase != "reserved" && observation.Phase != "revoked" {
		return errors.New("controller group identity missing")
	}
	if err := backend.host.authority.MarkQuiescent(cleanup, scope); err != nil {
		return fmt.Errorf("record controller quiescence: %w", err)
	}
	return nil
}

func (backend *factorySupervisorBackend) Quiescent(ctx context.Context, scope factory.ScopeHandle) (bool, error) {
	if scope.Backend != "launchd" {
		return false, errors.New("unsupported scope backend")
	}
	if scope.Kind == "controller" {
		if err := backend.validateController(scope); err != nil {
			return false, err
		}
	} else if err := backend.host.validateScope(scope); err != nil {
		return false, err
	}
	observation, err := backend.host.authority.Observe(ctx, scope)
	if err != nil {
		return false, fmt.Errorf("observe controller quiescence: %w", err)
	}
	if observation.Phase == "quiescent" {
		return true, nil
	}
	if scope.Kind != "controller" || observation.Phase == "reserved" {
		return false, nil
	}
	if observation.PID <= 1 || observation.PGID != observation.PID {
		return false, errors.New("controller group identity invalid")
	}
	err = backend.host.probe(observation.PGID)
	if err == nil {
		return false, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		if err := backend.Retire(ctx, scope); err != nil {
			return false, err
		}
		observation, err := backend.host.authority.Observe(ctx, scope)
		if err != nil {
			return false, fmt.Errorf("observe retired controller: %w", err)
		}
		return observation.Phase == "quiescent", nil
	}
	return false, fmt.Errorf("controller group observation uncertain: %w", err)
}

func (backend *factorySupervisorBackend) validateController(scope factory.ScopeHandle) error {
	req := factoryControllerRequest{Version: 1, Scope: scope, StateRoot: backend.view.StateRoot, ObjectiveID: backend.view.ObjectiveID, Runtime: backend.ref, Deadline: backend.view.Deadline}
	return validateFactoryControllerRequest(req)
}

var _ factory.SupervisorBackend = (*factorySupervisorBackend)(nil)
