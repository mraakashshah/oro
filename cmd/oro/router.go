// Package main implements the oro command-line tool for managing
// distributed AI agent swarms.
package main

import (
	"strings"
)

// RouteDestination represents where a command should be executed.
type RouteDestination int

const (
	// ArchitectLocal means the command should be executed by the architect.
	ArchitectLocal RouteDestination = iota
	// ForwardToManager means the command should be forwarded to the manager pane.
	ForwardToManager
)

// RouteCommand determines where a command should be routed based on its prefix.
// Pure function for testability.
//
// Routing rules:
//   - Commands starting with "bd " stay with the architect (ArchitectLocal)
//   - Commands starting with "oro " or "oro directive" forward to manager (ForwardToManager)
//   - All other commands (git, ls, unknown) forward to manager (ForwardToManager)
//   - Empty or whitespace-only commands forward to manager (ForwardToManager)
func RouteCommand(command string) RouteDestination {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return ForwardToManager
	}

	// Check if command starts with "bd " (including the space to avoid false matches)
	if strings.HasPrefix(trimmed, "bd ") {
		return ArchitectLocal
	}

	// Check if command starts with "oro " (all oro commands forward to manager)
	if strings.HasPrefix(trimmed, "oro ") {
		return ForwardToManager
	}

	// All other commands (git, ls, unknown, etc.) forward to manager
	return ForwardToManager
}

// FormatForwardMessage creates a user-facing message indicating the command was forwarded.
// Shows "[forwarded to manager]" for oro commands, "[forwarded]" for other commands.
func FormatForwardMessage(command string) string {
	trimmed := strings.TrimSpace(command)
	if strings.HasPrefix(trimmed, "oro ") {
		return "[forwarded to manager] " + trimmed
	}
	return "[forwarded] " + trimmed
}
