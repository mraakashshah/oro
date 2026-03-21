package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// capturedBdInvocation records what the bd stub captured.
type capturedBdInvocation struct {
	bdPath string
	args   []string
}

// stubBdRunner returns a bdRunner that records its invocation.
func stubBdRunner(out *capturedBdInvocation) bdRunner {
	return func(bdPath string, args []string) error {
		out.bdPath = bdPath
		out.args = args
		return nil
	}
}

// makeStandardBdTestDir creates a project root with .oro/config.yaml.
func makeStandardBdTestDir(t *testing.T, projectName string) string {
	t.Helper()
	tmp := t.TempDir()

	configPath := filepath.Join(tmp, ".oro", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("project: "+projectName+"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return tmp
}

// makeStealthBdTestDir creates a temp repo dir (no .oro/) and a stealth config under oroHome.
func makeStealthBdTestDir(t *testing.T, oroHome string) (repoRoot, stealthDir string) {
	t.Helper()
	tmp := t.TempDir()

	// Compute expected stealth hash (must match computePathHash implementation).
	absRoot, err := filepath.Abs(tmp)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		resolved = absRoot
	}
	h := sha256.Sum256([]byte(resolved))
	hash := fmt.Sprintf("%x", h[:8]) // 16 hex chars from first 8 bytes
	stealthDir = "s-" + hash

	// Create stealth config.
	stealthCfgDir := filepath.Join(oroHome, "projects", stealthDir)
	if err := os.MkdirAll(stealthCfgDir, 0o700); err != nil {
		t.Fatalf("mkdir stealth dir: %v", err)
	}
	cfg := "mode: stealth\n"
	if err := os.WriteFile(filepath.Join(stealthCfgDir, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write stealth config: %v", err)
	}

	return tmp, stealthDir
}

func TestOroBdWrapper(t *testing.T) {
	t.Run("standard mode passes through unchanged (no --db injected)", func(t *testing.T) {
		repoRoot := makeStandardBdTestDir(t, "myproject")
		oroHome := t.TempDir()
		t.Setenv("ORO_HOME", oroHome)

		var invoc capturedBdInvocation
		deps := bdDeps{
			lookPath: fakeLookPath("/usr/local/bin/bd"),
			runner:   stubBdRunner(&invoc),
		}

		if err := runBd(repoRoot, []string{"list"}, deps); err != nil {
			t.Fatalf("runBd standard: %v", err)
		}

		if len(invoc.args) != 1 || invoc.args[0] != "list" {
			t.Errorf("standard mode: want args [list], got %v", invoc.args)
		}
		for _, a := range invoc.args {
			if a == "--db" {
				t.Errorf("standard mode: --db should NOT be injected, got args %v", invoc.args)
			}
		}
	})

	t.Run("stealth mode prepends --db with s-<hash> beads path", func(t *testing.T) {
		oroHome := t.TempDir()
		t.Setenv("ORO_HOME", oroHome)
		t.Setenv("ORO_PROJECT", "") // isolate from ambient env
		repoRoot, stealthDir := makeStealthBdTestDir(t, oroHome)

		var invoc capturedBdInvocation
		deps := bdDeps{
			lookPath: fakeLookPath("/usr/local/bin/bd"),
			runner:   stubBdRunner(&invoc),
		}

		if err := runBd(repoRoot, []string{"list"}, deps); err != nil {
			t.Fatalf("runBd stealth: %v", err)
		}

		expectedDB := filepath.Join(oroHome, "projects", stealthDir, "beads")
		if !argPairPresent(invoc.args, "--db", expectedDB) {
			t.Errorf("stealth mode: expected --db %s in args, got %v", expectedDB, invoc.args)
		}
		// list subcommand should still be present.
		found := false
		for _, a := range invoc.args {
			if a == "list" {
				found = true
			}
		}
		if !found {
			t.Errorf("stealth mode: 'list' subcommand missing from args %v", invoc.args)
		}
	})

	t.Run("no project initialized returns actionable error", func(t *testing.T) {
		tmp := t.TempDir()
		oroHome := t.TempDir()
		t.Setenv("ORO_HOME", oroHome)
		t.Setenv("ORO_PROJECT", "") // isolate from ambient env

		deps := bdDeps{
			lookPath: fakeLookPath("/usr/local/bin/bd"),
			runner:   stubBdRunner(&capturedBdInvocation{}),
		}

		err := runBd(tmp, []string{"list"}, deps)
		if err == nil {
			t.Fatal("expected error when no project initialized")
		}
		// Should mention oro init.
		if !strings.Contains(err.Error(), "init") {
			t.Errorf("error should mention 'init', got: %v", err)
		}
	})

	t.Run("bd not in PATH returns actionable error", func(t *testing.T) {
		repoRoot := makeStandardBdTestDir(t, "myproject")
		oroHome := t.TempDir()
		t.Setenv("ORO_HOME", oroHome)

		deps := bdDeps{
			lookPath: func(string) (string, error) {
				return "", exec.ErrNotFound
			},
			runner: stubBdRunner(&capturedBdInvocation{}),
		}

		err := runBd(repoRoot, []string{"list"}, deps)
		if err == nil {
			t.Fatal("expected error when bd not in PATH")
		}
		if !strings.Contains(err.Error(), "PATH") {
			t.Errorf("error should mention 'PATH', got: %v", err)
		}
	})

	t.Run("oro bd --help shows bd help (DisableFlagParsing)", func(t *testing.T) {
		cmd := newBdCmd()
		if !cmd.DisableFlagParsing {
			t.Error("newBdCmd should have DisableFlagParsing=true so --help passes to bd")
		}
	})

	t.Run("bd registered in root command", func(t *testing.T) {
		root := newRootCmd()
		for _, sub := range root.Commands() {
			if sub.Name() == "bd" {
				return
			}
		}
		t.Fatal("expected 'bd' subcommand in root")
	})
}
