// Package main implements the oro command-line tool for managing
// distributed AI agent swarms.
package main

import "strings"

// FormatForwardMessage creates a managerless user-facing message indicating
// the command was forwarded.
func FormatForwardMessage(command string) string {
	trimmed := strings.TrimSpace(command)
	return "[forwarded] " + trimmed
}
