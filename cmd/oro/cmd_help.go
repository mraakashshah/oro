package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// helpText is the categorized help output for "oro help".
const helpText = `Oro — Agent swarm orchestrator

Lifecycle:
  init       Bootstrap dependencies and generate config
  start      Launch the swarm (tmux + dispatcher + workers)
  stop       Graceful shutdown
  cleanup    Clean all stale state after a crash

Monitoring:
  status     Show current swarm state
  logs       Query and tail dispatcher event logs
  dash       Launch interactive dashboard

Memory:
  remember   Store a memory
  recall     Search memories
  forget     Delete memories by ID
  memories   Browse and manage the memory store

Control:
  directive  Send a directive to the dispatcher (scale, focus, pause, resume)

Search:
  index      Semantic code search (build, search)

Internal:
  worker     Run an oro worker process (used by the dispatcher)

Use "oro <command> --help" for detailed usage of any command.
`

// newHelpCmd creates the "oro help" subcommand that displays a categorized
// overview. When called with an argument (e.g. "oro help status"), it falls
// through to cobra's built-in per-command help.
func newHelpCmd(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:   "help [command]",
		Short: "Show categorized command overview",
		Long:  "Displays a categorized overview of all oro subcommands.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				fmt.Fprint(cmd.OutOrStdout(), helpText)
				return nil
			}

			// Fall through to cobra's per-command help.
			target, _, err := root.Find(args)
			if err != nil || target == nil || target == root {
				return fmt.Errorf("unknown command %q", args[0])
			}
			return target.Help()
		},
	}
}
