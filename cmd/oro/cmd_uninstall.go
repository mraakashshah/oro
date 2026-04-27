package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

// uninstallOptions holds flags and resolved values for the uninstall command.
type uninstallOptions struct {
	oroHome             string
	force               bool
	keepData            bool
	globalGitignorePath string // override for testing; empty = auto-resolve
	w                   io.Writer
	stdin               io.Reader // for confirmation prompt
}

// newUninstallCmd creates the "oro uninstall" subcommand.
func newUninstallCmd() *cobra.Command {
	var force, keepData bool

	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove oro and all its artifacts from this machine",
		Long: `Cleanly removes all oro artifacts:
  - Stops running daemons and tmux sessions
  - Removes launchd agent (dolt server)
  - Cleans project artifacts (.beads symlinks, .oro/ dirs, .worktrees/, git hooks)
  - Removes oro entries from global gitignore
  - Removes ~/.oro/ directory (with confirmation)
  - Removes the oro binary itself

Use --force to skip confirmation. Use --keep-data to preserve ~/.oro/ (databases, bead history).`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			oroHome, err := resolveOroHome()
			if err != nil {
				oroHome = filepath.Join(os.Getenv("HOME"), ".oro")
			}
			opts := uninstallOptions{
				oroHome:  oroHome,
				force:    force,
				keepData: keepData,
				w:        cmd.OutOrStdout(),
				stdin:    os.Stdin,
			}
			return runUninstall(opts)
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "skip confirmation prompt")
	cmd.Flags().BoolVar(&keepData, "keep-data", false, "preserve ~/.oro/ (databases, bead history)")

	return cmd
}

// runUninstall is the core uninstall logic, separated for testability.
func runUninstall(opts uninstallOptions) error {
	w := opts.w

	// 1. Stop running daemons.
	stopDaemons(w, opts.oroHome)

	// 2. Unload launchd agent.
	fmt.Fprintln(w, "Removing launchd agent...")
	home, _ := os.UserHomeDir()
	if home != "" {
		if err := uninstallLaunchAgent(home); err != nil {
			fmt.Fprintf(w, "  warning: %v\n", err)
		} else {
			fmt.Fprintln(w, "  done.")
		}
	}

	// 3. Clean project artifacts.
	cleanProjectArtifacts(w, opts.oroHome)

	// 4. Clean global gitignore.
	cleanGlobalGitignore(w, opts.globalGitignorePath)

	// 5. Remove ~/.oro/ (unless --keep-data).
	if !opts.keepData {
		if err := removeOroHome(w, opts); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(w, "Keeping ~/.oro/ (--keep-data).")
	}

	// 6. Remove binaries.
	removeBinaries(w)

	// 7. Summary.
	fmt.Fprintln(w)
	fmt.Fprintln(w, "oro uninstalled.")
	fmt.Fprintln(w, "  Run 'hash -r' to clear your shell's command cache.")
	fmt.Fprintln(w, "  If scripts/quality_gate.sh exists in your projects, remove it manually.")

	return nil
}

// stopDaemons kills all running oro daemons.
func stopDaemons(w io.Writer, oroHome string) {
	fmt.Fprintln(w, "Stopping oro daemons...")
	daemons := discoverProjectDaemons(oroHome)
	if len(daemons) == 0 {
		fmt.Fprintln(w, "  no running daemons found.")
		return
	}
	for _, d := range daemons {
		fmt.Fprintf(w, "  stopping %s (PID %d)...\n", d.Project, d.PID)
		_ = syscall.Kill(d.PID, syscall.SIGTERM)
		_ = os.Remove(d.PIDPath)
	}
}

// cleanProjectArtifacts removes .beads symlinks, .oro/ dirs, .worktrees/, and
// git hooks for all known projects.
func cleanProjectArtifacts(w io.Writer, oroHome string) {
	fmt.Fprintln(w, "Cleaning project artifacts...")
	projectsDir := filepath.Join(oroHome, "projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		fmt.Fprintln(w, "  no projects found.")
		return
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		projDir := filepath.Join(projectsDir, e.Name())
		rootBytes, err := os.ReadFile(filepath.Join(projDir, "project.root")) //nolint:gosec // trusted path
		if err != nil {
			continue
		}
		root := strings.TrimSpace(string(rootBytes))
		if root == "" || !filepath.IsAbs(root) {
			continue
		}

		fmt.Fprintf(w, "  cleaning %s (%s)...\n", e.Name(), root)

		// Remove .beads symlink.
		beadsLink := filepath.Join(root, beadsDirName)
		if fi, err := os.Lstat(beadsLink); err == nil && fi.Mode()&os.ModeSymlink != 0 { //nolint:gosec // root validated as absolute path from trusted project.root
			_ = os.Remove(beadsLink) //nolint:gosec // root validated as absolute path from trusted project.root
		}

		// Remove .oro/ anchor dir.
		_ = os.RemoveAll(filepath.Join(root, ".oro")) //nolint:gosec // root validated as absolute path from trusted project.root

		// Remove .worktrees/ dir.
		_ = os.RemoveAll(filepath.Join(root, worktreesDirName)) //nolint:gosec // root validated as absolute path from trusted project.root

		// Remove git hooks.
		gitDir := filepath.Join(root, ".git")
		if fi, err := os.Stat(gitDir); err == nil && fi.IsDir() { //nolint:gosec // root validated as absolute path from trusted project.root
			_ = uninstallCanonicalHook(gitDir, "pre-push")
			_ = uninstallCanonicalHook(gitDir, "pre-commit")
		}
	}
}

// cleanGlobalGitignore removes oro-added entries from the global gitignore.
func cleanGlobalGitignore(w io.Writer, overridePath string) {
	fmt.Fprintln(w, "Cleaning global gitignore...")
	path := overridePath
	if path == "" {
		var err error
		path, err = resolveGlobalGitignorePath()
		if err != nil {
			fmt.Fprintf(w, "  warning: could not resolve global gitignore: %v\n", err)
			return
		}
	}

	data, err := os.ReadFile(path) //nolint:gosec // user's gitignore
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(w, "  no global gitignore found.")
		} else {
			fmt.Fprintf(w, "  warning: %v\n", err)
		}
		return
	}

	oroEntries := make(map[string]bool)
	for _, e := range oroGitignoreEntries() {
		oroEntries[e] = true
	}

	var kept []string
	skipHeader := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "# Oro / Beads (managed by oro init)" {
			skipHeader = true
			continue
		}
		if oroEntries[trimmed] {
			continue
		}
		if skipHeader && trimmed == "" {
			skipHeader = false
			continue
		}
		skipHeader = false
		kept = append(kept, line)
	}

	// Trim trailing empty lines.
	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
		kept = kept[:len(kept)-1]
	}

	result := strings.Join(kept, "\n")
	if result != "" {
		result += "\n"
	}

	if err := os.WriteFile(path, []byte(result), 0o644); err != nil { //nolint:gosec // gitignore needs to be readable
		fmt.Fprintf(w, "  warning: %v\n", err)
		return
	}
	fmt.Fprintln(w, "  done.")
}

// removeOroHome removes ~/.oro/ with confirmation.
func removeOroHome(w io.Writer, opts uninstallOptions) error {
	if _, err := os.Stat(opts.oroHome); os.IsNotExist(err) {
		fmt.Fprintln(w, "~/.oro/ does not exist, skipping.")
		return nil
	}

	if !opts.force {
		fmt.Fprintf(w, "Remove %s? This will delete databases and bead history. [y/N] ", opts.oroHome)
		reader := bufio.NewReader(opts.stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer != "y" && answer != "yes" {
			fmt.Fprintln(w, "Aborted. Use --keep-data to preserve data while removing everything else.")
			return nil
		}
	}

	fmt.Fprintf(w, "Removing %s...\n", opts.oroHome)
	if err := os.RemoveAll(opts.oroHome); err != nil {
		fmt.Fprintf(w, "  warning: %v\n", err)
	} else {
		fmt.Fprintln(w, "  done.")
	}
	return nil
}

// removeBinaries removes the oro binary (self-delete) and oro-search-hook.
func removeBinaries(w io.Writer) {
	fmt.Fprintln(w, "Removing binaries...")

	// Self-delete: find our own path.
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(w, "  warning: could not determine binary path: %v\n", err)
	} else {
		// Resolve symlinks.
		exe, _ = filepath.EvalSymlinks(exe)
		if err := os.Remove(exe); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(w, "  warning: could not remove %s: %v\n", exe, err)
		} else {
			fmt.Fprintf(w, "  removed %s\n", exe)
		}
	}

	// Remove oro-search-hook from known locations.
	home, _ := os.UserHomeDir()
	hookLocations := []string{
		filepath.Join(home, ".oro", "hooks", "oro-search-hook"),
		"/usr/local/bin/oro-search-hook",
	}
	for _, loc := range hookLocations {
		if err := os.Remove(loc); err == nil {
			fmt.Fprintf(w, "  removed %s\n", loc)
		}
	}
}
