package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallHookWrapper(t *testing.T) {
	t.Run("no_existing_hook_creates_wrapper", func(t *testing.T) {
		tmpDir := t.TempDir()
		gitDir := filepath.Join(tmpDir, ".git")
		if err := os.MkdirAll(filepath.Join(gitDir, "hooks"), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := installHookWrapper(gitDir, "pre-commit", oroPreCommitCheck); err != nil {
			t.Fatalf("installHookWrapper: %v", err)
		}

		hookPath := filepath.Join(gitDir, "hooks", "pre-commit")
		data, err := os.ReadFile(hookPath)
		if err != nil {
			t.Fatalf("read hook: %v", err)
		}
		content := string(data)

		if !strings.Contains(content, "managed by oro") {
			t.Error("wrapper should contain 'managed by oro' marker")
		}
		if !strings.Contains(content, oroPreCommitCheck) {
			t.Error("wrapper should embed oroPreCommitCheck")
		}

		info, _ := os.Stat(hookPath)
		if info.Mode()&0o111 == 0 {
			t.Error("hook should be executable")
		}
	})

	t.Run("existing_executable_hook_renamed_to_user", func(t *testing.T) {
		tmpDir := t.TempDir()
		gitDir := filepath.Join(tmpDir, ".git")
		hooksDir := filepath.Join(gitDir, "hooks")
		if err := os.MkdirAll(hooksDir, 0o750); err != nil {
			t.Fatal(err)
		}

		hookPath := filepath.Join(hooksDir, "pre-commit")
		if err := os.WriteFile(hookPath, []byte("#!/bin/sh\necho user-hook"), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := installHookWrapper(gitDir, "pre-commit", oroPreCommitCheck); err != nil {
			t.Fatalf("installHookWrapper: %v", err)
		}

		userPath := hookPath + ".user"
		userContent, err := os.ReadFile(userPath)
		if err != nil {
			t.Fatalf("read .user backup: %v", err)
		}
		if !strings.Contains(string(userContent), "echo user-hook") {
			t.Error(".user backup should contain original hook content")
		}

		wrapperContent, err := os.ReadFile(hookPath)
		if err != nil {
			t.Fatalf("read wrapper: %v", err)
		}
		if !strings.Contains(string(wrapperContent), "pre-commit.user") {
			t.Error("wrapper should invoke pre-commit.user")
		}
	})

	t.Run("existing_non_executable_hook_not_renamed", func(t *testing.T) {
		tmpDir := t.TempDir()
		gitDir := filepath.Join(tmpDir, ".git")
		hooksDir := filepath.Join(gitDir, "hooks")
		if err := os.MkdirAll(hooksDir, 0o750); err != nil {
			t.Fatal(err)
		}

		hookPath := filepath.Join(hooksDir, "pre-commit")
		if err := os.WriteFile(hookPath, []byte("not a script"), 0o644); err != nil {
			t.Fatal(err)
		}

		if err := installHookWrapper(gitDir, "pre-commit", oroPreCommitCheck); err != nil {
			t.Fatalf("installHookWrapper: %v", err)
		}

		userPath := hookPath + ".user"
		if _, err := os.Stat(userPath); err == nil {
			t.Error("non-executable hook should not be backed up to .user")
		}

		data, err := os.ReadFile(hookPath)
		if err != nil {
			t.Fatalf("read wrapper: %v", err)
		}
		if !strings.Contains(string(data), "managed by oro") {
			t.Error("wrapper should be installed over non-executable file")
		}
	})

	t.Run("missing_hooks_dir_created", func(t *testing.T) {
		tmpDir := t.TempDir()
		gitDir := filepath.Join(tmpDir, ".git")
		// Create .git but NOT .git/hooks
		if err := os.MkdirAll(gitDir, 0o750); err != nil {
			t.Fatal(err)
		}

		if err := installHookWrapper(gitDir, "pre-commit", oroPreCommitCheck); err != nil {
			t.Fatalf("installHookWrapper: %v", err)
		}

		hookPath := filepath.Join(gitDir, "hooks", "pre-commit")
		if _, err := os.Stat(hookPath); err != nil {
			t.Errorf("hook should exist after auto-creating hooks dir: %v", err)
		}
	})

	t.Run("idempotent_reinstall_no_double_backup", func(t *testing.T) {
		tmpDir := t.TempDir()
		gitDir := filepath.Join(tmpDir, ".git")
		if err := os.MkdirAll(filepath.Join(gitDir, "hooks"), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := installHookWrapper(gitDir, "pre-commit", oroPreCommitCheck); err != nil {
			t.Fatalf("first install: %v", err)
		}
		if err := installHookWrapper(gitDir, "pre-commit", oroPreCommitCheck); err != nil {
			t.Fatalf("second install: %v", err)
		}

		data, err := os.ReadFile(filepath.Join(gitDir, "hooks", "pre-commit"))
		if err != nil {
			t.Fatalf("read hook: %v", err)
		}
		if !strings.Contains(string(data), "managed by oro") {
			t.Error("wrapper should still be installed after idempotent reinstall")
		}

		userPath := filepath.Join(gitDir, "hooks", "pre-commit.user")
		if _, err := os.Stat(userPath); err == nil {
			t.Error("idempotent reinstall must not back up the oro wrapper itself")
		}
	})

	t.Run("core_hooksPath_respected", func(t *testing.T) {
		tmpDir := t.TempDir()
		gitDir := filepath.Join(tmpDir, ".git")
		customHooksDir := filepath.Join(tmpDir, "custom-hooks")
		if err := os.MkdirAll(gitDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(customHooksDir, 0o750); err != nil {
			t.Fatal(err)
		}

		gitConfig := fmt.Sprintf("[core]\n\thooksPath = %s\n", customHooksDir)
		if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(gitConfig), 0o600); err != nil {
			t.Fatal(err)
		}

		if err := installHookWrapper(gitDir, "pre-commit", oroPreCommitCheck); err != nil {
			t.Fatalf("installHookWrapper: %v", err)
		}

		hookPath := filepath.Join(customHooksDir, "pre-commit")
		if _, err := os.Stat(hookPath); err != nil {
			t.Errorf("hook should be in core.hooksPath directory: %v", err)
		}

		// Default hooks dir should be empty.
		defaultPath := filepath.Join(gitDir, "hooks", "pre-commit")
		if _, err := os.Stat(defaultPath); err == nil {
			t.Error("hook should NOT be in default hooks dir when core.hooksPath is set")
		}
	})

	t.Run("uninstall_restores_user_backup", func(t *testing.T) {
		tmpDir := t.TempDir()
		gitDir := filepath.Join(tmpDir, ".git")
		hooksDir := filepath.Join(gitDir, "hooks")
		if err := os.MkdirAll(hooksDir, 0o750); err != nil {
			t.Fatal(err)
		}

		hookPath := filepath.Join(hooksDir, "pre-commit")
		if err := os.WriteFile(hookPath, []byte("#!/bin/sh\necho user-original"), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := installHookWrapper(gitDir, "pre-commit", oroPreCommitCheck); err != nil {
			t.Fatalf("install: %v", err)
		}

		if err := uninstallHookWrapper(gitDir, "pre-commit"); err != nil {
			t.Fatalf("uninstall: %v", err)
		}

		data, err := os.ReadFile(hookPath)
		if err != nil {
			t.Fatalf("read hook after uninstall: %v", err)
		}
		if !strings.Contains(string(data), "echo user-original") {
			t.Error("original hook should be restored after uninstall")
		}

		if _, err := os.Stat(hookPath + ".user"); err == nil {
			t.Error(".user backup should be removed after successful restore")
		}
	})

	t.Run("pre_commit_check_rejects_oro_docs", func(t *testing.T) {
		if !strings.Contains(oroPreCommitCheck, "oro-docs/") {
			t.Error("oroPreCommitCheck should reference oro-docs/")
		}
		if !strings.Contains(oroPreCommitCheck, "exit 1") {
			t.Error("oroPreCommitCheck should exit 1 on violation")
		}
	})

	t.Run("pre_push_check_blocks_agent_and_epic_branches", func(t *testing.T) {
		if !strings.Contains(oroPrePushCheck, "agent/") {
			t.Error("oroPrePushCheck should reference agent/")
		}
		if !strings.Contains(oroPrePushCheck, "epic/") {
			t.Error("oroPrePushCheck should reference epic/")
		}
		if !strings.Contains(oroPrePushCheck, "exit 1") {
			t.Error("oroPrePushCheck should exit 1 on violation")
		}
	})

	t.Run("install_pre_push_hook", func(t *testing.T) {
		tmpDir := t.TempDir()
		gitDir := filepath.Join(tmpDir, ".git")
		if err := os.MkdirAll(filepath.Join(gitDir, "hooks"), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := installHookWrapper(gitDir, "pre-push", oroPrePushCheck); err != nil {
			t.Fatalf("installHookWrapper pre-push: %v", err)
		}

		hookPath := filepath.Join(gitDir, "hooks", "pre-push")
		data, err := os.ReadFile(hookPath)
		if err != nil {
			t.Fatalf("read pre-push hook: %v", err)
		}
		if !strings.Contains(string(data), "managed by oro") {
			t.Error("pre-push wrapper should contain 'managed by oro' marker")
		}
		if !strings.Contains(string(data), "pre-push.user") {
			t.Error("pre-push wrapper should reference pre-push.user")
		}
	})
}

func TestCanonicalHookContent(t *testing.T) {
	t.Run("pre-commit has required design markers", func(t *testing.T) {
		content, ok := canonicalHookContent("pre-commit")
		if !ok {
			t.Fatal("canonicalHookContent should return ok=true for pre-commit")
		}
		for _, marker := range []string{"managed by oro", "Author identity guard", "gofumpt"} {
			if !strings.Contains(content, marker) {
				t.Errorf("pre-commit canonical hook missing marker %q", marker)
			}
		}
	})

	t.Run("pre-push has required design markers", func(t *testing.T) {
		content, ok := canonicalHookContent("pre-push")
		if !ok {
			t.Fatal("canonicalHookContent should return ok=true for pre-push")
		}
		for _, marker := range []string{"managed by oro", "golangci-lint", "quality_gate.sh", "all checks"} {
			if !strings.Contains(content, marker) {
				t.Errorf("pre-push canonical hook missing marker %q", marker)
			}
		}
	})

	t.Run("unknown hook returns ok=false", func(t *testing.T) {
		_, ok := canonicalHookContent("commit-msg")
		if ok {
			t.Error("canonicalHookContent should return ok=false for unknown hook")
		}
	})
}

func TestInstallCanonicalHook(t *testing.T) {
	t.Run("fresh_repo_installs_canonical_content", func(t *testing.T) {
		tmpDir := t.TempDir()
		gitDir := filepath.Join(tmpDir, ".git")
		if err := os.MkdirAll(filepath.Join(gitDir, "hooks"), 0o750); err != nil {
			t.Fatal(err)
		}

		alreadyInstalled, err := installCanonicalHook(gitDir, "pre-commit", false)
		if err != nil {
			t.Fatalf("installCanonicalHook: %v", err)
		}
		if alreadyInstalled {
			t.Error("fresh install should return alreadyInstalled=false")
		}

		hookPath := filepath.Join(gitDir, "hooks", "pre-commit")
		data, err := os.ReadFile(hookPath) //nolint:gosec // test path
		if err != nil {
			t.Fatalf("read hook: %v", err)
		}
		content := string(data)

		for _, marker := range []string{"managed by oro", "Author identity guard", "gofumpt"} {
			if !strings.Contains(content, marker) {
				t.Errorf("installed pre-commit hook missing marker %q", marker)
			}
		}

		info, _ := os.Stat(hookPath)
		if info.Mode()&0o111 == 0 {
			t.Error("installed hook must be executable (0755)")
		}
		if info.Mode().Perm() != 0o755 {
			t.Errorf("hook mode should be 0755, got %v", info.Mode().Perm())
		}
	})

	t.Run("idempotent_returns_already_installed", func(t *testing.T) {
		tmpDir := t.TempDir()
		gitDir := filepath.Join(tmpDir, ".git")
		if err := os.MkdirAll(filepath.Join(gitDir, "hooks"), 0o750); err != nil {
			t.Fatal(err)
		}

		if _, err := installCanonicalHook(gitDir, "pre-commit", false); err != nil {
			t.Fatalf("first install: %v", err)
		}

		alreadyInstalled, err := installCanonicalHook(gitDir, "pre-commit", false)
		if err != nil {
			t.Fatalf("second install: %v", err)
		}
		if !alreadyInstalled {
			t.Error("second install should return alreadyInstalled=true")
		}

		// Verify hook content unchanged.
		hookPath := filepath.Join(gitDir, "hooks", "pre-commit")
		data, err := os.ReadFile(hookPath) //nolint:gosec // test path
		if err != nil {
			t.Fatalf("read hook: %v", err)
		}
		canonical, _ := canonicalHookContent("pre-commit")
		if string(data) != canonical {
			t.Error("idempotent reinstall must not change hook content")
		}
	})

	t.Run("drift_detected_returns_error_without_force", func(t *testing.T) {
		tmpDir := t.TempDir()
		gitDir := filepath.Join(tmpDir, ".git")
		hooksDir := filepath.Join(gitDir, "hooks")
		if err := os.MkdirAll(hooksDir, 0o750); err != nil {
			t.Fatal(err)
		}

		// Simulate bd rewriting the hook with BEADS INTEGRATION content.
		bdHook := "#!/usr/bin/env sh\n# --- BEGIN BEADS INTEGRATION v0.60.0 ---\nexec bd hook pre-commit\n# --- END BEADS INTEGRATION v0.60.0 ---\n"
		hookPath := filepath.Join(hooksDir, "pre-commit")
		if err := os.WriteFile(hookPath, []byte(bdHook), 0o755); err != nil { //nolint:gosec // test
			t.Fatal(err)
		}

		_, err := installCanonicalHook(gitDir, "pre-commit", false)
		if err == nil {
			t.Fatal("expected HookDriftError for drifted hook, got nil")
		}
		var driftErr *HookDriftError
		if !errors.As(err, &driftErr) {
			t.Fatalf("expected *HookDriftError, got %T: %v", err, err)
		}
		if driftErr.HookName != "pre-commit" {
			t.Errorf("HookDriftError.HookName should be 'pre-commit', got %q", driftErr.HookName)
		}
		// Hook content must be unchanged (we refused to overwrite).
		data, _ := os.ReadFile(hookPath) //nolint:gosec // test path
		if string(data) != bdHook {
			t.Error("drifted hook must not be overwritten without --force")
		}
	})

	t.Run("force_overwrites_drifted_hook", func(t *testing.T) {
		tmpDir := t.TempDir()
		gitDir := filepath.Join(tmpDir, ".git")
		hooksDir := filepath.Join(gitDir, "hooks")
		if err := os.MkdirAll(hooksDir, 0o750); err != nil {
			t.Fatal(err)
		}

		bdHook := "#!/usr/bin/env sh\nexec bd hook pre-commit\n"
		hookPath := filepath.Join(hooksDir, "pre-commit")
		if err := os.WriteFile(hookPath, []byte(bdHook), 0o755); err != nil { //nolint:gosec // test
			t.Fatal(err)
		}

		alreadyInstalled, err := installCanonicalHook(gitDir, "pre-commit", true)
		if err != nil {
			t.Fatalf("installCanonicalHook --force: %v", err)
		}
		if alreadyInstalled {
			t.Error("force overwrite should return alreadyInstalled=false")
		}

		data, err := os.ReadFile(hookPath) //nolint:gosec // test path
		if err != nil {
			t.Fatalf("read hook after force: %v", err)
		}
		canonical, _ := canonicalHookContent("pre-commit")
		if string(data) != canonical {
			t.Error("--force should overwrite drifted hook with canonical content")
		}

		info, _ := os.Stat(hookPath)
		if info.Mode().Perm() != 0o755 {
			t.Errorf("forced hook mode should be 0755, got %v", info.Mode().Perm())
		}
	})

	t.Run("unknown_hook_returns_error", func(t *testing.T) {
		tmpDir := t.TempDir()
		gitDir := filepath.Join(tmpDir, ".git")
		if err := os.MkdirAll(filepath.Join(gitDir, "hooks"), 0o750); err != nil {
			t.Fatal(err)
		}
		_, err := installCanonicalHook(gitDir, "commit-msg", false)
		if err == nil {
			t.Error("unknown hook should return an error")
		}
	})

	t.Run("creates_hooks_dir_if_missing", func(t *testing.T) {
		tmpDir := t.TempDir()
		gitDir := filepath.Join(tmpDir, ".git")
		if err := os.MkdirAll(gitDir, 0o750); err != nil {
			t.Fatal(err)
		}
		// No hooks dir.
		if _, err := installCanonicalHook(gitDir, "pre-push", false); err != nil {
			t.Fatalf("installCanonicalHook: %v", err)
		}
		hookPath := filepath.Join(gitDir, "hooks", "pre-push")
		if _, err := os.Stat(hookPath); err != nil {
			t.Errorf("hook should exist after auto-creating hooks dir: %v", err)
		}
	})
}

func TestUninstallCanonicalHook(t *testing.T) {
	t.Run("removes_installed_hook", func(t *testing.T) {
		tmpDir := t.TempDir()
		gitDir := filepath.Join(tmpDir, ".git")
		if err := os.MkdirAll(filepath.Join(gitDir, "hooks"), 0o750); err != nil {
			t.Fatal(err)
		}
		if _, err := installCanonicalHook(gitDir, "pre-commit", false); err != nil {
			t.Fatalf("install: %v", err)
		}
		if err := uninstallCanonicalHook(gitDir, "pre-commit"); err != nil {
			t.Fatalf("uninstall: %v", err)
		}
		hookPath := filepath.Join(gitDir, "hooks", "pre-commit")
		if _, err := os.Stat(hookPath); err == nil {
			t.Error("hook should be removed after uninstall")
		}
	})

	t.Run("no_error_if_hook_missing", func(t *testing.T) {
		tmpDir := t.TempDir()
		gitDir := filepath.Join(tmpDir, ".git")
		if err := os.MkdirAll(filepath.Join(gitDir, "hooks"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := uninstallCanonicalHook(gitDir, "pre-commit"); err != nil {
			t.Fatalf("uninstall of non-existent hook should not error: %v", err)
		}
	})
}
