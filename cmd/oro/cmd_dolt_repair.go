package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newDoltRepairCmd returns the "oro dolt repair" subcommand.
//
// startSharedDoltServer LEGAL CALLER — see D6 allowlist in allowlist_test.go.
// repair is the only path outside of setup that may directly spawn the shared
// server; it acquires an exclusive flock before doing so (future: oro-czv2 D3).
func newDoltRepairCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "repair",
		Short: "Restart the shared Dolt server after a failure",
		Long: `Stop any orphaned Dolt process on port 13307 and start a fresh
shared server using the ~/.oro/dolt data directory.

Run this command when 'oro dolt status' shows the server is not running
and 'oro dolt start' (which routes through launchd) cannot bring it up.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := ResolveDaemonPaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			_, err = startSharedDoltServer(paths.OroHome)
			if err != nil {
				return fmt.Errorf("repair: start shared dolt server: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "shared dolt server started")
			return nil
		},
	}
}
