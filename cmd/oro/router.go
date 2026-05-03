// Package main implements the oro command-line tool for managing
// distributed AI agent swarms.
package main

import "strings"

// FormatForwardMessage creates a user-facing message indicating the command was forwarded.
// Shows "[forwarded to manager]" for oro commands, "[forwarded]" for other commands.
func FormatForwardMessage(command string) string {
	trimmed := strings.TrimSpace(command)
	if strings.HasPrefix(trimmed, "oro ") {
		return "[forwarded to manager] " + trimmed
	}
	return "[forwarded] " + trimmed
}
