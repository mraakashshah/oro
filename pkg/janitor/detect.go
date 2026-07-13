// Package janitor runs deterministic repository-cleanliness detectors.
package janitor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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

type builtinDetector struct {
	name    string
	command string
	args    []string
	run     func(string) ([]Candidate, error)
}

// RunBuiltins runs fallback detectors when a project does not provide a
// janitor detector script. A missing detector binary is recorded in skipped,
// rather than returning an error. Detector output is kept as a candidate even
// when the detector exits non-zero, which is the normal lint finding signal.
//
//oro:testonly — production wiring is deferred to the dispatcher janitor lifecycle.
func RunBuiltins(ctx context.Context, worktree string) (cands []Candidate, ran []string, skipped []string, err error) {
	for _, detector := range builtinsFor(worktree) {
		if detector.run != nil {
			builtinCands, runBuiltinErr := detector.run(worktree)
			if runBuiltinErr != nil {
				return nil, nil, nil, fmt.Errorf("run janitor detector %q: %w", detector.name, runBuiltinErr)
			}
			ran = append(ran, detector.name)
			cands = append(cands, builtinCands...)
			continue
		}

		binary, lookPathErr := exec.LookPath(detector.command)
		if lookPathErr != nil {
			if errors.Is(lookPathErr, exec.ErrNotFound) {
				skipped = append(skipped, detector.name)
				continue
			}
			return nil, nil, nil, fmt.Errorf("find janitor detector %q: %w", detector.name, lookPathErr)
		}

		cmd := exec.CommandContext(ctx, binary, detector.args...) //nolint:gosec // binary and arguments are fixed built-in detector definitions
		cmd.Dir = worktree
		cmd.Env = processenv.ForWorkdir(os.Environ(), worktree)
		out, runErr := cmd.CombinedOutput()
		if ctx.Err() != nil {
			return nil, nil, nil, fmt.Errorf("run janitor detector %q: %w", detector.name, ctx.Err())
		}
		if runErr != nil && strings.TrimSpace(string(out)) == "" {
			return nil, nil, nil, fmt.Errorf("run janitor detector %q: %w", detector.name, runErr)
		}

		ran = append(ran, detector.name)
		cands = append(cands, candidatesFromOutput(detector.name, out)...)
	}
	return cands, ran, skipped, nil
}

func builtinsFor(worktree string) []builtinDetector {
	detectors := []builtinDetector{
		{name: "todo", run: staleTODOs},
		{name: "broken-links", run: brokenRelativeLinks},
		{name: "orphan-files", run: func(string) ([]Candidate, error) { return nil, nil }},
	}
	if isProjectFile(worktree, "go.mod") {
		detectors = append(detectors,
			builtinDetector{name: "deadcode", command: "deadcode", args: []string{"./..."}},
			builtinDetector{name: "dupl", command: "dupl", args: []string{"-plumbing", "."}},
			builtinDetector{name: "golangci-lint", command: "golangci-lint", args: []string{"run", "--fix=false"}},
		)
	}
	if isPythonProject(worktree) {
		detectors = append(detectors,
			builtinDetector{name: "ruff", command: "ruff", args: []string{"check", "."}},
			builtinDetector{name: "vulture", command: "vulture", args: []string{"."}},
		)
	}
	return detectors
}

func staleTODOs(worktree string) ([]Candidate, error) {
	var cands []Candidate
	deadline := time.Now().AddDate(0, 0, -60)
	err := filepath.WalkDir(worktree, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk %q: %w", path, walkErr)
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return fmt.Errorf("stat %q: %w", path, infoErr)
		}
		if !info.ModTime().Before(deadline) {
			return nil
		}
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("read %q: %w", path, readErr)
		}
		relPath, relErr := filepath.Rel(worktree, path)
		if relErr != nil {
			return fmt.Errorf("make %q relative: %w", path, relErr)
		}
		for lineNumber, line := range strings.Split(string(contents), "\n") {
			if strings.Contains(line, "TODO") || strings.Contains(line, "FIXME") {
				cands = append(cands, Candidate{Detector: "todo", File: relPath, Line: lineNumber + 1, Title: "stale TODO/FIXME", Detail: strings.TrimSpace(line)})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return cands, nil
}

func brokenRelativeLinks(worktree string) ([]Candidate, error) {
	var cands []Candidate
	err := filepath.WalkDir(worktree, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk %q: %w", path, walkErr)
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".md" {
			return nil
		}
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("read %q: %w", path, readErr)
		}
		relPath, relErr := filepath.Rel(worktree, path)
		if relErr != nil {
			return fmt.Errorf("make %q relative: %w", path, relErr)
		}
		for lineNumber, line := range strings.Split(string(contents), "\n") {
			for _, target := range markdownLinkTargets(line) {
				if _, statErr := os.Stat(filepath.Join(filepath.Dir(path), filepath.FromSlash(target))); errors.Is(statErr, os.ErrNotExist) {
					cands = append(cands, Candidate{Detector: "broken-links", File: relPath, Line: lineNumber + 1, Title: "broken relative link", Detail: target})
				} else if statErr != nil {
					return fmt.Errorf("check link target %q: %w", target, statErr)
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return cands, nil
}

func markdownLinkTargets(line string) []string {
	var targets []string
	for rest := line; ; {
		start := strings.Index(rest, "](")
		if start < 0 {
			return targets
		}
		rest = rest[start+2:]
		end := strings.IndexByte(rest, ')')
		if end < 0 {
			return targets
		}
		target := strings.TrimSpace(strings.Split(rest[:end], "#")[0])
		if target != "" && !strings.HasPrefix(target, "/") && !strings.Contains(target, "://") && !strings.HasPrefix(target, "mailto:") {
			targets = append(targets, target)
		}
		rest = rest[end+1:]
	}
}

func isProjectFile(worktree, name string) bool {
	_, err := os.Stat(filepath.Join(worktree, name))
	return err == nil
}

func isPythonProject(worktree string) bool {
	for _, name := range []string{"pyproject.toml", "requirements.txt", "setup.py", "setup.cfg"} {
		if isProjectFile(worktree, name) {
			return true
		}
	}
	return false
}

func candidatesFromOutput(detector string, output []byte) []Candidate {
	var cands []Candidate
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		cands = append(cands, Candidate{Detector: detector, Title: line, Detail: line})
	}
	return cands
}

// RunDetectScript runs the project-owned detector script in worktree.
// It returns found=false without an error when the script is absent so callers
// can fall back to built-in detectors. Malformed JSONL records are skipped and
// returned in skippedLines. A non-zero script exit returns an error containing
// the script's combined output.
//
//oro:testonly — production wiring is deferred to the dispatcher janitor lifecycle.
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
