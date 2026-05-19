package agentassets

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
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

func isMissingDir(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}
