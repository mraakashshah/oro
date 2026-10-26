//go:build darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"

	"oro/pkg/factory"
)

type factoryLaunchdRuntime struct {
	config    factoryLaunchdConfig
	authority factory.ScopeAuthority
	launchctl func(context.Context, ...string) (int, error)
	probe     func(int) error
	mu        sync.Mutex
	active    map[string]*factoryLaunchdCall
}

type factoryLaunchdCall struct {
	scope    factory.ScopeHandle
	cancel   context.CancelFunc
	retiring bool
	done     chan struct{}
}

func openFactoryLaunchdRuntime(ctx context.Context, config factoryLaunchdConfig) (*factoryLaunchdRuntime, error) {
	if err := validateFactoryLaunchdConfig(ctx, config); err != nil {
		return nil, err
	}
	authority, err := factory.OpenScopeAuthority(ctx, config.StateRoot, config.ObjectiveID)
	if err != nil {
		return nil, fmt.Errorf("open objective-bound factory scope authority: %w", err)
	}
	return &factoryLaunchdRuntime{
		config: config, authority: authority, launchctl: runFactoryLaunchctl, active: make(map[string]*factoryLaunchdCall),
		probe: func(group int) error { return syscall.Kill(-group, 0) },
	}, nil
}

func (runtime *factoryLaunchdRuntime) Close() error {
	if err := runtime.authority.Close(); err != nil {
		return fmt.Errorf("close factory scope authority: %w", err)
	}
	return nil
}

func (runtime *factoryLaunchdRuntime) Invoke(ctx context.Context, invocation factory.HostInvocation) (factory.HostResult, error) {
	invocation.Args = append([]string(nil), invocation.Args...)
	invocation.Env = append([]string(nil), invocation.Env...)
	invocation.Stdin = append([]byte(nil), invocation.Stdin...)
	invocation.Deadline = invocation.Deadline.UTC()
	if err := runtime.validateInvocation(ctx, invocation); err != nil {
		return factory.HostResult{}, err
	}
	ctx, cancel := context.WithDeadline(ctx, invocation.Deadline)
	defer cancel()
	call := &factoryLaunchdCall{cancel: cancel, done: make(chan struct{})}
	if err := runtime.register(invocation.Scope, call); err != nil {
		return factory.HostResult{}, err
	}
	defer runtime.unregister(invocation.Scope)
	prepared, publication, invokeErr := runtime.perform(ctx, invocation, call)
	close(call.done)
	retireErr := runtime.Retire(ctx, invocation.Scope)
	if err := errors.Join(invokeErr, retireErr); err != nil {
		return factory.HostResult{}, err
	}
	if prepared == nil {
		return factory.HostResult{}, errors.New("factory invocation has no prepared request")
	}
	return readFactoryRetiredResult(prepared.request, publication)
}

func (runtime *factoryLaunchdRuntime) Retire(ctx context.Context, scope factory.ScopeHandle) error {
	if err := runtime.validateScope(scope); err != nil {
		return err
	}
	deadline, err := runtime.retirementDeadline()
	if err != nil {
		return err
	}
	// Cleanup survives invocation cancellation within its frozen allowance and
	// original hard stop. It never extends execution or publication permission.
	cleanup, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	defer cancel()
	observation, err := runtime.retirementObservation(cleanup, scope)
	if err != nil {
		return err
	}
	if observation.Phase == "quiescent" {
		return nil
	}
	code, err := runtime.launchctl(cleanup, "bootout", runtime.serviceTarget(scope))
	if err != nil || (code != 0 && code != int(syscall.ESRCH)) {
		return fmt.Errorf("retire exact factory launchd service (exit %d): %w", code, errors.Join(err, errors.New("bootout not confirmed")))
	}
	if observation.PID != 0 || observation.PGID != 0 {
		if observation.PID <= 1 || observation.PGID != observation.PID {
			return errors.New("factory retirement lacks authenticated job process-group identity")
		}
		if err := waitFactoryGroupEmpty(cleanup, observation.PGID, runtime.probe); err != nil {
			return err
		}
	} else if observation.Phase == "granted" {
		return errors.New("granted factory scope has no authenticated group")
	}
	if err := runtime.authority.MarkQuiescent(cleanup, scope); err != nil {
		return fmt.Errorf("record factory scope quiescence: %w", err)
	}
	return nil
}

func (runtime *factoryLaunchdRuntime) retirementDeadline() (time.Time, error) {
	now := time.Now()
	if !now.Before(runtime.config.HardStop) {
		return time.Time{}, errors.New("factory retirement hard stop elapsed")
	}
	deadline := now.Add(runtime.config.CleanupTimeout)
	if runtime.config.HardStop.Before(deadline) {
		deadline = runtime.config.HardStop
	}
	return deadline, nil
}

func (runtime *factoryLaunchdRuntime) retirementObservation(ctx context.Context, scope factory.ScopeHandle) (factory.ScopeObservation, error) {
	runtime.mu.Lock()
	call := runtime.active[scope.ID]
	if call != nil && call.scope != scope {
		runtime.mu.Unlock()
		return factory.ScopeObservation{}, errors.New("factory retirement handle differs from active invocation")
	}
	if call != nil {
		call.retiring = true
		call.cancel()
	}
	runtime.mu.Unlock()
	// A canceled bootstrap may still be completing its launchd request. Wait for
	// the owned control command before bootout so late creation cannot escape it.
	if call != nil && call.done != nil {
		select {
		case <-call.done:
		case <-ctx.Done():
			return factory.ScopeObservation{}, fmt.Errorf("wait for owned factory bootstrap: %w", ctx.Err())
		}
	}
	observation, err := runtime.authority.Observe(ctx, scope)
	if err != nil {
		return observation, fmt.Errorf("observe factory retirement ownership: %w", err)
	}
	return observation, nil
}

func (runtime *factoryLaunchdRuntime) validateScope(scope factory.ScopeHandle) error {
	if err := validateFactoryJobScope(scope); err != nil {
		return err
	}
	expected := "dev.getoro.factory." + factoryLaunchdObjectiveHash(runtime.config) + "." + scope.Kind + "." + scope.ID + "." + scope.Nonce
	if scope.Name != expected {
		return errors.New("factory scope label belongs to a different objective")
	}
	return nil
}

func (*factoryLaunchdRuntime) serviceTarget(scope factory.ScopeHandle) string {
	return "gui/" + strconv.Itoa(os.Getuid()) + "/" + scope.Name
}

func runFactoryLaunchctl(ctx context.Context, args ...string) (int, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return -1, errors.New("factory launchctl requires a fixed deadline")
	}
	result, err := executeFactoryHostCommand(ctx, factory.HostInvocation{Path: "/bin/launchctl", Args: args, Env: []string{"LC_ALL=C"}, Dir: "/private/tmp", Deadline: deadline, OutputLimit: 4096})
	if err != nil {
		return result.ExitCode, fmt.Errorf("execute bounded launchctl: %w", err)
	}
	return result.ExitCode, nil
}

var _ factory.HostRuntime = (*factoryLaunchdRuntime)(nil)
