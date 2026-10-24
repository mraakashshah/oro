package main

import (
	"context"
	"errors"

	"oro/pkg/factory"
)

// The caller obtains view through factory.ReadManagedAdmission and freezes the
// artifact reference. This callback verifies immutable inputs only; the core
// separately authenticates committed admission and current work authority.
// ImageDigest is not represented in this volume artifact: PolicyHash binds the
// admitted image policy, without claiming a separate image verification here.
func newFactoryDockerConfigReader(view factory.ManagedAdmissionView, runtimePath, runtimeSHA256 string) factory.ManagedDockerConfigReader {
	return func(ctx context.Context) (factory.ManagedDockerConfig, error) {
		verified, err := readFactoryRuntimeConfig(ctx, runtimePath, runtimeSHA256)
		if err != nil {
			return factory.ManagedDockerConfig{}, err
		}
		if !factoryDockerStorageIdentityMatches(view, verified.factoryRuntimeConfig) {
			return factory.ManagedDockerConfig{}, errors.New("factory Docker storage runtime differs from verified admission view")
		}
		return factory.ManagedDockerConfig{
			StateRoot: verified.StateRoot, ObjectiveID: verified.ObjectiveID, PolicyHash: verified.PolicyHash,
			Deadline: verified.Deadline, HardStop: verified.HardStop, RuntimeConfigPath: verified.RuntimeConfigPath, RuntimeConfigSHA256: verified.RuntimeConfigSHA256,
			DockerPath: verified.DockerPath, DockerSHA256: verified.DockerSHA256, DockerConfig: verified.DockerConfig, DockerContext: verified.DockerContext, DockerEndpoint: verified.DockerEndpoint,
		}, nil
	}
}

func factoryDockerStorageIdentityMatches(view factory.ManagedAdmissionView, artifact factoryRuntimeConfig) bool {
	return view.StateRoot == artifact.StateRoot && view.ObjectiveID == artifact.ObjectiveID && view.PolicyHash == artifact.PolicyHash &&
		view.GitPath == artifact.GitPath && view.GitSHA256 == artifact.GitSHA256 && view.Repository == artifact.Repository && view.TargetRef == artifact.TargetRef &&
		view.Deadline.Equal(artifact.Deadline) && view.HardStop.Equal(artifact.HardStop) && view.CleanupTimeoutSeconds == artifact.CleanupTimeoutSeconds && view.DockerContext == artifact.DockerContext
}
