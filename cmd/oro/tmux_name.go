package main

import (
	"os"
	"path/filepath"
)

// TmuxSessionName returns the tmux session name for a project.
// Empty project returns "oro" for backward compatibility.
// Non-empty project returns "oro-<project>" for multi-project isolation.
func TmuxSessionName(project string) string {
	if project == "" {
		return "oro"
	}
	return "oro-" + project
}

// TmuxPaneTarget returns a tmux pane target string (<session>:<role>)
// for the given project and role (e.g. "manager", "worker").
func TmuxPaneTarget(project, role string) string {
	return TmuxSessionName(project) + ":" + role
}

// daemonLogPath returns the project-scoped daemon log file path.
// Empty project returns "oro-daemon.log", non-empty returns "oro-<project>-daemon.log".
func daemonLogPath(project string) string {
	name := "oro-daemon.log"
	if project != "" {
		name = "oro-" + project + "-daemon.log"
	}
	return filepath.Join(os.TempDir(), name)
}
