// Package processenv normalizes subprocess environments for isolated worktrees.
package processenv

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// ForWorkdir returns env with git worktree override variables stripped and PWD
// aligned with workdir. cmd.Dir changes the process cwd, but many nested tools
// inspect env PWD or git override variables before consulting the OS cwd.
func ForWorkdir(env []string, workdir string) []string {
	out := make([]string, 0, len(env)+6)
	pwdSet := workdir == ""
	values := envValues(env)
	cacheRoot := runtimeRoot(values["ORO_SUBPROCESS_CACHE_ROOT"], workdir, defaultCacheRoot())
	tmpRoot := runtimeRoot(values["ORO_SUBPROCESS_TMP_ROOT"], workdir, filepath.Join(os.TempDir(), "oro-subprocess"))
	token := runtimeToken(workdir)
	rewriteGOMODCACHE := workdir != "" && pathInside(values["GOMODCACHE"], workdir)

	for _, e := range env {
		key, _, ok := strings.Cut(e, "=")
		if !ok {
			out = append(out, e)
			continue
		}
		if isGitOverrideEnv(key) {
			continue
		}
		if key == "PWD" {
			if workdir != "" {
				out = append(out, "PWD="+workdir)
			}
			pwdSet = true
			continue
		}
		if workdir != "" && isolatesRuntimeEnv(key, rewriteGOMODCACHE) {
			continue
		}
		out = append(out, e)
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
	return out
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
