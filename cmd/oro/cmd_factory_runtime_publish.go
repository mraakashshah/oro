package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"oro/pkg/factory"
)

type (
	factoryRuntimeInputs struct{ OroPath, OroSHA256, DockerPath, DockerSHA256, DockerEndpoint string }
	factoryRuntimeIntent struct {
		Version         int                  `json:"version"`
		OriginalOroPath string               `json:"original_oro_path"`
		Config          factoryRuntimeConfig `json:"config"`
	}
)

func publishFactoryRuntime(ctx context.Context, view factory.ManagedAdmissionView, inputs factoryRuntimeInputs) (factory.ManagedRuntimeReference, error) {
	var zero factory.ManagedRuntimeReference
	if err := ctx.Err(); err != nil {
		return zero, fmt.Errorf("publication: %w", err)
	}
	if !time.Now().Before(view.Deadline) {
		return zero, errors.New("factory publication work deadline expired")
	}
	current, err := factory.ReadManagedAdmission(ctx, view.StateRoot, view.ObjectiveID)
	if err != nil {
		return zero, fmt.Errorf("publication: %w", err)
	}
	if !reflect.DeepEqual(current, view) {
		return zero, errors.New("factory publication admission changed")
	}
	if !factoryJobAbsolutePath(inputs.OroPath) {
		return zero, errors.New("factory Oro input path must be canonical")
	}
	config := factoryRuntimeConfig{Version: 1, StateRoot: view.StateRoot, ObjectiveID: view.ObjectiveID, PolicyHash: view.PolicyHash, OroPath: filepath.Join(view.StateRoot, view.ObjectiveID, "oro"), OroSHA256: inputs.OroSHA256, GitPath: view.GitPath, GitSHA256: view.GitSHA256, Repository: view.Repository, TargetRef: view.TargetRef, Deadline: view.Deadline, HardStop: view.HardStop, CleanupTimeoutSeconds: view.CleanupTimeoutSeconds, DockerPath: inputs.DockerPath, DockerSHA256: inputs.DockerSHA256, DockerContext: view.DockerContext, DockerEndpoint: inputs.DockerEndpoint}
	data, err := canonicalFactoryRuntimeConfig(config)
	if err != nil {
		return zero, fmt.Errorf("publication: %w", err)
	}
	intent, err := json.Marshal(factoryRuntimeIntent{Version: 1, OriginalOroPath: inputs.OroPath, Config: config})
	if err != nil || len(intent) > factoryRuntimeConfigMaxBytes {
		return zero, errors.New("factory publication intent exceeds bound")
	}
	if len(intent)+len(data)+factoryRuntimeBundleMetadataBytes+4*(64<<10)+factoryRuntimeContextMaxBytes > factoryRuntimeBundleMaxBytes {
		return zero, errors.New("factory publication exceeds runtime allowance")
	}
	ctx, cancel := context.WithDeadline(ctx, view.Deadline)
	defer cancel()
	objective, lock, err := openFactoryPublication(ctx, view, true)
	if err != nil {
		return zero, fmt.Errorf("publication: %w", err)
	}
	defer objective.Close()
	defer lock.Close()
	current, err = factory.ReadManagedAdmission(ctx, view.StateRoot, view.ObjectiveID)
	if err != nil || !reflect.DeepEqual(current, view) {
		return zero, errors.New("factory publication admission changed")
	}
	if err := buildFactoryPublishedRuntime(ctx, objective, inputs, config, data, intent); err != nil {
		return zero, err
	}
	ref := factory.ManagedRuntimeReference{Path: factoryRuntimeConfigPath(config), SHA256: factoryHostDigest(data)}
	if err := verifyFactoryPublishedRuntimeLocked(ctx, view, ref, objective); err != nil {
		return zero, fmt.Errorf("publication: %w", err)
	}
	return ref, nil
}

func buildFactoryPublishedRuntime(ctx context.Context, objective *os.Root, inputs factoryRuntimeInputs, config factoryRuntimeConfig, data, intent []byte) error {
	if err := ensureFactoryPublicationIntent(objective, intent); err != nil {
		return fmt.Errorf("publication: %w", err)
	}
	if err := ensureFactoryPublicationOro(ctx, objective, inputs); err != nil {
		return fmt.Errorf("publication: %w", err)
	}
	if err := verifyFactoryRuntimeUniqueProgram(ctx, inputs.DockerPath, inputs.DockerSHA256); err != nil {
		return fmt.Errorf("publication: %w", err)
	}
	if err := verifyFactoryRuntimeEndpoint(inputs.DockerEndpoint); err != nil {
		return fmt.Errorf("publication: %w", err)
	}
	if err := ensureFactoryPublicationBundle(objective, config, data); err != nil {
		return fmt.Errorf("publication: %w", err)
	}
	return nil
}

func verifyFactoryPublishedRuntime(ctx context.Context, view factory.ManagedAdmissionView, ref factory.ManagedRuntimeReference) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("publication: %w", err)
	}
	current, err := factory.ReadManagedAdmission(ctx, view.StateRoot, view.ObjectiveID)
	if err != nil {
		return fmt.Errorf("publication: %w", err)
	}
	if !reflect.DeepEqual(current, view) {
		return errors.New("factory publication admission changed")
	}
	objective, lock, err := openFactoryPublication(ctx, view, false)
	if err != nil {
		return fmt.Errorf("publication: %w", err)
	}
	defer objective.Close()
	defer lock.Close()
	return verifyFactoryPublishedRuntimeLocked(ctx, view, ref, objective)
}

func verifyFactoryPublishedRuntimeLocked(ctx context.Context, view factory.ManagedAdmissionView, ref factory.ManagedRuntimeReference, objective *os.Root) error {
	if ref.Path != filepath.Join(view.StateRoot, view.ObjectiveID, "runtime", "runtime.json") {
		return errors.New("factory publication reference mismatch")
	}
	info, err := objective.Lstat("oro")
	if err != nil || !factoryHostPrivateOwner(info) || info.Mode().Perm() != 0o500 || info.Size() <= 0 {
		return errors.New("invalid published Oro")
	}
	config, err := readFactoryRuntimeConfig(ctx, ref.Path, ref.SHA256)
	if err != nil {
		return fmt.Errorf("read published runtime: %w", err)
	}
	if config.OroPath != filepath.Join(view.StateRoot, view.ObjectiveID, "oro") || !factoryDockerStorageIdentityMatches(view, config.factoryRuntimeConfig) {
		return errors.New("factory runtime differs from admission")
	}
	return nil
}
