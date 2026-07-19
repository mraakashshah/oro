package config_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"oro/pkg/config"
)

func TestLoadStoragePolicyPrecedence(t *testing.T) {
	t.Run("defaults user project environment and CLI tighten in order", func(t *testing.T) {
		root := t.TempDir()
		userPath := filepath.Join(root, "user.yaml")
		projectPath := filepath.Join(root, "project.yaml")
		writeStoragePolicyConfig(t, userPath, `storage:
  namespace_stop_bytes: 800
  aggregate_admission_bytes: 3000
  deletion_roots:
    - /trusted/user
  cleaners:
    - executable: user-clean
      args: [prune]
  environment:
    ORO_STORAGE_NAMESPACE_STOP_BYTES: true
`)
		writeStoragePolicyConfig(t, projectPath, `storage:
  namespace_stop_bytes: 700
  aggregate_admission_bytes: 2500
`)

		policy, err := config.LoadStoragePolicy(context.Background(), config.StoragePolicySources{
			UserConfigPath:    userPath,
			ProjectConfigPath: projectPath,
			Environment: map[string]string{
				"ORO_STORAGE_NAMESPACE_STOP_BYTES": "600",
			},
			CLI: config.StoragePolicyOverrides{
				NamespaceStopBytes: 500,
			},
		})
		if err != nil {
			t.Fatalf("LoadStoragePolicy() error = %v", err)
		}

		if got := policy.NamespaceStopBytes; got != 500 {
			t.Errorf("NamespaceStopBytes = %d, want 500", got)
		}
		if got := policy.AggregateAdmissionBytes; got != 2500 {
			t.Errorf("AggregateAdmissionBytes = %d, want 2500", got)
		}
		if got := policy.Provenance.NamespaceStopBytes; got != config.PolicySourceCLI {
			t.Errorf("NamespaceStopBytes provenance = %q, want %q", got, config.PolicySourceCLI)
		}
		if got := policy.Provenance.AggregateAdmissionBytes; got != config.PolicySourceProject {
			t.Errorf("AggregateAdmissionBytes provenance = %q, want %q", got, config.PolicySourceProject)
		}
		if len(policy.DeletionRoots) != 1 || policy.DeletionRoots[0] != "/trusted/user" {
			t.Errorf("DeletionRoots = %q, want trusted user root", policy.DeletionRoots)
		}
		if len(policy.Cleaners) != 1 || policy.Cleaners[0].Executable != "user-clean" || !policy.Cleaners[0].Trusted {
			t.Errorf("Cleaners = %+v, want trusted user cleaner", policy.Cleaners)
		}

		policy.DeletionRoots[0] = "mutated"
		if policy.DeletionRoots[0] != "mutated" {
			t.Fatal("test setup failed to mutate returned snapshot")
		}
		if roots := policy.Clone().DeletionRoots; roots[0] != "mutated" {
			t.Errorf("Clone() roots = %q, want a faithful immutable snapshot", roots)
		}
	})

	t.Run("project cannot weaken a host limit", func(t *testing.T) {
		root := t.TempDir()
		userPath := filepath.Join(root, "user.yaml")
		projectPath := filepath.Join(root, "project.yaml")
		writeStoragePolicyConfig(t, userPath, "storage:\n  namespace_stop_bytes: 800\n")
		writeStoragePolicyConfig(t, projectPath, "storage:\n  namespace_stop_bytes: 801\n")

		_, err := config.LoadStoragePolicy(context.Background(), config.StoragePolicySources{
			UserConfigPath: userPath, ProjectConfigPath: projectPath,
		})
		if !errors.Is(err, config.ErrPolicyWeakening) {
			t.Fatalf("LoadStoragePolicy() error = %v, want ErrPolicyWeakening", err)
		}
	})

	t.Run("project cannot add a cleaner", func(t *testing.T) {
		projectPath := filepath.Join(t.TempDir(), "project.yaml")
		writeStoragePolicyConfig(t, projectPath, `storage:
  cleaners:
    - executable: project-clean
      args: [all]
`)

		_, err := config.LoadStoragePolicy(context.Background(), config.StoragePolicySources{ProjectConfigPath: projectPath})
		if !errors.Is(err, config.ErrUntrustedCleaner) {
			t.Fatalf("LoadStoragePolicy() error = %v, want ErrUntrustedCleaner", err)
		}
	})

	t.Run("malformed input fails", func(t *testing.T) {
		projectPath := filepath.Join(t.TempDir(), "project.yaml")
		writeStoragePolicyConfig(t, projectPath, "storage: [\n")

		if _, err := config.LoadStoragePolicy(context.Background(), config.StoragePolicySources{ProjectConfigPath: projectPath}); err == nil {
			t.Fatal("LoadStoragePolicy() error = nil, want malformed config error")
		}
	})
}

func writeStoragePolicyConfig(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
