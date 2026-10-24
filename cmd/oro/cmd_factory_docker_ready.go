package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
	"time"

	"oro/pkg/factory"

	"github.com/spf13/cobra"
)

type factoryDockerReadyRequest struct {
	DockerPath, DockerSHA256, DockerConfig, Context string
	ContainerID, ScopeID, ScopeNonce, ScopeKind     string
	ObjectiveHash, PolicyHash, Image                string
	Generation                                      uint64
	GuardDeadline, Deadline                         time.Time
}

func newFactoryDockerReadyCmd() *cobra.Command {
	var request factoryDockerReadyRequest
	var guardDeadline, deadline string
	command := &cobra.Command{
		Use: "_docker-start-ready", Hidden: true, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var err error
			request.GuardDeadline, err = parseFactoryDockerDeadline(guardDeadline)
			if err != nil {
				return err
			}
			request.Deadline, err = parseFactoryDockerDeadline(deadline)
			if err != nil {
				return err
			}
			return runFactoryDockerReady(cmd.Context(), request, cmd.OutOrStdout())
		},
	}
	bindFactoryDockerReadyFlags(command, &request)
	command.Flags().StringVar(&guardDeadline, "guard-deadline", "", "immutable guard UTC deadline")
	command.Flags().StringVar(&deadline, "deadline", "", "immutable invocation UTC deadline")
	_ = command.MarkFlagRequired("guard-deadline")
	_ = command.MarkFlagRequired("deadline")
	return command
}

func bindFactoryDockerReadyFlags(command *cobra.Command, r *factoryDockerReadyRequest) {
	flags := []struct {
		name  string
		value *string
	}{
		{"docker", &r.DockerPath},
		{"docker-sha256", &r.DockerSHA256},
		{"docker-config", &r.DockerConfig},
		{"context", &r.Context},
		{"container", &r.ContainerID},
		{"scope-id", &r.ScopeID},
		{"scope-nonce", &r.ScopeNonce},
		{"scope-kind", &r.ScopeKind},
		{"objective-hash", &r.ObjectiveHash},
		{"policy-hash", &r.PolicyHash},
		{"image", &r.Image},
	}
	for _, flag := range flags {
		command.Flags().StringVar(flag.value, flag.name, "", "exact admitted "+flag.name)
		_ = command.MarkFlagRequired(flag.name)
	}
	command.Flags().Uint64Var(&r.Generation, "scope-generation", 0, "exact admitted generation")
	_ = command.MarkFlagRequired("scope-generation")
}

func parseFactoryDockerDeadline(value string) (time.Time, error) {
	deadline, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || deadline.UTC().Format(time.RFC3339Nano) != value {
		return time.Time{}, errors.New("factory Docker deadline must be canonical UTC RFC3339Nano")
	}
	return deadline, nil
}

func runFactoryDockerReady(ctx context.Context, request factoryDockerReadyRequest, output io.Writer) error {
	if output == nil {
		return errors.New("factory Docker readiness requires an output writer")
	}
	if err := validateFactoryDockerReady(ctx, request); err != nil {
		return err
	}
	ctx, cancel := context.WithDeadline(ctx, request.Deadline)
	defer cancel()
	inspection, err := executeFactoryHostCommand(ctx, factoryDockerInvocation(request, "inspect", "--type", "container", request.ContainerID))
	if err != nil || inspection.ExitCode != 0 {
		return errors.Join(errors.New("inspect exact factory Docker container failed"), err)
	}
	if err := validateFactoryDockerInspection(request, inspection.Stdout); err != nil {
		return err
	}
	if err := attachFactoryDockerReady(ctx, factoryDockerInvocation(request, "start", "--attach", request.ContainerID)); err != nil {
		return err
	}
	if err := factoryHostDeadline(ctx, request.Deadline); err != nil {
		return err
	}
	receipt := "READY " + request.ContainerID + " " + request.GuardDeadline.UTC().Format(time.RFC3339Nano) + "\n"
	n, err := io.WriteString(output, receipt)
	if err == nil && n != len(receipt) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return fmt.Errorf("write exact factory Docker readiness: %w", err)
	}
	return factoryHostDeadline(ctx, request.Deadline)
}

func validateFactoryDockerReady(ctx context.Context, r factoryDockerReadyRequest) error {
	if err := factoryHostDeadline(ctx, r.Deadline); err != nil {
		return err
	}
	if r.Deadline.After(r.GuardDeadline) || r.GuardDeadline.IsZero() || r.GuardDeadline.UTC().Year() > 9999 ||
		(r.ScopeKind != "keeper" && r.ScopeKind != "container") || r.Generation == 0 || r.Generation > math.MaxInt64 {
		return errors.New("factory Docker readiness requires exact admitted identities and bounds")
	}
	if err := validateFactoryDockerIdentities(r); err != nil {
		return err
	}
	if err := factoryHostPrivateDirectory(r.DockerConfig); err != nil {
		return err
	}
	return verifyFactoryRuntimeProgram(ctx, r.DockerPath, r.DockerSHA256)
}

func validateFactoryDockerIdentities(r factoryDockerReadyRequest) error {
	values := []struct {
		value  string
		length int
	}{{r.ContainerID, 64}, {r.ScopeID, 32}, {r.ScopeNonce, 32}, {r.ObjectiveHash, 16}, {r.PolicyHash, 64}}
	for _, value := range values {
		if !factoryJobLowerHex(value.value, value.length) {
			return errors.New("factory Docker readiness identity is not canonical hex")
		}
	}
	if !strings.HasPrefix(r.Image, "sha256:") || !factoryJobLowerHex(strings.TrimPrefix(r.Image, "sha256:"), 64) ||
		!regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`).MatchString(r.Context) {
		return errors.New("factory Docker readiness requires an immutable image and explicit context")
	}
	return nil
}

func factoryDockerInvocation(r factoryDockerReadyRequest, args ...string) factory.HostInvocation {
	return factory.HostInvocation{
		Path: r.DockerPath, Dir: r.DockerConfig, Args: append([]string{"--config", r.DockerConfig, "--context", r.Context}, args...),
		Env: []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "LANG=C"}, Deadline: r.Deadline, OutputLimit: 64 << 10,
	}
}
