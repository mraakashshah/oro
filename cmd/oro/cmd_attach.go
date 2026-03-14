package main

import (
	"fmt"
	"os"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
)

// attachConfig holds injectable dependencies for the attach command.
type attachConfig struct {
	pidPath  string
	sockPath string
	tmuxName string
	runner   CmdRunner
	isTTY    func() bool
	attachFn func() error // calls AttachInteractive; injectable for testing
}

// runAttach executes the attach flow: checks daemon status, tmux session health,
// TTY, then delegates to attachFn.
func runAttach(cfg *attachConfig) error {
	// 1. Check daemon status.
	status, _, err := DaemonStatus(cfg.pidPath, cfg.sockPath)
	if err != nil {
		return fmt.Errorf("check daemon status: %w", err)
	}
	switch status {
	case StatusStopped:
		return fmt.Errorf("no running session — use `oro start`")
	case StatusStale:
		return fmt.Errorf("stale PID found — run `oro cleanup` then `oro start`")
	}

	// 2. Check tmux session exists.
	sess := &TmuxSession{Name: cfg.tmuxName, Runner: cfg.runner}
	if !sess.Exists() {
		return fmt.Errorf("dispatcher running in daemon-only mode, no tmux session")
	}

	// 3. Check session health.
	if !sess.isHealthy() {
		return fmt.Errorf("session unhealthy — run `oro stop && oro start`")
	}

	// 4. Check TTY (must be Stdin, not Stdout — AttachInteractive needs terminal input).
	if !cfg.isTTY() {
		return fmt.Errorf("cannot attach without a terminal (stdin is not a TTY)")
	}

	// 5. Attach.
	return cfg.attachFn()
}

// newAttachCmd creates the "oro attach" subcommand.
func newAttachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "attach",
		Short: "Connect to a running swarm session",
		Long:  "Attaches your terminal to the running oro tmux session.\nRequires the swarm to be running (use 'oro start' first).",
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := ResolvePaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			sess := &TmuxSession{Name: TmuxSessionName(readProjectName()), Runner: &ExecRunner{}}
			cfg := &attachConfig{
				pidPath:  paths.PIDPath,
				sockPath: paths.SocketPath,
				tmuxName: sess.Name,
				runner:   sess.Runner,
				isTTY:    func() bool { return isatty.IsTerminal(os.Stdin.Fd()) },
				attachFn: sess.AttachInteractive,
			}
			return runAttach(cfg)
		},
	}
}
