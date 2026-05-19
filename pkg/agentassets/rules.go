package agentassets

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

const (
	claudeRulesSourceDir = "rules/claude"
	claudeRulesTargetDir = ".claude/rules"
)

// ClaudeRuleAssets discovers Oro-owned Claude rule assets and maps them to
// Claude's rules directory.
func ClaudeRuleAssets(source fs.FS) ([]RuleAsset, error) {
	entries, err := fs.ReadDir(source, claudeRulesSourceDir)
	if err != nil {
		if isMissingDir(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", claudeRulesSourceDir, err)
	}

	assets := make([]RuleAsset, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasPrefix(name, "oro-") || !strings.HasSuffix(name, ".md") {
			return nil, fmt.Errorf("claude rule asset %q must match oro-*.md", name)
		}

		sourcePath := path.Join(claudeRulesSourceDir, name)
		targetPath := path.Join(claudeRulesTargetDir, name)
		if !strings.HasPrefix(targetPath, claudeRulesTargetDir+"/") {
			return nil, fmt.Errorf("claude rule target %q escapes %s", targetPath, claudeRulesTargetDir)
		}

		content, err := fs.ReadFile(source, sourcePath)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", sourcePath, err)
		}

		assets = append(assets, RuleAsset{
			Source:  sourcePath,
			Target:  targetPath,
			Content: slices.Clone(content),
		})
	}

	return assets, nil
}

// InstallClaudeRules writes Oro-owned Claude rule assets below targetDir.
func InstallClaudeRules(ctx context.Context, targetDir string, assets []RuleAsset) error {
	for _, asset := range assets {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("install claude rules: %w", err)
		}

		cleanTarget, err := cleanClaudeRuleTarget(asset.Target)
		if err != nil {
			return err
		}

		destPath := filepath.Join(targetDir, filepath.FromSlash(cleanTarget))
		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil { //nolint:gosec // rules need to be readable
			return fmt.Errorf("create claude rules dir: %w", err)
		}
		if err := os.WriteFile(destPath, asset.Content, 0o644); err != nil { //nolint:gosec // rules need to be readable
			return fmt.Errorf("write %s: %w", cleanTarget, err)
		}
	}

	return nil
}

func cleanClaudeRuleTarget(target string) (string, error) {
	if target == "" || path.IsAbs(target) || filepath.IsAbs(target) {
		return "", fmt.Errorf("claude rule target %q escapes %s", target, claudeRulesTargetDir)
	}

	cleanTarget := path.Clean(filepath.ToSlash(target))
	if !strings.HasPrefix(cleanTarget, claudeRulesTargetDir+"/") {
		return "", fmt.Errorf("claude rule target %q escapes %s", target, claudeRulesTargetDir)
	}

	name := path.Base(cleanTarget)
	if !strings.HasPrefix(name, "oro-") || !strings.HasSuffix(name, ".md") {
		return "", fmt.Errorf("claude rule target %q must match %s/oro-*.md", target, claudeRulesTargetDir)
	}

	return cleanTarget, nil
}

func isMissingDir(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}
