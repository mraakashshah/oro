package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"time"

	"oro/pkg/factory"
)

// The optional reference preserves ordinary host/observer construction. Only a
// cleanup invocation reads the artifact; publication belongs to trusted start.
func validateFactoryCleanupConfigReference(config factoryLaunchdConfig) error {
	if config.RuntimeConfigPath == "" && config.RuntimeConfigSHA256 == "" {
		return nil
	}
	expected := filepath.Join(config.StateRoot, config.ObjectiveID, "runtime", "runtime.json")
	if !factoryJobAbsolutePath(config.RuntimeConfigPath) || config.RuntimeConfigPath != expected ||
		!factoryJobLowerHex(config.RuntimeConfigSHA256, 64) {
		return errors.New("factory cleanup requires its exact objective runtime path and SHA-256")
	}
	return nil
}

// The caller must obtain permit from current ScopeAuthority before any launch
// side effect. This restriction grants nothing and cannot authenticate admission
// from artifact bytes alone. The helper independently rechecks durable authority.
func validateFactoryCleanupInvocation(ctx context.Context, config factoryLaunchdConfig, invocation factory.HostInvocation, permit factory.DockerCleanupPermit) error {
	if err := factoryHostDeadline(ctx, invocation.Deadline); err != nil {
		return err
	}
	if err := validateFactoryCleanupConfigReference(config); err != nil {
		return err
	}
	if !factoryCleanupCommandMatches(config, invocation, permit) {
		return errors.New("factory cleanup permits only the exact frozen Oro helper invocation")
	}
	if err := validateFactoryDockerCleanupPlan(permit); err != nil {
		return err
	}
	canonical, err := json.Marshal(permit)
	if err != nil || !bytes.Equal(invocation.Stdin, canonical) {
		return errors.Join(errors.New("factory cleanup stdin differs from its canonical authenticated permit"), err)
	}
	ctx, cancel := context.WithDeadline(ctx, permit.Deadline)
	defer cancel()
	verified, err := readFactoryRuntimeConfig(ctx, config.RuntimeConfigPath, config.RuntimeConfigSHA256)
	if err != nil {
		return err
	}
	if !factoryCleanupRuntimeIdentityMatches(config, verified.factoryRuntimeConfig) ||
		permit.Binding.Context != verified.DockerContext || permit.Deadline.After(verified.HardStop) ||
		permit.Binding.Labels["dev.getoro.factory.objective"] != factoryLaunchdObjectiveHash(config) ||
		permit.Binding.Labels["dev.getoro.factory.policy"] != verified.PolicyHash {
		return errors.New("factory cleanup runtime differs from its trusted configuration or permit")
	}
	return factoryHostDeadline(ctx, permit.Deadline)
}

func factoryCleanupCommandMatches(config factoryLaunchdConfig, invocation factory.HostInvocation, permit factory.DockerCleanupPermit) bool {
	args := []string{"factory", "_docker-cleanup", "--runtime-config", config.RuntimeConfigPath, "--runtime-config-sha256", config.RuntimeConfigSHA256}
	env := []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "LANG=C"}
	return config.RuntimeConfigPath != "" && invocation.Path == config.OroPath && invocation.Dir == config.StateRoot &&
		slices.Equal(invocation.Args, args) && slices.Equal(invocation.Env, env) && invocation.OutputLimit == 1<<20 &&
		invocation.Scope == permit.Scope && invocation.Deadline.Equal(permit.Deadline)
}

func factoryCleanupRuntimeIdentityMatches(config factoryLaunchdConfig, artifact factoryRuntimeConfig) bool {
	return config.StateRoot == artifact.StateRoot && config.ObjectiveID == artifact.ObjectiveID &&
		config.OroPath == artifact.OroPath && config.OroSHA256 == artifact.OroSHA256 &&
		config.GitPath == artifact.GitPath && config.GitSHA256 == artifact.GitSHA256 &&
		config.Repository == artifact.Repository && config.TargetRef == artifact.TargetRef &&
		config.HardStop.Equal(artifact.HardStop) && config.CleanupTimeout == time.Duration(artifact.CleanupTimeoutSeconds)*time.Second
}
