package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"oro/pkg/storage"
)

func TestStorageCleanDevToolsCLI(t *testing.T) {
	const gib = uint64(1 << 30)
	providers := []storage.CacheProvider{
		{
			ID:          "go",
			DefaultPath: func() string { return t.TempDir() },
			Scope:       storage.UserScope,
			Concurrency: storage.Concurrent,
			Ownership:   storage.ToolNative,
			Cleaner:     storage.CleanerDescriptor{Executable: "go", Args: []string{"clean"}, Trusted: true},
		},
		{
			ID:          "uv",
			DefaultPath: func() string { return t.TempDir() },
			Scope:       storage.UserScope,
			Concurrency: storage.Concurrent,
			Ownership:   storage.ToolNative,
			Cleaner:     storage.CleanerDescriptor{Executable: "uv", Args: []string{"cache", "prune"}, Trusted: true},
		},
		{
			ID:          "npx",
			DefaultPath: func() string { return t.TempDir() },
			Scope:       storage.UserScope,
			Concurrency: storage.NoMaintenance,
			Ownership:   storage.ToolNative,
		},
	}
	var calls []string
	runner := func(_ context.Context, maintenance storage.ProviderMaintenance) (storage.MaintenanceEvidence, error) {
		calls = append(calls, maintenance.Provider.ID)
		return storage.MaintenanceEvidence{
			ProviderID: maintenance.Provider.ID,
			Before:     storage.MaintenanceSnapshot{FreeBytes: 3 * gib},
			After:      storage.MaintenanceSnapshot{FreeBytes: 5 * gib},
			ExitCode:   0,
		}, nil
	}
	deps := storageCleanDependencies{providers: providers, runProviderMaintenance: runner}

	t.Run("dry-run does not invoke a provider", func(t *testing.T) {
		cmd := newStorageCleanCmdWithDependencies(deps)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--scope", "dev-tools", "--json"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("execute dry-run dev-tools clean: %v", err)
		}
		if len(calls) != 0 {
			t.Fatalf("dry-run provider calls = %v, want none", calls)
		}
		var got struct {
			Apply     bool `json:"apply"`
			Providers []struct {
				ProviderID string `json:"provider_id"`
			} `json:"providers"`
		}
		if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
			t.Fatalf("decode dry-run JSON %q: %v", out.String(), err)
		}
		if got.Apply || len(got.Providers) != 0 {
			t.Errorf("dry-run result = %+v, want no applied provider evidence", got)
		}
	})

	t.Run("apply emits provider evidence and human summary", func(t *testing.T) {
		cmd := newStorageCleanCmdWithDependencies(deps)
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--scope", "dev-tools", "--apply", "--json"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("execute apply dev-tools clean: %v", err)
		}
		if got, want := calls, []string{"go", "uv"}; strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("apply provider calls = %v, want %v", got, want)
		}
		var got struct {
			Apply      bool   `json:"apply"`
			FreedBytes uint64 `json:"freed_bytes"`
			FreeBytes  uint64 `json:"free_bytes"`
			Providers  []struct {
				ProviderID string                      `json:"provider_id"`
				Before     storage.MaintenanceSnapshot `json:"before"`
				After      storage.MaintenanceSnapshot `json:"after"`
			} `json:"providers"`
		}
		if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
			t.Fatalf("decode apply JSON %q: %v", out.String(), err)
		}
		if !got.Apply || got.FreedBytes != 4*gib || got.FreeBytes != 5*gib || len(got.Providers) != 2 {
			t.Errorf("apply JSON = %+v, want two evidence records with 4 GiB freed and 5 GiB free", got)
		}
		for _, provider := range got.Providers {
			if provider.Before.FreeBytes != 3*gib || provider.After.FreeBytes != 5*gib {
				t.Errorf("provider evidence = %+v, want before/after free bytes", provider)
			}
		}

		cmd = newStorageCleanCmdWithDependencies(deps)
		out.Reset()
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--scope", "dev-tools", "--apply"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("execute human apply dev-tools clean: %v", err)
		}
		if !strings.Contains(out.String(), "Freed 4.00 GiB — now 5.00 GiB free") {
			t.Errorf("human output = %q, want freed/current free summary", out.String())
		}
	})
}
