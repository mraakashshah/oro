package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"oro/pkg/factory"

	"github.com/spf13/cobra"
)

func newFactoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "factory",
		Short: "Experimental autonomous delivery with trusted command adapters",
		Long: "Experimental autonomous delivery and recovery using trusted command adapters.\n" +
			"Use disposable or externally isolated repositories; this command does not sandbox programs.\n" +
			"State is stored only in the explicitly supplied state directory.",
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(newFactoryRunCmd(), newFactoryStatusCmd(), newFactoryHostExecCmd(), newFactoryDockerReadyCmd(), newFactoryDockerCleanupCmd(), newFactoryControllerCmd(), newFactorySuperviseCmd())
	return cmd
}

func newFactoryRunCmd() *cobra.Command {
	var specPath, stateDir string
	cmd := &cobra.Command{
		Use:   "run --spec <file> --state-dir <directory>",
		Short: "Run an experimental objective with trusted worker, acceptance and review programs",
		Long: "Run or resume an experimental objective with trusted command adapters and a fixed retry budget.\n" +
			"The spec is strict JSON. The state directory must be absolute and outside the source repository.\n" +
			"Print the durable terminal status as JSON; return success only when the objective completed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			spec, err := readFactorySpec(specPath)
			if err != nil {
				return err
			}
			status, err := factory.Run(cmd.Context(), stateDir, spec)
			if err != nil {
				return fmt.Errorf("run factory: %w", err)
			}
			if err := writeFactoryStatus(cmd.OutOrStdout(), status); err != nil {
				return err
			}
			if status.State != "completed" {
				return fmt.Errorf("factory objective %q %s: %s", status.ID, status.State, status.Reason)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&specPath, "spec", "", "objective specification JSON file (required)")
	cmd.Flags().StringVar(&stateDir, "state-dir", "", "absolute factory state directory (required)")
	_ = cmd.MarkFlagRequired("spec")
	_ = cmd.MarkFlagRequired("state-dir")
	return cmd
}

func newFactoryStatusCmd() *cobra.Command {
	var id, stateDir string
	cmd := &cobra.Command{
		Use:   "status --id <objective> --state-dir <directory>",
		Short: "Read experimental factory status without executing trusted adapters",
		Long: "Read existing experimental factory state as JSON without starting trusted command adapters\n" +
			"or changing the objective. Missing state is an error. A successful read returns success\n" +
			"even when the stored objective failed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			status, err := factory.ReadStatus(cmd.Context(), stateDir, id)
			if err != nil {
				return fmt.Errorf("read factory status: %w", err)
			}
			return writeFactoryStatus(cmd.OutOrStdout(), status)
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "objective ID (required)")
	cmd.Flags().StringVar(&stateDir, "state-dir", "", "absolute factory state directory (required)")
	_ = cmd.MarkFlagRequired("id")
	_ = cmd.MarkFlagRequired("state-dir")
	return cmd
}

func readFactorySpec(path string) (factory.Spec, error) {
	file, err := os.Open(path) //nolint:gosec // Explicit local CLI input path, opened only for reading.
	if err != nil {
		return factory.Spec{}, fmt.Errorf("open factory specification: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var spec factory.Spec
	if err := decoder.Decode(&spec); err != nil {
		return factory.Spec{}, fmt.Errorf("decode factory specification: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return factory.Spec{}, errors.New("factory specification contains multiple JSON values")
		}
		return factory.Spec{}, fmt.Errorf("read trailing factory specification: %w", err)
	}
	return spec, nil
}

func writeFactoryStatus(output io.Writer, status factory.Status) error {
	if err := json.NewEncoder(output).Encode(status); err != nil {
		return fmt.Errorf("write factory status: %w", err)
	}
	return nil
}
