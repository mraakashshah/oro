package main

import (
	"io/fs"
	"testing"
)

func TestEmbeddedAssets(t *testing.T) {
	// EmbeddedAssets is populated at build time via go:embed.
	// The _assets/ directory must be staged before compilation
	// (see Makefile stage-assets target).
	entries, err := fs.ReadDir(EmbeddedAssets, "_assets")
	if err != nil {
		t.Skip("embedded assets not staged (run via 'make test' or stage assets first)")
	}
	if len(entries) == 0 {
		t.Fatal("EmbeddedAssets is empty — expected skills, hooks, beacons, and CLAUDE.md")
	}

	t.Run("skills_directory_has_entries", func(t *testing.T) {
		entries, err := fs.ReadDir(EmbeddedAssets, "_assets/skills")
		if err != nil {
			t.Fatalf("failed to read _assets/skills: %v", err)
		}
		if len(entries) == 0 {
			t.Fatal("_assets/skills/ is empty — expected skill directories")
		}
		t.Logf("found %d entries in _assets/skills/", len(entries))
	})

	t.Run("hooks_directory_has_entries", func(t *testing.T) {
		entries, err := fs.ReadDir(EmbeddedAssets, "_assets/hooks")
		if err != nil {
			t.Fatalf("failed to read _assets/hooks: %v", err)
		}
		if len(entries) == 0 {
			t.Fatal("_assets/hooks/ is empty — expected hook scripts")
		}
		t.Logf("found %d entries in _assets/hooks/", len(entries))
	})

	t.Run("beacons_directory_has_entries", func(t *testing.T) {
		entries, err := fs.ReadDir(EmbeddedAssets, "_assets/beacons")
		if err != nil {
			t.Fatalf("failed to read _assets/beacons: %v", err)
		}
		if len(entries) == 0 {
			t.Fatal("_assets/beacons/ is empty — expected beacon configs")
		}
		t.Logf("found %d entries in _assets/beacons/", len(entries))
	})

	t.Run("CLAUDE_md_is_readable", func(t *testing.T) {
		data, err := fs.ReadFile(EmbeddedAssets, "_assets/CLAUDE.md")
		if err != nil {
			t.Fatalf("failed to read _assets/CLAUDE.md: %v", err)
		}
		if len(data) == 0 {
			t.Fatal("_assets/CLAUDE.md is empty")
		}
		t.Logf("CLAUDE.md is %d bytes", len(data))
	})
}
