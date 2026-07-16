// Package processenv normalizes subprocess environments for isolated worktrees.
package processenv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// SocketPathEnv scopes an Oro subprocess to one dispatcher/project socket.
	SocketPathEnv = "ORO_SOCKET_PATH"
	// WorkerIDEnv identifies the managed worker that owns a subprocess tree.
	WorkerIDEnv = "ORO_WORKER_ID"
)

// WithWorkerOwnership replaces inherited ownership values with the exact
// dispatcher socket and worker ID for a managed worker subprocess.
func WithWorkerOwnership(env []string, socketPath, workerID string) []string {
	markers := WorkerOwnershipMarkers(socketPath, workerID)
	out := make([]string, 0, len(env)+len(markers))
	out = append(out, markers...)
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if ok && (key == SocketPathEnv || key == WorkerIDEnv) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// WorkerOwnershipMarkers returns the complete marker tuple required to own a
// worker subprocess. An incomplete scope intentionally produces no markers.
func WorkerOwnershipMarkers(socketPath, workerID string) []string {
	if socketPath == "" || workerID == "" {
		return nil
	}
	return []string{SocketPathEnv + "=" + socketPath, WorkerIDEnv + "=" + workerID}
}

// CommandContainsAllMarkers reports whether entries contain every exact
// ownership marker. Callers must preserve entry boundaries so marker-shaped
// text within another variable's value never proves ownership.
func CommandContainsAllMarkers(entries, markers []string) bool {
	if len(markers) == 0 {
		return false
	}
	found := make(map[string]bool, len(entries))
	for _, entry := range entries {
		found[entry] = true
	}
	for _, marker := range markers {
		if marker == "" || !found[marker] {
			return false
		}
	}
	return true
}

// ForWorkdir returns env with git worktree override variables stripped and PWD
// aligned with workdir. cmd.Dir changes the process cwd, but many nested tools
// inspect env PWD or git override variables before consulting the OS cwd.
func ForWorkdir(env []string, workdir string) []string {
	out := make([]string, 0, len(env)+11)
	pwdSet := workdir == ""
	values := envValues(env)
	cacheRoot := runtimeRoot(values["ORO_SUBPROCESS_CACHE_ROOT"], workdir, defaultCacheRoot())
	tmpRoot := runtimeRoot(values["ORO_SUBPROCESS_TMP_ROOT"], workdir, defaultTmpRoot())
	token := runtimeToken(workdir)
	rewriteGOMODCACHE := workdir != "" && pathInside(values["GOMODCACHE"], workdir)

	for _, e := range env {
		entry, keep, handledPWD := normalizeEnvEntry(e, workdir, rewriteGOMODCACHE)
		if handledPWD {
			pwdSet = true
		}
		if keep {
			out = append(out, entry)
		}
	}
	if !pwdSet {
		out = append(out, "PWD="+workdir)
	}
	if workdir != "" {
		goCache := filepath.Join(cacheRoot, token, "go-build")
		lintCache := filepath.Join(cacheRoot, token, "golangci-lint")
		uvCache := filepath.Join(cacheRoot, token, "uv")
		tmpDir := filepath.Join(tmpRoot, token)
		_ = os.MkdirAll(goCache, 0o750)
		_ = os.MkdirAll(lintCache, 0o750)
		_ = os.MkdirAll(uvCache, 0o750)
		_ = os.MkdirAll(tmpDir, 0o750)
		out = append(out,
			"GOCACHE="+goCache,
			"GOLANGCI_LINT_CACHE="+lintCache,
			"UV_CACHE_DIR="+uvCache,
			"TMPDIR="+tmpDir,
			"TMP="+tmpDir,
			"TEMP="+tmpDir,
		)
		if rewriteGOMODCACHE {
			modCache := filepath.Join(cacheRoot, token, "gomodcache")
			_ = os.MkdirAll(modCache, 0o750)
			out = append(out, "GOMODCACHE="+modCache)
		}
	}
	out = append(out,
		"GIT_EDITOR=true",
		"GIT_SEQUENCE_EDITOR=true",
		"GIT_MERGE_AUTOEDIT=no",
		"VISUAL=true",
		"EDITOR=true",
	)
	return out
}

func normalizeEnvEntry(entry, workdir string, rewriteGOMODCACHE bool) (normalized string, keep, handledPWD bool) {
	key, value, ok := strings.Cut(entry, "=")
	if !ok {
		return entry, true, false
	}
	if isGitOverrideEnv(key) {
		return "", false, false
	}
	if isInteractiveGitEditorEnv(key) {
		return "", false, false
	}
	if key == "PWD" {
		if workdir == "" {
			return "", false, true
		}
		return "PWD=" + workdir, true, true
	}
	if workdir != "" && isolatesRuntimeEnv(key, rewriteGOMODCACHE) {
		return "", false, false
	}
	if isLocaleEnv(key) && value != "" && !localeAvailable(value) {
		return key + "=C", true, false
	}
	return entry, true, false
}

func isInteractiveGitEditorEnv(key string) bool {
	switch key {
	case "GIT_EDITOR", "GIT_SEQUENCE_EDITOR", "GIT_MERGE_AUTOEDIT", "VISUAL", "EDITOR":
		return true
	default:
		return false
	}
}

func isLocaleEnv(key string) bool {
	return key == "LC_ALL" || key == "LANG"
}

func localeAvailable(locale string) bool {
	switch locale {
	case "C", "POSIX":
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "locale", "-a").Output() //nolint:gosec // fixed command and args
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == locale {
			return true
		}
	}
	return false
}

func isGitOverrideEnv(key string) bool {
	switch key {
	case "GIT_COMMON_DIR", "GIT_DIR", "GIT_INDEX_FILE", "GIT_PREFIX", "GIT_WORK_TREE":
		return true
	default:
		return false
	}
}

func isolatesRuntimeEnv(key string, rewriteGOMODCACHE bool) bool {
	switch key {
	case "GOCACHE", "GOLANGCI_LINT_CACHE", "UV_CACHE_DIR", "TMPDIR", "TMP", "TEMP":
		return true
	case "GOMODCACHE":
		return rewriteGOMODCACHE
	default:
		return false
	}
}

func envValues(env []string) map[string]string {
	values := make(map[string]string, len(env))
	for _, e := range env {
		key, value, ok := strings.Cut(e, "=")
		if ok {
			values[key] = value
		}
	}
	return values
}

func defaultCacheRoot() string {
	dir, err := os.UserCacheDir()
	if err != nil || dir == "" {
		return filepath.Join(os.TempDir(), "oro-subprocess-cache")
	}
	return filepath.Join(dir, "oro", "subprocess")
}

func defaultTmpRoot() string {
	root := os.TempDir()
	if runtime.GOOS == "darwin" {
		root = "/tmp"
	}
	return filepath.Join(root, "oro-subprocess")
}

func runtimeRoot(configured, workdir, fallback string) string {
	if configured != "" && !pathInside(configured, workdir) {
		return configured
	}
	return fallback
}

func runtimeToken(workdir string) string {
	if workdir == "" {
		return "default"
	}
	clean := filepath.Clean(workdir)
	if abs, err := filepath.Abs(clean); err == nil {
		clean = abs
	}
	sum := sha256.Sum256([]byte(clean))
	return hex.EncodeToString(sum[:])[:16]
}

func pathInside(path, root string) bool {
	if path == "" || root == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != "..")
}
