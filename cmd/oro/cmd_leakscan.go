package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"

	"oro/pkg/leakscan"

	"github.com/spf13/cobra"
)

type leakscanCmdOutput struct {
	Decision string                  `json:"decision"`
	Matches  []leakscan.SummaryMatch `json:"matches"`
}

func newLeakscanCmd() *cobra.Command {
	var (
		fromStdin  bool
		diffRange  string
		filePath   string
		allowPath  string
		minEntropy float64
	)
	cmd := &cobra.Command{
		Use:           "leakscan (--stdin | --diff <range> | --file <path>)",
		Short:         "Scan content for credential leaks",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			content, sourcePath, err := readLeakscanInput(cmd.InOrStdin(), fromStdin, diffRange, filePath)
			if err != nil {
				return err
			}
			allow, err := loadLeakscanAllowlist(allowPath)
			if err != nil {
				return err
			}
			result := leakscan.Result{}
			if sourcePath != "" && allow.AllowsPath(sourcePath) {
				result.Redacted = content
			} else if diffRange != "" {
				result = leakscan.ScanDiffWithMinEntropy(content, leakscan.DefaultPatterns(), allow, minEntropy)
			} else {
				result = leakscan.ScanWithMinEntropy(content, leakscan.DefaultPatterns(), allow, minEntropy)
			}
			if err := writeLeakscanJSON(cmd.OutOrStdout(), result); err != nil {
				return err
			}
			if result.ShouldBlock {
				return fmt.Errorf("leakscan: blocked")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&fromStdin, "stdin", false, "read content from stdin")
	cmd.Flags().StringVar(&diffRange, "diff", "", "scan added lines from git diff range")
	cmd.Flags().StringVar(&filePath, "file", "", "scan content from file")
	cmd.Flags().StringVar(&allowPath, "allowlist", "", "YAML allowlist file")
	cmd.Flags().Float64Var(&minEntropy, "min-entropy", 4.0, "minimum entropy threshold for generic token matches")
	return cmd
}

func readLeakscanInput(stdin io.Reader, fromStdin bool, diffRange, filePath string) (content string, sourcePath string, err error) {
	sources := 0
	for _, enabled := range []bool{fromStdin, diffRange != "", filePath != ""} {
		if enabled {
			sources++
		}
	}
	if sources != 1 {
		return "", "", fmt.Errorf("leakscan: specify exactly one of --stdin, --diff, or --file")
	}
	if fromStdin {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return "", "", fmt.Errorf("read stdin: %w", err)
		}
		return string(data), "", nil
	}
	if diffRange != "" {
		data, err := exec.Command("git", "diff", diffRange).Output() //nolint:gosec // diffRange is an explicit CLI argument passed to git as one argv value
		if err != nil {
			return "", "", fmt.Errorf("git diff %s: %w", diffRange, err)
		}
		return string(data), "", nil
	}
	data, err := os.ReadFile(filePath) //nolint:gosec // user explicitly supplies the file path to scan
	if err != nil {
		return "", "", fmt.Errorf("read file: %w", err)
	}
	return string(data), filePath, nil
}

func loadLeakscanAllowlist(path string) (leakscan.Allowlist, error) {
	if path == "" {
		return leakscan.Allowlist{}, nil
	}
	allow, err := leakscan.LoadAllowlist(path)
	if err != nil {
		return leakscan.Allowlist{}, err
	}
	return allow, nil
}

func writeLeakscanJSON(w io.Writer, result leakscan.Result) error {
	decision := "Clean"
	if result.ShouldBlock {
		decision = "Block"
	}
	matches := make([]leakscan.SummaryMatch, 0, len(result.Matches))
	if len(result.Matches) > 0 {
		if err := json.Unmarshal(leakscan.SummaryJSON(result), &matches); err != nil {
			return fmt.Errorf("summarize matches: %w", err)
		}
	}
	data, err := json.Marshal(leakscanCmdOutput{Decision: decision, Matches: matches})
	if err != nil {
		return fmt.Errorf("encode leakscan JSON: %w", err)
	}
	if _, err := fmt.Fprintln(w, string(data)); err != nil {
		return fmt.Errorf("write leakscan JSON: %w", err)
	}
	return nil
}
