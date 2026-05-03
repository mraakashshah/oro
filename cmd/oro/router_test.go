package main

import (
	"testing"
)

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
