package main

import "testing"

func TestTmuxSessionName(t *testing.T) {
	tests := []struct {
		project string
		want    string
	}{
		{"", "oro"},
		{"myproj", "oro-myproj"},
		{"2026-03-13-fda-letter", "oro-2026-03-13-fda-letter"},
		{"a", "oro-a"},
	}
	for _, tt := range tests {
		t.Run(tt.project, func(t *testing.T) {
			got := TmuxSessionName(tt.project)
			if got != tt.want {
				t.Errorf("TmuxSessionName(%q) = %q, want %q", tt.project, got, tt.want)
			}
		})
	}
}

func TestTmuxPaneTarget(t *testing.T) {
	tests := []struct {
		project string
		role    string
		want    string
	}{
		{"", "manager", "oro:manager"},
		{"", "worker", "oro:worker"},
		{"myproj", "manager", "oro-myproj:manager"},
		{"myproj", "worker", "oro-myproj:worker"},
	}
	for _, tt := range tests {
		t.Run(tt.project+"/"+tt.role, func(t *testing.T) {
			got := TmuxPaneTarget(tt.project, tt.role)
			if got != tt.want {
				t.Errorf("TmuxPaneTarget(%q, %q) = %q, want %q", tt.project, tt.role, got, tt.want)
			}
		})
	}
}
