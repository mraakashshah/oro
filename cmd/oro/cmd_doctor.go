package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
)

// doctorRecoverConfig holds injectable dependencies for the dolt recovery procedure.
type doctorRecoverConfig struct {
	beadsDir string                                  // .beads directory for the project (contains dolt/, full-state.jsonl)
	w        io.Writer                               // output writer
	now      func() time.Time                        // injectable clock for backup naming
	runCmd   func(name string, args ...string) error // injectable command runner (bd init)
}

// isDoltCorrupt reports true when the dolt data directory is corrupt.
// Corruption is detected by the presence of a .dolt subdirectory directly
// inside beadsDir/dolt/ (i.e. the dolt init ran in the wrong directory,
// creating beadsDir/dolt/.dolt instead of beadsDir/dolt/<dbname>/.dolt).
func isDoltCorrupt(beadsDir string) bool {
	corruptMarker := filepath.Join(beadsDir, "dolt", ".dolt")
	_, err := os.Stat(corruptMarker)
	return err == nil
}

// runDoctorRecoverDolt performs the safe dolt recovery procedure:
//  1. Detect corrupt dolt (presence of .dolt directly under beadsDir/dolt).
//  2. Back up the corrupt directory to beadsDir/backup/dolt-corrupt-YYYYMMDD-HHMMSS.
//  3. Copy full-state.jsonl → issues.jsonl (if present), then run bd init --from-jsonl.
//     If full-state.jsonl is absent, warn and run bd init (empty reinit).
//  4. Return error if bd init fails (caller should abort).
func runDoctorRecoverDolt(cfg *doctorRecoverConfig) error {
	if !isDoltCorrupt(cfg.beadsDir) {
		fmt.Fprintln(cfg.w, "dolt: healthy (no recovery needed)")
		return nil
	}

	fmt.Fprintln(cfg.w, "dolt: corrupt dolt detected (stray .dolt directory)")

	// Step 1: move corrupt dolt dir to backup.
	ts := cfg.now().UTC().Format("20060102-150405")
	backupDir := filepath.Join(cfg.beadsDir, "backup", "dolt-corrupt-"+ts)
	if err := os.MkdirAll(filepath.Join(cfg.beadsDir, "backup"), 0o750); err != nil {
		return fmt.Errorf("create backup dir: %w", err)
	}

	srcDolt := filepath.Join(cfg.beadsDir, "dolt")
	if err := os.Rename(srcDolt, backupDir); err != nil {
		return fmt.Errorf("backup corrupt dolt to %s: %w", backupDir, err)
	}
	fmt.Fprintf(cfg.w, "backed up corrupt dolt → %s\n", backupDir)

	// Step 2: determine restore strategy.
	fullStatePath := filepath.Join(cfg.beadsDir, "full-state.jsonl")
	hasFullState := false
	if _, err := os.Stat(fullStatePath); err == nil {
		hasFullState = true
	}

	if !hasFullState {
		fmt.Fprintln(cfg.w, "warning: full-state.jsonl not found — reinitialising with empty database")
		if err := cfg.runCmd("bd", "init"); err != nil {
			return fmt.Errorf("bd init: %w", err)
		}
		fmt.Fprintln(cfg.w, "dolt: restored (empty database)")
		return nil
	}

	// Copy full-state.jsonl → issues.jsonl so bd init --from-jsonl can read it.
	issuesPath := filepath.Join(cfg.beadsDir, "issues.jsonl")
	data, err := os.ReadFile(fullStatePath) //nolint:gosec // path constructed from beadsDir (caller-controlled)
	if err != nil {
		return fmt.Errorf("read full-state.jsonl: %w", err)
	}
	if err := os.WriteFile(issuesPath, data, 0o600); err != nil { //nolint:gosec // path constructed from beadsDir
		return fmt.Errorf("write issues.jsonl: %w", err)
	}

	// Reinitialise from the snapshot.
	if err := cfg.runCmd("bd", "init", "--from-jsonl"); err != nil {
		return fmt.Errorf("bd init --from-jsonl: %w", err)
	}

	fmt.Fprintln(cfg.w, "dolt: restored from full-state.jsonl")
	return nil
}

// newDoctorCmd creates the "oro doctor" subcommand.
func newDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose and repair oro installation issues",
		Long: `Diagnose and repair common oro installation issues.

Path resolution is mode-aware: in standard mode paths are under <project>/.oro/,
while in stealth mode (oro init --stealth) paths are under ~/.oro/projects/s-<hash>/.
Subcommands automatically detect the active mode via the project config.

Subcommands:
  recover-dolt  Detect and recover a corrupt Dolt database`,
	}
	cmd.AddCommand(newDoctorRecoverDoltCmd())
	return cmd
}

// newDoctorRecoverDoltCmd creates the "oro doctor recover-dolt" subcommand.
func newDoctorRecoverDoltCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "recover-dolt",
		Short: "Detect and recover a corrupt Dolt database",
		Long: `Detects a corrupt Dolt database (stray .dolt directory directly under .beads/dolt/)
and performs a safe recovery:

  1. Back up the corrupt directory to .beads/backup/dolt-corrupt-DATE.
  2. If .beads/full-state.jsonl exists, copy it to .beads/issues.jsonl and run
     'bd init --from-jsonl' to restore beads from the snapshot.
  3. If full-state.jsonl is absent, warn and run 'bd init' for an empty reinit.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			repoRoot, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working dir: %w", err)
			}
			projPaths, err := ResolvePaths(repoRoot)
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			cfg := &doctorRecoverConfig{
				beadsDir: projPaths.BeadsDir,
				w:        cmd.OutOrStdout(),
				now:      time.Now,
				runCmd:   defaultRunCmd,
			}
			return runDoctorRecoverDolt(cfg)
		},
	}
}

// defaultRunCmd runs a command by name with the given args, inheriting stdout/stderr.
func defaultRunCmd(name string, args ...string) error {
	//nolint:gosec // name and args come from trusted internal callers
	cmd := exec.CommandContext(context.Background(), name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run %s: %w", name, err)
	}
	return nil
}
