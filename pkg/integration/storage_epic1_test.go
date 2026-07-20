package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	baseEnv := []string{
		"PATH=/bin",
		"ORO_SUBPROCESS_TMP_ROOT=" + filepath.Join(fixture, "tmp"),
	}
	firstEnv := processenv.ForWorkdir(baseEnv, worktreeA)
	secondEnv := processenv.ForWorkdir(baseEnv, worktreeB)
	first := integrationEnvMap(firstEnv)
	second := integrationEnvMap(secondEnv)

	for _, key := range []string{"GOCACHE", "GOMODCACHE", "UV_CACHE_DIR", "GOLANGCI_LINT_CACHE", "NPM_CONFIG_CACHE"} {
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
	for _, tmpDir := range []string{first["TMPDIR"], second["TMPDIR"]} {
		if !integrationPathInside(tmpDir, fixture) {
			t.Fatalf("TMPDIR escaped fixture: path=%q fixture=%q", tmpDir, fixture)
		}
	}

	integrationWriteGoCacheFixture(t, worktreeA, "alpha")
	integrationWriteGoCacheFixture(t, worktreeB, "beta")
	initial := integrationRunGoCacheFixtures(t, []integrationGoCacheRun{
		{worktree: worktreeA, env: firstEnv, value: "alpha"},
		{worktree: worktreeB, env: secondEnv, value: "beta"},
	})
	for i, output := range initial {
		if strings.Contains(output, "(cached)") {
			t.Fatalf("initial conflicting cache run %d unexpectedly reused stale results:\n%s", i, output)
		}
	}

	reused := integrationRunGoCacheFixtures(t, []integrationGoCacheRun{
		{worktree: worktreeA, env: firstEnv, value: "alpha"},
		{worktree: worktreeB, env: secondEnv, value: "beta"},
	})
	for i, output := range reused {
		if !strings.Contains(output, "(cached)") {
			t.Fatalf("shared Go cache run %d did not report reuse:\n%s", i, output)
		}
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
	baseEnv := []string{
		"PATH=/bin",
		"ORO_SUBPROCESS_TMP_ROOT=" + filepath.Join(fixture, "tmp"),
	}
	first := integrationEnvMap(processenv.ForWorkdir(baseEnv, worktreeA))
	second := integrationEnvMap(processenv.ForWorkdir(baseEnv, worktreeB))
	for _, key := range []string{"GOCACHE", "GOMODCACHE", "UV_CACHE_DIR", "GOLANGCI_LINT_CACHE", "NPM_CONFIG_CACHE"} {
		if first[key] != second[key] {
			t.Errorf("standalone %s differs across worktrees: %q != %q", key, first[key], second[key])
		}
		if !integrationPathInside(first[key], fixture) || !integrationPathInside(second[key], fixture) {
			t.Errorf("standalone %s escaped fixture: first=%q second=%q fixture=%q", key, first[key], second[key], fixture)
		}
	}
	if first["TMPDIR"] == second["TMPDIR"] {
		t.Fatalf("standalone TMPDIR unexpectedly shared: %q", first["TMPDIR"])
	}
	for _, tmpDir := range []string{first["TMPDIR"], second["TMPDIR"]} {
		if !integrationPathInside(tmpDir, fixture) {
			t.Fatalf("standalone TMPDIR escaped fixture: path=%q fixture=%q", tmpDir, fixture)
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

func TestStorageEpic1WrapperIsolatesToolCaches(t *testing.T) {
	root := integrationProjectRoot(t)
	fakeBin := t.TempDir()
	tracePath := filepath.Join(t.TempDir(), "cache-env.trace")
	operatorRoot := t.TempDir()
	wrapperTmp := t.TempDir()

	fakeGit := "#!/bin/sh\nprintf '%s\\n' main\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "git"), []byte(fakeGit), 0o750); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	fakeGo := `#!/bin/sh
printf '%s\n' "$HOME|$XDG_CACHE_HOME|$GOCACHE|$GOMODCACHE|$UV_CACHE_DIR|$GOLANGCI_LINT_CACHE|$NPM_CONFIG_CACHE|$TMPDIR|$ORO_SUBPROCESS_TMP_ROOT" >> "$STORAGE_ENV_TRACE"
if [ ! -e "$GOMODCACHE/readonly/module.go" ]; then
	mkdir -p "$GOMODCACHE/readonly"
	: > "$GOMODCACHE/readonly/module.go"
	chmod 400 "$GOMODCACHE/readonly/module.go"
	chmod 500 "$GOMODCACHE/readonly"
fi
for name in TestStorageSharedCacheEndToEnd TestStorageStandalonePolicyParity TestStorageCLIAndHealthWiring; do
	case "$*" in
		*"$name"*) printf '=== RUN   %s\n--- PASS: %s (0.00s)\nPASS\n' "$name" "$name"; exit 0 ;;
	esac
done
exit 1
`
	if err := os.WriteFile(filepath.Join(fakeBin, "go"), []byte(fakeGo), 0o750); err != nil {
		t.Fatalf("write fake go: %v", err)
	}

	cmd := exec.Command("bash", filepath.Join(root, "scripts", "test_storage_epic1_shared_cache.sh")) //nolint:gosec // repository-owned acceptance script
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"PATH="+fakeBin+":/usr/bin:/bin",
		"HOME="+filepath.Join(operatorRoot, "home"),
		"XDG_CACHE_HOME="+filepath.Join(operatorRoot, "cache"),
		"GOCACHE="+filepath.Join(operatorRoot, "go-build"),
		"GOMODCACHE="+filepath.Join(operatorRoot, "go-mod"),
		"UV_CACHE_DIR="+filepath.Join(operatorRoot, "uv"),
		"GOLANGCI_LINT_CACHE="+filepath.Join(operatorRoot, "golangci-lint"),
		"NPM_CONFIG_CACHE="+filepath.Join(operatorRoot, "npm"),
		"TMPDIR="+wrapperTmp,
		"STORAGE_ENV_TRACE="+tracePath,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run epic 1 wrapper: %v\n%s", err, output)
	}
	if !strings.HasSuffix(strings.TrimSpace(string(output)), "STORAGE_EPIC1_PASS") {
		t.Fatalf("wrapper output missing final sentinel:\n%s", output)
	}

	trace, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("read cache environment trace: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(trace)), "\n")
	if len(lines) != 3 {
		t.Fatalf("cache environment trace lines = %d, want 3: %q", len(lines), trace)
	}
	for _, line := range lines {
		for _, path := range strings.Split(line, "|") {
			if path == "" || !integrationPathInside(path, wrapperTmp) {
				t.Fatalf("wrapper cache path escaped isolated root %q: %q", wrapperTmp, path)
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

type integrationGoCacheRun struct {
	worktree string
	env      []string
	value    string
}

func integrationWriteGoCacheFixture(t *testing.T, worktree, value string) {
	t.Helper()
	files := map[string]string{
		"go.mod":   "module example.com/oro/sharedcachefixture\n\ngo 1.26\n",
		"value.go": "package sharedcachefixture\n\nfunc Value() string { return \"" + value + "\" }\n",
		"value_test.go": "package sharedcachefixture\n\nimport \"testing\"\n\nfunc TestValue(t *testing.T) {\n" +
			"\tif got := Value(); got != \"" + value + "\" { t.Fatalf(\"Value() = %q\", got) }\n" +
			"\tt.Log(\"fixture-value=" + value + "\")\n}\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(worktree, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write Go cache fixture %s: %v", name, err)
		}
	}
}

func integrationRunGoCacheFixtures(t *testing.T, runs []integrationGoCacheRun) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	commands := make([]*exec.Cmd, len(runs))
	outputs := make([]bytes.Buffer, len(runs))
	for i, run := range runs {
		cmd := exec.CommandContext(ctx, "go", "test", "-v", ".") //nolint:gosec // fixed tool and arguments
		cmd.Dir = run.worktree
		cmd.Env = run.env
		cmd.Stdout = &outputs[i]
		cmd.Stderr = &outputs[i]
		if err := cmd.Start(); err != nil {
			t.Fatalf("start Go cache fixture %q: %v", run.value, err)
		}
		commands[i] = cmd
	}

	result := make([]string, len(runs))
	for i, cmd := range commands {
		err := cmd.Wait()
		result[i] = outputs[i].String()
		if err != nil {
			t.Fatalf("run Go cache fixture %q: %v\n%s", runs[i].value, err, result[i])
		}
		if !strings.Contains(result[i], "fixture-value="+runs[i].value) {
			t.Fatalf("Go cache fixture %q returned contaminated output:\n%s", runs[i].value, result[i])
		}
	}
	return result
}

func integrationPathInside(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
