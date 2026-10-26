package main

import (
	"context"
	"errors"
	"time"

	"github.com/spf13/cobra"

	"oro/pkg/factory"
)

func newFactoryControllerCmd() *cobra.Command {
	return buildFactoryControllerCmd(openFactoryLaunchdRuntime)
}

//nolint:wrapcheck // The hidden command propagates underlying one-shot failures.
func buildFactoryControllerCmd(openRuntime func(context.Context, factoryLaunchdConfig) (*factoryLaunchdRuntime, error)) *cobra.Command {
	var path, digest, socket string
	cmd := &cobra.Command{Use: "_controller", Hidden: true, Args: cobra.NoArgs, SilenceUsage: true, RunE: func(cmd *cobra.Command, _ []string) error {
		request, err := readFactoryControllerRequest(path, digest)
		if err != nil {
			return err
		}
		if err := authorizeFactoryController(cmd.Context(), request, digest, socket); err != nil {
			return err
		}
		ctx, cancel := context.WithDeadline(cmd.Context(), request.Deadline)
		defer cancel()
		view, err := factory.ReadManagedAdmission(ctx, request.StateRoot, request.ObjectiveID)
		if err != nil {
			return err
		}
		ref, err := factory.ReadManagedRuntimeReference(ctx, request.StateRoot, request.ObjectiveID)
		if err != nil {
			return err
		}
		if ref != request.Runtime || view.StateRoot != request.StateRoot || view.ObjectiveID != request.ObjectiveID || !view.Deadline.Equal(request.Deadline) {
			return errors.New("controller authority differs from admission")
		}
		artifact, err := readFactoryRuntimeConfig(ctx, ref.Path, ref.SHA256)
		if err != nil {
			return err
		}
		if !factoryDockerStorageIdentityMatches(view, artifact.factoryRuntimeConfig) {
			return errors.New("controller runtime differs from admission")
		}
		config := factoryLaunchdConfig{StateRoot: view.StateRoot, ObjectiveID: view.ObjectiveID, OroPath: artifact.OroPath, OroSHA256: artifact.OroSHA256, GitPath: view.GitPath, GitSHA256: view.GitSHA256, Repository: view.Repository, TargetRef: view.TargetRef, RuntimeConfigPath: ref.Path, RuntimeConfigSHA256: ref.SHA256, HardStop: view.HardStop, CleanupTimeout: time.Duration(view.CleanupTimeoutSeconds) * time.Second}
		host, err := openRuntime(ctx, config)
		if err != nil {
			return err
		}
		defer host.Close()
		_, err = factory.RunManagedController(ctx, request.StateRoot, request.ObjectiveID, request.Scope.Generation, request.Scope.ID, host, newFactoryDockerConfigReader(view, ref.Path, ref.SHA256))
		return err
	}}
	cmd.Flags().StringVar(&path, "request", "", "immutable request")
	cmd.Flags().StringVar(&digest, "request-sha256", "", "request digest")
	cmd.Flags().StringVar(&socket, "socket", "", "authority socket")
	for _, flag := range []string{"request", "request-sha256", "socket"} {
		_ = cmd.MarkFlagRequired(flag)
	}
	return cmd
}
