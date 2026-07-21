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

	"oro/pkg/storage"
)

// ForWorkdir returns env with git worktree override variables stripped and PWD
// aligned with workdir. cmd.Dir changes the process cwd, but many nested tools
// inspect env PWD or git override variables before consulting the OS cwd.
func ForWorkdir(env []string, workdir string) []string {
	resolvedEnv := resolveSharedCacheEnv(env, workdir)
	out := make([]string, 0, len(resolvedEnv)+11)
	pwdSet := workdir == ""
	values := envValues(resolvedEnv)
	tmpRoot := runtimeRoot(values["ORO_SUBPROCESS_TMP_ROOT"], workdir, defaultTmpRoot())
	token := runtimeToken(workdir)

	for _, e := range resolvedEnv {
		entry, keep, handledPWD := normalizeEnvEntry(e, workdir)
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
		out = append(out, sharedCacheEntries(resolvedEnv)...)
		tmpDir := filepath.Join(tmpRoot, token)
		_ = os.MkdirAll(tmpDir, 0o750)
		out = append(out,
			"TMPDIR="+tmpDir,
			"TMP="+tmpDir,
			"TEMP="+tmpDir,
		)
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

func normalizeEnvEntry(entry, workdir string) (normalized string, keep, handledPWD bool) {
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
	if workdir != "" && isolatesRuntimeEnv(key) {
		return "", false, false
	}
	if isLocaleEnv(key) && value != "" && !localeAvailable(value) {
		return key + "=C", true, false
	}
	return entry, true, false
}

// StripQualityGateEnv reports whether a KEY=VALUE entry must be dropped from a
// spawned quality-gate subprocess environment. Mutation controls and the lock
// timeout are re-derived per run; the ORO_QG_* test seams (marker mode,
// serial-lane-only, repo-root override, sleeps, regression inject) would let a
// leaked daemon environment skip the entire gate and pass with zero checks.
func StripQualityGateEnv(kv string) bool {
	key, _, ok := strings.Cut(kv, "=")
	if !ok {
		return false
	}
	switch key {
	case "ORO_SKIP_MUTATION", "ORO_RUN_MUTATION", "ORO_MUTATION_BASE",
		"ORO_QG_LOCK_TIMEOUT_SECONDS",
		"ORO_QG_PHASE_MARKER_DIR", "ORO_QG_SERIAL_LANE_ONLY",
		"ORO_QG_SERIAL_LANE_RUN_OVERRIDE", "ORO_QG_REPO_ROOT_OVERRIDE",
		"ORO_QG_MAIN_SLEEP", "ORO_QG_SERIAL_SLEEP", "ORO_QG_PROBE_ID",
		"ORO_QG_INJECT_TIMING_REGRESSION":
		return true
	default:
		return false
	}
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

func isolatesRuntimeEnv(key string) bool {
	switch key {
	case "GOCACHE", "GOMODCACHE", "GOLANGCI_LINT_CACHE", "UV_CACHE_DIR", "NPM_CONFIG_CACHE", "TMPDIR", "TMP", "TEMP":
		return true
	default:
		return false
	}
}

func resolveSharedCacheEnv(env []string, workdir string) []string {
	resolved, err := storage.ResolveCacheEnv(env, workdir, storage.StoragePolicy{})
	if err != nil {
		return env
	}
	for _, entry := range resolved.Env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !isSharedCacheEnv(key) || value == "" {
			continue
		}
		_ = os.MkdirAll(value, 0o750)
	}
	return resolved.Env
}

func isSharedCacheEnv(key string) bool {
	switch key {
	case "GOCACHE", "GOMODCACHE", "GOLANGCI_LINT_CACHE", "UV_CACHE_DIR", "NPM_CONFIG_CACHE":
		return true
	default:
		return false
	}
}

func sharedCacheEntries(env []string) []string {
	entries := make([]string, 0, 5)
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && value != "" && isSharedCacheEnv(key) {
			entries = append(entries, entry)
		}
	}
	return entries
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
