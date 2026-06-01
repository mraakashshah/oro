package ops

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PromptManifest records the file line ranges included in a review prompt.
type PromptManifest struct {
	Shown map[string][][2]int
}

// DroppedFinding records a structured finding discarded before gating.
type DroppedFinding struct {
	Finding Finding
	Layer   string
	Reason  string
}

// ValidateFinding rejects evidence that was not shown in the prompt manifest.
//
//oro:testonly — wired into production by subsequent structured-review phases.
func ValidateFinding(m PromptManifest, repoRoot string, f Finding) error {
	for _, ev := range f.Evidence {
		if err := validateEvidence(m, repoRoot, ev); err != nil {
			return err
		}
	}
	return nil
}

// PartitionFindings keeps valid findings and drops invalid ones with validation metadata.
//
//oro:testonly — wired into production by subsequent structured-review phases.
func PartitionFindings(m PromptManifest, repoRoot string, in []Finding) (kept []Finding, dropped []DroppedFinding) {
	for _, f := range in {
		if err := ValidateFinding(m, repoRoot, f); err != nil {
			dropped = append(dropped, DroppedFinding{
				Finding: f,
				Layer:   "validation",
				Reason:  err.Error(),
			})
			continue
		}
		kept = append(kept, f)
	}
	return kept, dropped
}

func validateEvidence(m PromptManifest, repoRoot string, ev Evidence) error {
	file, err := normalizeManifestPath(ev.File)
	if err != nil {
		return err
	}
	ranges, ok := m.Shown[file]
	if !ok {
		return fmt.Errorf("evidence path not in manifest: %s", file)
	}
	if !rangeShown(ranges, ev.LineStart, ev.LineEnd) {
		return fmt.Errorf("evidence lines outside manifest range: %s:%d-%d", file, ev.LineStart, ev.LineEnd)
	}
	if ev.Quote == "" {
		return nil
	}
	if err := validateLiteralQuote(repoRoot, file, ev); err != nil {
		return err
	}
	return nil
}

func normalizeManifestPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("evidence path is empty")
	}
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("evidence path must be relative: %s", path)
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", fmt.Errorf("evidence path escapes manifest: %s", path)
	}
	return clean, nil
}

func rangeShown(ranges [][2]int, start, end int) bool {
	if start <= 0 || end < start {
		return false
	}
	for _, r := range ranges {
		if start >= r[0] && end <= r[1] {
			return true
		}
	}
	return false
}

func validateLiteralQuote(repoRoot, file string, ev Evidence) error {
	text, err := evidenceLineText(repoRoot, file, ev.LineStart, ev.LineEnd)
	if err != nil {
		return err
	}
	if !strings.Contains(text, ev.Quote) {
		return fmt.Errorf("evidence quote not literal: %s:%d-%d", file, ev.LineStart, ev.LineEnd)
	}
	return nil
}

func evidenceLineText(repoRoot, file string, start, end int) (string, error) {
	path := filepath.Join(repoRoot, filepath.FromSlash(file))
	cleanRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", fmt.Errorf("resolve repo root: %w", err)
	}
	cleanPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve evidence path: %w", err)
	}
	if cleanPath != cleanRoot && !strings.HasPrefix(cleanPath, cleanRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("evidence path escapes repo root: %s", file)
	}

	data, err := os.ReadFile(cleanPath) //nolint:gosec // path was normalized against repoRoot and manifest-relative input.
	if err != nil {
		return "", fmt.Errorf("read evidence file: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	if start > len(lines) {
		return "", fmt.Errorf("evidence line outside file: %s:%d", file, start)
	}
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(lines[start-1:end], "\n"), nil
}
