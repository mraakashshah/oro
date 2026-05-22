package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

// checkpointHarnessRunner runs the §18.3 verify-checkpoint test and returns
// the result. Decoupled from go-test subprocess to allow injection in tests.
type checkpointHarnessRunner interface {
	run(ctx context.Context) (passed bool, output string, err error)
}

// goTestCheckpointRunner invokes the dispatcher package's E2E test via go test.
type goTestCheckpointRunner struct{}

func (r *goTestCheckpointRunner) run(ctx context.Context) (passed bool, output string, err error) {
	modOut, err := exec.CommandContext(ctx, "go", "env", "GOMOD").Output()
	if err != nil {
		return false, "", fmt.Errorf("locate module root: %w", err)
	}
	modRoot := strings.TrimSuffix(strings.TrimSpace(string(modOut)), "/go.mod")
	modRoot = strings.TrimSuffix(modRoot, "go.mod") // Windows fallback

	cmd := exec.CommandContext(ctx, "go", "test",
		"-v", "-count=1", "-timeout=120s",
		"-run", "TestCheckpointE2EFromHighContext",
		"./pkg/dispatcher/",
	)
	cmd.Dir = modRoot
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	runErr := cmd.Run()
	output = out.String()
	passed = runErr == nil && strings.Contains(output, "--- PASS: TestCheckpointE2EFromHighContext")
	return passed, output, nil
}

func newHarnessCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "harness",
		Short:        "Run Oro harness verification tests (§18)",
		Long:         "oro harness runs multi-phase E2E verification tests defined in §18 of the harness architecture spec.",
		SilenceUsage: true,
	}
	cmd.AddCommand(newHarnessVerifyCheckpointCmd(), newHarnessDogfoodCmd())
	return cmd
}

func newHarnessVerifyCheckpointCmd() *cobra.Command {
	return newHarnessVerifyCheckpointCmdWithRunner(&goTestCheckpointRunner{})
}

func newHarnessVerifyCheckpointCmdWithRunner(runner checkpointHarnessRunner) *cobra.Command {
	return &cobra.Command{
		Use:           "verify-checkpoint",
		Short:         "§18.3 verify-checkpoint — context-safety control loop E2E test",
		Long:          "verify-checkpoint runs the §18.3 checkpoint E2E test: dispatcher emits checkpoint, worker respawns, bead closes.",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			passed, output, err := runner.run(ctx)
			if err != nil {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "§18.3 verify-checkpoint ERROR: %v\n", err)
				return err
			}

			if passed {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "§18.3 verify-checkpoint PASS\n%s", output)
				return nil
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "§18.3 verify-checkpoint FAIL\n%s", output)
			return fmt.Errorf("§18.3 verify-checkpoint: FAIL")
		},
	}
}
