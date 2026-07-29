package main

import (
	"fmt"
	"os"
	"os/exec"
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

	t.Run("existing_oro_distributed_pre_push_symlink_not_backed_up", func(t *testing.T) {
		tmpDir := t.TempDir()
		gitDir := filepath.Join(tmpDir, ".git")
		hooksDir := filepath.Join(gitDir, "hooks")
		repoHooksDir := filepath.Join(tmpDir, "git", "hooks")
		if err := os.MkdirAll(hooksDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(repoHooksDir, 0o750); err != nil {
			t.Fatal(err)
		}

		repoHookPath := filepath.Join(repoHooksDir, "pre-push")
		repoHookContent := []byte("#!/usr/bin/env sh\n\nset -e\n\n# Run Oro's full quality gate before push. Mutation testing remains disabled\n# unless the quality gate is run separately with --mutation-testing.\nORO_QG_CONTEXT=push scripts/quality_gate.sh\n")
		if err := os.WriteFile(repoHookPath, repoHookContent, 0o755); err != nil { //nolint:gosec // test hook
			t.Fatal(err)
		}

		hookPath := filepath.Join(hooksDir, "pre-push")
		if err := os.Symlink(repoHookPath, hookPath); err != nil {
			t.Fatal(err)
		}

		if err := installHookWrapper(gitDir, "pre-push", oroPrePushCheck); err != nil {
			t.Fatalf("installHookWrapper pre-push: %v", err)
		}

		if _, err := os.Lstat(hookPath + ".user"); err == nil {
			t.Fatal("oro-distributed pre-push hook must not be preserved as pre-push.user")
		}

		info, err := os.Lstat(hookPath)
		if err != nil {
			t.Fatalf("stat installed hook: %v", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			t.Fatal("installed wrapper must replace the symlink instead of writing through it")
		}

		gotRepoHook, err := os.ReadFile(repoHookPath) //nolint:gosec // test hook
		if err != nil {
			t.Fatalf("read repo hook: %v", err)
		}
		if string(gotRepoHook) != string(repoHookContent) {
			t.Fatal("install must not overwrite the repository's distributed hook target")
		}
	})

	t.Run("idempotent_reinstall_removes_oro_distributed_pre_push_user_hook", func(t *testing.T) {
		tmpDir := t.TempDir()
		gitDir := filepath.Join(tmpDir, ".git")
		hooksDir := filepath.Join(gitDir, "hooks")
		repoHooksDir := filepath.Join(tmpDir, "git", "hooks")
		if err := os.MkdirAll(hooksDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(repoHooksDir, 0o750); err != nil {
			t.Fatal(err)
		}

		repoHookPath := filepath.Join(repoHooksDir, "pre-push")
		repoHookContent := []byte("#!/usr/bin/env sh\n\nset -e\n\n# Run Oro's full quality gate before push. Mutation testing remains disabled\n# unless the quality gate is run separately with --mutation-testing.\nORO_QG_CONTEXT=push scripts/quality_gate.sh\n")
		if err := os.WriteFile(repoHookPath, repoHookContent, 0o755); err != nil { //nolint:gosec // test hook
			t.Fatal(err)
		}

		hookPath := filepath.Join(hooksDir, "pre-push")
		wrapper := buildWrapperScript("pre-push", oroPrePushCheck)
		if err := os.WriteFile(hookPath, []byte(wrapper), 0o755); err != nil { //nolint:gosec // test hook
			t.Fatal(err)
		}
		if err := os.Symlink(repoHookPath, hookPath+".user"); err != nil {
			t.Fatal(err)
		}

		if err := installHookWrapper(gitDir, "pre-push", oroPrePushCheck); err != nil {
			t.Fatalf("installHookWrapper pre-push: %v", err)
		}

		if _, err := os.Lstat(hookPath + ".user"); err == nil {
			t.Fatal("reinstall must remove stale oro-distributed pre-push.user")
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
		if !strings.Contains(oroPrePushCheck, "ORO_QG_CONTEXT=push") {
			t.Error("oroPrePushCheck should run quality gate in push context")
		}
		if strings.Contains(oroPrePushCheck, "ORO_RUN_MUTATION=1 ") || strings.Contains(oroPrePushCheck, "ORO_RUN_MUTATION=1\"") {
			t.Error("oroPrePushCheck should not enable mutation by default")
		}
		if !strings.Contains(oroPrePushCheck, "scripts/quality_gate.sh") {
			t.Error("oroPrePushCheck should run scripts/quality_gate.sh")
		}
		if !strings.Contains(oroPrePushCheck, "mutation testing disabled by default") {
			t.Error("oroPrePushCheck should describe mutation testing as disabled by default")
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

	t.Run("pre_push_hook_runs_explicit_quality_gate_path", func(t *testing.T) {
		tmpDir := t.TempDir()
		gitDir := filepath.Join(tmpDir, ".git")
		if err := os.MkdirAll(filepath.Join(gitDir, "hooks"), 0o750); err != nil {
			t.Fatal(err)
		}

		marker := filepath.Join(tmpDir, "qg-context.txt")
		qgPath := filepath.Join(tmpDir, "stealth-quality_gate.sh")
		qgScript := `#!/bin/sh
printf '%s:%s' "${ORO_RUN_MUTATION:-unset}" "${ORO_QG_CONTEXT:-unset}" > "$MARKER"
`
		if err := os.WriteFile(qgPath, []byte(qgScript), 0o755); err != nil { //nolint:gosec // test hook script
			t.Fatal(err)
		}

		if err := installHookWrapper(gitDir, "pre-push", buildOroPrePushCheck(qgPath)); err != nil {
			t.Fatalf("installHookWrapper pre-push: %v", err)
		}

		hookPath := filepath.Join(gitDir, "hooks", "pre-push")
		cmd := exec.Command(hookPath, "origin", "git@example.invalid:repo.git") //nolint:gosec // test-created hook
		cmd.Dir = tmpDir
		cmd.Stdin = strings.NewReader("refs/heads/main 0000000000000000000000000000000000000000 refs/heads/main 0000000000000000000000000000000000000000\n")
		cmd.Env = append(os.Environ(), "MARKER="+marker)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("pre-push hook failed: %v\n%s", err, string(output))
		}

		got, err := os.ReadFile(marker) //nolint:gosec // test marker
		if err != nil {
			t.Fatalf("read marker: %v", err)
		}
		if string(got) != "unset:push" {
			t.Fatalf("expected explicit quality gate to run in push context with mutation disabled by default, got %q", string(got))
		}
	})

	t.Run("pre_push_hook_prefers_repo_quality_gate_to_stale_explicit_path", func(t *testing.T) {
		tmpDir := t.TempDir()
		gitDir := filepath.Join(tmpDir, ".git")
		if err := os.MkdirAll(filepath.Join(gitDir, "hooks"), 0o750); err != nil {
			t.Fatal(err)
		}

		marker := filepath.Join(tmpDir, "selected-quality-gate.txt")
		repoQGPath := filepath.Join(tmpDir, "scripts", "quality_gate.sh")
		if err := os.MkdirAll(filepath.Dir(repoQGPath), 0o750); err != nil {
			t.Fatal(err)
		}
		repoQGScript := "#!/bin/sh\nprintf repo > \"$MARKER\"\n"
		if err := os.WriteFile(repoQGPath, []byte(repoQGScript), 0o755); err != nil { //nolint:gosec // test hook script
			t.Fatal(err)
		}

		staleQGPath := filepath.Join(tmpDir, "stale-quality_gate.sh")
		staleQGScript := "#!/bin/sh\nprintf stale > \"$MARKER\"\n"
		if err := os.WriteFile(staleQGPath, []byte(staleQGScript), 0o755); err != nil { //nolint:gosec // test hook script
			t.Fatal(err)
		}

		if err := installHookWrapper(gitDir, "pre-push", buildOroPrePushCheck(staleQGPath)); err != nil {
			t.Fatalf("installHookWrapper pre-push: %v", err)
		}

		hookPath := filepath.Join(gitDir, "hooks", "pre-push")
		cmd := exec.Command(hookPath, "origin", "git@example.invalid:repo.git") //nolint:gosec // test-created hook
		cmd.Dir = tmpDir
		cmd.Stdin = strings.NewReader("refs/heads/main 0000000000000000000000000000000000000000 refs/heads/main 0000000000000000000000000000000000000000\n")
		cmd.Env = append(os.Environ(), "MARKER="+marker)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("pre-push hook failed: %v\n%s", err, string(output))
		}

		got, err := os.ReadFile(marker) //nolint:gosec // test marker
		if err != nil {
			t.Fatalf("read marker: %v", err)
		}
		if string(got) != "repo" {
			t.Fatalf("expected repository quality gate to win over stale explicit path, got %q", string(got))
		}
	})
}
