package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

type doctrineAuditResult struct {
	rules          int
	promotionRows  int
	doctrineLevels int
}

func newDoctrineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctrine",
		Short: "Inspect enforcement doctrine artifacts",
	}
	cmd.AddCommand(newDoctrineAuditCmd())
	return cmd
}

func newDoctrineAuditCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "audit",
		Short:        "Validate rule audit and enforcement doctrine artifacts",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := auditDoctrineArtifacts()
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Doctrine audit PASS\n")
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "assets/rules-audit.md rules: %d\n", result.rules)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "assets/rules-audit.md level-6 promotion paths: %d\n", result.promotionRows)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "assets/doctrine.md levels: %d\n", result.doctrineLevels)
			return nil
		},
	}
}

func auditDoctrineArtifacts() (doctrineAuditResult, error) {
	root, err := findDoctrineRepoRoot()
	if err != nil {
		return doctrineAuditResult{}, err
	}

	auditPath := filepath.Join(root, "assets", "rules-audit.md")
	auditBytes, err := os.ReadFile(auditPath) //nolint:gosec // fixed repository artifact path.
	if err != nil {
		return doctrineAuditResult{}, fmt.Errorf("read assets/rules-audit.md: %w", err)
	}
	doctrinePath := filepath.Join(root, "assets", "doctrine.md")
	doctrineBytes, err := os.ReadFile(doctrinePath) //nolint:gosec // fixed repository artifact path.
	if err != nil {
		return doctrineAuditResult{}, fmt.Errorf("read assets/doctrine.md: %w", err)
	}

	result := doctrineAuditResult{
		rules:          countAuditRuleRows(string(auditBytes)),
		promotionRows:  countPromotionRows(string(auditBytes)),
		doctrineLevels: countDoctrineLevels(string(doctrineBytes)),
	}
	if result.rules == 0 {
		return doctrineAuditResult{}, fmt.Errorf("assets/rules-audit.md has no rule inventory rows")
	}
	if result.doctrineLevels != 6 {
		return doctrineAuditResult{}, fmt.Errorf("assets/doctrine.md documents %d enforcement levels, want 6", result.doctrineLevels)
	}
	return result, nil
}

func findDoctrineRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	for {
		if doctrineFileExists(filepath.Join(dir, "go.mod")) && doctrineFileExists(filepath.Join(dir, "assets", "rules-audit.md")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("locate repository root with assets/rules-audit.md")
		}
		dir = parent
	}
}

func doctrineFileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info == nil {
		return false
	}
	return !info.IsDir()
}

func countAuditRuleRows(markdown string) int {
	const heading = "## Rule Inventory"
	section, ok := markdownSection(markdown, heading)
	if !ok {
		return 0
	}
	count := 0
	for _, line := range strings.Split(section, "\n") {
		if isRuleRow(line) {
			count++
		}
	}
	return count
}

func countPromotionRows(markdown string) int {
	const heading = "## Level 6 Rules With Clear Promotion Paths"
	section, ok := markdownSection(markdown, heading)
	if !ok {
		return 0
	}
	count := 0
	for _, line := range strings.Split(section, "\n") {
		if isRuleRow(line) {
			count++
		}
	}
	return count
}

func markdownSection(markdown, heading string) (string, bool) {
	start := strings.Index(markdown, heading)
	if start < 0 {
		return "", false
	}
	section := markdown[start+len(heading):]
	if next := strings.Index(section, "\n## "); next >= 0 {
		section = section[:next]
	}
	return section, true
}

func isRuleRow(line string) bool {
	return len(line) > len("| R000") &&
		strings.HasPrefix(line, "| R") &&
		line[3] >= '0' &&
		line[3] <= '9'
}

func countDoctrineLevels(markdown string) int {
	count := 0
	for level := 1; level <= 6; level++ {
		if strings.Contains(markdown, fmt.Sprintf("LEVEL %d", level)) {
			count++
		}
	}
	return count
}
