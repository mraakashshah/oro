// Package janitor runs deterministic repository-cleanliness detectors.
package janitor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"oro/pkg/processenv"
)

const detectScriptPath = "scripts/janitor_detect.sh"

// Candidate is one finding emitted by a deterministic janitor detector.
type Candidate struct {
	Detector string `json:"detector"`
	File     string `json:"file"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Line     int    `json:"line"`
}

// RunDetectScript runs the project-owned detector script in worktree.
// It returns found=false without an error when the script is absent so callers
// can fall back to built-in detectors. Malformed JSONL records are skipped and
// returned in skippedLines. A non-zero script exit returns an error containing
// the script's combined output.
func RunDetectScript(ctx context.Context, worktree string) (cands []Candidate, skippedLines []string, found bool, err error) {
	scriptPath := filepath.Join(worktree, detectScriptPath)
	if _, statErr := os.Stat(scriptPath); statErr != nil {
		if os.IsNotExist(statErr) {
			return nil, nil, false, nil
		}
		return nil, nil, false, fmt.Errorf("stat janitor detector script: %w", statErr)
	}

	cmd := exec.CommandContext(ctx, "bash", scriptPath) //nolint:gosec // script path is constructed from the provided worktree
	cmd.Dir = worktree
	cmd.Env = processenv.ForWorkdir(os.Environ(), worktree)
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		return nil, nil, true, fmt.Errorf("run janitor detector: %w: %s", runErr, strings.TrimSpace(string(out)))
	}

	cands, skippedLines, err = parseCandidates(out)
	if err != nil {
		return nil, nil, true, err
	}
	return cands, skippedLines, true, nil
}

func parseCandidates(output []byte) ([]Candidate, []string, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var cands []Candidate
	var skippedLines []string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var candidate Candidate
		if err := json.Unmarshal([]byte(line), &candidate); err != nil {
			skippedLines = append(skippedLines, line)
			continue
		}
		cands = append(cands, candidate)
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("read janitor detector output: %w", err)
	}
	return cands, skippedLines, nil
}
