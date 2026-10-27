package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"oro/pkg/factory"

	"github.com/spf13/cobra"
)

func newFactoryModelCmd() *cobra.Command {
	var path, digest string
	cmd := &cobra.Command{Use: "_model-terra", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return runFactoryModelEntry(cmd, path, digest)
	}}
	cmd.Flags().StringVar(&path, "runtime-config", "", "exact frozen runtime artifact")
	cmd.Flags().StringVar(&digest, "runtime-config-sha256", "", "admitted runtime artifact SHA-256")
	_ = cmd.MarkFlagRequired("runtime-config")
	_ = cmd.MarkFlagRequired("runtime-config-sha256")
	return cmd
}

func runFactoryModelEntry(cmd *cobra.Command, path, digest string) error {
	config, err := readFactoryRuntimeConfig(cmd.Context(), path, digest)
	if err != nil {
		return err
	}
	if config.ModelHelper == nil {
		return errors.New("terra model adapter unavailable")
	}
	input, ok := cmd.InOrStdin().(*os.File)
	if !ok || input == nil {
		return errors.New("model helper requires inherited stdin pipe")
	}
	data, err := readFactoryModelInput(cmd.Context(), input, config.Deadline)
	if err != nil {
		return err
	}
	request, err := factory.DecodeModelRequest(data)
	if err != nil {
		return fmt.Errorf("decode model request: %w", err)
	}
	ctx, cancel := context.WithDeadline(cmd.Context(), request.ExecutionDeadline)
	defer cancel()
	if err := verifyFactoryModelEntry(ctx, config, request, path, digest); err != nil {
		return err
	}
	if err := factory.RunTerraModel(ctx, request, cmd.OutOrStdout()); err != nil {
		return fmt.Errorf("model helper: %w", err)
	}
	return nil
}

func verifyFactoryModelEntry(ctx context.Context, config factoryDockerCleanupConfig, request factory.ModelRequest, path, digest string) error {
	ref := factory.ManagedRuntimeReference{Path: path, SHA256: digest}
	if request.StateRoot != config.StateRoot || request.ObjectiveID != config.ObjectiveID || request.Runtime != ref || !factory.EqualModelAdapterConfig(&request.Adapter, config.ModelHelper) || request.ExecutionDeadline.After(config.Deadline) || !time.Now().Before(request.ExecutionDeadline) {
		return errors.New("model request differs from frozen runtime")
	}
	active, err := factory.ReadManagedRuntimeReference(ctx, request.StateRoot, request.ObjectiveID)
	if err != nil || active != ref {
		return errors.New("model runtime is not active")
	}
	return verifyFactoryModelProgram(ctx, config, request)
}

func verifyFactoryModelProgram(ctx context.Context, config factoryDockerCleanupConfig, request factory.ModelRequest) error {
	view, err := factory.ReadManagedAdmission(ctx, request.StateRoot, request.ObjectiveID)
	if err != nil || !factoryDockerStorageIdentityMatches(view, config.factoryRuntimeConfig) || view.ModelSHA256 != config.OroSHA256 || view.ModelSHA256 != config.ModelHelper.HelperSHA256 {
		return errors.New("model helper differs from admitted program")
	}
	executable, err := os.Executable()
	if err != nil || executable != view.ModelPath {
		return errors.New("model helper is not the admitted executable")
	}
	actual, err := factoryStartProgramDigest(ctx, executable)
	if err != nil || actual != config.ModelHelper.HelperSHA256 {
		return errors.New("model helper bytes differ from frozen runtime")
	}
	return nil
}

func readFactoryModelInput(ctx context.Context, input *os.File, deadline time.Time) ([]byte, error) {
	if err := factoryHostDeadline(ctx, deadline); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	pipe, err := openFactoryCleanupInput(input)
	if err != nil {
		return nil, err
	}
	if err := pipe.file.SetReadDeadline(deadline); err != nil {
		return nil, errors.Join(err, pipe.close())
	}
	done := make(chan error, 1)
	stop := context.AfterFunc(ctx, func() { done <- pipe.file.SetReadDeadline(time.Now()) })
	data, readErr := io.ReadAll(io.LimitReader(pipe.file, (64<<10)+1))
	var callbackErr error
	if !stop() {
		callbackErr = <-done
	}
	if err := errors.Join(readErr, callbackErr, pipe.close(), ctx.Err()); err != nil {
		return nil, err
	}
	if len(data) > 64<<10 {
		return nil, errors.New("model request exceeds limit")
	}
	return data, nil
}
