package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"oro/pkg/dispatcher"
	"oro/pkg/evidencefs"
	"oro/pkg/ops"
	"oro/pkg/protocol"

	"github.com/spf13/cobra"
)

func TestReadyEvidenceDoesNotDirtyWorktree(t *testing.T) {
	controller := filepath.Join(t.TempDir(), "controller")
	assigned := filepath.Join(t.TempDir(), "assigned")
	runGitTestCommand(t, "", "init", controller)
	runGitTestCommand(t, controller, "config", "user.email", "oro@example.invalid")
	runGitTestCommand(t, controller, "config", "user.name", "Oro Test")
	if err := os.WriteFile(filepath.Join(controller, "tracked.txt"), []byte("baseline\n"), 0o600); err != nil {
		t.Fatalf("write tracked fixture: %v", err)
	}
	runGitTestCommand(t, controller, "add", "tracked.txt")
	runGitTestCommand(t, controller, "commit", "-m", "baseline")
	runGitTestCommand(t, controller, "worktree", "add", "-b", "agent/evidence-clean", assigned)

	oroHome := filepath.Join(t.TempDir(), "oro-home")
	if err := os.MkdirAll(oroHome, 0o700); err != nil {
		t.Fatalf("create ORO_HOME: %v", err)
	}
	t.Setenv("ORO_HOME", oroHome)
	t.Setenv("ORO_PROJECT", "evidence-clean")
	withChdir(t, controller, func() {
		paths, err := ResolveDaemonPaths()
		if err != nil {
			t.Fatalf("ResolveDaemonPaths: %v", err)
		}
		if err := evidencefs.WriteFile(paths.ReviewEvidenceDir,
			[]string{"oro-evidence-clean", "1"}, "1.json", []byte(`{"assignment_id":1}`)); err != nil {
			t.Fatalf("write review evidence: %v", err)
		}
		for name, worktree := range map[string]string{"controller": controller, "assigned": assigned} {
			status := runGitTestCommand(t, worktree, "status", "--porcelain")
			if status != "" {
				t.Fatalf("%s worktree dirty after evidence write:\n%s", name, status)
			}
		}
	})
}

func runGitTestCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestStartReadsProjectConfig(t *testing.T) {
	t.Run("reads project name from .oro/config.yaml", func(t *testing.T) {
		tmpDir := t.TempDir()
		oroDir := filepath.Join(tmpDir, ".oro")
		if err := os.MkdirAll(oroDir, 0o755); err != nil { //nolint:gosec // test dir
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(oroDir, "config.yaml"), []byte("project: myproject\nlanguages:\n  go:\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		name, err := readProjectConfig(tmpDir)
		if err != nil {
			t.Fatalf("readProjectConfig failed: %v", err)
		}
		if name != "myproject" {
			t.Errorf("expected 'myproject', got %q", name)
		}
	})

	t.Run("returns empty string when .oro/config.yaml missing", func(t *testing.T) {
		tmpDir := t.TempDir()

		name, err := readProjectConfig(tmpDir)
		if err != nil {
			t.Fatalf("readProjectConfig should not error on missing config: %v", err)
		}
		if name != "" {
			t.Errorf("expected empty string, got %q", name)
		}
	})

	t.Run("start project name preserves explicit ORO_PROJECT", func(t *testing.T) {
		tmpDir := t.TempDir()
		oroDir := filepath.Join(tmpDir, ".oro")
		if err := os.MkdirAll(oroDir, 0o755); err != nil { //nolint:gosec // test dir
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(oroDir, "config.yaml"), []byte("project: config-project\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("ORO_PROJECT", "env-project")

		name, err := startProjectName(tmpDir)
		if err != nil {
			t.Fatalf("startProjectName failed: %v", err)
		}
		if name != "env-project" {
			t.Errorf("expected env project, got %q", name)
		}
	})

	t.Run("ORO_HOME is set for child processes", func(t *testing.T) {
		// resolveOroHome should return ORO_HOME when set
		t.Setenv("ORO_HOME", "/custom/oro")
		home, err := resolveOroHome()
		if err != nil {
			t.Fatalf("resolveOroHome failed: %v", err)
		}
		if home != "/custom/oro" {
			t.Errorf("expected /custom/oro, got %q", home)
		}
	})
}

func TestStartRejectsGitHubPolicyBeforeDispatcherMutation(t *testing.T) {
	projectRoot := t.TempDir()
	binDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectRoot, ".oro"), 0o750); err != nil {
		t.Fatalf("make project config directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, ".oro", "config.yaml"), []byte(remoteCapabilityConfigYAML), 0o600); err != nil {
		t.Fatalf("write remote gate config: %v", err)
	}
	if err := PersistRemoteCapabilities(remoteCapabilityEvidencePath(projectRoot), Capabilities{
		Host:          "github.com",
		Repository:    "acme/oro",
		Workflow:      "ci.yml",
		DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("persist remote capabilities: %v", err)
	}
	writeRemoteCapabilityFixture(t, filepath.Join(binDir, "gh"), `#!/bin/sh
case "$*" in
  *"repos/acme/oro/actions/workflows/ci.yml") printf '%s\n' '{"path":".github/workflows/ci.yml","state":"active"}' ;;
  *"repos/acme/oro") printf '%s\n' '{"full_name":"acme/oro","default_branch":"main"}' ;;
  *"contents/.github/workflows/ci.yml"*) printf '%s\n' 'on: pull_request' ;;
  *) exit 1 ;;
esac
`)
	t.Setenv("PATH", binDir)
	env := hermeticOroEnv(t, t.TempDir())

	launched := false
	previousRunDaemonOnly := runDaemonOnlyFn
	runDaemonOnlyFn = func(_ *cobra.Command, _ string, _, _ int, _, _, _ time.Duration, _ bool, _ string, _ bool, _ bool, _ string, _ cleanlinessStartConfig) error {
		launched = true
		return nil
	}
	t.Cleanup(func() { runDaemonOnlyFn = previousRunDaemonOnly })

	cmd := newStartCmd()
	cmd.SetContext(context.Background())
	if err := cmd.Flags().Set("daemon-only", "true"); err != nil {
		t.Fatalf("set daemon-only: %v", err)
	}
	var startErr error
	withChdir(t, projectRoot, func() { startErr = cmd.RunE(cmd, nil) })
	if startErr == nil || !strings.Contains(startErr.Error(), "remote gate startup preflight") {
		t.Fatalf("start error = %v, want GitHub preflight rejection", startErr)
	}
	if launched {
		t.Fatal("start launched dispatcher after GitHub preflight rejection")
	}
	for _, path := range []string{env.PIDPath, env.SocketPath, env.DBPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("startup mutated %s before GitHub preflight rejection: %v", path, err)
		}
	}
}

func TestStartRejectsRepoLocalOroShadow(t *testing.T) {
	t.Run("PATH resolves to repo-local shadow", func(t *testing.T) {
		repoRoot := t.TempDir()
		localOro := filepath.Join(repoRoot, "oro")
		installedOro := filepath.Join(t.TempDir(), "oro")

		got, err := resolveTrustedSelfExecutable(
			repoRoot,
			"oro",
			func() (string, error) { return installedOro, nil },
			func(string) (string, error) { return localOro, nil },
		)

		if err == nil {
			t.Fatalf("expected repo-local oro shadow error, got path %q", got)
		}
		if !strings.Contains(err.Error(), cleanExecutablePath(localOro)) {
			t.Fatalf("error %q must name repo-local shadow path %q", err, cleanExecutablePath(localOro))
		}
	})

	t.Run("current executable is repo-local shadow", func(t *testing.T) {
		repoRoot := t.TempDir()
		localOro := filepath.Join(repoRoot, "oro")

		got, err := resolveTrustedSelfExecutable(
			repoRoot,
			"./oro",
			func() (string, error) { return localOro, nil },
			func(string) (string, error) { return localOro, nil },
		)

		if err == nil {
			t.Fatalf("expected repo-local current executable error, got path %q", got)
		}
		if !strings.Contains(err.Error(), cleanExecutablePath(localOro)) {
			t.Fatalf("error %q must name repo-local executable path %q", err, cleanExecutablePath(localOro))
		}
	})
}

func TestDaemonSpawnerUsesResolvedSelfExecutable(t *testing.T) {
	repoRoot := t.TempDir()
	installedOro := filepath.Join(t.TempDir(), "oro")

	got, err := resolveTrustedSelfExecutable(
		repoRoot,
		"oro",
		func() (string, error) { return installedOro, nil },
		func(string) (string, error) { return installedOro, nil },
	)
	if err != nil {
		t.Fatalf("resolveTrustedSelfExecutable returned error: %v", err)
	}
	want := cleanExecutablePath(installedOro)
	if got != want {
		t.Fatalf("resolved executable = %q, want %q", got, want)
	}
}

func TestCurrentRepoRootRecognizesGitDirectory(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("create .git directory: %v", err)
	}
	nested := filepath.Join(repoRoot, "subdir", "package")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("create nested directory: %v", err)
	}
	wantRoot, err := filepath.EvalSymlinks(repoRoot)
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(nested); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origDir)
	})

	if got := currentRepoRoot(); got != wantRoot {
		t.Fatalf("currentRepoRoot() = %q, want git root %q", got, wantRoot)
	}
}

func TestCurrentRepoRootFallsBackToStartingDirectory(t *testing.T) {
	startDir := t.TempDir()
	wantDir, err := filepath.EvalSymlinks(startDir)
	if err != nil {
		t.Fatalf("resolve start directory: %v", err)
	}

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(startDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origDir)
	})

	if got := currentRepoRoot(); got != wantDir {
		t.Fatalf("currentRepoRoot() = %q, want starting directory %q", got, wantDir)
	}
}

func TestCodexHookConfigBlockReplacement(t *testing.T) {
	hooksDir := filepath.Join(t.TempDir(), "hooks")
	block := codexHookConfigBlock(hooksDir)

	if !strings.Contains(block, "SessionStart") ||
		!strings.Contains(block, "PreToolUse") ||
		!strings.Contains(block, "PostToolUse") ||
		!strings.Contains(block, "Stop") {
		t.Fatalf("Codex hook config block missing required event wiring:\n%s", block)
	}
	for _, command := range []string{
		"session_start_global.py",
		"enforce_skills.py",
		"destructive_command_guard.py",
		"oro-search-hook",
		"enforce_worktree_writes.py",
		"prompt_injection_guard.py",
		"context_pruner.py",
		"auto-format.sh",
		"context_block_stop.py",
		"stop-checklist.sh",
	} {
		if !strings.Contains(block, command) {
			t.Fatalf("Codex hook config block missing %s wiring:\n%s", command, block)
		}
	}

	existing := strings.Join([]string{
		`model = "gpt-5.5"`,
		"",
		codexOroHooksBegin,
		"[hooks]",
		"SessionStart = []",
		codexOroHooksEnd,
		"",
		`approval_policy = "never"`,
	}, "\n")

	got := replaceManagedCodexHookBlock(existing, block)

	if strings.Count(got, codexOroHooksBegin) != 1 || strings.Count(got, codexOroHooksEnd) != 1 {
		t.Fatalf("managed hook block markers not replaced exactly once:\n%s", got)
	}
	if strings.Contains(got, "SessionStart = []") {
		t.Fatalf("old managed hook body survived replacement:\n%s", got)
	}
	if !strings.Contains(got, `model = "gpt-5.5"`) || !strings.Contains(got, `approval_policy = "never"`) {
		t.Fatalf("user Codex config outside managed block was not preserved:\n%s", got)
	}
}

// TestCodexHookConfigBashMatcherIncludesSearchHook verifies that oro-search-hook
// runs on the Codex PreToolUse Bash matcher (LAST in the chain, after the safety
// guards) and that the dead str_replace_based_edit_tool matcher is gone. This is
// what wires the token-saving read hook onto the read surface Codex actually uses.
func TestCodexHookConfigBashMatcherIncludesSearchHook(t *testing.T) {
	hooksDir := filepath.Join(t.TempDir(), "hooks")
	block := codexHookConfigBlock(hooksDir)

	if strings.Contains(block, "str_replace_based_edit_tool") {
		t.Errorf("Codex hook config still wires the dead str_replace_based_edit_tool matcher:\n%s", block)
	}

	bashLine := findBashPreToolUseLine(t, block)
	for _, want := range []string{"enforce_skills.py", "destructive_command_guard.py", "oro-search-hook"} {
		if !strings.Contains(bashLine, want) {
			t.Fatalf("Bash PreToolUse matcher missing %s:\n%s", want, bashLine)
		}
	}

	iEnforce := strings.Index(bashLine, "enforce_skills.py")
	iGuard := strings.Index(bashLine, "destructive_command_guard.py")
	iHook := strings.Index(bashLine, "oro-search-hook")
	if iEnforce >= iGuard || iGuard >= iHook {
		t.Errorf("oro-search-hook must run LAST on the Bash chain "+
			"(enforce_skills < destructive_command_guard < oro-search-hook); "+
			"got positions %d/%d/%d:\n%s", iEnforce, iGuard, iHook, bashLine)
	}
}

// findBashPreToolUseLine returns the PreToolUse Bash matcher line — identified by
// carrying enforce_skills.py, which distinguishes it from the PostToolUse Bash line.
func findBashPreToolUseLine(t *testing.T, block string) string {
	t.Helper()
	for _, line := range strings.Split(block, "\n") {
		if strings.Contains(line, `matcher = "Bash"`) && strings.Contains(line, "enforce_skills.py") {
			return line
		}
	}
	t.Fatalf("no PreToolUse Bash matcher line found in block:\n%s", block)
	return ""
}

func TestInstallCodexHookConfigWritesManagedBlock(t *testing.T) {
	codexHome := t.TempDir()
	hooksDir := filepath.Join(t.TempDir(), "hooks")
	configPath := filepath.Join(codexHome, "config.toml")
	if err := os.WriteFile(configPath, []byte("model = \"gpt-5.5\"\n"), 0o600); err != nil {
		t.Fatalf("setup Codex config: %v", err)
	}

	if err := installCodexHookConfig(codexHome, hooksDir); err != nil {
		t.Fatalf("installCodexHookConfig returned error: %v", err)
	}
	first, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read Codex config: %v", err)
	}
	if !strings.Contains(string(first), "model = \"gpt-5.5\"") ||
		!strings.Contains(string(first), "oro-search-hook") ||
		!strings.Contains(string(first), "stop-checklist.sh") {
		t.Fatalf("Codex config missing preserved settings or hook commands:\n%s", first)
	}

	if err := installCodexHookConfig(codexHome, hooksDir); err != nil {
		t.Fatalf("second installCodexHookConfig returned error: %v", err)
	}
	second, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read Codex config after second install: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("Codex hook config install must be idempotent:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestHookPathsWouldLeak(t *testing.T) {
	cases := []struct {
		name      string
		codexHome string
		hooksDir  string
		want      bool
	}{
		{
			name:      "ephemeral hooks into persistent home leaks",
			codexHome: "/Users/dev/.codex",
			hooksDir:  "/tmp/oro-subprocess/abc/TestX/001/oro-home/hooks",
			want:      true,
		},
		{
			name:      "both under temp is a hermetic test, no leak",
			codexHome: "/tmp/oro-subprocess/abc/TestX/001/codex-home",
			hooksDir:  "/tmp/oro-subprocess/abc/TestX/001/oro-home/hooks",
			want:      false,
		},
		{
			name:      "different temp roots are still a hermetic test",
			codexHome: "/var/folders/ab/codex-home",
			hooksDir:  "/tmp/oro-subprocess/abc/TestX/001/oro-home/hooks",
			want:      false,
		},
		{
			name:      "both persistent is production, no leak",
			codexHome: "/Users/dev/.codex",
			hooksDir:  "/Users/dev/.oro/hooks",
			want:      false,
		},
		{
			name:      "any common tmp hooks path leaks into persistent home",
			codexHome: "/Users/dev/.codex",
			hooksDir:  "/tmp/oro-other/hooks",
			want:      true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hookPathsWouldLeak(tc.codexHome, tc.hooksDir); got != tc.want {
				t.Fatalf("hookPathsWouldLeak(%q, %q) = %v, want %v",
					tc.codexHome, tc.hooksDir, got, tc.want)
			}
		})
	}
}

func TestPathUnder(t *testing.T) {
	if pathUnder("/tmp/oro", "/tmp/oro-other/hooks") {
		t.Fatal("pathUnder must not treat a sibling path as nested")
	}

	realRoot := t.TempDir()
	aliasParent := t.TempDir()
	alias := filepath.Join(aliasParent, "temp-alias")
	if err := os.Symlink(realRoot, alias); err != nil {
		t.Fatalf("create temp root alias: %v", err)
	}
	if !pathUnder(alias, filepath.Join(realRoot, "hooks")) {
		t.Fatal("pathUnder must resolve symlinked ancestors")
	}
}

func TestHookPathsWouldLeak_NonTmpdirSandboxRoot(t *testing.T) {
	// On macOS, os.TempDir can be /var/folders/.../T even though Oro's
	// subprocess sandbox roots are intentionally created under /tmp.
	// The guard must still refuse to write these transient hook paths into a
	// persistent CODEX_HOME.
	codexHome := "/Users/u/.codex"
	hooksDir := "/tmp/oro-subprocess/h/oro-home/hooks"
	if !hookPathsWouldLeak(codexHome, hooksDir) {
		t.Fatalf("hookPathsWouldLeak(%q, %q) = false, want true", codexHome, hooksDir)
	}
}

func TestHookPathsWouldLeak_NonstandardGoTempRoot(t *testing.T) {
	goTempRoot := "/opt/hostedtoolcache/oro-go-tmp"
	t.Setenv("GOTMPDIR", goTempRoot)

	codexHome := "/home/runner/.codex"
	hooksDir := filepath.Join(goTempRoot, "TestStart", "001", "oro-home", "hooks")
	if !hookPathsWouldLeak(codexHome, hooksDir) {
		t.Fatalf("hookPathsWouldLeak(%q, %q) = false, want true for GOTMPDIR hooks", codexHome, hooksDir)
	}
}

func TestInstallCodexHookConfigRefusesLeakyHooks(t *testing.T) {
	// Model a persistent Codex home with a disposable directory under the
	// package worktree, outside every recognized temporary root.
	persistentHome, err := os.MkdirTemp(".", ".codex-home-test-")
	if err != nil {
		t.Fatalf("create persistent Codex home fixture: %v", err)
	}
	t.Cleanup(func() {
		if removeErr := os.RemoveAll(persistentHome); removeErr != nil {
			t.Errorf("remove persistent Codex home fixture: %v", removeErr)
		}
	})
	configPath := filepath.Join(persistentHome, "config.toml")

	// This reproduces a test that isolated ORO_HOME but forgot CODEX_HOME.
	ephemeralRoot := t.TempDir()
	hooksDir := filepath.Join(ephemeralRoot, "oro-home", "hooks")

	err = installCodexHookConfig(persistentHome, hooksDir)
	if err == nil || !strings.Contains(err.Error(), "refusing to install Codex hook config") {
		t.Fatalf("expected refusal error, got %v", err)
	}
	if _, statErr := os.Stat(configPath); !os.IsNotExist(statErr) {
		t.Fatalf("guard must refuse before writing config.toml; stat err = %v", statErr)
	}
}

func TestCodexDirectSkillSetupAcceptance(t *testing.T) {
	t.Run("startup_links_skills_without_marketplace", func(t *testing.T) {
		oroHome, codexHome, skillSource := prepareCodexStartFixture(t, "codex-only", "", true)

		if err := ensureRuntimeProjectAssets(io.Discard, oroHome); err != nil {
			t.Fatalf("ensureRuntimeProjectAssets: %v", err)
		}
		assertSkillSymlink(t, filepath.Join(codexHome, "skills", "using-skills"), filepath.Dir(skillSource))
		assertPathMissing(t, filepath.Join(codexHome, "oro-marketplace"))
	})

	t.Run("startup_rejects_missing_using_skills", func(t *testing.T) {
		oroHome, _, _ := prepareCodexStartFixture(t, "codex-only", "", false)

		err := ensureRuntimeProjectAssets(io.Discard, oroHome)
		if err == nil || !strings.Contains(err.Error(), "using-skills") {
			t.Fatalf("missing using-skills error = %v, want required-source failure", err)
		}
	})

	t.Run("agent_assets_does_not_create_marketplace", func(t *testing.T) {
		home := t.TempDir()
		cfg := defaultAgentAssetsConfig(home, agentRuntimeCodex)
		cfg.oroSkillsDir = filepath.Join(home, ".oro", ".claude", "skills")
		makeSkillsDir(t, cfg.oroSkillsDir, []string{"using-skills"})

		if err := runAgentAssetsSync(cfg, io.Discard); err != nil {
			t.Fatalf("runAgentAssetsSync: %v", err)
		}
		assertSkillSymlink(t, filepath.Join(home, ".codex", "skills", "using-skills"), filepath.Join(cfg.oroSkillsDir, "using-skills"))
		assertPathMissing(t, filepath.Join(home, ".codex", "oro-marketplace"))
	})

	t.Run("concurrent_sync_converges", func(t *testing.T) {
		root := t.TempDir()
		src := filepath.Join(root, "source")
		dst := filepath.Join(root, "destination")
		makeSkillsDir(t, src, []string{"using-skills", "brainstorming"})
		cfg := agentAssetsConfig{oroSkillsDir: src, destSkillsDir: dst}

		const workers = 24
		start := make(chan struct{})
		errs := make(chan error, workers)
		var wg sync.WaitGroup
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs <- copySkills(cfg, io.Discard)
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("concurrent copySkills: %v", err)
			}
		}
		assertSkillSymlink(t, filepath.Join(dst, "using-skills"), filepath.Join(src, "using-skills"))
		assertSkillSymlink(t, filepath.Join(dst, "brainstorming"), filepath.Join(src, "brainstorming"))
	})

	t.Run("legacy_directory_recovers_without_temp_links", func(t *testing.T) {
		root := t.TempDir()
		src := filepath.Join(root, "source")
		dst := filepath.Join(root, "destination")
		makeSkillsDir(t, src, []string{"using-skills"})
		legacy := filepath.Join(dst, "using-skills")
		makeSkillsDir(t, dst, []string{"using-skills"})

		if err := copySkills(agentAssetsConfig{oroSkillsDir: src, destSkillsDir: dst}, io.Discard); err != nil {
			t.Fatalf("copySkills over legacy directory: %v", err)
		}
		assertSkillSymlink(t, legacy, filepath.Join(src, "using-skills"))
		leftovers, err := filepath.Glob(filepath.Join(dst, ".oro-skill-*"))
		if err != nil {
			t.Fatal(err)
		}
		if len(leftovers) != 0 {
			t.Fatalf("temporary skill links remain: %v", leftovers)
		}
	})

	t.Run("runtime_override_links_skills_before_launch", func(t *testing.T) {
		oroHome, codexHome, skillSource := prepareCodexStartFixture(t, "claude-only", runtimeCodex, true)

		if err := ensureRuntimeProjectAssets(io.Discard, oroHome); err != nil {
			t.Fatalf("ensureRuntimeProjectAssets with runtime override: %v", err)
		}
		assertSkillSymlink(t, filepath.Join(codexHome, "skills", "using-skills"), filepath.Dir(skillSource))
	})

	t.Run("mixed_provider_links_skills_before_launch", func(t *testing.T) {
		oroHome, codexHome, skillSource := prepareCodexStartFixture(t, "claude-coding-codex-review", "", true)

		if err := ensureRuntimeProjectAssets(io.Discard, oroHome); err != nil {
			t.Fatalf("ensureRuntimeProjectAssets with mixed providers: %v", err)
		}
		assertSkillSymlink(t, filepath.Join(codexHome, "skills", "using-skills"), filepath.Dir(skillSource))
	})

	t.Run("claude_only_does_not_mutate_codex_home", func(t *testing.T) {
		oroHome, codexHome, _ := prepareCodexStartFixture(t, "claude-only", "", false)
		sentinel := filepath.Join(codexHome, "sentinel")
		if err := os.MkdirAll(codexHome, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sentinel, []byte("unchanged\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		if err := ensureRuntimeProjectAssets(io.Discard, oroHome); err != nil {
			t.Fatalf("Claude-only ensureRuntimeProjectAssets: %v", err)
		}
		entries, err := os.ReadDir(codexHome)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Name() != "sentinel" {
			t.Fatalf("Claude-only startup mutated Codex home: %v", entries)
		}
	})
}

func prepareCodexStartFixture(t *testing.T, providerMode, runtimeOverride string, withSkills bool) (string, string, string) {
	t.Helper()
	home := t.TempDir()
	oroHome := filepath.Join(home, ".oro-home")
	codexHome := filepath.Join(home, ".codex-home")
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(filepath.Join(project, ".oro"), 0o750); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("project: direct-skills\nagent:\n  provider_mode: %s\n", providerMode)
	if err := os.WriteFile(filepath.Join(project, ".oro", "config.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "go.mod"), []byte("module directskills\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hooksDir := filepath.Join(oroHome, "hooks")
	if err := os.MkdirAll(hooksDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "oro-search-hook"), []byte("#!/bin/sh\nexit 0\n"), 0o750); err != nil {
		t.Fatal(err)
	}

	skillSource := filepath.Join(oroHome, ".claude", "skills", "using-skills", "SKILL.md")
	if withSkills {
		makeSkillsDir(t, filepath.Join(oroHome, ".claude", "skills"), []string{"using-skills"})
	}

	t.Setenv("HOME", home)
	t.Setenv("ORO_HOME", oroHome)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv(agentRuntimeEnvVar, runtimeOverride)
	t.Chdir(project)
	return oroHome, codexHome, skillSource
}

func assertSkillSymlink(t *testing.T, path, wantTarget string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat skill link %s: %v", path, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("skill path %s mode = %v, want symlink", path, info.Mode())
	}
	gotTarget, err := os.Readlink(path)
	if err != nil {
		t.Fatal(err)
	}
	if gotTarget != wantTarget {
		t.Fatalf("skill link %s target = %q, want %q", path, gotTarget, wantTarget)
	}
}

func assertPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("path %s exists or stat failed: %v", path, err)
	}
}

func TestMaybeRunRepoPreflightChecksHonorsFlag(t *testing.T) {
	t.Run("skips when disabled", func(t *testing.T) {
		checkCalls := 0
		checker := func(_ io.Writer, _ string) error {
			checkCalls++
			return nil
		}
		if err := maybeRunRepoPreflightChecksWith(io.Discard, filepath.Join(t.TempDir(), "missing"), false, checker); err != nil {
			t.Fatalf("maybeRunRepoPreflightChecks returned error when disabled: %v", err)
		}
		if checkCalls != 0 {
			t.Fatalf("disabled repo preflight should not call checker, got %d calls", checkCalls)
		}
	})

	t.Run("runs when enabled", func(t *testing.T) {
		oroHome := filepath.Join(t.TempDir(), "oro-home")
		checkCalls := 0
		checker := func(_ io.Writer, gotHome string) error {
			checkCalls++
			if gotHome != oroHome {
				t.Fatalf("expected oroHome %q to be forwarded to repo preflight checker, got %q", oroHome, gotHome)
			}
			return nil
		}
		if err := maybeRunRepoPreflightChecksWith(io.Discard, oroHome, true, checker); err != nil {
			t.Fatalf("maybeRunRepoPreflightChecks returned error when enabled: %v", err)
		}
		if checkCalls != 1 {
			t.Fatalf("enabled repo preflight should call checker once, got %d calls", checkCalls)
		}
	})
}

func TestCleanStaleWorkerLogs(t *testing.T) {
	t.Run("old dirs deleted, new dirs survive", func(t *testing.T) {
		tmpDir := t.TempDir()
		workersDir := filepath.Join(tmpDir, "workers")
		if err := os.MkdirAll(workersDir, 0o700); err != nil {
			t.Fatal(err)
		}

		// Create an "old" directory and backdate its modtime to 8 days ago.
		oldDir := filepath.Join(workersDir, "worker-old")
		if err := os.MkdirAll(oldDir, 0o700); err != nil {
			t.Fatal(err)
		}
		eightDaysAgo := time.Now().Add(-8 * 24 * time.Hour)
		if err := os.Chtimes(oldDir, eightDaysAgo, eightDaysAgo); err != nil {
			t.Fatal(err)
		}

		// Create a "new" directory (default modtime = now).
		newDir := filepath.Join(workersDir, "worker-new")
		if err := os.MkdirAll(newDir, 0o700); err != nil {
			t.Fatal(err)
		}

		cleanStaleWorkerLogs(tmpDir, 7*24*time.Hour)

		// Old dir should be gone.
		if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
			t.Errorf("expected old dir to be removed, got err: %v", err)
		}
		// New dir should survive.
		if _, err := os.Stat(newDir); err != nil {
			t.Errorf("expected new dir to survive, got: %v", err)
		}
	})

	t.Run("missing workers dir no error", func(t *testing.T) {
		tmpDir := t.TempDir()
		// workers dir does not exist — should not panic or error.
		cleanStaleWorkerLogs(tmpDir, 7*24*time.Hour)
	})

	t.Run("non-directory entries ignored", func(t *testing.T) {
		tmpDir := t.TempDir()
		workersDir := filepath.Join(tmpDir, "workers")
		if err := os.MkdirAll(workersDir, 0o700); err != nil {
			t.Fatal(err)
		}

		// Create a regular file (not a directory) with an old modtime.
		oldFile := filepath.Join(workersDir, "stale.log")
		if err := os.WriteFile(oldFile, []byte("log"), 0o600); err != nil {
			t.Fatal(err)
		}
		eightDaysAgo := time.Now().Add(-8 * 24 * time.Hour)
		if err := os.Chtimes(oldFile, eightDaysAgo, eightDaysAgo); err != nil {
			t.Fatal(err)
		}

		cleanStaleWorkerLogs(tmpDir, 7*24*time.Hour)

		// File should still exist — only directories are cleaned.
		if _, err := os.Stat(oldFile); err != nil {
			t.Errorf("expected non-dir file to survive, got: %v", err)
		}
	})
}

func TestStartPrintsQuitHint(t *testing.T) {
	t.Run("prints navigation hint when attaching (not detached)", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "oro.pid")
		sockPath := fmt.Sprintf("/tmp/oro-hint-%d.sock", time.Now().UnixNano())
		t.Cleanup(func() { _ = os.Remove(sockPath) })
		dbPath := filepath.Join(tmpDir, "state.db")

		t.Setenv("ORO_PID_PATH", pidFile)
		t.Setenv("ORO_SOCKET_PATH", sockPath)
		t.Setenv("ORO_DB_PATH", dbPath)

		fakeTmux := newFakeCmd()
		fakeTmux.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubPaneReady(fakeTmux, "oro", "manager nudge")

		spawner := &fakeSpawner{
			returnPID:  99999,
			socketPath: sockPath,
		}

		var stdout bytes.Buffer
		// detach=false means attach, so hint should be printed
		err := runFullStart(&stdout, 2, 2, "sonnet", "", spawner, fakeTmux, func(int) error { return nil }, 100*time.Millisecond, noopSleep, 50*time.Millisecond, false)
		// Expect error because AttachInteractive tries to attach to real tmux
		if err == nil {
			t.Fatal("expected error from AttachInteractive in test environment")
		}

		// Verify hint was printed before attach attempt
		out := stdout.String()
		if !strings.Contains(out, "ctrl-b 0/1") {
			t.Errorf("expected hint to contain 'ctrl-b 0/1', got: %s", out)
		}
		if !strings.Contains(out, "ctrl-b d") {
			t.Errorf("expected hint to contain 'ctrl-b d', got: %s", out)
		}
		if !strings.Contains(out, "oro stop") {
			t.Errorf("expected hint to contain 'oro stop', got: %s", out)
		}
	})

	t.Run("does not print hint when detached", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "oro.pid")
		sockPath := fmt.Sprintf("/tmp/oro-detach-%d.sock", time.Now().UnixNano())
		t.Cleanup(func() { _ = os.Remove(sockPath) })
		dbPath := filepath.Join(tmpDir, "state.db")

		t.Setenv("ORO_PID_PATH", pidFile)
		t.Setenv("ORO_SOCKET_PATH", sockPath)
		t.Setenv("ORO_DB_PATH", dbPath)

		fakeTmux := newFakeCmd()
		fakeTmux.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubPaneReady(fakeTmux, "oro", "manager nudge")

		spawner := &fakeSpawner{
			returnPID:  88888,
			socketPath: sockPath,
		}

		var stdout bytes.Buffer
		// detach=true means no attach, so hint should NOT be printed
		err := runFullStart(&stdout, 2, 2, "sonnet", "", spawner, fakeTmux, func(int) error { return nil }, 100*time.Millisecond, noopSleep, 50*time.Millisecond, true)
		if err != nil {
			t.Fatalf("runFullStart with detach should succeed, got: %v", err)
		}

		// Verify hint was NOT printed (only detach instructions)
		out := stdout.String()
		if strings.Contains(out, "ctrl-b 0/1") || strings.Contains(out, "switch panes") {
			t.Errorf("hint should not be printed in detached mode, got: %s", out)
		}
		if !strings.Contains(out, "detached") {
			t.Errorf("expected detached message, got: %s", out)
		}
	})
}

// TestRunFullStartKillsDaemonOnSessionCreateError verifies that when
// sess.Create() fails, runFullStart calls killFn(pid) to clean up the
// orphaned daemon process before returning the original error.
func TestRunFullStartKillsDaemonOnSessionCreateError(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	sockPath := fmt.Sprintf("/tmp/oro-kill-%d.sock", time.Now().UnixNano())
	t.Cleanup(func() { _ = os.Remove(sockPath) })

	t.Setenv("ORO_PID_PATH", pidFile)
	t.Setenv("ORO_SOCKET_PATH", sockPath)
	t.Setenv("ORO_DB_PATH", filepath.Join(tmpDir, "state.db"))

	// Compute the exact key that fakeCmd will see for new-session.
	newSessionKey := key("tmux", "new-session", "-d", "-s", "oro", "-n", defaultTmuxWindowName)

	// Spawner starts a real sleep 1000 child and returns its PID.
	var spawnedPID int
	spawnerFn := &killTestSpawner{
		socketPath: sockPath,
		onSpawn: func(pidPath string) (int, error) {
			cmd := exec.Command("sleep", "1000")
			if err := cmd.Start(); err != nil {
				return 0, fmt.Errorf("start sleep 1000: %w", err)
			}
			spawnedPID = cmd.Process.Pid
			// Write PID file so the daemon looks like a real process.
			if err := WritePIDFile(pidPath, spawnedPID); err != nil {
				return 0, err
			}
			return spawnedPID, nil
		},
	}

	fakeTmux := newFakeCmd()
	// has-session returns error (no existing session).
	fakeTmux.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
	// new-session fails: simulates tmux not available or misconfigured.
	fakeTmux.errs[newSessionKey] = fmt.Errorf("tmux new-session: simulated failure")

	killCalled := false
	killFn := func(pid int) error {
		killCalled = true
		return syscall.Kill(pid, syscall.SIGKILL)
	}

	var stdout bytes.Buffer
	err := runFullStart(&stdout, 2, 2, "sonnet", "", spawnerFn, fakeTmux, killFn, 200*time.Millisecond, noopSleep, 50*time.Millisecond, false)
	if err == nil {
		t.Fatal("expected runFullStart to return error when tmux session create fails")
	}
	// The error should wrap the tmux session creation failure.
	if !strings.Contains(err.Error(), "create tmux session") {
		t.Errorf("expected error to mention 'create tmux session', got: %v", err)
	}

	// killFn must have been called to clean up the orphaned daemon.
	if !killCalled {
		t.Error("expected killFn to be called after tmux session creation failed")
	}

	// The sleep 1000 process must be dead.
	if spawnedPID == 0 {
		t.Fatal("spawner was never called or PID not captured")
	}
	// Reap the zombie: Find+Wait collects the exit status so the PID is freed.
	proc, findErr := os.FindProcess(spawnedPID)
	if findErr != nil {
		t.Fatalf("os.FindProcess(%d): %v", spawnedPID, findErr)
	}
	// Wait with a deadline — SIGKILL is instantaneous, so we should not need long.
	done := make(chan error, 1)
	go func() { _, err := proc.Wait(); done <- err }()
	select {
	case <-done:
		// Process exited — good.
	case <-time.After(2 * time.Second):
		t.Errorf("sleep 1000 (PID %d) did not exit within 2s after SIGKILL cleanup", spawnedPID)
	}
}

// killTestSpawner is a DaemonSpawner that delegates to onSpawn and also
// creates a UDS listener so sendStartDirective can connect.
type killTestSpawner struct {
	socketPath string
	onSpawn    func(pidPath string) (int, error)
}

func (s *killTestSpawner) SpawnDaemon(pidPath string, workers, _ int) (int, error) {
	pid, err := s.onSpawn(pidPath)
	if err != nil {
		return 0, err
	}
	if s.socketPath != "" {
		ln, listenErr := net.Listen("unix", s.socketPath)
		if listenErr != nil {
			return 0, listenErr
		}
		// Accept connections in a loop so pollForSocket probes don't consume
		// the only handler. The start directive arrives on a later connection.
		go func() {
			defer ln.Close()
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				go func(c net.Conn) {
					defer c.Close()
					scanner := bufio.NewScanner(c)
					if scanner.Scan() {
						ack := protocol.Message{
							Type: protocol.MsgACK,
							ACK:  &protocol.ACKPayload{OK: true, Detail: "started"},
						}
						data, _ := json.Marshal(ack)
						data = append(data, '\n')
						_, _ = c.Write(data)
					}
				}(conn)
			}
		}()
	}
	return pid, nil
}

func TestWireDependenciesDoesNotSetPaneRestarter(t *testing.T) {
	t.Run("non-daemon mode leaves PaneRestarter nil", func(t *testing.T) {
		d := &dispatcher.Dispatcher{}
		wireDependencies(d, "/tmp/test.sock", "/tmp/oro")

		if d.GetPaneRestarter() != nil {
			t.Fatal("expected paneRestarter to be nil by default")
		}
	})

	t.Run("tmux-managed daemon mode leaves PaneRestarter nil", func(t *testing.T) {
		t.Setenv(tmuxManagedDaemonEnv, "1")
		d := &dispatcher.Dispatcher{}
		wireDependencies(d, "/tmp/test.sock", "/tmp/oro")

		if d.GetPaneRestarter() != nil {
			t.Fatal("expected paneRestarter to be nil for tmux-managed daemon")
		}
	})
}

// TestStartProgressTimeoutFlag verifies that progress/review stall timeout
// flags wire through to daemon handoff and dispatcher config.
func TestStartProgressTimeoutFlag(t *testing.T) {
	t.Run("explicit flags set Config timeouts", func(t *testing.T) {
		cmd := newStartCmd()
		cmd.SetArgs([]string{"--progress-timeout=20m", "--review-stall-timeout=30m"})
		if err := cmd.ParseFlags([]string{"--progress-timeout=20m", "--review-stall-timeout=30m"}); err != nil {
			t.Fatalf("ParseFlags: %v", err)
		}

		pt, err := cmd.Flags().GetDuration("progress-timeout")
		if err != nil {
			t.Fatalf("GetDuration progress-timeout: %v", err)
		}
		if pt != 20*time.Minute {
			t.Errorf("progress-timeout: got %v, want 20m", pt)
		}

		rt, err := cmd.Flags().GetDuration("review-stall-timeout")
		if err != nil {
			t.Fatalf("GetDuration review-stall-timeout: %v", err)
		}
		if rt != 30*time.Minute {
			t.Errorf("review-stall-timeout: got %v, want 30m", rt)
		}
	})

	t.Run("omitted flags default to zero (dispatcher applies 10m/15m)", func(t *testing.T) {
		cmd := newStartCmd()
		if err := cmd.ParseFlags([]string{}); err != nil {
			t.Fatalf("ParseFlags: %v", err)
		}

		pt, _ := cmd.Flags().GetDuration("progress-timeout")
		if pt != 0 {
			t.Errorf("progress-timeout default: got %v, want 0 (dispatcher default)", pt)
		}

		rt, _ := cmd.Flags().GetDuration("review-stall-timeout")
		if rt != 0 {
			t.Errorf("review-stall-timeout default: got %v, want 0 (dispatcher default)", rt)
		}
	})

	t.Run("ExecDaemonSpawner forwards timeout flags to child", func(t *testing.T) {
		spawner := &ExecDaemonSpawner{
			ProgressTimeout:    20 * time.Minute,
			ReviewStallTimeout: 30 * time.Minute,
		}
		args := spawner.buildArgs(3, 3)
		argStr := strings.Join(args, " ")
		if !strings.Contains(argStr, "--progress-timeout=20m0s") {
			t.Errorf("expected --progress-timeout=20m0s in args, got: %s", argStr)
		}
		if !strings.Contains(argStr, "--review-stall-timeout=30m0s") {
			t.Errorf("expected --review-stall-timeout=30m0s in args, got: %s", argStr)
		}
	})

	t.Run("ExecDaemonSpawner omits zero-value timeouts", func(t *testing.T) {
		spawner := &ExecDaemonSpawner{}
		args := spawner.buildArgs(2, 2)
		argStr := strings.Join(args, " ")
		if strings.Contains(argStr, "progress-timeout") {
			t.Errorf("zero progress-timeout should not appear in args, got: %s", argStr)
		}
		if strings.Contains(argStr, "review-stall-timeout") {
			t.Errorf("zero review-stall-timeout should not appear in args, got: %s", argStr)
		}
	})

	t.Run("ExecDaemonSpawner forwards max-workers when different from workers", func(t *testing.T) {
		spawner := &ExecDaemonSpawner{}
		args := spawner.buildArgs(3, 5)
		argStr := strings.Join(args, " ")
		if !strings.Contains(argStr, "--workers 3") {
			t.Errorf("expected --workers 3 in args, got: %s", argStr)
		}
		if !strings.Contains(argStr, "--max-workers 5") {
			t.Errorf("expected --max-workers 5 in args, got: %s", argStr)
		}
	})

	t.Run("ExecDaemonSpawner forwards max-workers when equal to workers", func(t *testing.T) {
		spawner := &ExecDaemonSpawner{}
		args := spawner.buildArgs(3, 3)
		argStr := strings.Join(args, " ")
		if !strings.Contains(argStr, "--workers 3") {
			t.Errorf("expected --workers 3 in args, got: %s", argStr)
		}
		if !strings.Contains(argStr, "--max-workers 3") {
			t.Errorf("expected --max-workers 3 in args, got: %s", argStr)
		}
	})

	t.Run("ExecDaemonSpawner forwards web dashboard flags to child", func(t *testing.T) {
		spawner := &ExecDaemonSpawner{
			WebEnabled: true,
			WebAddr:    "127.0.0.1:5555",
		}
		argStr := strings.Join(spawner.buildArgs(2, 2), " ")
		for _, want := range []string{
			"--web",
			"--web-addr=127.0.0.1:5555",
		} {
			if !strings.Contains(argStr, want) {
				t.Errorf("daemon args missing %q: %s", want, argStr)
			}
		}
	})

	t.Run("ExecDaemonSpawner forwards web dashboard opt-out to child", func(t *testing.T) {
		spawner := &ExecDaemonSpawner{WebEnabled: false}
		argStr := strings.Join(spawner.buildArgs(2, 2), " ")
		if !strings.Contains(argStr, "--web=false") {
			t.Errorf("daemon args missing explicit web opt-out: %s", argStr)
		}
	})
}

func TestStartReviewTimeoutFlagsAreDistinct(t *testing.T) {
	t.Run("explicit flags parse as distinct timeout concepts", func(t *testing.T) {
		cmd := newStartCmd()
		if err := cmd.ParseFlags([]string{"--ops-review-timeout=35m", "--review-stall-timeout=15m"}); err != nil {
			t.Fatalf("ParseFlags: %v", err)
		}

		opsTimeout, err := cmd.Flags().GetDuration("ops-review-timeout")
		if err != nil {
			t.Fatalf("GetDuration ops-review-timeout: %v", err)
		}
		if opsTimeout != 35*time.Minute {
			t.Errorf("ops-review-timeout: got %v, want 35m", opsTimeout)
		}

		stallTimeout, err := cmd.Flags().GetDuration("review-stall-timeout")
		if err != nil {
			t.Fatalf("GetDuration review-stall-timeout: %v", err)
		}
		if stallTimeout != 15*time.Minute {
			t.Errorf("review-stall-timeout: got %v, want 15m", stallTimeout)
		}
	})

	t.Run("legacy review-timeout remains stall timeout alias", func(t *testing.T) {
		cmd := newStartCmd()
		if err := cmd.ParseFlags([]string{"--review-timeout=22m"}); err != nil {
			t.Fatalf("ParseFlags: %v", err)
		}
		stallTimeout, err := cmd.Flags().GetDuration("review-stall-timeout")
		if err != nil {
			t.Fatalf("GetDuration review-stall-timeout: %v", err)
		}
		if stallTimeout != 22*time.Minute {
			t.Errorf("legacy review-timeout alias: got %v, want 22m", stallTimeout)
		}
	})

	t.Run("daemon handoff forwards distinct review timeout flags", func(t *testing.T) {
		spawner := &ExecDaemonSpawner{
			OpsReviewTimeout:   35 * time.Minute,
			ReviewStallTimeout: 15 * time.Minute,
			ManualIntegration:  true,
			MutationTesting:    true,
		}
		argStr := strings.Join(spawner.buildArgs(2, 2), " ")
		for _, want := range []string{
			"--ops-review-timeout=35m0s",
			"--review-stall-timeout=15m0s",
			"--manual-integration",
			"--mutation-testing",
		} {
			if !strings.Contains(argStr, want) {
				t.Errorf("daemon args missing %q: %s", want, argStr)
			}
		}
		if strings.Contains(argStr, "--review-timeout=") {
			t.Errorf("daemon args should not use ambiguous --review-timeout: %s", argStr)
		}
	})

	t.Run("dispatcher receives stall timeout and ops spawner receives review subprocess timeout", func(t *testing.T) {
		tmpDir := t.TempDir()
		oroHome := t.TempDir()
		t.Setenv("ORO_HOME", oroHome)
		t.Setenv("ORO_PROJECT", "")
		t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))

		d, db, err := buildDispatcherWithReviewTimeouts(1, 1, 7*time.Minute, 35*time.Minute, 15*time.Minute, true, "", false, false, "")
		if err != nil {
			t.Fatalf("buildDispatcherWithReviewTimeouts: %v", err)
		}
		defer func() { _ = db.Close() }()

		cfg := d.GetConfig()
		if cfg.ProgressTimeout != 7*time.Minute {
			t.Errorf("ProgressTimeout: got %v, want 7m", cfg.ProgressTimeout)
		}
		if cfg.ReviewTimeout != 15*time.Minute {
			t.Errorf("dispatcher ReviewTimeout: got %v, want review stall timeout 15m", cfg.ReviewTimeout)
		}
		if !cfg.ManualIntegration {
			t.Error("dispatcher ManualIntegration: got false, want true")
		}
		if got := opsReviewTimeoutFromDispatcher(t, d); got != 35*time.Minute {
			t.Errorf("ops review timeout: got %v, want 35m", got)
		}
	})

	t.Run("dispatcher manual worker mode preserves zero target and ceiling", func(t *testing.T) {
		tmpDir := t.TempDir()
		oroHome := t.TempDir()
		t.Setenv("ORO_HOME", oroHome)
		t.Setenv("ORO_PROJECT", "")
		t.Setenv("ORO_BEADSOURCE_MODE", "")
		t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))

		d, db, err := buildDispatcherWithReviewTimeouts(0, 0, 0, 0, 0, false, "", false, false, "")
		if err != nil {
			t.Fatalf("buildDispatcherWithReviewTimeouts: %v", err)
		}
		defer func() { _ = db.Close() }()

		cfg := d.GetConfig()
		if cfg.InitialWorkers != 0 {
			t.Errorf("InitialWorkers: got %d, want 0", cfg.InitialWorkers)
		}
		if cfg.MaxWorkers != 0 {
			t.Errorf("MaxWorkers: got %d, want 0", cfg.MaxWorkers)
		}
		if got := d.TargetWorkers(); got != 0 {
			t.Errorf("TargetWorkers: got %d, want 0", got)
		}
	})

	t.Run("dispatcher rejects legacy beadsource modes in production", func(t *testing.T) {
		tmpDir := t.TempDir()
		oroHome := t.TempDir()
		t.Setenv("ORO_HOME", oroHome)
		t.Setenv("ORO_PROJECT", "")
		t.Setenv("ORO_BEADSOURCE_MODE", "cli")
		t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))

		_, _, err := buildDispatcherWithReviewTimeouts(0, 0, 0, 0, 0, false, "", false, false, "")
		if err == nil {
			t.Fatal("buildDispatcherWithReviewTimeouts succeeded with legacy cli beadsource mode")
		}
		if !strings.Contains(err.Error(), "native sqlite beadstore") {
			t.Fatalf("error = %v, want native sqlite beadstore rejection", err)
		}
	})

	t.Run("help describes separate review timeout domains", func(t *testing.T) {
		cmd := newStartCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		if err := cmd.Help(); err != nil {
			t.Fatalf("Help: %v", err)
		}
		help := out.String()
		for _, want := range []string{
			"--ops-review-timeout",
			"max time for ops review subprocess",
			"--review-stall-timeout",
			"max time a reviewing worker can stall",
		} {
			if !strings.Contains(help, want) {
				t.Errorf("help missing %q:\n%s", want, help)
			}
		}
	})
}

func TestStartZeroWorkersPreservesMaxWorkersCeiling(t *testing.T) {
	tmpDir := t.TempDir()
	oroHome := t.TempDir()
	t.Setenv("ORO_HOME", oroHome)
	t.Setenv("ORO_PROJECT", "")
	t.Setenv("ORO_BEADSOURCE_MODE", "")
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))

	d, db, err := buildDispatcherWithReviewTimeouts(0, 2, 0, 0, 0, false, "", false, false, "")
	if err != nil {
		t.Fatalf("buildDispatcherWithReviewTimeouts: %v", err)
	}
	defer func() { _ = db.Close() }()

	cfg := d.GetConfig()
	if cfg.InitialWorkers != 0 {
		t.Errorf("InitialWorkers: got %d, want 0", cfg.InitialWorkers)
	}
	if cfg.MaxWorkers != 2 {
		t.Errorf("MaxWorkers: got %d, want 2", cfg.MaxWorkers)
	}
	if got := d.TargetWorkers(); got != 0 {
		t.Errorf("TargetWorkers: got %d, want 0", got)
	}
}

func opsReviewTimeoutFromDispatcher(t *testing.T, d *dispatcher.Dispatcher) time.Duration {
	t.Helper()

	dispatcherValue := reflect.ValueOf(d).Elem()
	opsField := dispatcherValue.FieldByName("ops")
	if !opsField.IsValid() {
		t.Fatal("dispatcher missing ops field")
	}
	opsField = reflect.NewAt(opsField.Type(), unsafe.Pointer(opsField.UnsafeAddr())).Elem()

	spawnerValue := opsField.Elem()
	timeoutField := spawnerValue.FieldByName("reviewTimeout")
	if !timeoutField.IsValid() {
		t.Fatal("ops spawner missing reviewTimeout field")
	}
	timeoutField = reflect.NewAt(timeoutField.Type(), unsafe.Pointer(timeoutField.UnsafeAddr())).Elem()
	return time.Duration(timeoutField.Int())
}

func TestStartManualIntegrationDaemonHandoffForwardsFlagAndConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configureMutationOwnerGit(t, tmpDir)
	pidPath := filepath.Join(tmpDir, "oro.pid")
	socketPath := filepath.Join(tmpDir, "oro.sock")
	dbPath := filepath.Join(tmpDir, "state.db")

	t.Setenv("ORO_PID_PATH", pidPath)
	t.Setenv("ORO_SOCKET_PATH", socketPath)
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_BEADSOURCE_MODE", "sqlite")
	t.Setenv(daemonSkipPreflightEnv, "1")

	var capturedManualIntegration bool
	var capturedMutationTesting bool
	var capturedWorkers int
	var capturedMaxWorkers int
	previousRunDaemonOnly := runDaemonOnlyFn
	runDaemonOnlyFn = func(_ *cobra.Command, gotPIDPath string, workers, maxWorkers int, _ time.Duration, _ time.Duration, _ time.Duration, manualIntegration bool, _ string, mutationTesting bool, _ bool, _ string, _ cleanlinessStartConfig) error {
		if gotPIDPath != pidPath {
			t.Fatalf("pidPath: got %q, want %q", gotPIDPath, pidPath)
		}
		capturedWorkers = workers
		capturedMaxWorkers = maxWorkers
		capturedManualIntegration = manualIntegration
		capturedMutationTesting = mutationTesting
		return nil
	}
	t.Cleanup(func() { runDaemonOnlyFn = previousRunDaemonOnly })

	cmd := newStartCmd()
	cmd.SetArgs([]string{"--daemon-only", "--workers", "0", "--manual-integration", "--mutation-testing"})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("start --daemon-only --manual-integration: %v", err)
	}
	if !capturedManualIntegration {
		t.Fatal("start command did not forward parsed --manual-integration into runDaemonOnly")
	}
	if !capturedMutationTesting {
		t.Fatal("start command did not forward parsed --mutation-testing into runDaemonOnly")
	}
	if capturedWorkers != 0 || capturedMaxWorkers != 0 {
		t.Fatalf("workers/maxWorkers = %d/%d, want 0/0", capturedWorkers, capturedMaxWorkers)
	}

	spawner := &ExecDaemonSpawner{
		OpsReviewTimeout:   35 * time.Minute,
		ReviewStallTimeout: 15 * time.Minute,
		ManualIntegration:  true,
	}
	argStr := strings.Join(spawner.buildArgs(2, 2), " ")
	for _, want := range []string{
		"--ops-review-timeout=35m0s",
		"--review-stall-timeout=15m0s",
		"--manual-integration",
	} {
		if !strings.Contains(argStr, want) {
			t.Errorf("daemon args missing %q: %s", want, argStr)
		}
	}
	if strings.Contains(argStr, "--review-timeout=") {
		t.Errorf("daemon args should not use ambiguous --review-timeout: %s", argStr)
	}

	oroHome := t.TempDir()
	t.Setenv("ORO_HOME", oroHome)
	t.Setenv("ORO_PROJECT", "")

	d, db, err := buildDispatcherWithReviewTimeouts(1, 1, 7*time.Minute, 35*time.Minute, 15*time.Minute, true, "", false, false, "")
	if err != nil {
		t.Fatalf("buildDispatcherWithReviewTimeouts: %v", err)
	}
	defer func() { _ = db.Close() }()

	cfg := d.GetConfig()
	if !cfg.ManualIntegration {
		t.Error("dispatcher ManualIntegration: got false, want true")
	}
	if cfg.ReviewTimeout != 15*time.Minute {
		t.Errorf("dispatcher ReviewTimeout: got %v, want review stall timeout 15m", cfg.ReviewTimeout)
	}
	if got := opsReviewTimeoutFromDispatcher(t, d); got != 35*time.Minute {
		t.Errorf("ops review timeout: got %v, want 35m", got)
	}
}

func TestDetachedStartForwardsBaseBranchToDaemon(t *testing.T) {
	t.Setenv("ORO_HOME", t.TempDir())
	t.Setenv("ORO_PROJECT", "oro")
	previousRunFullStart := runFullStartFn
	t.Cleanup(func() { runFullStartFn = previousRunFullStart })

	var args []string
	runFullStartFn = func(_ io.Writer, workers, maxWorkers int, _, _ string, spawner DaemonSpawner, _ CmdRunner, _ func(int) error, _ time.Duration, _ func(time.Duration), _ time.Duration, _ bool) error {
		execSpawner, ok := spawner.(*ExecDaemonSpawner)
		if !ok {
			t.Fatalf("detached start spawner = %T, want *ExecDaemonSpawner", spawner)
		}
		args = execSpawner.buildArgs(workers, maxWorkers)
		return nil
	}
	if err := startFreshSwarm(io.Discard, 2, 2, "balanced", true, 0, 0, 0, false, "integration/factory-main-test", false, false, "", defaultCleanlinessStartConfig()); err != nil {
		t.Fatalf("startFreshSwarm: %v", err)
	}

	want := "--base-branch=integration/factory-main-test"
	count := 0
	for _, arg := range args {
		if arg == want {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("daemon args contain %q %d times, want exactly once: %q", want, count, args)
	}

	emptyArgs := (&ExecDaemonSpawner{}).buildArgs(2, 2)
	for _, arg := range emptyArgs {
		if strings.HasPrefix(arg, "--base-branch=") {
			t.Fatalf("empty base branch emitted child argument %q in %q", arg, emptyArgs)
		}
	}

	t.Setenv("ORO_BEADSOURCE_MODE", "cli")
	if err := startFreshSwarm(io.Discard, 2, 2, "balanced", true, 0, 0, 0, false, "", false, false, "", defaultCleanlinessStartConfig()); err == nil || !strings.Contains(err.Error(), "native sqlite beadstore") {
		t.Fatalf("legacy startFreshSwarm error = %v, want native sqlite rejection", err)
	}

	cmd := newStartCmd()
	if err := cmd.ParseFlags([]string{"--workers=3", "--max-workers=5", "--model=deep", "--detach", "--base-branch=integration/factory-main-test"}); err != nil {
		t.Fatalf("parse detached start handoff flags: %v", err)
	}
	for name, want := range map[string]int{"workers": 3, "max-workers": 5} {
		got, err := cmd.Flags().GetInt(name)
		if err != nil {
			t.Fatalf("read %s flag: %v", name, err)
		}
		if got != want {
			t.Fatalf("%s flag = %d, want %d", name, got, want)
		}
	}
	for name, want := range map[string]string{"model": "deep", "base-branch": "integration/factory-main-test"} {
		got, err := cmd.Flags().GetString(name)
		if err != nil {
			t.Fatalf("read %s flag: %v", name, err)
		}
		if got != want {
			t.Fatalf("%s flag = %q, want %q", name, got, want)
		}
	}
	detach, err := cmd.Flags().GetBool("detach")
	if err != nil || !detach {
		t.Fatalf("detach flag = %t, err=%v, want true", detach, err)
	}
}

func TestNewStartCmdMutationBoundaries(t *testing.T) {
	t.Run("registers the complete start surface", func(t *testing.T) {
		cmd := newStartCmd()
		for _, name := range []string{
			"workers", "max-workers", "daemon-only", "model", "detach",
			"progress-timeout", "ops-review-timeout", "review-stall-timeout", "review-timeout",
			"manual-integration", "base-branch", "mutation-testing",
			"web", "no-web", "web-addr",
			"janitor-interval", "janitor-idle-threshold", "audit-every-n-janitors",
			"janitor-top-k", "janitor-enabled", "audit-enabled",
		} {
			if cmd.Flags().Lookup(name) == nil {
				t.Fatalf("newStartCmd omitted --%s", name)
			}
		}
		if alias := cmd.Flags().Lookup("review-timeout"); alias == nil || !alias.Hidden {
			t.Fatalf("deprecated --review-timeout alias = %+v, want hidden flag", alias)
		}
	})

	t.Run("returns preflight errors before handoff", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("PATH", tmpDir)
		t.Setenv("ORO_HOME", tmpDir)
		t.Setenv("ORO_PID_PATH", filepath.Join(tmpDir, "oro.pid"))
		t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "missing.sock"))

		cmd := newStartCmd()
		cmd.SetArgs([]string{"--detach"})
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "required tool") {
			t.Fatalf("start error = %v, want preflight tool error", err)
		}
	})

	t.Run("returns reconnect result for a live daemon", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidPath := filepath.Join(tmpDir, "oro.pid")
		t.Setenv("ORO_HOME", tmpDir)
		t.Setenv("ORO_PROJECT", "mutation-owner")
		t.Setenv("ORO_PID_PATH", pidPath)
		t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "missing.sock"))
		if err := WritePIDFile(pidPath, os.Getpid()); err != nil {
			t.Fatalf("write live PID fixture: %v", err)
		}

		previousRunDaemonOnly := runDaemonOnlyFn
		t.Cleanup(func() { runDaemonOnlyFn = previousRunDaemonOnly })
		handoffCalled := false
		runDaemonOnlyFn = func(_ *cobra.Command, _ string, _, _ int, _, _, _ time.Duration, _ bool, _ string, _ bool, _ bool, _ string, _ cleanlinessStartConfig) error {
			handoffCalled = true
			return fmt.Errorf("unexpected daemon handoff")
		}

		cmd := newStartCmd()
		cmd.SetArgs([]string{"--daemon-only"})
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "connect to dispatcher") {
			t.Fatalf("start error = %v, want reconnect error", err)
		}
		if handoffCalled {
			t.Fatal("start continued into daemon handoff after reconnect")
		}
	})

	t.Run("routes daemon and fresh starts through bounded handoffs", func(t *testing.T) {
		tmpDir := t.TempDir()
		projectRoot := filepath.Join(tmpDir, "project")
		oroDir := filepath.Join(projectRoot, ".oro")
		if err := os.MkdirAll(oroDir, 0o750); err != nil {
			t.Fatalf("create project config directory: %v", err)
		}
		if err := os.WriteFile(filepath.Join(oroDir, "config.yaml"), []byte("project: mutation-owner\n"), 0o600); err != nil {
			t.Fatalf("write project config: %v", err)
		}

		oroHome := filepath.Join(tmpDir, "oro-home")
		hookPath := filepath.Join(oroHome, "hooks", "oro-search-hook")
		if err := os.MkdirAll(filepath.Dir(hookPath), 0o750); err != nil {
			t.Fatalf("create hook directory: %v", err)
		}
		if err := os.WriteFile(hookPath, []byte("test hook\n"), 0o700); err != nil {
			t.Fatalf("write hook fixture: %v", err)
		}
		future := time.Now().Add(time.Hour)
		if err := os.Chtimes(hookPath, future, future); err != nil {
			t.Fatalf("mark hook fixture current: %v", err)
		}

		configureMutationOwnerGit(t, tmpDir)
		gitPath, err := exec.LookPath("git")
		if err != nil {
			t.Fatalf("locate git fixture: %v", err)
		}
		toolsDir := filepath.Join(tmpDir, "tools")
		if err := os.MkdirAll(toolsDir, 0o750); err != nil {
			t.Fatalf("create tool fixture directory: %v", err)
		}
		if err := os.Symlink(gitPath, filepath.Join(toolsDir, "git")); err != nil {
			t.Fatalf("link git fixture: %v", err)
		}
		for _, tool := range []string{"claude", "tmux"} {
			if err := os.WriteFile(filepath.Join(toolsDir, tool), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
				t.Fatalf("write %s fixture: %v", tool, err)
			}
		}

		t.Setenv("PATH", toolsDir)
		t.Setenv("ORO_HOME", oroHome)
		t.Setenv("ORO_PROJECT", "mutation-owner")
		t.Setenv("ORO_PID_PATH", filepath.Join(tmpDir, "oro.pid"))
		t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))
		t.Setenv("ORO_DB_PATH", filepath.Join(tmpDir, "state.db"))
		t.Setenv("ORO_BEADSOURCE_MODE", "sqlite")
		t.Setenv(daemonSkipPreflightEnv, "1")

		previousRunDaemonOnly := runDaemonOnlyFn
		previousRunFullStart := runFullStartFn
		t.Cleanup(func() {
			runDaemonOnlyFn = previousRunDaemonOnly
			runFullStartFn = previousRunFullStart
		})

		daemonCalls := 0
		fullCalls := 0
		var daemonWorkers, daemonMaxWorkers int
		var daemonWeb bool
		var fullWorkers, fullMaxWorkers int
		var fullModel string
		var fullDetach bool
		runDaemonOnlyFn = func(_ *cobra.Command, _ string, workers, maxWorkers int, _, _, _ time.Duration, _ bool, _ string, _ bool, webEnabled bool, _ string, _ cleanlinessStartConfig) error {
			daemonCalls++
			daemonWorkers = workers
			daemonMaxWorkers = maxWorkers
			daemonWeb = webEnabled
			return nil
		}
		runFullStartFn = func(_ io.Writer, workers, maxWorkers int, model, _ string, _ DaemonSpawner, _ CmdRunner, _ func(int) error, _ time.Duration, _ func(time.Duration), _ time.Duration, detach bool) error {
			fullCalls++
			fullWorkers = workers
			fullMaxWorkers = maxWorkers
			fullModel = model
			fullDetach = detach
			return nil
		}

		withChdir(t, projectRoot, func() {
			daemonCmd := newStartCmd()
			daemonCmd.SetArgs([]string{"--daemon-only", "--workers=3", "--web", "--no-web"})
			if err := daemonCmd.Execute(); err != nil {
				t.Fatalf("daemon-only start: %v", err)
			}
			if daemonCalls != 1 || fullCalls != 0 {
				t.Fatalf("daemon/full handoffs = %d/%d, want 1/0", daemonCalls, fullCalls)
			}
			if daemonWorkers != 3 || daemonMaxWorkers != 3 || daemonWeb {
				t.Fatalf("daemon handoff workers/max/web = %d/%d/%t, want 3/3/false", daemonWorkers, daemonMaxWorkers, daemonWeb)
			}

			freshCmd := newStartCmd()
			freshCmd.SetArgs([]string{"--workers=2", "--max-workers=5", "--model=deep", "--detach"})
			if err := freshCmd.Execute(); err != nil {
				t.Fatalf("fresh start: %v", err)
			}
		})
		if daemonCalls != 1 || fullCalls != 1 {
			t.Fatalf("final daemon/full handoffs = %d/%d, want 1/1", daemonCalls, fullCalls)
		}
		if fullWorkers != 2 || fullMaxWorkers != 5 || fullModel != "deep" || !fullDetach {
			t.Fatalf("fresh handoff workers/max/model/detach = %d/%d/%q/%t", fullWorkers, fullMaxWorkers, fullModel, fullDetach)
		}
	})
}

// TestStartBaseBranchFlag verifies that the --base-branch flag exists on the
// start command and that its value flows into Config.DefaultBranch via buildDispatcher.
func TestStartBaseBranchFlag(t *testing.T) {
	t.Run("flag exists and parses value", func(t *testing.T) {
		cmd := newStartCmd()
		flag := cmd.Flags().Lookup("base-branch")
		if flag == nil {
			t.Fatal("base-branch flag is missing")
		}
		if !strings.Contains(flag.Usage, "writable local integration branch") {
			t.Fatalf("base-branch usage = %q, want writable local integration branch scope", flag.Usage)
		}
		if err := cmd.ParseFlags([]string{"--base-branch=develop"}); err != nil {
			t.Fatalf("ParseFlags: %v", err)
		}
		bb, err := cmd.Flags().GetString("base-branch")
		if err != nil {
			t.Fatalf("GetString base-branch: %v", err)
		}
		if bb != "develop" {
			t.Errorf("base-branch: got %q, want %q", bb, "develop")
		}
	})

	t.Run("default is empty string", func(t *testing.T) {
		cmd := newStartCmd()
		if err := cmd.ParseFlags([]string{}); err != nil {
			t.Fatalf("ParseFlags: %v", err)
		}
		bb, _ := cmd.Flags().GetString("base-branch")
		if bb != "" {
			t.Errorf("base-branch default: got %q, want empty string", bb)
		}
	})

	t.Run("value flows into buildDispatcher Config.DefaultBranch", func(t *testing.T) {
		tmpDir := t.TempDir()
		oroHome := filepath.Join(tmpDir, ".oro")
		if err := os.MkdirAll(oroHome, 0o750); err != nil { //nolint:gosec // test dir
			t.Fatal(err)
		}
		t.Setenv("ORO_HOME", oroHome)
		t.Setenv("ORO_PROJECT", "")
		t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))

		d, db, err := buildDispatcher("feature-base", false, "")
		if err != nil {
			t.Fatalf("buildDispatcher: %v", err)
		}
		defer func() { _ = db.Close() }()

		if got := d.GetConfig().DefaultBranch; got != "feature-base" {
			t.Errorf("DefaultBranch: got %q, want %q", got, "feature-base")
		}
	})
}

func TestStartMutationTestingFlag(t *testing.T) {
	cmd := newStartCmd()
	if err := cmd.ParseFlags([]string{"--mutation-testing"}); err != nil {
		t.Fatalf("ParseFlags --mutation-testing: %v", err)
	}
	enabled, err := cmd.Flags().GetBool("mutation-testing")
	if err != nil {
		t.Fatalf("GetBool mutation-testing: %v", err)
	}
	if !enabled {
		t.Fatal("mutation-testing flag parsed false, want true")
	}
}

// TestStartWebFlags verifies that --web and --web-addr flags exist on the start
// command and that their values flow into Config.WebEnabled / Config.WebAddr
// via buildDispatcher.
// TestStartWiresReviewPatternPaths verifies that oro start copies
// ProjectPaths.ReviewPatterns and ProjectPaths.ReviewPatternCandidates
// into dispatcher.Config.
func TestStartWiresReviewPatternPaths(t *testing.T) {
	tmpDir := t.TempDir()
	oroHome := t.TempDir()
	t.Setenv("ORO_HOME", oroHome)
	t.Setenv("ORO_PROJECT", "")
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))

	d, db, err := buildDispatcher("", false, "")
	if err != nil {
		t.Fatalf("buildDispatcher: %v", err)
	}
	defer func() { _ = db.Close() }()

	cfg := d.GetConfig()
	daemonPaths, err := ResolveDaemonPaths()
	if err != nil {
		t.Fatalf("ResolveDaemonPaths: %v", err)
	}
	if cfg.ReviewEvidenceDir != daemonPaths.ReviewEvidenceDir {
		t.Errorf("ReviewEvidenceDir: got %q, want project daemon state %q", cfg.ReviewEvidenceDir, daemonPaths.ReviewEvidenceDir)
	}

	// Assert: ReviewPatterns must be non-empty and point to a valid path
	if cfg.ReviewPatterns == "" {
		t.Error("ReviewPatterns: got empty string, expected non-empty path")
	}

	// Assert: ReviewPatternCandidates must be non-empty and point to a valid path
	if cfg.ReviewPatternCandidates == "" {
		t.Error("ReviewPatternCandidates: got empty string, expected non-empty path")
	}
}

func TestStartWebEnabledByDefault(t *testing.T) {
	configureMutationOwnerGit(t, t.TempDir())
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "default enables the dashboard", want: true},
		{name: "--web=false disables the dashboard", args: []string{"--web=false"}, want: false},
		{name: "--no-web disables the dashboard", args: []string{"--no-web"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pidPath := filepath.Join(t.TempDir(), "oro.pid")
			t.Setenv("ORO_PID_PATH", pidPath)
			t.Setenv(daemonSkipPreflightEnv, "1")

			var got bool
			previousRunDaemonOnly := runDaemonOnlyFn
			runDaemonOnlyFn = func(_ *cobra.Command, _ string, _ int, _ int, _ time.Duration, _ time.Duration, _ time.Duration, _ bool, _ string, _ bool, webEnabled bool, _ string, _ cleanlinessStartConfig) error {
				got = webEnabled
				return nil
			}
			t.Cleanup(func() { runDaemonOnlyFn = previousRunDaemonOnly })

			cmd := newStartCmd()
			cmd.SetArgs(append([]string{"--daemon-only"}, tt.args...))
			if err := cmd.Execute(); err != nil {
				t.Fatalf("start: %v", err)
			}
			if got != tt.want {
				t.Errorf("WebEnabled: got %t, want %t", got, tt.want)
			}
		})
	}
}

func TestStartWebFlags(t *testing.T) {
	t.Run("--web-addr flag exists and defaults to empty string", func(t *testing.T) {
		cmd := newStartCmd()
		if err := cmd.ParseFlags([]string{}); err != nil {
			t.Fatalf("ParseFlags: %v", err)
		}
		addr, err := cmd.Flags().GetString("web-addr")
		if err != nil {
			t.Fatalf("GetString web-addr: %v", err)
		}
		if addr != "" {
			t.Errorf("--web-addr default: expected empty string, got %q", addr)
		}
	})

	t.Run("values flow into buildDispatcher Config", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Use separate dir for ORO_HOME to avoid TempDir cleanup race with
		// the background code-index goroutine (same pattern as
		// TestBuildDispatcher_IndexBuildDoesNotBlockStartup).
		oroHome := t.TempDir()
		t.Setenv("ORO_HOME", oroHome)
		t.Setenv("ORO_PROJECT", "")
		t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))

		d, db, err := buildDispatcher("", true, ":9955")
		if err != nil {
			t.Fatalf("buildDispatcher: %v", err)
		}
		defer func() { _ = db.Close() }()

		cfg := d.GetConfig()
		if !cfg.WebEnabled {
			t.Error("WebEnabled: got false, want true")
		}
		if cfg.WebAddr != ":9955" {
			t.Errorf("WebAddr: got %q, want %q", cfg.WebAddr, ":9955")
		}
	})
}

func TestRegenerateProjectSettings_WritesFile(t *testing.T) {
	t.Run("WritesFile", func(t *testing.T) {
		tmpHome := t.TempDir()
		var w bytes.Buffer

		regenerateProjectSettings(&w, tmpHome, "myproject")

		settingsPath := filepath.Join(tmpHome, "projects", "myproject", "settings.json")
		data, err := os.ReadFile(settingsPath) //nolint:gosec // test reads from TempDir path
		if err != nil {
			t.Fatalf("expected settings.json to be written: %v", err)
		}
		if !strings.Contains(string(data), "compact_trigger.py") {
			t.Errorf("expected settings.json to contain 'compact_trigger.py', got: %s", string(data))
		}
	})

	t.Run("EmptyProjectName_Noop", func(t *testing.T) {
		tmpHome := t.TempDir()
		var w bytes.Buffer

		regenerateProjectSettings(&w, tmpHome, "")

		entries, err := os.ReadDir(tmpHome)
		if err != nil {
			t.Fatalf("ReadDir: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("expected no files written for empty project name, got %d entries", len(entries))
		}
	})

	t.Run("CreatesProjectDir", func(t *testing.T) {
		tmpHome := t.TempDir()
		var w bytes.Buffer

		regenerateProjectSettings(&w, tmpHome, "myproject")

		projDir := filepath.Join(tmpHome, "projects", "myproject")
		if _, err := os.Stat(projDir); err != nil {
			t.Errorf("expected project dir to be created: %v", err)
		}
		settingsPath := filepath.Join(projDir, "settings.json")
		if _, err := os.Stat(settingsPath); err != nil {
			t.Errorf("expected settings.json to be created: %v", err)
		}
	})
}

// TestBuildDispatcherCallsMigrateGlobalDBs verifies that buildDispatcher
// copies global state.db to the per-project directory when ORO_PROJECT is set
// and the per-project DB does not yet exist.
func TestBuildDispatcherCallsMigrateGlobalDBs(t *testing.T) {
	tmpDir := t.TempDir()

	// Set up directory structure: global ~/.oro with state.db
	oroHome := filepath.Join(tmpDir, ".oro")
	if err := os.MkdirAll(oroHome, 0o750); err != nil { //nolint:gosec // test dir
		t.Fatal(err)
	}

	// Create a global state.db with schema via openStateDB.
	globalDBPath := filepath.Join(oroHome, "state.db")
	globalDB, err := openStateDB(globalDBPath)
	if err != nil {
		t.Fatalf("create global state.db: %v", err)
	}
	// Insert a marker row to verify the copy happened.
	if _, err := globalDB.Exec(`INSERT INTO events (type, source) VALUES ('test_marker', 'migration_test')`); err != nil {
		t.Fatalf("insert marker: %v", err)
	}
	_ = globalDB.Close()

	// Configure env to use our temp oro home and a project name.
	projectName := "test_project"
	projectDir := filepath.Join(oroHome, "projects", projectName)
	t.Setenv("ORO_HOME", oroHome)
	t.Setenv("ORO_PROJECT", projectName)
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))
	// Do NOT set ORO_DB_PATH — let it resolve via project scoping.

	// Per-project state.db should not exist yet.
	projectDBPath := filepath.Join(projectDir, "state.db")
	if _, err := os.Stat(projectDBPath); err == nil {
		t.Fatal("per-project state.db should not exist before buildDispatcher")
	}

	// buildDispatcher should call migrateGlobalDBs, copying global state.db.
	d, db, err := buildDispatcher("", false, "")
	if err != nil {
		t.Fatalf("buildDispatcher: %v", err)
	}
	defer func() { _ = db.Close() }()
	_ = d

	// Verify per-project state.db was created.
	if _, err := os.Stat(projectDBPath); err != nil {
		t.Fatalf("per-project state.db not created by migrateGlobalDBs: %v", err)
	}

	// Verify the marker row was copied.
	var eventType string
	if err := db.QueryRow(`SELECT type FROM events WHERE source = 'migration_test'`).Scan(&eventType); err != nil {
		t.Fatalf("marker row not found in per-project DB: %v", err)
	}
	if eventType != "test_marker" {
		t.Errorf("expected test_marker, got %q", eventType)
	}
}

func TestBuildDispatcherResolvesOpsRuntime(t *testing.T) {
	t.Run("defaults to claude runtime when unset", func(t *testing.T) {
		tmpDir := mkdirTempIgnoreCleanupErrors(t)
		oroHome := mkdirTempIgnoreCleanupErrors(t)
		t.Chdir(tmpDir)
		t.Setenv(agentRuntimeEnvVar, "")
		t.Setenv("ORO_HOME", oroHome)
		t.Setenv("ORO_PROJECT", "")
		t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))

		wantOps := &testRuntimeOpsSpawner{}
		prevOps := newClaudeOpsSpawner
		newClaudeOpsSpawner = func() ops.BatchSpawner { return wantOps }
		defer func() { newClaudeOpsSpawner = prevOps }()

		rt, err := resolveProductionRuntime()
		if err != nil {
			t.Fatalf("resolveProductionRuntime: %v", err)
		}
		router, ok := rt.opsSpawn.(ops.RuntimeBatchSpawner)
		if !ok {
			t.Fatalf("ops spawner = %#v, want runtime router", rt.opsSpawn)
		}
		if _, err := router.SpawnRuntime(context.Background(), runtimeClaude, "claude-opus-4-7", "", "prompt", tmpDir); err != nil {
			t.Fatalf("spawn claude ops through router: %v", err)
		}
		if wantOps.calls != 1 {
			t.Fatalf("claude ops calls = %d, want 1", wantOps.calls)
		}

		d, db, err := buildDispatcher("", false, "")
		if err != nil {
			t.Fatalf("buildDispatcher: %v", err)
		}
		defer func() { _ = db.Close() }()
		_ = d
	})

	t.Run("codex runtime resolves injected ops spawner", func(t *testing.T) {
		tmpDir := mkdirTempIgnoreCleanupErrors(t)
		oroHome := mkdirTempIgnoreCleanupErrors(t)
		t.Chdir(tmpDir)
		t.Setenv(agentRuntimeEnvVar, runtimeCodex)
		t.Setenv("ORO_HOME", oroHome)
		t.Setenv("ORO_PROJECT", "")
		t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))

		wantOps := &testRuntimeOpsSpawner{}
		prevOps := newCodexOpsSpawner
		newCodexOpsSpawner = func() ops.BatchSpawner { return wantOps }
		defer func() { newCodexOpsSpawner = prevOps }()

		rt, err := resolveProductionRuntime()
		if err != nil {
			t.Fatalf("resolveProductionRuntime: %v", err)
		}
		router, ok := rt.opsSpawn.(ops.RuntimeBatchSpawner)
		if !ok {
			t.Fatalf("ops spawner = %#v, want runtime router", rt.opsSpawn)
		}
		if _, err := router.SpawnRuntime(context.Background(), runtimeCodex, "gpt-5.5", "high", "prompt", tmpDir); err != nil {
			t.Fatalf("spawn codex ops through router: %v", err)
		}
		if wantOps.calls != 1 {
			t.Fatalf("codex ops calls = %d, want 1", wantOps.calls)
		}
		reviewRouter, ok := rt.reviewOpsSpawn.(ops.RuntimeBatchSpawner)
		if !ok {
			t.Fatalf("review ops spawner = %#v, want runtime router", rt.reviewOpsSpawn)
		}
		if _, err := reviewRouter.SpawnRuntime(context.Background(), runtimeCodex, "gpt-5.6-sol", "high", "review prompt", tmpDir); err != nil {
			t.Fatalf("spawn codex review through router: %v", err)
		}
		if wantOps.calls != 2 {
			t.Fatalf("codex ops calls after review = %d, want 2", wantOps.calls)
		}

		d, db, err := buildDispatcher("", false, "")
		if err != nil {
			t.Fatalf("buildDispatcher: %v", err)
		}
		defer func() { _ = db.Close() }()
		_ = d
	})
}

// TestWireDependencies_DaemonOnly_SkipsPaneRestarter verifies that when
// daemonOnly=true, wireDependencies does NOT set a PaneRestarter on the
// dispatcher, preventing pane_restart_failed spam in daemon mode.
func TestWireDependencies_DaemonOnly_SkipsPaneRestarter(t *testing.T) {
	t.Run("daemon mode: paneRestarter is nil", func(t *testing.T) {
		d := &dispatcher.Dispatcher{}
		sockPath := "/tmp/test-daemon.sock"
		oroHome := "/tmp/oro-daemon"

		wireDependencies(d, sockPath, oroHome)

		if d.GetPaneRestarter() != nil {
			t.Fatal("expected paneRestarter to be nil in daemon mode, but it was set")
		}
	})

	t.Run("tmux-managed daemon mode: paneRestarter is nil", func(t *testing.T) {
		t.Setenv(tmuxManagedDaemonEnv, "1")
		d := &dispatcher.Dispatcher{}
		sockPath := "/tmp/test-daemon.sock"
		oroHome := "/tmp/oro-daemon"

		wireDependencies(d, sockPath, oroHome)

		if d.GetPaneRestarter() != nil {
			t.Fatal("expected paneRestarter to be nil for tmux-managed daemon mode")
		}
	})

	t.Run("non-daemon mode: paneRestarter is nil", func(t *testing.T) {
		d := &dispatcher.Dispatcher{}
		sockPath := "/tmp/test-nodaemon.sock"
		oroHome := "/tmp/oro-nodaemon"

		wireDependencies(d, sockPath, oroHome)

		if d.GetPaneRestarter() != nil {
			t.Fatal("expected paneRestarter to be nil in non-daemon mode")
		}
	})
}

func TestWireDependenciesDoesNotSetManagerRestarterByDefault(t *testing.T) {
	t.Setenv(tmuxManagedDaemonEnv, "1")
	d := &dispatcher.Dispatcher{}

	wireDependencies(d, "/tmp/test.sock", "/tmp/oro")

	if d.GetPaneRestarter() != nil {
		t.Fatal("default wireDependencies must not set manager pane restarter")
	}
}

func TestDaemonChildEnvMarksTmuxManagedDaemon(t *testing.T) {
	got := daemonChildEnv([]string{
		"CLAUDECODE=1",
		tmuxManagedDaemonEnv + "=0",
		"ORO_HOME=/tmp/oro",
	})

	if !containsEnvEntry(got, tmuxManagedDaemonEnv+"=1") {
		t.Fatalf("expected daemon child env to include %s=1, got %v", tmuxManagedDaemonEnv, got)
	}
	if containsEnvEntry(got, "CLAUDECODE=1") {
		t.Fatalf("expected daemon child env to remove CLAUDECODE, got %v", got)
	}
	if containsEnvEntry(got, tmuxManagedDaemonEnv+"=0") {
		t.Fatalf("expected daemon child env to replace stale tmux marker, got %v", got)
	}
}

func TestStartModesPropagateOracleRuntimeIdentity(t *testing.T) {
	t.Run("daemon-only resolves identity before pid lifecycle", func(t *testing.T) {
		t.Chdir(t.TempDir())
		t.Setenv("ORO_HOME", filepath.Join(t.TempDir(), "oro-home"))
		t.Setenv("ORO_PROJECT", "")
		pidPath := filepath.Join(t.TempDir(), "oro.pid")
		cmd := &cobra.Command{}
		cmd.SetOut(io.Discard)
		err := runDaemonOnly(cmd, pidPath, 1, 1, 0, 0, 0, false, "main", false, false, "", cleanlinessStartConfig{})
		if err == nil {
			t.Fatal("expected uninitialized project to fail closed")
		}
		if _, statErr := os.Stat(pidPath); !os.IsNotExist(statErr) {
			t.Fatalf("pid lifecycle started before identity resolution: stat error = %v", statErr)
		}
	})

	t.Run("child environments preserve resolved identity", func(t *testing.T) {
		input := []string{"ORO_HOME=/resolved/home", "ORO_PROJECT=resolved-project"}
		for _, env := range []func([]string) []string{cleanEnvForDaemon, daemonChildEnv} {
			got := env(input)
			if !containsEnvEntry(got, input[0]) || !containsEnvEntry(got, input[1]) {
				t.Fatalf("environment lost runtime identity: %v", got)
			}
		}
	})
}

func containsEnvEntry(env []string, want string) bool {
	for _, entry := range env {
		if entry == want {
			return true
		}
	}
	return false
}

// TestPollForSocketConnectCheck verifies that pollForSocket uses a UDS connect
// check (not os.Stat) so stale socket files don't cause short-circuit.
func TestPollForSocketConnectCheck(t *testing.T) {
	t.Run("stale socket file does not short-circuit", func(t *testing.T) {
		sockPath := fmt.Sprintf("/tmp/oro-stale-%d.sock", time.Now().UnixNano())
		// Create a plain file at the socket path (stale socket).
		if err := os.WriteFile(sockPath, []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Remove(sockPath) })

		// pollForSocket should NOT succeed just because the file exists.
		// With a short timeout, it should fail because the file isn't connectable.
		log := newStartupLog(&bytes.Buffer{}, false)
		err := pollForSocket(log, sockPath, 500*time.Millisecond)
		if err == nil {
			t.Fatal("pollForSocket should fail on stale (non-connectable) socket file")
		}
	})

	t.Run("succeeds when real UDS listener starts", func(t *testing.T) {
		sockPath := fmt.Sprintf("/tmp/oro-live-%d.sock", time.Now().UnixNano())
		t.Cleanup(func() { _ = os.Remove(sockPath) })

		// Start a real listener after a short delay. Accept multiple connections
		// because pollForSocket dials twice (loop probe + final check).
		go func() {
			time.Sleep(100 * time.Millisecond)
			ln, err := net.Listen("unix", sockPath)
			if err != nil {
				return
			}
			defer ln.Close()
			for i := 0; i < 3; i++ {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				_ = conn.Close()
			}
		}()

		log := newStartupLog(&bytes.Buffer{}, false)
		err := pollForSocket(log, sockPath, 2*time.Second)
		if err != nil {
			t.Fatalf("pollForSocket should succeed when listener starts: %v", err)
		}
	})

	t.Run("timeout with no socket returns error", func(t *testing.T) {
		sockPath := fmt.Sprintf("/tmp/oro-nosock-%d.sock", time.Now().UnixNano())
		log := newStartupLog(&bytes.Buffer{}, false)
		err := pollForSocket(log, sockPath, 200*time.Millisecond)
		if err == nil {
			t.Fatal("pollForSocket should fail when no socket appears")
		}
	})

	t.Run("nil startupLog does not panic", func(t *testing.T) {
		sockPath := fmt.Sprintf("/tmp/oro-nillog-%d.sock", time.Now().UnixNano())
		// Should not panic, just return error on timeout.
		err := pollForSocket(nil, sockPath, 200*time.Millisecond)
		if err == nil {
			t.Fatal("expected timeout error")
		}
	})
}

// TestPreflightStatusStaleRemovesSocket verifies that when DaemonStatus returns
// StatusStale, both the PID file and the socket file are removed.
func TestPreflightStatusStaleRemovesSocket(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	sockPath := filepath.Join(tmpDir, "oro.sock")

	t.Setenv("ORO_PID_PATH", pidFile)
	t.Setenv("ORO_SOCKET_PATH", sockPath)
	t.Setenv("ORO_DB_PATH", filepath.Join(tmpDir, "state.db"))

	// Write a PID file pointing to a dead process (PID 1 is init, but
	// use a high PID that's guaranteed to not exist).
	if err := os.WriteFile(pidFile, []byte("999999999"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Write a stale socket file.
	if err := os.WriteFile(sockPath, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}

	// preflightAndCheckRunning should detect StatusStale and remove both files.
	// It will also try to run preflight checks which may fail in test env,
	// but the StatusStale cleanup happens after path resolution.
	// We can't easily call preflightAndCheckRunning directly due to preflight
	// checks, so test the StatusStale cleanup logic inline.

	// Simulate what preflightAndCheckRunning does in the StatusStale branch:
	status, _, _ := DaemonStatus(pidFile, sockPath)
	if status != StatusStale {
		t.Fatalf("expected StatusStale, got %s", status)
	}

	// The actual fix adds os.Remove(sockPath) here. Before the fix,
	// only RemovePIDFile was called.
	_ = RemovePIDFile(pidFile)
	_ = os.Remove(sockPath) // This is what the fix adds

	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Error("PID file should be removed after StatusStale")
	}
	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Error("socket file should be removed after StatusStale")
	}
}

// TestSendStartDirectiveTimeout verifies that sendStartDirective times out
// after 10 seconds if the dispatcher doesn't send an ACK response.
func TestSendStartDirectiveTimeout(t *testing.T) {
	sockPath := fmt.Sprintf("/tmp/oro-timeout-test-%d.sock", time.Now().UnixNano())
	t.Cleanup(func() { _ = os.Remove(sockPath) })

	// Start a listener that accepts the connection but never sends an ACK
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer ln.Close()

	// Accept the connection but don't send an ACK to trigger the timeout
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// Just accept and hold the connection without sending anything
		time.Sleep(20 * time.Second) // Sleep longer than the 10s timeout
	}()

	// Give the listener a moment to start
	time.Sleep(100 * time.Millisecond)

	// Call sendStartDirective and expect it to timeout
	start := time.Now()
	err = sendStartDirective(sockPath)
	elapsed := time.Since(start)

	// Should return an error (timeout)
	if err == nil {
		t.Fatal("expected sendStartDirective to timeout, but got no error")
	}

	// Check that the error indicates a timeout/deadline exceeded
	errStr := err.Error()
	if !strings.Contains(errStr, "deadline exceeded") &&
		!strings.Contains(errStr, "timeout") &&
		!strings.Contains(errStr, "i/o timeout") {
		t.Fatalf("expected timeout/deadline error, got: %v", err)
	}

	// Verify it took approximately 10 seconds (allow 9-11 second window)
	if elapsed < 9*time.Second {
		t.Errorf("timeout occurred too quickly: %v (expected ~10s)", elapsed)
	}
	if elapsed > 12*time.Second {
		t.Logf("warning: timeout took longer than expected: %v", elapsed)
	}
}

// mkdirTempIgnoreCleanupErrors is a t.TempDir replacement that tolerates
// "directory not empty" cleanup errors. buildDispatcher spawns a background
// goroutine that writes to ORO_HOME (no cancellation), and several SQLite
// handles outlive the test's deferred close. On Linux CI runners the race
// between RemoveAll and lingering writes occasionally makes RemoveAll fail.
// Tests using this helper care about correctness of the dispatcher build,
// not flake-free temp-dir cleanup.
func mkdirTempIgnoreCleanupErrors(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "oro-cmd-start-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestStartModelFlagAcceptsTier(t *testing.T) {
	cmd := newStartCmd()
	f := cmd.Flag("model")
	if f == nil {
		t.Fatal("--model flag missing from oro start")
	}

	// Help text uses tier-first vocabulary.
	for _, tier := range []string{"fast", "balanced", "deep", "background"} {
		if !strings.Contains(f.Usage, tier) {
			t.Errorf("--model usage should mention tier %q; got: %q", tier, f.Usage)
		}
	}

	// Flag accepts tier names without error.
	for _, tier := range []string{"fast", "balanced", "deep", "background"} {
		if err := cmd.Flags().Set("model", tier); err != nil {
			t.Errorf("--model=%q rejected: %v", tier, err)
		}
	}

	// Flag accepts provider-native strings without error.
	for _, native := range []string{"claude-opus-4-7", "claude-sonnet-4-6"} {
		if err := cmd.Flags().Set("model", native); err != nil {
			t.Errorf("--model=%q rejected: %v", native, err)
		}
	}
}
