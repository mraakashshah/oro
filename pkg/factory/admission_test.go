package factory //nolint:testpackage // Public Run regressions share the real repository and adapter fixtures.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRepositoryRootAdmission(t *testing.T) {
	for _, scenario := range []string{"nested sibling state", "nested sibling policy", "nonrepository", "missing repository"} {
		t.Run(scenario, func(t *testing.T) {
			spec, state := engineFixture(t, "repair")
			root := spec.Repository
			marker := filepath.Join(t.TempDir(), "adapter-started")
			engineWrite(t, spec.Worker.Path, "#!/bin/sh\ntouch "+engineQuote(marker)+"\nexit 1\n", 0o700)
			switch scenario {
			case "nested sibling state", "nested sibling policy":
				spec.Repository = filepath.Join(root, "nested")
				if err := os.Mkdir(spec.Repository, 0o700); err != nil {
					t.Fatal(err)
				}
				if scenario == "nested sibling state" {
					state = filepath.Join(root, "factory-state")
				} else {
					body, err := os.ReadFile(spec.Worker.Path)
					if err != nil {
						t.Fatal(err)
					}
					spec.Worker.Path = filepath.Join(root, "sibling-policy")
					engineWrite(t, spec.Worker.Path, string(body), 0o700)
				}
			case "nonrepository":
				spec.Repository = t.TempDir()
			case "missing repository":
				spec.Repository = filepath.Join(t.TempDir(), "missing")
			}
			if status, err := Run(t.Context(), state, spec); err == nil {
				t.Errorf("invalid repository admitted: %+v", status)
			}
			for _, path := range []string{state, marker} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("rejected admission created %s: %v", path, err)
				}
			}
		})
	}
	t.Run("root symlink alias", func(t *testing.T) {
		spec, state := engineFixture(t, "repair")
		alias := filepath.Join(t.TempDir(), "repository-alias")
		if err := os.Symlink(spec.Repository, alias); err != nil {
			t.Fatal(err)
		}
		spec.Repository = alias
		status, err := Run(t.Context(), state, spec)
		if err != nil || status.State != "completed" {
			t.Fatalf("actual root alias rejected: %+v, %v", status, err)
		}
	})
}
