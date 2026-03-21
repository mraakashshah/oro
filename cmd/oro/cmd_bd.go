package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"
)

// bdRunner abstracts executing bd for testability.
type bdRunner func(bdPath string, args []string) error

// bdDeps holds injectable dependencies for runBd.
type bdDeps struct {
	runner   bdRunner
	lookPath func(string) (string, error)
}

// execBdRunner replaces the current process with bd.
func execBdRunner(bdPath string, args []string) error {
	if err := syscall.Exec(bdPath, append([]string{bdPath}, args...), os.Environ()); err != nil { //nolint:gosec // intentionally replacing process with user-selected binary
		return fmt.Errorf("exec bd: %w", err)
	}
	return nil
}

// newBdCmd creates the "oro bd" subcommand.
func newBdCmd() *cobra.Command {
	return newBdCmdWithDeps(bdDeps{
		runner:   execBdRunner,
		lookPath: exec.LookPath,
	})
}

func newBdCmdWithDeps(deps bdDeps) *cobra.Command {
	return &cobra.Command{
		Use:                "bd",
		Short:              "Run bd with project-aware --db flag",
		Long:               "Wrapper around bd that resolves the project from CWD.\nIn stealth mode, prepends --db pointing to ~/.oro/projects/s-<hash>/beads/.\nIn standard mode, passes arguments through unchanged.",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBd(".", args, deps)
		},
	}
}

// runBd is the testable core of the bd wrapper command.
// dir is used as the repo root for project detection (typically ".").
// args are the arguments to pass to bd (after any injected --db flag).
func runBd(dir string, args []string, deps bdDeps) error {
	// 1. Locate bd binary.
	bdPath, err := deps.lookPath("bd")
	if err != nil {
		return fmt.Errorf("bd not found in PATH: install it from https://github.com/... or ensure it is on your PATH")
	}

	// 2. Detect project mode from dir.
	name, mode, detectErr := detectProjectMode(dir)
	if detectErr != nil {
		return detectErr
	}

	// 3. Build final args.
	var finalArgs []string
	if mode == "stealth" {
		oroHome, homeErr := resolveOroHome()
		if homeErr != nil {
			return fmt.Errorf("resolve oro home: %w", homeErr)
		}
		beadsPath := filepath.Join(oroHome, "projects", name, "beads")
		finalArgs = append([]string{"--db", beadsPath}, args...)
	} else {
		// Standard mode: pass through unchanged.
		finalArgs = args
	}

	return deps.runner(bdPath, finalArgs)
}
