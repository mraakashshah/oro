package main

import (
	"testing"
)

// TestDashModel_Init verifies the model initializes with BoardView active.
func TestDashModel_Init(t *testing.T) {
	m := newModel()

	// Model should initialize with BoardView as the active view
	if m.activeView != BoardView {
		t.Errorf("expected activeView to be BoardView, got %v", m.activeView)
	}

	// Init should return a tick command for periodic refresh
	cmd := m.Init()
	if cmd == nil {
		t.Error("expected Init() to return a tick command, got nil")
	}
}
