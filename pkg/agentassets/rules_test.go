package agentassets_test

import (
	"io/fs"
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
