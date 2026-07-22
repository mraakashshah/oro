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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

const detectScriptPath = "scripts/janitor_detect.sh"

var errDetectorSkipped = errors.New("janitor detector skipped")

var todoMarkerPattern = regexp.MustCompile(`(?i)(//|#|--|/\*|<!--)[[:space:]]*(TODO|FIXME)(\([^)]*\))?([[:space:]]*:[[:space:]]*|[[:space:]]+|[[:space:]]*$)`)

var todoBlockContinuationPattern = regexp.MustCompile(`(?i)^[[:space:]]*\*?[[:space:]]*(TODO|FIXME)(\([^)]*\))?([[:space:]]*:[[:space:]]*|[[:space:]]+|[[:space:]]*$)`)

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
// targetBranch is passed to repository-level probes such as CI; when it is
// empty, those probes are skipped and cannot contribute re-run commands.
func RunBuiltins(ctx context.Context, worktree, targetBranch string, options ...RunOption) (cands []Candidate, ran, skipped []string, err error) {
	commands, err := commandRunnerFor(worktree, options)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, detector := range builtinsFor(worktree, targetBranch, commands) {
		builtinCands, detectorSkipped, runErr := runBuiltin(ctx, worktree, detector, commands)
		if runErr != nil {
			return nil, nil, nil, runErr
		}
		if detectorSkipped {
			skipped = append(skipped, detector.name)
			continue
		}
		ran = append(ran, detector.name)
		cands = append(cands, builtinCands...)
	}
	return cands, ran, skipped, nil
}

// RunBuiltin reruns one named deterministic detector in worktree. A detector
// that is unknown or unavailable is an error so callers never mistake a
// skipped acceptance check for a clean repository.
func RunBuiltin(ctx context.Context, worktree, targetBranch, name string, options ...RunOption) ([]Candidate, error) {
	commands, err := commandRunnerFor(worktree, options)
	if err != nil {
		return nil, err
	}
	for _, detector := range builtinsFor(worktree, targetBranch, commands) {
		if detector.name != name {
			continue
		}
		candidates, skipped, err := runBuiltin(ctx, worktree, detector, commands)
		if err != nil {
			return nil, err
		}
		if skipped {
			return nil, fmt.Errorf("janitor detector %q skipped: unavailable or missing required configuration", name)
		}
		return candidates, nil
	}
	return nil, fmt.Errorf("unknown janitor detector %q", name)
}

func runBuiltin(ctx context.Context, worktree string, detector builtinDetector, commands commandRunner) ([]Candidate, bool, error) {
	if detector.run != nil {
		candidates, err := detector.run(ctx, worktree)
		if errors.Is(err, errDetectorSkipped) {
			return nil, true, nil
		}
		if err != nil {
			return nil, false, fmt.Errorf("run janitor detector %q: %w", detector.name, err)
		}
		return candidates, false, nil
	}

	binary, err := exec.LookPath(detector.command)
	if errors.Is(err, exec.ErrNotFound) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("find janitor detector %q: %w", detector.name, err)
	}

	output, runErr := commands.run(ctx, binary, detector.args...)
	if ctx.Err() != nil {
		return nil, false, fmt.Errorf("run janitor detector %q: %w", detector.name, ctx.Err())
	}
	if runErr != nil && strings.TrimSpace(string(output.stdout)) == "" {
		return nil, false, detectorRunError(detector.name, runErr, string(output.stderr))
	}
	return candidatesFromOutput(worktree, detector.name, output.stdout), false, nil
}

func builtinsFor(worktree, targetBranch string, commands commandRunner) []builtinDetector {
	detectors := []builtinDetector{
		{name: "ci", run: func(ctx context.Context, worktree string) ([]Candidate, error) {
			return ciDetector(ctx, worktree, targetBranch, commands)
		}},
		{name: "todo", run: func(ctx context.Context, worktree string) ([]Candidate, error) {
			return staleTODOs(ctx, worktree, commands)
		}},
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
	DatabaseID         int64  `json:"databaseId"`
	WorkflowDatabaseID int64  `json:"workflowDatabaseId"`
	WorkflowName       string `json:"workflowName"`
	Conclusion         string `json:"conclusion"`
	URL                string `json:"url"`
}

type ciJob struct {
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
}

type ciRunDetails struct {
	Jobs []ciJob `json:"jobs"`
}

func ciDetector(ctx context.Context, worktree, targetBranch string, commands commandRunner) ([]Candidate, error) {
	gh, err := exec.LookPath("gh")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, errDetectorSkipped
		}
		return nil, fmt.Errorf("find gh: %w", err)
	}
	if targetBranch == "" {
		return nil, errDetectorSkipped
	}
	host, err := ciRemoteHost(ctx, worktree, commands)
	if err != nil {
		return nil, err
	}
	authenticated, err := ciAuthenticated(ctx, commands, gh, host)
	if err != nil {
		return nil, err
	}
	if !authenticated {
		return nil, errDetectorSkipped
	}
	out, err := runCIProbe(ctx, commands, gh, "run", "list", "--branch", targetBranch, "--limit", "100", "--json", "databaseId,workflowDatabaseId,workflowName,conclusion,url")
	if err != nil {
		return nil, err
	}
	var runs []ciWorkflowRun
	if err := json.Unmarshal(out, &runs); err != nil {
		return nil, fmt.Errorf("parse gh CI runs: %w", err)
	}
	return ciCandidates(ctx, worktree, commands, gh, latestFailingCIRuns(runs))
}

func ciRemoteHost(ctx context.Context, worktree string, commands commandRunner) (string, error) {
	result, err := commands.run(ctx, "git", "-C", worktree, "remote")
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("list CI remotes: %w", ctx.Err())
		}
		return "", fmt.Errorf("list CI remotes: %w", err)
	}
	if !slices.Contains(strings.Fields(string(result.stdout)), "origin") {
		return "", errDetectorSkipped
	}

	result, err = commands.run(ctx, "git", "-C", worktree, "remote", "get-url", "origin")
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("read CI remote: %w", ctx.Err())
		}
		return "", fmt.Errorf("read CI remote: %w", err)
	}
	host, err := gitRemoteHost(strings.TrimSpace(string(result.stdout)))
	if err != nil {
		return "", err
	}
	return host, nil
}

func gitRemoteHost(remote string) (string, error) {
	if strings.Contains(remote, "://") {
		parsed, err := url.Parse(remote)
		if err == nil && parsed.Hostname() != "" {
			return parsed.Hostname(), nil
		}
		return "", errors.New("CI remote has no hostname")
	}
	hostAndPath := remote
	if at := strings.LastIndex(hostAndPath, "@"); at >= 0 {
		hostAndPath = hostAndPath[at+1:]
	}
	if colon := strings.Index(hostAndPath, ":"); colon > 0 && !strings.Contains(hostAndPath[:colon], "/") {
		return hostAndPath[:colon], nil
	}
	return "", errors.New("CI remote has no hostname")
}

func ciAuthenticated(ctx context.Context, commands commandRunner, gh, host string) (bool, error) {
	if _, err := commands.run(ctx, gh, "auth", "status", "--active", "--hostname", host); err != nil {
		if ctx.Err() != nil {
			return false, fmt.Errorf("check gh authentication: %w", ctx.Err())
		}
		return false, nil
	}
	return true, nil
}

func runCIProbe(ctx context.Context, commands commandRunner, gh string, args ...string) ([]byte, error) {
	output, runErr := commands.run(ctx, gh, args...)
	if ctx.Err() != nil {
		return nil, fmt.Errorf("run CI detector: %w", ctx.Err())
	}
	if runErr != nil {
		detail := strings.TrimSpace(string(output.stderr))
		if detail == "" {
			return nil, fmt.Errorf("run gh CI probe: %w", runErr)
		}
		return nil, fmt.Errorf("run gh CI probe: %w: %s", runErr, detail)
	}
	return output.stdout, nil
}

func latestFailingCIRuns(runs []ciWorkflowRun) []ciWorkflowRun {
	failing := make([]ciWorkflowRun, 0, len(runs))
	seenWorkflows := make(map[string]struct{}, len(runs))
	// gh run list returns newest runs first, so the first occurrence is the
	// current conclusion for that workflow.
	for _, run := range runs {
		workflowKey := ciWorkflowKey(run)
		if _, seen := seenWorkflows[workflowKey]; seen {
			continue
		}
		seenWorkflows[workflowKey] = struct{}{}
		if run.Conclusion != "failure" {
			continue
		}
		failing = append(failing, run)
	}
	return failing
}

func ciWorkflowKey(run ciWorkflowRun) string {
	if run.WorkflowDatabaseID != 0 {
		return "workflow:" + strconv.FormatInt(run.WorkflowDatabaseID, 10)
	}
	return "run:" + strconv.FormatInt(run.DatabaseID, 10)
}

func ciCandidates(ctx context.Context, worktree string, commands commandRunner, gh string, runs []ciWorkflowRun) ([]Candidate, error) {
	var candidates []Candidate
	for _, run := range runs {
		out, err := runCIProbe(ctx, commands, gh, "run", "view", strconv.FormatInt(run.DatabaseID, 10), "--json", "jobs")
		if err != nil {
			return nil, err
		}
		var details ciRunDetails
		if err := json.Unmarshal(out, &details); err != nil {
			return nil, fmt.Errorf("parse gh CI jobs for run %d: %w", run.DatabaseID, err)
		}
		workflow := firstNonEmpty(run.WorkflowName, "CI workflow")
		job := firstNonEmpty(strings.Join(failedCIJobNames(details.Jobs), ", "), "unspecified job")
		logOutput, err := runCIProbe(ctx, commands, gh, "run", "view", strconv.FormatInt(run.DatabaseID, 10), "--log-failed")
		if err != nil {
			return nil, err
		}
		for _, candidate := range candidatesFromOutput(worktree, "ci", logOutput) {
			candidate.Title = workflow + " failed"
			candidate.Detail = fmt.Sprintf("workflow: %s; job: %s; run: %s; evidence: %s", workflow, job, run.URL, candidate.Detail)
			candidates = append(candidates, candidate)
		}
	}
	return candidates, nil
}

func failedCIJobNames(jobs []ciJob) []string {
	names := make([]string, 0, len(jobs))
	for _, job := range jobs {
		if job.Conclusion == "failure" && job.Name != "" {
			names = append(names, job.Name)
		}
	}
	return names
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

func staleTODOs(ctx context.Context, worktree string, commands commandRunner) ([]Candidate, error) {
	var cands []Candidate
	deadline := time.Now().AddDate(0, 0, -60)
	err := walkJanitorFiles(worktree, func(path string, entry fs.DirEntry) error {
		fileCands, candidateErr := staleTODOCandidates(ctx, worktree, deadline, path, entry, commands)
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

func staleTODOCandidates(ctx context.Context, worktree string, deadline time.Time, path string, _ fs.DirEntry, commands commandRunner) ([]Candidate, error) {
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
	updatedAt, found, err := lastGitUpdate(ctx, relPath, commands)
	if err != nil {
		return nil, err
	}
	if !found || !updatedAt.Before(deadline) {
		return nil, nil
	}
	return fileCands, nil
}

func lastGitUpdate(ctx context.Context, relPath string, commands commandRunner) (time.Time, bool, error) {
	output, err := commands.run(ctx, "git", "log", "-1", "--format=%ct", "--", filepath.ToSlash(relPath))
	if err != nil {
		return time.Time{}, false, fmt.Errorf("find TODO age for %q: %w", relPath, err)
	}
	rawTimestamp := strings.TrimSpace(string(output.stdout))
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
	inBlockComment := false
	for lineNumber, line := range strings.Split(string(contents), "\n") {
		trimmed := strings.TrimSpace(line)
		isBlockContinuation := inBlockComment && todoBlockContinuationPattern.MatchString(line)
		if todoMarkerPattern.MatchString(line) || isBlockContinuation {
			cands = append(cands, Candidate{Detector: "todo", File: path, Line: lineNumber + 1, Title: "stale TODO/FIXME", Detail: strings.TrimSpace(line)})
		}
		if inBlockComment {
			if strings.Contains(trimmed, "*/") {
				inBlockComment = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "/*") && !strings.Contains(strings.TrimPrefix(trimmed, "/*"), "*/") {
			inBlockComment = true
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
	document := goldmark.DefaultParser().Parse(text.NewReader(contents))
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		var destination []byte
		switch typed := node.(type) {
		case *ast.Link:
			destination = typed.Destination
		case *ast.Image:
			destination = typed.Destination
		default:
			return ast.WalkContinue, nil
		}
		target, ok := relativeMarkdownTarget(destination)
		if !ok {
			return ast.WalkContinue, nil
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(path), filepath.FromSlash(target))); errors.Is(err, os.ErrNotExist) {
			cands = append(cands, Candidate{Detector: "broken-links", File: relPath, Line: markdownNodeLine(contents, node), Title: "broken relative link", Detail: target})
		}
		return ast.WalkContinue, nil
	})
	return cands
}

func relativeMarkdownTarget(destination []byte) (string, bool) {
	target := strings.TrimSpace(strings.SplitN(string(destination), "#", 2)[0])
	if target == "" || strings.HasPrefix(target, "/") || strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
		return "", false
	}
	return target, true
}

func markdownNodeLine(contents []byte, node ast.Node) int {
	offset := -1
	_ = ast.Walk(node, func(child ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering || offset >= 0 {
			return ast.WalkContinue, nil
		}
		if value, ok := child.(*ast.Text); ok {
			offset = value.Segment.Start
			return ast.WalkStop, nil
		}
		return ast.WalkContinue, nil
	})
	for parent := node.Parent(); offset < 0 && parent != nil; parent = parent.Parent() {
		lines := parent.Lines()
		if lines != nil && lines.Len() > 0 {
			offset = lines.At(0).Start
		}
	}
	if offset < 0 || offset > len(contents) {
		return 1
	}
	return bytes.Count(contents[:offset], []byte{'\n'}) + 1
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

func candidatesFromOutput(worktree, detector string, output []byte) []Candidate {
	var cands []Candidate
	locationPattern := regexp.MustCompile(`:(\d+)(?::\d+|,\d+)?(?::|\s|$)`)
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		file, lineNumber, ok := outputRepositoryLocation(worktree, line, locationPattern)
		if !ok {
			continue
		}
		cands = append(cands, Candidate{Detector: detector, File: file, Line: lineNumber, Title: line, Detail: line})
	}
	return cands
}

func outputRepositoryLocation(worktree, line string, locationPattern *regexp.Regexp) (file string, lineNumber int, found bool) {
	for _, match := range locationPattern.FindAllStringSubmatchIndex(line, -1) {
		candidateLine, err := strconv.Atoi(line[match[2]:match[3]])
		if err != nil || candidateLine <= 0 {
			continue
		}
		for _, rawPath := range outputPathSuffixes(line[:match[0]]) {
			relPath, fullPath, ok := repositoryOutputPath(worktree, rawPath)
			if !ok || !repositoryLineExists(fullPath, candidateLine) {
				continue
			}
			return relPath, candidateLine, true
		}
	}
	return "", 0, false
}

func outputPathSuffixes(prefix string) []string {
	prefix = strings.TrimSpace(prefix)
	paths := []string{prefix}
	for index, char := range prefix {
		if unicode.IsSpace(char) {
			paths = append(paths, strings.TrimSpace(prefix[index+len(string(char)):]))
		}
	}
	return paths
}

func repositoryOutputPath(worktree, rawPath string) (relativePath, fullPath string, found bool) {
	rawPath = strings.Trim(strings.TrimSpace(rawPath), `"'`)
	rawPath = strings.TrimPrefix(rawPath, "##[error]")
	localPath := filepath.Clean(filepath.FromSlash(rawPath))
	if localPath == "." || localPath == "" {
		return "", "", false
	}
	if filepath.IsAbs(localPath) {
		relPath, err := filepath.Rel(worktree, localPath)
		if err != nil {
			return "", "", false
		}
		localPath = relPath
	}
	if localPath == ".." || strings.HasPrefix(localPath, ".."+string(filepath.Separator)) {
		return "", "", false
	}
	fullPath = filepath.Join(worktree, localPath)
	info, err := os.Stat(fullPath)
	if err != nil || !info.Mode().IsRegular() {
		return "", "", false
	}
	return filepath.ToSlash(localPath), fullPath, true
}

func repositoryLineExists(path string, lineNumber int) bool {
	data, err := os.ReadFile(path) //nolint:gosec // path was resolved to a regular file inside the scan worktree.
	if err != nil {
		return false
	}
	return lineNumber <= len(strings.Split(string(data), "\n"))
}

// RunDetectScript runs the project-owned detector script in worktree.
// It returns found=false without an error when the script is absent so callers
// can fall back to built-in detectors. Malformed JSONL records are skipped and
// returned in skippedLines. A non-zero script exit returns an error containing
// the script's combined output.
func RunDetectScript(ctx context.Context, worktree string, options ...RunOption) (cands []Candidate, skippedLines []string, found bool, err error) {
	scriptPath := filepath.Join(worktree, detectScriptPath)
	if _, statErr := os.Stat(scriptPath); statErr != nil {
		if os.IsNotExist(statErr) {
			return nil, nil, false, nil
		}
		return nil, nil, false, fmt.Errorf("stat janitor detector script: %w", statErr)
	}

	commands, configErr := commandRunnerFor(worktree, options)
	if configErr != nil {
		return nil, nil, true, configErr
	}
	output, runErr := commands.run(ctx, "bash", scriptPath)
	if runErr != nil {
		combined := append(append([]byte(nil), output.stdout...), output.stderr...)
		return nil, nil, true, fmt.Errorf("run janitor detector: %w: %s", runErr, strings.TrimSpace(string(combined)))
	}

	cands, skippedLines, err = parseCandidates(output.stdout)
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
