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

	// Init should return nil (no initial commands)
	cmd := m.Init()
	if cmd != nil {
		t.Errorf("expected Init() to return nil, got %v", cmd)
	}
}
