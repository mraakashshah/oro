package main

import (
	"bytes"
	"io"
	"net/http"
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
	oldClient := dashboardHTTPClient
	t.Cleanup(func() { dashboardHTTPClient = oldClient })

	dashboardHTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.String() != "http://127.0.0.1:4545" {
				t.Fatalf("request URL = %q, want http://127.0.0.1:4545", req.URL.String())
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("")),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		}),
	}

	cmd := newDashboardCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--addr", "127.0.0.1:4545"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("dashboard command failed: %v", err)
	}
	if !strings.Contains(buf.String(), "http://127.0.0.1:4545") {
		t.Fatalf("expected output to contain dashboard URL, got %q", buf.String())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}
