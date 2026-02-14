package main

import (
	"testing"
)

func TestRouteCommand(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		wantDest RouteDestination
	}{
		// bd commands stay with architect
		{
			name:     "bd stats",
			command:  "bd stats",
			wantDest: ArchitectLocal,
		},
		{
			name:     "bd ready",
			command:  "bd ready",
			wantDest: ArchitectLocal,
		},
		{
			name:     "bd create",
			command:  `bd create --title="test" --type=task`,
			wantDest: ArchitectLocal,
		},
		{
			name:     "bd with leading whitespace",
			command:  "  bd list",
			wantDest: ArchitectLocal,
		},

		// oro commands forward to manager
		{
			name:     "oro start",
			command:  "oro start",
			wantDest: ForwardToManager,
		},
		{
			name:     "oro stop",
			command:  "oro stop",
			wantDest: ForwardToManager,
		},
		{
			name:     "oro directive status",
			command:  "oro directive status",
			wantDest: ForwardToManager,
		},
		{
			name:     "oro directive scale 3",
			command:  "oro directive scale 3",
			wantDest: ForwardToManager,
		},
		{
			name:     "oro directive pause",
			command:  "oro directive pause",
			wantDest: ForwardToManager,
		},
		{
			name:     "oro with leading whitespace",
			command:  "  oro directive resume",
			wantDest: ForwardToManager,
		},

		// unknown commands forward to manager
		{
			name:     "git status",
			command:  "git status",
			wantDest: ForwardToManager,
		},
		{
			name:     "ls -la",
			command:  "ls -la",
			wantDest: ForwardToManager,
		},
		{
			name:     "make test",
			command:  "make test",
			wantDest: ForwardToManager,
		},
		{
			name:     "unknown command",
			command:  "some-random-command arg1 arg2",
			wantDest: ForwardToManager,
		},

		// edge cases
		{
			name:     "empty command",
			command:  "",
			wantDest: ForwardToManager,
		},
		{
			name:     "only whitespace",
			command:  "   ",
			wantDest: ForwardToManager,
		},
		{
			name:     "bd as part of larger command",
			command:  "echo bd stats",
			wantDest: ForwardToManager,
		},
		{
			name:     "oro as part of larger command",
			command:  "echo oro status",
			wantDest: ForwardToManager,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RouteCommand(tt.command)
			if got != tt.wantDest {
				t.Errorf("RouteCommand(%q) = %v, want %v", tt.command, got, tt.wantDest)
			}
		})
	}
}

func TestFormatForwardMessage(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{
			name:    "oro directive scale",
			command: "oro directive scale 3",
			want:    "[forwarded to manager] oro directive scale 3",
		},
		{
			name:    "oro status",
			command: "oro status",
			want:    "[forwarded to manager] oro status",
		},
		{
			name:    "git status",
			command: "git status",
			want:    "[forwarded] git status",
		},
		{
			name:    "unknown command",
			command: "make test",
			want:    "[forwarded] make test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatForwardMessage(tt.command)
			if got != tt.want {
				t.Errorf("FormatForwardMessage(%q) = %q, want %q", tt.command, got, tt.want)
			}
		})
	}
}
