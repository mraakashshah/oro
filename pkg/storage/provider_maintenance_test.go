package storage_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"oro/pkg/storage"
)

func TestProviderMaintenanceUsesFixedArgv(t *testing.T) {
	if providerMaintenanceHelperArgs() {
		runProviderMaintenanceHelper(t)
		return
	}

	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(root+"/cache-entry", []byte("cached"), 0o600); err != nil {
		t.Fatalf("seed provider usage: %v", err)
	}
	report := root + "/argv.txt"
	provider := storage.CacheProvider{
		ID:          "argv-probe",
		Variables:   []string{"PROVIDER_CACHE"},
		DefaultPath: func() string { return root },
		Scope:       storage.UserScope,
		Concurrency: storage.Serialized,
		Ownership:   storage.ToolNative,
		Cleaner: storage.CleanerDescriptor{
			Executable: os.Args[0],
			Args: []string{
				"-test.run=^TestProviderMaintenanceUsesFixedArgv$",
				"--",
				report,
				"$ORO_MAINTENANCE_EXPANSION_PROBE",
				"*.not-expanded",
			},
			Trusted: true,
		},
	}

	failing := provider
	failing.Cleaner.Args = append([]string(nil), provider.Cleaner.Args...)
	failing.Cleaner.Args[2] = root + "/failed-argv.txt"
	failing.Cleaner.Args = append(failing.Cleaner.Args, "--fail")
	failedEvidence, err := storage.RunProviderMaintenance(context.Background(), storage.ProviderMaintenance{Provider: failing})
	if err == nil {
		t.Fatal("RunProviderMaintenance() error = nil, want failed provider error")
	}
	if failedEvidence.ExitCode != 17 {
		t.Fatalf("failed provider exit code = %d, want 17", failedEvidence.ExitCode)
	}
	if failedEvidence.Before.FreeBytes == 0 || failedEvidence.After.FreeBytes == 0 ||
		failedEvidence.Before.UsedBytes == 0 || failedEvidence.After.UsedBytes == 0 {
		t.Fatalf("failed provider evidence = %+v, want before and after free/used bytes", failedEvidence)
	}

	evidence, err := storage.RunProviderMaintenance(context.Background(), storage.ProviderMaintenance{Provider: provider})
	if err != nil {
		t.Fatalf("RunProviderMaintenance() error = %v", err)
	}
	if evidence.Before.FreeBytes == 0 || evidence.After.FreeBytes == 0 {
		t.Fatalf("evidence free bytes = before %d, after %d, want non-zero", evidence.Before.FreeBytes, evidence.After.FreeBytes)
	}
	if evidence.Before.UsedBytes == 0 || evidence.After.UsedBytes == 0 {
		t.Fatalf("evidence used bytes = before %d, after %d, want non-zero", evidence.Before.UsedBytes, evidence.After.UsedBytes)
	}
	if evidence.ExitCode != 0 {
		t.Fatalf("evidence exit code = %d, want 0", evidence.ExitCode)
	}

	contents, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read argv report: %v", err)
	}
	if got, want := string(contents), "$ORO_MAINTENANCE_EXPANSION_PROBE\n*.not-expanded"; got != want {
		t.Fatalf("command argv = %q, want %q", got, want)
	}
}

func providerMaintenanceHelperArgs() bool {
	for i, arg := range os.Args {
		if arg == "--" {
			return len(os.Args) == i+4 || len(os.Args) == i+5
		}
	}
	return false
}

func runProviderMaintenanceHelper(t *testing.T) {
	t.Helper()
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 || (len(os.Args) != separator+4 && len(os.Args) != separator+5) {
		t.Fatalf("helper args = %q, want report and two literal arguments after --", os.Args)
	}
	if err := os.WriteFile(os.Args[separator+1], []byte(strings.Join(os.Args[separator+2:], "\n")), 0o600); err != nil {
		t.Fatalf("write argv report: %v", err)
	}
	if os.Args[len(os.Args)-1] == "--fail" {
		os.Exit(17)
	}
}
