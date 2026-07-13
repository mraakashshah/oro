// Package janitor runs deterministic repository-cleanliness detectors.
package janitor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"oro/pkg/processenv"
)

const detectScriptPath = "scripts/janitor_detect.sh"

var errDetectorSkipped = errors.New("janitor detector skipped")

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
	run     func(context.Context, string) ([]Candidate, error)
}

type janitorFile struct {
	relPath  string
	contents []byte
}

// RunBuiltins runs fallback detectors when a project does not provide a
// janitor detector script. A missing detector binary is recorded in skipped,
// rather than returning an error. Detector output is kept as a candidate even
// when the detector exits non-zero, which is the normal lint finding signal.
//
//oro:testonly — production wiring is deferred to the dispatcher janitor lifecycle.
func RunBuiltins(ctx context.Context, worktree string) (cands []Candidate, ran, skipped []string, err error) {
	for _, detector := range builtinsFor(worktree) {
		if detector.run != nil {
			builtinCands, runBuiltinErr := detector.run(ctx, worktree)
			if runBuiltinErr != nil {
				if errors.Is(runBuiltinErr, errDetectorSkipped) {
					skipped = append(skipped, detector.name)
					continue
				}
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
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		runErr := cmd.Run()
		if ctx.Err() != nil {
			return nil, nil, nil, fmt.Errorf("run janitor detector %q: %w", detector.name, ctx.Err())
		}
		if runErr != nil && strings.TrimSpace(stdout.String()) == "" {
			return nil, nil, nil, detectorRunError(detector.name, runErr, stderr.String())
		}

		ran = append(ran, detector.name)
		cands = append(cands, candidatesFromOutput(detector.name, stdout.Bytes())...)
	}
	return cands, ran, skipped, nil
}

func builtinsFor(worktree string) []builtinDetector {
	detectors := []builtinDetector{
		{name: "ci", run: ciDetector},
		{name: "todo", run: staleTODOs},
		{name: "broken-links", run: brokenRelativeLinks},
		{name: "orphan-files", run: orphanFiles},
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

type ciWorkflowRun struct {
	WorkflowName string `json:"workflowName"`
	DisplayTitle string `json:"displayTitle"`
	Conclusion   string `json:"conclusion"`
	URL          string `json:"url"`
}

func ciDetector(ctx context.Context, worktree string) ([]Candidate, error) {
	gh, err := exec.LookPath("gh")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, errDetectorSkipped
		}
		return nil, fmt.Errorf("find gh: %w", err)
	}
	branch, err := currentBranch(ctx, worktree)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("find CI branch: %w", ctx.Err())
		}
		// The rest of the built-in suite can scan a plain directory in tests
		// and small repositories. CI status has no meaningful branch there.
		return nil, errDetectorSkipped
	}
	cmd := exec.CommandContext(ctx, gh, "run", "list", "--branch", branch, "--status", "failure", "--json", "workflowName,displayTitle,conclusion,url") //nolint:gosec // gh path and arguments are fixed CI detector definitions
	cmd.Dir = worktree
	cmd.Env = processenv.ForWorkdir(os.Environ(), worktree)
	out, runErr := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("run CI detector: %w", ctx.Err())
	}
	if runErr != nil {
		// gh uses a non-zero exit for both an absent login and inaccessible
		// repository. CI health is optional in those environments.
		return nil, errDetectorSkipped
	}
	var runs []ciWorkflowRun
	if err := json.Unmarshal(out, &runs); err != nil {
		return nil, fmt.Errorf("parse gh CI runs: %w", err)
	}
	return ciCandidates(runs), nil
}

func currentBranch(ctx context.Context, worktree string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "branch", "--show-current") //nolint:gosec // fixed git command in the supplied scan worktree
	cmd.Dir = worktree
	cmd.Env = processenv.ForWorkdir(os.Environ(), worktree)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("find CI branch: %w", err)
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" {
		return "", errors.New("find CI branch: detached HEAD")
	}
	return branch, nil
}

func ciCandidates(runs []ciWorkflowRun) []Candidate {
	candidates := make([]Candidate, 0, len(runs))
	for _, run := range runs {
		if run.Conclusion != "failure" {
			continue
		}
		workflow := firstNonEmpty(run.WorkflowName, "CI workflow")
		job := firstNonEmpty(run.DisplayTitle, "unspecified job")
		candidates = append(candidates, Candidate{
			Detector: "ci",
			Title:    workflow + " failed",
			Detail:   fmt.Sprintf("workflow: %s; job: %s; run: %s", workflow, job, run.URL),
		})
	}
	return candidates
}

func firstNonEmpty(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func detectorRunError(detector string, runErr error, stderr string) error {
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		return fmt.Errorf("run janitor detector %q: %w", detector, runErr)
	}
	return fmt.Errorf("run janitor detector %q: %w: %s", detector, runErr, detail)
}

func staleTODOs(ctx context.Context, worktree string) ([]Candidate, error) {
	var cands []Candidate
	deadline := time.Now().AddDate(0, 0, -60)
	err := walkJanitorFiles(worktree, func(path string, entry fs.DirEntry) error {
		fileCands, candidateErr := staleTODOCandidates(ctx, worktree, deadline, path, entry)
		if candidateErr != nil {
			return candidateErr
		}
		cands = append(cands, fileCands...)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan stale TODOs: %w", err)
	}
	return cands, nil
}

func brokenRelativeLinks(_ context.Context, worktree string) ([]Candidate, error) {
	var cands []Candidate
	err := walkJanitorFiles(worktree, func(path string, entry fs.DirEntry) error {
		fileCands, candidateErr := brokenLinkCandidates(worktree, path, entry)
		if candidateErr != nil {
			return candidateErr
		}
		cands = append(cands, fileCands...)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan broken relative links: %w", err)
	}
	return cands, nil
}

func orphanFiles(_ context.Context, worktree string) ([]Candidate, error) {
	files, err := readJanitorFiles(worktree)
	if err != nil {
		return nil, fmt.Errorf("read files for orphan scan: %w", err)
	}
	var cands []Candidate
	for _, file := range files {
		if !isAssetOrScript(file.relPath) || isReferenced(file, files) {
			continue
		}
		cands = append(cands, Candidate{
			Detector: "orphan-files",
			File:     filepath.ToSlash(file.relPath),
			Title:    "orphan file",
			Detail:   "unreferenced asset or script",
		})
	}
	return cands, nil
}

func readJanitorFiles(worktree string) ([]janitorFile, error) {
	var files []janitorFile
	err := walkJanitorFiles(worktree, func(path string, entry fs.DirEntry) error {
		info, infoErr := entry.Info()
		if infoErr != nil {
			return fmt.Errorf("stat %q: %w", path, infoErr)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		contents, readErr := os.ReadFile(path) //nolint:gosec // path comes from filepath.WalkDir under the supplied worktree
		if readErr != nil {
			return fmt.Errorf("read %q: %w", path, readErr)
		}
		relPath, relErr := filepath.Rel(worktree, path)
		if relErr != nil {
			return fmt.Errorf("make %q relative: %w", path, relErr)
		}
		files = append(files, janitorFile{relPath: relPath, contents: contents})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk janitor files: %w", err)
	}
	return files, nil
}

func isAssetOrScript(path string) bool {
	topDir, _, found := strings.Cut(filepath.ToSlash(path), "/")
	return found && (topDir == "assets" || topDir == "scripts")
}

func isReferenced(target janitorFile, files []janitorFile) bool {
	relPath := []byte(filepath.ToSlash(target.relPath))
	baseName := []byte(filepath.Base(target.relPath))
	for _, file := range files {
		if file.relPath == target.relPath {
			continue
		}
		if bytes.Contains(file.contents, relPath) || bytes.Contains(file.contents, baseName) {
			return true
		}
	}
	return false
}

func walkJanitorFiles(worktree string, visit func(string, fs.DirEntry) error) error {
	err := filepath.WalkDir(worktree, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk %q: %w", path, walkErr)
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".worktrees", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		return visit(path, entry)
	})
	if err != nil {
		return fmt.Errorf("walk janitor files: %w", err)
	}
	return nil
}

func staleTODOCandidates(ctx context.Context, worktree string, deadline time.Time, path string, _ fs.DirEntry) ([]Candidate, error) {
	contents, err := os.ReadFile(path) //nolint:gosec // path comes from filepath.WalkDir under the supplied worktree
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", path, err)
	}
	relPath, err := filepath.Rel(worktree, path)
	if err != nil {
		return nil, fmt.Errorf("make %q relative: %w", path, err)
	}
	fileCands := todoCandidates(relPath, contents)
	if len(fileCands) == 0 {
		return nil, nil
	}
	updatedAt, found, err := lastGitUpdate(ctx, worktree, relPath)
	if err != nil {
		return nil, err
	}
	if !found || !updatedAt.Before(deadline) {
		return nil, nil
	}
	return fileCands, nil
}

func lastGitUpdate(ctx context.Context, worktree, relPath string) (time.Time, bool, error) {
	cmd := exec.CommandContext(ctx, "git", "log", "-1", "--format=%ct", "--", filepath.ToSlash(relPath)) //nolint:gosec // relPath comes from WalkDir under worktree and follows --
	cmd.Dir = worktree
	out, err := cmd.Output()
	if err != nil {
		return time.Time{}, false, fmt.Errorf("find TODO age for %q: %w", relPath, err)
	}
	rawTimestamp := strings.TrimSpace(string(out))
	if rawTimestamp == "" {
		return time.Time{}, false, nil
	}
	timestamp, err := strconv.ParseInt(rawTimestamp, 10, 64)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("parse TODO age for %q: %w", relPath, err)
	}
	return time.Unix(timestamp, 0), true, nil
}

func todoCandidates(path string, contents []byte) []Candidate {
	var cands []Candidate
	for lineNumber, line := range strings.Split(string(contents), "\n") {
		if strings.Contains(line, "TODO") || strings.Contains(line, "FIXME") {
			cands = append(cands, Candidate{Detector: "todo", File: path, Line: lineNumber + 1, Title: "stale TODO/FIXME", Detail: strings.TrimSpace(line)})
		}
	}
	return cands
}

func brokenLinkCandidates(worktree, path string, _ fs.DirEntry) ([]Candidate, error) {
	if filepath.Ext(path) != ".md" {
		return nil, nil
	}
	contents, err := os.ReadFile(path) //nolint:gosec // path comes from filepath.WalkDir under the supplied worktree
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", path, err)
	}
	relPath, err := filepath.Rel(worktree, path)
	if err != nil {
		return nil, fmt.Errorf("make %q relative: %w", path, err)
	}
	return findBrokenLinks(path, relPath, contents), nil
}

func findBrokenLinks(path, relPath string, contents []byte) []Candidate {
	var cands []Candidate
	for lineNumber, line := range strings.Split(string(contents), "\n") {
		for _, target := range markdownLinkTargets(line) {
			if _, err := os.Stat(filepath.Join(filepath.Dir(path), filepath.FromSlash(target))); errors.Is(err, os.ErrNotExist) {
				cands = append(cands, Candidate{Detector: "broken-links", File: relPath, Line: lineNumber + 1, Title: "broken relative link", Detail: target})
			}
		}
	}
	return cands
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
