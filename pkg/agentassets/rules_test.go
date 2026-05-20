package agentassets_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"oro/pkg/agentassets"
)

func TestClaudeRulesAssets(t *testing.T) {
	t.Run("discovers oro markdown rules", func(t *testing.T) {
		source := fstest.MapFS{
			"rules/claude/oro-standards.md": {Data: []byte("# Standards\n")},
			"rules/claude/oro-tdd.md":       {Data: []byte("# TDD\n")},
			"rules/codex/oro.rules":         {Data: []byte("ignored\n")},
		}

		assets, err := agentassets.ClaudeRuleAssets(source)
		if err != nil {
			t.Fatalf("ClaudeRuleAssets returned error: %v", err)
		}

		want := []agentassets.RuleAsset{
			{
				Source:  "rules/claude/oro-standards.md",
				Target:  ".claude/rules/oro-standards.md",
				Content: []byte("# Standards\n"),
			},
			{
				Source:  "rules/claude/oro-tdd.md",
				Target:  ".claude/rules/oro-tdd.md",
				Content: []byte("# TDD\n"),
			},
		}
		assertRuleAssets(t, assets, want)
	})

	t.Run("empty asset directory is no-op", func(t *testing.T) {
		source := fstest.MapFS{
			"rules/claude": {Mode: fs.ModeDir},
		}

		assets, err := agentassets.ClaudeRuleAssets(source)
		if err != nil {
			t.Fatalf("ClaudeRuleAssets returned error: %v", err)
		}
		if len(assets) != 0 {
			t.Fatalf("expected no assets, got %#v", assets)
		}
	})

	t.Run("missing asset directory is no-op", func(t *testing.T) {
		assets, err := agentassets.ClaudeRuleAssets(fstest.MapFS{})
		if err != nil {
			t.Fatalf("ClaudeRuleAssets returned error: %v", err)
		}
		if len(assets) != 0 {
			t.Fatalf("expected no assets, got %#v", assets)
		}
	})

	t.Run("rejects non oro markdown filenames", func(t *testing.T) {
		source := fstest.MapFS{
			"rules/claude/standards.md": {Data: []byte("# Standards\n")},
		}

		_, err := agentassets.ClaudeRuleAssets(source)
		if err == nil {
			t.Fatal("expected non-oro markdown filename to be rejected")
		}
		if !strings.Contains(err.Error(), "standards.md") {
			t.Fatalf("expected error to identify rejected filename, got %v", err)
		}
	})

	t.Run("rejects oro rule assets without markdown extension", func(t *testing.T) {
		source := fstest.MapFS{
			"rules/claude/oro-standards.txt": {Data: []byte("# Standards\n")},
		}

		_, err := agentassets.ClaudeRuleAssets(source)
		if err == nil {
			t.Fatal("expected non-markdown rule asset to be rejected")
		}
		if !strings.Contains(err.Error(), "oro-standards.txt") {
			t.Fatalf("expected error to identify rejected filename, got %v", err)
		}
	})

	t.Run("target paths stay inside claude rules directory", func(t *testing.T) {
		source := fstest.MapFS{
			"rules/claude/oro-safe.md": {Data: []byte("# Safe\n")},
		}

		assets, err := agentassets.ClaudeRuleAssets(source)
		if err != nil {
			t.Fatalf("ClaudeRuleAssets returned error: %v", err)
		}

		for _, asset := range assets {
			if !strings.HasPrefix(asset.Target, ".claude/rules/") {
				t.Fatalf("target %q must stay under .claude/rules/", asset.Target)
			}
			if strings.Contains(asset.Target, "..") {
				t.Fatalf("target %q must not escape rules directory", asset.Target)
			}
		}
	})
}

func TestClaudeRulesInstallPreservesUserRules(t *testing.T) {
	ctx := context.Background()
	targetDir := t.TempDir()
	rulesDir := filepath.Join(targetDir, ".claude", "rules")
	if err := os.MkdirAll(rulesDir, 0o755); err != nil {
		t.Fatalf("setup rules dir: %v", err)
	}

	userFiles := map[string]string{
		"standards.md": "user standards\n",
		"beads.md":     "user beads\n",
	}
	for name, content := range userFiles {
		if err := os.WriteFile(filepath.Join(rulesDir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("setup user rule %s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(rulesDir, "oro-worker.md"), []byte("old oro worker\n"), 0o644); err != nil {
		t.Fatalf("setup existing oro rule: %v", err)
	}

	assets := []agentassets.RuleAsset{
		{
			Source:  "rules/claude/oro-worker.md",
			Target:  ".claude/rules/oro-worker.md",
			Content: []byte("new oro worker\n"),
		},
		{
			Source:  "rules/claude/oro-review.md",
			Target:  ".claude/rules/oro-review.md",
			Content: []byte("new oro review\n"),
		},
	}

	if err := agentassets.InstallClaudeRules(ctx, targetDir, assets); err != nil {
		t.Fatalf("InstallClaudeRules returned error: %v", err)
	}

	for name, want := range userFiles {
		got, err := os.ReadFile(filepath.Join(rulesDir, name))
		if err != nil {
			t.Fatalf("read user rule %s: %v", name, err)
		}
		if string(got) != want {
			t.Fatalf("user rule %s = %q, want %q", name, got, want)
		}
	}

	assertFileContent(t, filepath.Join(rulesDir, "oro-worker.md"), "new oro worker\n")
	assertFileContent(t, filepath.Join(rulesDir, "oro-review.md"), "new oro review\n")

	missingTargetDir := filepath.Join(t.TempDir(), "home")
	err := agentassets.InstallClaudeRules(ctx, missingTargetDir, []agentassets.RuleAsset{
		{
			Source:  "rules/claude/oro-created.md",
			Target:  ".claude/rules/oro-created.md",
			Content: []byte("created\n"),
		},
	})
	if err != nil {
		t.Fatalf("InstallClaudeRules should create missing target dirs: %v", err)
	}
	assertFileContent(t, filepath.Join(missingTargetDir, ".claude", "rules", "oro-created.md"), "created\n")

	err = agentassets.InstallClaudeRules(ctx, targetDir, []agentassets.RuleAsset{
		{
			Source:  "rules/claude/oro-escape.md",
			Target:  ".claude/rules/../oro-escape.md",
			Content: []byte("escape\n"),
		},
	})
	if err == nil {
		t.Fatal("expected path traversal target to return error")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("expected traversal error to mention escape, got %v", err)
	}
}

func TestClaudeRulesInstallRejectsInvalidTargets(t *testing.T) {
	ctx := context.Background()
	targetDir := t.TempDir()

	tests := []struct {
		name    string
		target  string
		wantErr string
	}{
		{
			name:    "absolute slash path",
			target:  "/.claude/rules/oro-absolute.md",
			wantErr: "escapes",
		},
		{
			name:    "non oro rule",
			target:  ".claude/rules/standards.md",
			wantErr: "oro-*.md",
		},
		{
			name:    "outside rules dir",
			target:  ".claude/oro-worker.md",
			wantErr: "escapes",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := agentassets.InstallClaudeRules(ctx, targetDir, []agentassets.RuleAsset{
				{
					Source:  "rules/claude/oro-worker.md",
					Target:  tc.target,
					Content: []byte("content\n"),
				},
			})
			if err == nil {
				t.Fatal("expected invalid target to return error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error to contain %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestClaudeRulesInstallHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := agentassets.InstallClaudeRules(ctx, t.TempDir(), []agentassets.RuleAsset{
		{
			Source:  "rules/claude/oro-worker.md",
			Target:  ".claude/rules/oro-worker.md",
			Content: []byte("content\n"),
		},
	})
	if err == nil {
		t.Fatal("expected canceled context to return error")
	}
	if !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("expected context cancellation error, got %v", err)
	}
}

func TestClaudeRulesSyncWiredIntoClaudeGenerator(t *testing.T) {
	source := fstest.MapFS{
		"rules/claude/oro-worker.md":   {Data: []byte("# Worker\n")},
		"rules/claude/oro-reviewer.md": {Data: []byte("# Reviewer\n")},
		"rules/codex/oro-worker.md":    {Data: []byte("ignored\n")},
	}

	assets, err := (agentassets.ClaudeGenerator{Source: source}).RuleAssets()
	if err != nil {
		t.Fatalf("RuleAssets returned error: %v", err)
	}

	want := []agentassets.RuleAsset{
		{
			Source:  "rules/claude/oro-reviewer.md",
			Target:  ".claude/rules/oro-reviewer.md",
			Content: []byte("# Reviewer\n"),
		},
		{
			Source:  "rules/claude/oro-worker.md",
			Target:  ".claude/rules/oro-worker.md",
			Content: []byte("# Worker\n"),
		},
	}
	assertRuleAssets(t, assets, want)

	for _, asset := range assets {
		name := filepath.Base(asset.Target)
		if !strings.HasPrefix(name, "oro-") {
			t.Fatalf("Claude rule target %q would overwrite a user-authored non-oro rule", asset.Target)
		}
	}
}

func TestCodexRulesAssets(t *testing.T) {
	assets := agentassets.CodexRuleAssets()
	if len(assets) != 1 {
		t.Fatalf("CodexRuleAssets returned %d assets, want 1", len(assets))
	}

	asset := assets[0]
	if asset.Source != "rules/codex/oro.rules" {
		t.Fatalf("Codex rule source = %q", asset.Source)
	}
	if asset.Target != "rules/oro.rules" {
		t.Fatalf("Codex rule target = %q", asset.Target)
	}

	content := string(asset.Content)
	for _, want := range []string{
		`prefix_rule(pattern=["oro"], decision="allow")`,
		`prefix_rule(pattern=["go", "test"], decision="allow")`,
		`prefix_rule(pattern=["golangci-lint"], decision="allow")`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("Codex rules missing %q:\n%s", want, content)
		}
	}
}

func TestInstallCodexRulesWritesOnlyOroRules(t *testing.T) {
	ctx := context.Background()
	targetDir := t.TempDir()

	if err := agentassets.InstallCodexRules(ctx, targetDir, agentassets.CodexRuleAssets()); err != nil {
		t.Fatalf("InstallCodexRules returned error: %v", err)
	}

	rulesPath := filepath.Join(targetDir, "rules", "oro.rules")
	assertFileContent(t, rulesPath, string(agentassets.CodexRuleAssets()[0].Content))

	for _, target := range []string{
		"rules/../oro.rules",
		"rules/default.rules",
		filepath.Join(targetDir, "rules", "oro.rules"),
		"",
	} {
		err := agentassets.InstallCodexRules(ctx, targetDir, []agentassets.RuleAsset{{
			Source:  "rules/codex/bad.rules",
			Target:  target,
			Content: []byte("bad\n"),
		}})
		if err == nil {
			t.Fatalf("expected invalid Codex rule target %q to fail", target)
		}
	}
}

func assertRuleAssets(t *testing.T, got, want []agentassets.RuleAsset) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("asset count = %d, want %d: %#v", len(got), len(want), got)
	}

	for i := range want {
		if got[i].Source != want[i].Source {
			t.Errorf("asset[%d].Source = %q, want %q", i, got[i].Source, want[i].Source)
		}
		if got[i].Target != want[i].Target {
			t.Errorf("asset[%d].Target = %q, want %q", i, got[i].Target, want[i].Target)
		}
		if string(got[i].Content) != string(want[i].Content) {
			t.Errorf("asset[%d].Content = %q, want %q", i, got[i].Content, want[i].Content)
		}
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}
