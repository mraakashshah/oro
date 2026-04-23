package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDashboardCmdRegisteredInRoot(t *testing.T) {
	root := newRootCmd()
	for _, sub := range root.Commands() {
		if sub.Name() == "dashboard" {
			return
		}
	}
	t.Fatal("expected 'dashboard' command to be registered in root")
}

func TestNormalizeDashboardURL(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", "http://127.0.0.1:4444"},
		{":5555", "http://127.0.0.1:5555"},
		{"127.0.0.1:4444", "http://127.0.0.1:4444"},
		{"http://localhost:9000", "http://localhost:9000"},
	}
	for _, tt := range tests {
		if got := normalizeDashboardURL(tt.in); got != tt.want {
			t.Errorf("normalizeDashboardURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDashboardCmdPrintsReachableURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cmd := newDashboardCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--addr", srv.URL})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("dashboard command failed: %v", err)
	}
	if !strings.Contains(buf.String(), srv.URL) {
		t.Fatalf("expected output to contain %q, got %q", srv.URL, buf.String())
	}
}
