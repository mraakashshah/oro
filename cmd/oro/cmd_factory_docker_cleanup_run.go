package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	"oro/pkg/factory"
)

// The helper inherits the authenticated wrapper's group. It never records a new
// HELLO, allocates a scope, or treats a Docker observation as scope retirement.
func runFactoryDockerCleanup(ctx context.Context, config factoryDockerCleanupConfig, permit factory.DockerCleanupPermit, output io.Writer) error {
	if output == nil {
		return errors.New("factory cleanup requires an output writer")
	}
	inspect, err := factoryDockerCleanupInspectArgs(permit)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithDeadline(ctx, permit.Deadline)
	defer cancel()
	if err := verifyFactoryDockerCleanupConfig(ctx, config, permit); err != nil {
		return err
	}
	authority, err := factory.OpenScopeAuthority(ctx, config.StateRoot, config.ObjectiveID)
	if err != nil {
		return fmt.Errorf("open factory cleanup authority: %w", err)
	}
	defer authority.Close()
	runner := factoryDockerCleanupRunner{config: config, permit: permit, authority: authority}
	result, err := runner.observe(ctx, inspect)
	if err != nil {
		return err
	}
	if permit.Operation != factory.DockerCleanupInspect && result.Observation != "observed_absent" {
		result, err = runner.mutate(ctx, inspect, result.Binding)
		if err != nil {
			return err
		}
	}
	data, err := factory.EncodeDockerCleanupResult(result, permit)
	if err != nil {
		return fmt.Errorf("encode factory cleanup observation: %w", err)
	}
	if err := runner.authorize(ctx); err != nil {
		return err
	}
	n, err := output.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return fmt.Errorf("write factory cleanup observation: %w", err)
	}
	return factoryHostDeadline(ctx, permit.Deadline)
}

type factoryDockerCleanupRunner struct {
	config    factoryDockerCleanupConfig
	permit    factory.DockerCleanupPermit
	authority factory.ScopeAuthority
}

func (r factoryDockerCleanupRunner) observe(ctx context.Context, args []string) (factory.DockerCleanupResult, error) {
	child, err := r.execute(ctx, args)
	if err != nil {
		return factory.DockerCleanupResult{}, err
	}
	binding, observation, err := parseFactoryDockerCleanupInspection(r.permit, child)
	if err != nil {
		return factory.DockerCleanupResult{}, err
	}
	canonical, err := json.Marshal(r.permit)
	if err != nil {
		return factory.DockerCleanupResult{}, fmt.Errorf("encode cleanup permit identity: %w", err)
	}
	return factory.DockerCleanupResult{
		Version: 1, PermitSHA256: factoryHostDigest(canonical), Scope: r.permit.Scope, Target: r.permit.Target,
		Operation: r.permit.Operation, Binding: binding, Observation: observation,
	}, nil
}

func (r factoryDockerCleanupRunner) mutate(ctx context.Context, inspect []string, binding factory.DockerCleanupBinding) (factory.DockerCleanupResult, error) {
	args, err := factoryDockerCleanupMutationArgs(r.permit, binding)
	if err != nil {
		return factory.DockerCleanupResult{}, err
	}
	mutation, err := r.execute(ctx, args)
	if err != nil || mutation.ExitCode != 0 {
		return factory.DockerCleanupResult{}, errors.Join(errors.New("exact factory Docker mutation did not acknowledge success"), err)
	}
	result, err := r.observe(ctx, inspect)
	if err != nil {
		return factory.DockerCleanupResult{}, err
	}
	result.MutationAcknowledged = true
	// The shared result codec rejects a still-present remove target or a still-
	// running stop target. Failed postconditions do not become success receipts.
	return result, nil
}

func (r factoryDockerCleanupRunner) execute(ctx context.Context, args []string) (factory.HostResult, error) {
	if err := r.authorize(ctx); err != nil {
		return factory.HostResult{}, err
	}
	invocation := factory.HostInvocation{
		Scope: r.permit.Scope, Path: r.config.DockerPath, Dir: r.config.DockerConfig,
		Args:     append([]string{"--config", r.config.DockerConfig, "--context", r.config.DockerContext}, args...),
		Env:      []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "LANG=C"},
		Deadline: r.permit.Deadline, OutputLimit: factoryDockerCleanupInspectMaxBytes,
	}
	result, err := executeFactoryHostCommand(ctx, invocation)
	if err != nil {
		return result, fmt.Errorf("execute bounded factory Docker cleanup: %w", err)
	}
	return result, nil
}

func (r factoryDockerCleanupRunner) authorize(ctx context.Context) error {
	if err := verifyFactoryDockerCleanupConfig(ctx, r.config, r.permit); err != nil {
		return err
	}
	current, err := r.authority.CleanupPermit(ctx, r.permit.Scope)
	if err != nil {
		return fmt.Errorf("read current factory cleanup permit: %w", err)
	}
	want, wantErr := json.Marshal(r.permit)
	got, gotErr := json.Marshal(current)
	if wantErr != nil || gotErr != nil || !bytes.Equal(want, got) {
		return errors.New("factory cleanup permit changed")
	}
	granted, err := r.authority.Granted(ctx, r.permit.Scope)
	if err != nil || !granted {
		return errors.Join(errors.New("factory cleanup lacks a live execution grant"), err)
	}
	observation, err := r.authority.Observe(ctx, r.permit.Scope)
	if err != nil {
		return fmt.Errorf("observe factory cleanup ownership: %w", err)
	}
	group, err := syscall.Getpgid(os.Getpid())
	if err != nil || observation.Phase != "granted" || !observation.Deadline.Equal(r.permit.Deadline) ||
		observation.PID <= 1 || observation.PID != observation.PGID || group != observation.PGID {
		return errors.Join(errors.New("factory cleanup is outside its authenticated wrapper group"), err)
	}
	return factoryHostDeadline(ctx, r.permit.Deadline)
}

func verifyFactoryDockerCleanupConfig(ctx context.Context, config factoryDockerCleanupConfig, permit factory.DockerCleanupPermit) error {
	if err := factoryHostDeadline(ctx, permit.Deadline); err != nil {
		return err
	}
	verified, err := readFactoryRuntimeConfig(ctx, config.RuntimeConfigPath, config.RuntimeConfigSHA256)
	if err != nil {
		return err
	}
	objective := factoryHostDigest([]byte(verified.StateRoot + "\x00" + verified.ObjectiveID))[:16]
	sameHelper := factory.EqualModelAdapterConfig(verified.ModelHelper, config.ModelHelper)
	verifiedCopy, configCopy := verified, config
	verifiedCopy.ModelHelper, configCopy.ModelHelper = nil, nil
	if !sameHelper || verifiedCopy != configCopy || permit.Binding.Context != verified.DockerContext ||
		permit.Binding.Labels["dev.getoro.factory.objective"] != objective ||
		permit.Binding.Labels["dev.getoro.factory.policy"] != verified.PolicyHash || permit.Deadline.After(verified.HardStop) {
		return errors.New("factory cleanup runtime differs from its admitted permit")
	}
	return nil
}
