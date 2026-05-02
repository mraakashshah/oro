package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var dashboardHTTPClient = &http.Client{Timeout: 2 * time.Second} //nolint:gochecknoglobals // injectable for tests.

func newDashboardCmd() *cobra.Command {
	var addr string

	cmd := &cobra.Command{
		Use:   "dashboard",
		Short: "Open the local web dashboard",
		Long:  "Connect to the local web dashboard served by `oro start --web`.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			url := normalizeDashboardURL(addr)
			resp, err := dashboardHTTPClient.Get(url) //nolint:noctx,gosec // local operator convenience command
			if err != nil {
				return fmt.Errorf("dashboard not reachable at %s: %w\nrun `oro start --web` first", url, err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("dashboard returned %d at %s\nrun `oro start --web` first", resp.StatusCode, url)
			}
			fmt.Fprintln(cmd.OutOrStdout(), url)
			return nil
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:4444", "dashboard listen address")
	return cmd
}

func normalizeDashboardURL(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = "127.0.0.1:4444"
	}
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return addr
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	return "http://" + addr
}
