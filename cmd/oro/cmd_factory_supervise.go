package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"oro/pkg/factory"
)

func newFactorySuperviseCmd() *cobra.Command {
	return buildFactorySuperviseCmd(openFactorySupervisorBackend)
}

func buildFactorySuperviseCmd(openBackend func(context.Context, string, string, factory.ManagedRuntimeReference) (*factorySupervisorBackend, error)) *cobra.Command {
	var root, id, path, digest, hardStop string
	cmd := &cobra.Command{Use: "_supervise", Hidden: true, Args: cobra.NoArgs, SilenceUsage: true, RunE: func(cmd *cobra.Command, _ []string) error {
		err := runFactorySupervise(cmd.Context(), root, id, path, digest, hardStop, openBackend)
		if err != nil {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), factorySupervisorDiagnostic(err))
		}
		return nil
	}}
	cmd.Flags().StringVar(&root, "state-dir", "", "state directory")
	cmd.Flags().StringVar(&id, "id", "", "objective")
	cmd.Flags().StringVar(&path, "runtime-config", "", "runtime configuration")
	cmd.Flags().StringVar(&digest, "runtime-config-sha256", "", "runtime digest")
	cmd.Flags().StringVar(&hardStop, "hard-stop", "", "original hard stop")
	for _, flag := range []string{"state-dir", "id", "runtime-config", "runtime-config-sha256", "hard-stop"} {
		_ = cmd.MarkFlagRequired(flag)
	}
	return cmd
}

const factorySupervisorDiagnosticLimit = 4096

func factorySupervisorDiagnostic(err error) string {
	const prefix = "factory supervisor stopped: "
	var result strings.Builder
	result.WriteString(prefix)
	for _, r := range err.Error() {
		if unicode.IsControl(r) || r == utf8.RuneError {
			r = ' '
		}
		if result.Len()+utf8.RuneLen(r)+1 > factorySupervisorDiagnosticLimit {
			break
		}
		result.WriteRune(r)
	}
	return result.String()
}

//nolint:wrapcheck // The service caller emits one sanitized diagnostic.
func runFactorySupervise(parent context.Context, root, id, path, digest, hardStop string, openBackend func(context.Context, string, string, factory.ManagedRuntimeReference) (*factorySupervisorBackend, error)) error {
	stop, err := time.Parse(time.RFC3339Nano, hardStop)
	if err != nil || stop.Location() != time.UTC || stop.Format(time.RFC3339Nano) != hardStop || !time.Now().Before(stop) {
		return errors.New("invalid or expired hard stop")
	}
	ctx, cancel := context.WithDeadline(parent, stop)
	defer cancel()
	view, err := factory.ReadManagedAdmission(ctx, root, id)
	if err != nil {
		return err
	}
	ref := factory.ManagedRuntimeReference{Path: path, SHA256: digest}
	active, err := factory.ReadManagedRuntimeReference(ctx, root, id)
	if err != nil {
		return err
	}
	if ref != active || !view.HardStop.Equal(stop) {
		return errors.New("supervisor authority differs from admission")
	}
	backend, err := openBackend(ctx, root, id, ref)
	if err != nil {
		return err
	}
	defer backend.Close()
	_, err = factory.Supervise(ctx, root, id, backend)
	return err
}
