package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// goreleaserConfig mirrors the subset of GoReleaser v2 config we need to validate.
type goreleaserConfig struct {
	Version int `yaml:"version"`
	Before  struct {
		Hooks []string `yaml:"hooks"`
	} `yaml:"before"`
	Builds []struct {
		ID      string   `yaml:"id"`
		Main    string   `yaml:"main"`
		Binary  string   `yaml:"binary"`
		Env     []string `yaml:"env"`
		Goos    []string `yaml:"goos"`
		Goarch  []string `yaml:"goarch"`
		Ldflags []string `yaml:"ldflags"`
	} `yaml:"builds"`
	Archives []struct {
		ID           string   `yaml:"id"`
		Format       string   `yaml:"format"`
		NameTemplate string   `yaml:"name_template"`
		Builds       []string `yaml:"builds"`
	} `yaml:"archives"`
	Checksum struct {
		Algorithm string `yaml:"algorithm"`
	} `yaml:"checksum"`
	Release struct {
		GitHub struct {
			Owner string `yaml:"owner"`
			Name  string `yaml:"name"`
		} `yaml:"github"`
	} `yaml:"release"`
}

func TestGoReleaserConfigValid(t *testing.T) {
	// Find repo root (two levels up from cmd/oro/).
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine test file path")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	configPath := filepath.Join(repoRoot, ".goreleaser.yml")

	data, err := os.ReadFile(configPath) //nolint:gosec // test reads a known config file
	if err != nil {
		t.Fatalf("failed to read .goreleaser.yml: %v", err)
	}

	var cfg goreleaserConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("failed to parse .goreleaser.yml: %v", err)
	}

	t.Run("version_is_2", func(t *testing.T) {
		if cfg.Version != 2 {
			t.Errorf("expected version 2, got %d", cfg.Version)
		}
	})

	t.Run("has_3_builds", func(t *testing.T) {
		if len(cfg.Builds) != 3 {
			t.Errorf("expected 3 builds, got %d", len(cfg.Builds))
		}
	})

	t.Run("build_binaries", func(t *testing.T) {
		wantBinaries := map[string]string{
			"oro":             "./cmd/oro",
			"oro-dash":        "./cmd/oro-dash",
			"oro-search-hook": "./cmd/oro-search-hook",
		}
		for _, b := range cfg.Builds {
			expectedMain, found := wantBinaries[b.Binary]
			if !found {
				t.Errorf("unexpected binary name: %s", b.Binary)
				continue
			}
			if b.Main != expectedMain {
				t.Errorf("binary %s: expected main=%s, got main=%s", b.Binary, expectedMain, b.Main)
			}
			delete(wantBinaries, b.Binary)
		}
		for name := range wantBinaries {
			t.Errorf("missing build for binary: %s", name)
		}
	})

	t.Run("darwin_only", func(t *testing.T) {
		for _, b := range cfg.Builds {
			if len(b.Goos) != 1 || b.Goos[0] != "darwin" {
				t.Errorf("build %s: expected goos=[darwin], got %v", b.ID, b.Goos)
			}
			wantArch := []string{"amd64", "arm64"}
			if len(b.Goarch) != 2 {
				t.Errorf("build %s: expected 2 goarch entries, got %d", b.ID, len(b.Goarch))
				continue
			}
			for i, arch := range wantArch {
				if b.Goarch[i] != arch {
					t.Errorf("build %s: expected goarch[%d]=%s, got %s", b.ID, i, arch, b.Goarch[i])
				}
			}
		}
	})

	t.Run("cgo_disabled", func(t *testing.T) {
		for _, b := range cfg.Builds {
			found := false
			for _, env := range b.Env {
				if env == "CGO_ENABLED=0" {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("build %s: missing CGO_ENABLED=0 in env", b.ID)
			}
		}
	})

	t.Run("ldflags_contain_version", func(t *testing.T) {
		wantFragment := "-X oro/internal/appversion.version={{.Version}}"
		for _, b := range cfg.Builds {
			found := false
			for _, lf := range b.Ldflags {
				if lf == wantFragment || strings.Contains(lf, "oro/internal/appversion.version") {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("build %s: ldflags missing appversion.version injection", b.ID)
			}
		}
	})

	t.Run("before_hooks_stage_assets", func(t *testing.T) {
		found := false
		for _, h := range cfg.Before.Hooks {
			if h == "make stage-assets" {
				found = true
				break
			}
		}
		if !found {
			t.Error("before.hooks missing 'make stage-assets'")
		}
	})

	t.Run("single_archive_with_all_builds", func(t *testing.T) {
		if len(cfg.Archives) == 0 {
			t.Fatal("no archives defined")
		}
		archive := cfg.Archives[0]
		if archive.Format != "tar.gz" {
			t.Errorf("expected archive format tar.gz, got %s", archive.Format)
		}
		// The archive should reference all 3 build IDs.
		if len(archive.Builds) != 3 {
			t.Errorf("expected archive to reference 3 builds, got %d: %v", len(archive.Builds), archive.Builds)
		}
	})

	t.Run("checksum_sha256", func(t *testing.T) {
		if cfg.Checksum.Algorithm != "sha256" {
			t.Errorf("expected checksum algorithm sha256, got %s", cfg.Checksum.Algorithm)
		}
	})

	t.Run("release_github_repo", func(t *testing.T) {
		if cfg.Release.GitHub.Owner != "mraakashshah" {
			t.Errorf("expected release owner mraakashshah, got %s", cfg.Release.GitHub.Owner)
		}
		if cfg.Release.GitHub.Name != "oro" {
			t.Errorf("expected release repo name oro, got %s", cfg.Release.GitHub.Name)
		}
	})
}

func TestGoReleaserCheck(t *testing.T) {
	goreleaser, err := exec.LookPath("goreleaser")
	if err != nil {
		t.Skip("goreleaser not installed, skipping config check")
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine test file path")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	configPath := filepath.Join(repoRoot, ".goreleaser.yml")

	cmd := exec.Command(goreleaser, "check", "--config", configPath) //nolint:gosec // test runs a known tool
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("goreleaser check failed: %v\n%s", err, out)
	}
	t.Logf("goreleaser check output: %s", out)
}
