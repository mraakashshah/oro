package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestJanitorStartPlumbing(t *testing.T) {
	t.Run("defaults reach dispatcher config", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("ORO_HOME", t.TempDir())
		t.Setenv("ORO_PROJECT", "")
		t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))

		d, db, err := buildDispatcher("", false, "")
		if err != nil {
			t.Fatalf("buildDispatcher: %v", err)
		}
		defer func() { _ = db.Close() }()

		cfg := d.GetConfig()
		if cfg.JanitorInterval != 50 {
			t.Errorf("JanitorInterval = %d, want 50", cfg.JanitorInterval)
		}
		if cfg.AuditEveryNJanitors != 5 {
			t.Errorf("AuditEveryNJanitors = %d, want 5", cfg.AuditEveryNJanitors)
		}
		if cfg.JanitorTopK != 5 {
			t.Errorf("JanitorTopK = %d, want 5", cfg.JanitorTopK)
		}
		if cfg.JanitorIdleThreshold != 0 {
			t.Errorf("JanitorIdleThreshold = %d, want 0", cfg.JanitorIdleThreshold)
		}
		if !cfg.JanitorEnabled {
			t.Error("JanitorEnabled = false, want true")
		}
		if !cfg.AuditEnabled {
			t.Error("AuditEnabled = false, want true")
		}
	})

	t.Run("daemon handoff preserves explicit settings", func(t *testing.T) {
		cleanliness := cleanlinessStartConfig{
			JanitorInterval:      0,
			JanitorIdleThreshold: 3,
			AuditEveryNJanitors:  7,
			JanitorTopK:          9,
			JanitorEnabled:       false,
			AuditEnabled:         false,
		}
		args := strings.Join((&ExecDaemonSpawner{Cleanliness: cleanliness}).buildArgs(1, 1), " ")
		for _, want := range []string{
			"--janitor-interval=0",
			"--janitor-idle-threshold=3",
			"--audit-every-n-janitors=7",
			"--janitor-top-k=9",
			"--janitor-enabled=false",
			"--audit-enabled=false",
		} {
			if !strings.Contains(args, want) {
				t.Errorf("daemon args missing %q: %s", want, args)
			}
		}

		tmpDir := t.TempDir()
		t.Setenv("ORO_HOME", t.TempDir())
		t.Setenv("ORO_PROJECT", "")
		t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))
		d, db, err := buildDispatcherWithReviewTimeoutsAndCleanliness(1, 1, 0, 0, 0, false, "", false, false, "", cleanliness)
		if err != nil {
			t.Fatalf("buildDispatcherWithReviewTimeoutsAndCleanliness: %v", err)
		}
		defer func() { _ = db.Close() }()
		cfg := d.GetConfig()
		if cfg.JanitorInterval != 0 || cfg.JanitorEnabled || cfg.AuditEnabled {
			t.Errorf("dispatcher cleanliness config = %+v, want disabled janitor/audit with zero interval", cfg)
		}
	})

	t.Run("flags allow disabling roles and janitor interval", func(t *testing.T) {
		cmd := newStartCmd()
		args := []string{
			"--janitor-enabled=false",
			"--audit-enabled=false",
			"--janitor-interval=0",
		}
		if err := cmd.ParseFlags(args); err != nil {
			t.Fatalf("ParseFlags(%v): %v", args, err)
		}

		for _, name := range []string{"janitor-enabled", "audit-enabled"} {
			value, err := cmd.Flags().GetBool(name)
			if err != nil {
				t.Fatalf("GetBool(%q): %v", name, err)
			}
			if value {
				t.Errorf("%s = true, want false", name)
			}
		}
		interval, err := cmd.Flags().GetInt("janitor-interval")
		if err != nil {
			t.Fatalf("GetInt(janitor-interval): %v", err)
		}
		if interval != 0 {
			t.Errorf("janitor-interval = %d, want 0", interval)
		}
	})
}
