package dispatcher //nolint:testpackage // mutation owner exercises unexported config boundaries

import (
	"testing"
	"time"
)

func TestSplitBranchConfigMutationOwner(t *testing.T) {
	t.Run("defaults preserve separate branch roles", func(t *testing.T) {
		resolved := (&Config{
			BaseRef:       " origin/main ",
			TargetBranch:  " integration/local ",
			DefaultBranch: "legacy",
			MaxWorkers:    2,
		}).withDefaults()
		if resolved.BaseRef != "origin/main" || resolved.TargetBranch != "integration/local" || resolved.DefaultBranch != "integration/local" {
			t.Fatalf("resolved branches base/target/legacy = %q/%q/%q", resolved.BaseRef, resolved.TargetBranch, resolved.DefaultBranch)
		}
		if err := resolved.validateBranchConfig(); err != nil {
			t.Fatalf("validate local target: %v", err)
		}
		if err := resolved.validateOperationalConfig(); err != nil {
			t.Fatalf("validate defaulted operations: %v", err)
		}

		legacy := (&Config{DefaultBranch: " release "}).withDefaults()
		if legacy.BaseRef != "release" || legacy.TargetBranch != "release" || legacy.DefaultBranch != "release" {
			t.Fatalf("legacy defaults base/target/legacy = %q/%q/%q", legacy.BaseRef, legacy.TargetBranch, legacy.DefaultBranch)
		}
		empty := (&Config{}).withDefaults()
		if empty.BaseRef != "main" || empty.TargetBranch != "main" || empty.DefaultBranch != "main" {
			t.Fatalf("empty defaults base/target/legacy = %q/%q/%q", empty.BaseRef, empty.TargetBranch, empty.DefaultBranch)
		}
		if empty.InitialWorkers != 10 || empty.MaxWorkers != 10 {
			t.Fatalf("empty worker defaults = %d/%d, want 10/10", empty.InitialWorkers, empty.MaxWorkers)
		}
		if empty.HeartbeatTimeout != 45*time.Second ||
			empty.ProgressTimeout != 10*time.Minute ||
			empty.PollInterval != 10*time.Second ||
			empty.FallbackPollInterval != 60*time.Second ||
			empty.CycleScanInterval != 60*time.Second ||
			empty.ShutdownTimeout != 10*time.Second {
			t.Fatalf("empty operational duration defaults = heartbeat %v, progress %v, poll %v, fallback %v, cycle %v, shutdown %v",
				empty.HeartbeatTimeout, empty.ProgressTimeout, empty.PollInterval,
				empty.FallbackPollInterval, empty.CycleScanInterval, empty.ShutdownTimeout)
		}
		if empty.ConsolidateAfterN != 5 || empty.PaneContextThreshold != 40 ||
			empty.PaneMonitorInterval != 5*time.Second ||
			empty.PaneRestartCooldown != 2*time.Minute ||
			empty.PaneInactivityTimeout != 10*time.Minute ||
			empty.ReviewTimeout != 15*time.Minute || empty.ReviewDeadGrace != 30*time.Second ||
			!empty.RegressionRevert || empty.CheckpointThreshold != 75 ||
			empty.WebAddr != "127.0.0.1:4444" {
			t.Fatalf("empty service defaults = consolidate %d, pane threshold/monitor/restart/inactivity %d/%v/%v/%v, review/dead %v/%v, regression %t, checkpoint %d, web %q",
				empty.ConsolidateAfterN, empty.PaneContextThreshold, empty.PaneMonitorInterval,
				empty.PaneRestartCooldown, empty.PaneInactivityTimeout, empty.ReviewTimeout,
				empty.ReviewDeadGrace, empty.RegressionRevert, empty.CheckpointThreshold, empty.WebAddr)
		}
	})

	t.Run("branch validation remains first", func(t *testing.T) {
		cfg := Config{TargetBranch: "origin/main", MaxWorkers: -1}
		if err := cfg.validate(); err == nil || err.Error() != `TargetBranch must name a writable local branch, got remote-tracking ref "origin/main"` {
			t.Fatalf("branch-first validation error = %v", err)
		}
		cfg.TargetBranch = "refs/remotes/upstream/main"
		if err := cfg.validateBranchConfig(); err == nil || err.Error() != `TargetBranch must name a writable local branch, got remote-tracking ref "refs/remotes/upstream/main"` {
			t.Fatalf("refs/remotes validation error = %v", err)
		}
	})

	t.Run("operational validation follows valid branch", func(t *testing.T) {
		valid := Config{
			BaseRef:              "origin/main",
			TargetBranch:         "main",
			MaxWorkers:           1,
			JanitorEnabled:       true,
			HeartbeatTimeout:     time.Second,
			ProgressTimeout:      time.Second,
			PollInterval:         time.Second,
			FallbackPollInterval: time.Second,
			CycleScanInterval:    time.Second,
			ShutdownTimeout:      time.Second,
		}
		if err := valid.validateOperationalConfig(); err != nil {
			t.Fatalf("valid operational config: %v", err)
		}
		zeroWorkers := valid
		zeroWorkers.MaxWorkers = 0
		if err := zeroWorkers.validateOperationalConfig(); err != nil {
			t.Fatalf("zero MaxWorkers must remain valid: %v", err)
		}

		tests := []struct {
			name string
			want string
			edit func(*Config)
		}{
			{name: "max workers", want: "MaxWorkers must be non-negative, got -1", edit: func(c *Config) { c.MaxWorkers = -1 }},
			{name: "janitor interval", want: "JanitorInterval must be non-negative, got -1", edit: func(c *Config) { c.JanitorInterval = -1 }},
			{name: "janitor idle threshold", want: "JanitorIdleThreshold must be non-negative, got -1", edit: func(c *Config) { c.JanitorIdleThreshold = -1 }},
			{name: "audit cadence", want: "AuditEveryNJanitors must be non-negative, got -1", edit: func(c *Config) { c.AuditEveryNJanitors = -1 }},
			{name: "janitor top k", want: "JanitorTopK must be non-negative, got -1", edit: func(c *Config) { c.JanitorTopK = -1 }},
			{name: "audit requires janitor", want: "AuditEnabled requires JanitorEnabled because audit counters are driven by janitor cycles", edit: func(c *Config) { c.AuditEnabled, c.JanitorEnabled = true, false }},
			{name: "heartbeat zero", want: "HeartbeatTimeout must be positive, got 0s", edit: func(c *Config) { c.HeartbeatTimeout = 0 }},
			{name: "progress zero", want: "ProgressTimeout must be positive, got 0s", edit: func(c *Config) { c.ProgressTimeout = 0 }},
			{name: "poll zero", want: "PollInterval must be positive, got 0s", edit: func(c *Config) { c.PollInterval = 0 }},
			{name: "fallback poll zero", want: "FallbackPollInterval must be positive, got 0s", edit: func(c *Config) { c.FallbackPollInterval = 0 }},
			{name: "cycle scan zero", want: "CycleScanInterval must be positive, got 0s", edit: func(c *Config) { c.CycleScanInterval = 0 }},
			{name: "shutdown zero", want: "ShutdownTimeout must be positive, got 0s", edit: func(c *Config) { c.ShutdownTimeout = 0 }},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				cfg := valid
				tt.edit(&cfg)
				if err := cfg.validateOperationalConfig(); err == nil || err.Error() != tt.want {
					t.Fatalf("operational validation error = %v, want %q", err, tt.want)
				}
			})
		}
	})
}
