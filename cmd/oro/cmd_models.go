package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"oro/pkg/modelartifacts"

	"github.com/spf13/cobra"
)

// defaultModelDir returns the default model directory (~/.oro/models).
func defaultModelDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".oro", "models")
	}
	return filepath.Join(home, ".oro", "models")
}

// resolveModelDir returns dir if non-empty, otherwise the default.
func resolveModelDir(dir string) string {
	if dir != "" {
		return dir
	}
	return defaultModelDir()
}

// modelLocalPath returns the expected local path for a spec under modelDir.
func modelLocalPath(modelDir string, spec modelartifacts.ModelSpec) string {
	return filepath.Join(modelDir, spec.Name, spec.Filename)
}

// newModelsCmd creates the "oro models" parent command.
func newModelsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "models",
		Short: "Manage ONNX model artifacts for semantic memory",
		Long:  "Commands for listing, verifying, and prefetching ONNX model artifacts used by semantic memory.",
	}
	cmd.AddCommand(newModelsListCmd())
	cmd.AddCommand(newModelsVerifyCmd())
	cmd.AddCommand(newModelsPrefetchCmd())
	return cmd
}

// newModelsListCmd creates the production "oro models list" subcommand.
func newModelsListCmd() *cobra.Command {
	var modelDir string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List known ONNX models and their local presence",
		Long:  "Print one row per known model with columns: NAME, FILENAME, SHA256, PRESENT, PATH.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runModelsList(cmd, modelartifacts.KnownModels, resolveModelDir(modelDir))
		},
	}
	cmd.Flags().StringVar(&modelDir, "model-dir", "", "model directory (default ~/.oro/models)")
	return cmd
}

// newModelsListCmdWithSpecs creates the "oro models list" subcommand with injected specs/dir (for testing).
func newModelsListCmdWithSpecs(specs []modelartifacts.ModelSpec, modelDir string) *cobra.Command {
	cmd := &cobra.Command{
		Use: "list",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runModelsList(cmd, specs, modelDir)
		},
	}
	return cmd
}

// runModelsList prints a table of model specs and their local presence.
func runModelsList(cmd *cobra.Command, specs []modelartifacts.ModelSpec, modelDir string) error {
	const colFmt = "%-30s %-16s %-20s %-7s %s\n"
	fmt.Fprintf(cmd.OutOrStdout(), colFmt, "NAME", "FILENAME", "SHA256", "PRESENT", "PATH")
	fmt.Fprintf(cmd.OutOrStdout(), "%s\n", strings.Repeat("-", 100))
	for _, s := range specs {
		path := modelLocalPath(modelDir, s)
		present := fileExists(path)
		fmt.Fprintf(cmd.OutOrStdout(), colFmt, s.Name, s.Filename, s.SHA256, fmt.Sprintf("%v", present), path)
	}
	return nil
}

// newModelsVerifyCmd creates the production "oro models verify" subcommand.
func newModelsVerifyCmd() *cobra.Command {
	var modelDir string
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify SHA256 digests of downloaded models",
		Long:  "Check each known model's SHA256. Exits 0 if all present files match; exits 1 on any mismatch.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runModelsVerify(cmd, modelartifacts.KnownModels, resolveModelDir(modelDir))
		},
	}
	cmd.Flags().StringVar(&modelDir, "model-dir", "", "model directory (default ~/.oro/models)")
	return cmd
}

// newModelsVerifyCmdWithSpecs creates the "oro models verify" subcommand with injected specs/dir (for testing).
func newModelsVerifyCmdWithSpecs(specs []modelartifacts.ModelSpec, modelDir string) *cobra.Command {
	cmd := &cobra.Command{
		Use: "verify",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runModelsVerify(cmd, specs, modelDir)
		},
	}
	return cmd
}

// runModelsVerify checks SHA256 of each spec and writes one stderr line per failure.
// Returns an error (exit 1) if any check fails.
func runModelsVerify(cmd *cobra.Command, specs []modelartifacts.ModelSpec, modelDir string) error {
	var failures int
	for _, s := range specs {
		path := modelLocalPath(modelDir, s)
		if err := modelartifacts.VerifyModel(path, s.SHA256); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "%s: %v\n", s.Name, err)
			failures++
		}
	}
	if failures > 0 {
		return fmt.Errorf("verify: %d model(s) failed checks", failures)
	}
	return nil
}

// newModelsPrefetchCmd creates the production "oro models prefetch" subcommand.
func newModelsPrefetchCmd() *cobra.Command {
	var modelDir string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "prefetch",
		Short: "Download missing or outdated ONNX model artifacts",
		Long:  "Download and verify each known model into the model directory. Use --dry-run to preview without downloading.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runModelsPrefetch(cmd, modelartifacts.KnownModels, resolveModelDir(modelDir), dryRun)
		},
	}
	cmd.Flags().StringVar(&modelDir, "model-dir", "", "model directory (default ~/.oro/models)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print URLs and target paths without downloading")
	return cmd
}

// newModelsPrefetchCmdWithSpecs creates the "oro models prefetch" subcommand with injected specs/dir (for testing).
func newModelsPrefetchCmdWithSpecs(specs []modelartifacts.ModelSpec, modelDir string) *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use: "prefetch",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runModelsPrefetch(cmd, specs, modelDir, dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print URLs and target paths without downloading")
	return cmd
}

// runModelsPrefetch downloads models or (in dry-run) prints what it would fetch.
func runModelsPrefetch(cmd *cobra.Command, specs []modelartifacts.ModelSpec, modelDir string, dryRun bool) error {
	if dryRun {
		for _, s := range specs {
			path := modelLocalPath(modelDir, s)
			fmt.Fprintf(cmd.OutOrStdout(), "%s  →  %s\n", s.URL, path)
		}
		return nil
	}
	if err := modelartifacts.PrefetchModels(context.Background(), modelDir, specs); err != nil {
		return fmt.Errorf("prefetch: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "All models up to date.\n")
	return nil
}
