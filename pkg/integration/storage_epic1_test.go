package integration_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/processenv"
)

// TestStorageSharedCacheEndToEnd proves that sibling worktrees share only
// external tool caches, while retaining distinct subprocess scratch roots.
func TestStorageSharedCacheEndToEnd(t *testing.T) {
	fixture := t.TempDir()
	worktreeA := filepath.Join(fixture, "worktree-a")
	worktreeB := filepath.Join(fixture, "worktree-b")
	for _, worktree := range []string{worktreeA, worktreeB} {
		if err := os.MkdirAll(worktree, 0o750); err != nil {
			t.Fatalf("create worktree %q: %v", worktree, err)
		}
	}

	home := filepath.Join(fixture, "home")
	if err := os.MkdirAll(home, 0o750); err != nil {
		t.Fatalf("create fixture home: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(fixture, "shared-cache"))
	baseEnv := []string{"PATH=/bin"}
	first := integrationEnvMap(processenv.ForWorkdir(baseEnv, worktreeA))
	second := integrationEnvMap(processenv.ForWorkdir(baseEnv, worktreeB))

	for _, key := range []string{"GOCACHE", "GOMODCACHE", "UV_CACHE_DIR", "GOLANGCI_LINT_CACHE"} {
		if first[key] == "" || first[key] != second[key] {
			t.Fatalf("%s is not shared: first=%q second=%q", key, first[key], second[key])
		}
		if integrationPathInside(first[key], worktreeA) || integrationPathInside(second[key], worktreeB) {
			t.Fatalf("%s points into a worktree: first=%q second=%q", key, first[key], second[key])
		}
		if !integrationPathInside(first[key], fixture) || !integrationPathInside(second[key], fixture) {
			t.Fatalf("%s escaped fixture: first=%q second=%q fixture=%q", key, first[key], second[key], fixture)
		}
	}
	if first["TMPDIR"] == second["TMPDIR"] {
		t.Fatalf("TMPDIR unexpectedly shared: %q", first["TMPDIR"])
	}

	proof := filepath.Join(first["GOCACHE"], "shared-proof")
	if err := os.WriteFile(proof, []byte("cache-hit"), 0o600); err != nil {
		t.Fatalf("write shared cache proof: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(second["GOCACHE"], "shared-proof"))
	if err != nil {
		t.Fatalf("read sibling cache proof: %v", err)
	}
	if string(got) != "cache-hit" {
		t.Fatalf("shared cache proof = %q, want cache-hit", got)
	}
}

// TestStorageStandalonePolicyParity proves standalone worktree normalization
// applies the same cache policy for each sibling worktree.
func TestStorageStandalonePolicyParity(t *testing.T) {
	fixture := t.TempDir()
	worktreeA := filepath.Join(fixture, "standalone-a")
	worktreeB := filepath.Join(fixture, "standalone-b")
	for _, worktree := range []string{worktreeA, worktreeB} {
		if err := os.MkdirAll(worktree, 0o750); err != nil {
			t.Fatalf("create worktree %q: %v", worktree, err)
		}
	}
	home := filepath.Join(fixture, "home")
	if err := os.MkdirAll(home, 0o750); err != nil {
		t.Fatalf("create fixture home: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(fixture, "cache"))
	baseEnv := []string{"PATH=/bin"}
	first := integrationEnvMap(processenv.ForWorkdir(baseEnv, worktreeA))
	second := integrationEnvMap(processenv.ForWorkdir(baseEnv, worktreeB))
	for _, key := range []string{"GOCACHE", "GOMODCACHE", "UV_CACHE_DIR", "GOLANGCI_LINT_CACHE"} {
		if first[key] != second[key] {
			t.Errorf("standalone %s differs across worktrees: %q != %q", key, first[key], second[key])
		}
		if !integrationPathInside(first[key], fixture) || !integrationPathInside(second[key], fixture) {
			t.Errorf("standalone %s escaped fixture: first=%q second=%q fixture=%q", key, first[key], second[key], fixture)
		}
	}
}

// TestStorageCLIAndHealthWiring proves the compiled CLI exposes both storage
// status and health storage fields with an entirely isolated Oro home.
func TestStorageCLIAndHealthWiring(t *testing.T) {
	bin := buildOroBinary(t)
	fixture := t.TempDir()
	oroHome := filepath.Join(fixture, "oro-home")
	if err := os.MkdirAll(oroHome, 0o750); err != nil {
		t.Fatalf("create isolated Oro home: %v", err)
	}
	env := append(os.Environ(),
		"ORO_HOME="+oroHome,
		"HOME="+filepath.Join(fixture, "home"),
		"XDG_CACHE_HOME="+filepath.Join(fixture, "cache"),
		"ORO_PID_PATH="+filepath.Join(fixture, "oro.pid"),
		"ORO_SOCKET_PATH="+filepath.Join(fixture, "oro.sock"),
		"ORO_DB_PATH="+filepath.Join(fixture, "state.db"),
	)
	for _, args := range [][]string{{"storage", "status", "--json"}, {"health", "--json"}} {
		cmd := exec.Command(bin, args...) //nolint:gosec // test-owned binary and constant arguments
		cmd.Env = env
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("oro %s: %v\n%s", strings.Join(args, " "), err, output)
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(output, &payload); err != nil {
			t.Fatalf("oro %s emitted invalid JSON: %v\n%s", strings.Join(args, " "), err, output)
		}
		if len(payload) == 0 {
			t.Fatalf("oro %s emitted empty JSON", strings.Join(args, " "))
		}
		if args[0] == "health" {
			var metrics map[string]json.RawMessage
			if err := json.Unmarshal(payload["metrics"], &metrics); err != nil || len(metrics["storage"]) == 0 {
				t.Fatalf("health output missing metrics.storage field: %s", output)
			}
		}
	}
}

func integrationEnvMap(env []string) map[string]string {
	values := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	return values
}

func integrationPathInside(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
