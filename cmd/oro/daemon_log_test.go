package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDaemonLogPath(t *testing.T) {
	tests := []struct {
		project string
		want    string
	}{
		{"", filepath.Join(os.TempDir(), "oro-daemon.log")},
		{"myproj", filepath.Join(os.TempDir(), "oro-myproj-daemon.log")},
		{"2026-03-13-fda-letter", filepath.Join(os.TempDir(), "oro-2026-03-13-fda-letter-daemon.log")},
	}
	for _, tt := range tests {
		t.Run(tt.project, func(t *testing.T) {
			got := daemonLogPath(tt.project)
			if got != tt.want {
				t.Errorf("daemonLogPath(%q) = %q, want %q", tt.project, got, tt.want)
			}
		})
	}
}
