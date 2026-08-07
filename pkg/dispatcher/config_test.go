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
		cfg := Config{
			BaseRef:              "origin/main",
			TargetBranch:         "main",
			MaxWorkers:           -1,
			HeartbeatTimeout:     time.Second,
			ProgressTimeout:      time.Second,
			PollInterval:         time.Second,
			FallbackPollInterval: time.Second,
			CycleScanInterval:    time.Second,
			ShutdownTimeout:      time.Second,
		}
		if err := cfg.validate(); err == nil || err.Error() != "MaxWorkers must be non-negative, got -1" {
			t.Fatalf("first operational validation error = %v", err)
		}
	})
}
