package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestCodexFullDisciplineParity(t *testing.T) {
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex CLI not installed on PATH")
	}

	projectDir := t.TempDir()
	oroHome := filepath.Join(t.TempDir(), "oro-home")
	codexHome := filepath.Join(t.TempDir(), "codex-home")
	pidPath := filepath.Join(t.TempDir(), "oro.pid")
	socketPath := fmt.Sprintf("/tmp/oro-codex-parity-%d.sock", time.Now().UnixNano())
	t.Cleanup(func() { _ = os.Remove(socketPath) })

	writeFile(t, filepath.Join(projectDir, "go.mod"), "module parity\n\ngo 1.22\n")
	writeFile(t, filepath.Join(projectDir, "sample.go"), largeGoFixture())
	writeFile(t, filepath.Join(projectDir, ".oro", "config.yaml"), `project: codex-parity
agent:
  tiers:
    balanced:
      runtime: codex
      model: gpt-5.5
      reasoning: low
`)
	runGitForCodexParity(t, projectDir, "init")
	runGitForCodexParity(t, projectDir, "config", "user.email", "oro-test@example.invalid")
	runGitForCodexParity(t, projectDir, "config", "user.name", "Oro Test")
	runGitForCodexParity(t, projectDir, "add", ".")
	runGitForCodexParity(t, projectDir, "commit", "-m", "init")

	t.Setenv("ORO_HOME", oroHome)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("ORO_PID_PATH", pidPath)
	t.Setenv("ORO_SOCKET_PATH", socketPath)
	t.Setenv("ORO_DB_PATH", filepath.Join(t.TempDir(), "state.db"))
	t.Setenv("ORO_BEAD_SOURCE", "sqlite")
	t.Setenv("ORO_DAEMON_SKIP_PREFLIGHT", "1")
	buildSearchHookForCodexParity(t, filepath.Join(oroHome, "hooks", "oro-search-hook"))
	t.Setenv("HOME", t.TempDir())

	origRunDaemonOnly := runDaemonOnlyFn
	t.Cleanup(func() { runDaemonOnlyFn = origRunDaemonOnly })
	var capturedStart startCapture
	runDaemonOnlyFn = func(cmd *cobra.Command, pidPath string, workers, maxWorkers int, progressTimeout, opsReviewTimeout, reviewStallTimeout time.Duration, manualIntegration bool, baseBranch string, webEnabled bool, webAddr string) error {
		capturedStart = startCapture{workers: workers, maxWorkers: maxWorkers}
		return WritePIDFile(pidPath, os.Getpid())
	}

	withChdir(t, projectDir, func() {
		root := newRootCmd()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs([]string{"start", "--daemon-only", "--workers", "0", "--max-workers", "0"})
		if err := root.Execute(); err != nil {
			t.Fatalf("oro start with Codex balanced tier failed: %v\n%s", err, out.String())
		}
	})
	if capturedStart.workers != 0 || capturedStart.maxWorkers != 0 {
		t.Fatalf("oro start did not reach daemon launch path: %+v", capturedStart)
	}

	assertFileExists(t, filepath.Join(projectDir, "AGENTS.md"))
	assertFileContains(t, filepath.Join(codexHome, "rules", "oro.rules"), "prefix_rule")

	hooks := listCodexHooks(t, codexHome, projectDir)
	assertCodexHookEvents(t, hooks, []string{"SessionStart", "PreToolUse", "PostToolUse", "Stop"})
	assertCodexHookCommandContains(t, hooks, "SessionStart", "session_start_global.py")
	assertCodexHookCommandContains(t, hooks, "PreToolUse", "enforce_skills.py")
	assertCodexHookCommandContains(t, hooks, "PreToolUse", "oro-search-hook")
	assertCodexHookCommandContains(t, hooks, "PostToolUse", "prompt_injection_guard.py")
	assertCodexHookCommandContains(t, hooks, "PostToolUse", "auto-format.sh")
	assertCodexHookCommandContains(t, hooks, "Stop", "stop-checklist.sh")

	fireHookFixture(t, filepath.Join(oroHome, "hooks", "session_start_global.py"), codexSessionStartPayload(projectDir))
	fireHookFixture(t, filepath.Join(oroHome, "hooks", "enforce_skills.py"), codexPreToolUsePayload(projectDir, "Bash"))
	fireHookFixture(t, filepath.Join(oroHome, "hooks", "prompt_injection_guard.py"), codexPostToolUsePayload(projectDir, "Bash"))
	fireHookFixture(t, filepath.Join(oroHome, "hooks", "stop-checklist.sh"), codexStopPayload(projectDir))

	searchOut := runSearchHookFixture(t, filepath.Join(oroHome, "hooks", "oro-search-hook"), filepath.Join(projectDir, "sample.go"))
	if !strings.Contains(searchOut, `"permissionDecision":"deny"`) {
		t.Fatalf("oro-search-hook must deny full Read-equivalent with AST summary, got %s", searchOut)
	}
	if !strings.Contains(searchOut, "func Exported") {
		t.Fatalf("oro-search-hook summary missing AST function outline, got %s", searchOut)
	}
}

type startCapture struct {
	workers    int
	maxWorkers int
}

type codexHookMetadata struct {
	EventName string `json:"eventName"`
	Matcher   string `json:"matcher"`
	Command   string `json:"command"`
	Enabled   bool   `json:"enabled"`
}

func listCodexHooks(t *testing.T, codexHome, cwd string) []codexHookMetadata {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := startCodexAppServer(ctx, t, codexHome)
	defer client.close()

	client.request(t, "initialize", map[string]any{
		"clientInfo": map[string]any{"name": "oro-codex-parity-test", "version": "0"},
		"capabilities": map[string]any{
			"experimentalApi": true,
		},
	})
	resp := client.request(t, "hooks/list", map[string]any{"cwds": []string{cwd}})

	var decoded struct {
		Data []struct {
			Hooks []codexHookMetadata `json:"hooks"`
		} `json:"data"`
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal hooks/list response: %v", err)
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode hooks/list response: %v\n%s", err, raw)
	}
	if len(decoded.Data) != 1 {
		t.Fatalf("hooks/list returned %d cwd entries, want 1: %s", len(decoded.Data), raw)
	}
	return decoded.Data[0].Hooks
}

type codexRPCClient struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	id     int
}

func startCodexAppServer(ctx context.Context, t *testing.T, codexHome string) *codexRPCClient {
	t.Helper()
	cmd := exec.CommandContext(ctx, "codex", "app-server", "--listen", "stdio://") //nolint:gosec // fixed test command
	cmd.Env = append(os.Environ(), "CODEX_HOME="+codexHome)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("codex stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("codex stdout pipe: %v", err)
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start codex app-server: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if stderr.Len() > 0 && !strings.Contains(stderr.String(), "setlocale") {
			t.Logf("codex app-server stderr:\n%s", stderr.String())
		}
	})
	return &codexRPCClient{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}
}

func (c *codexRPCClient) request(t *testing.T, method string, params any) map[string]any {
	t.Helper()
	c.id++
	payload := map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method, "params": params}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal %s request: %v", method, err)
	}
	if _, err := fmt.Fprintln(c.stdin, string(data)); err != nil {
		t.Fatalf("write %s request: %v", method, err)
	}
	for {
		line, err := c.stdout.ReadBytes('\n')
		if err != nil {
			t.Fatalf("read %s response: %v", method, err)
		}
		var msg map[string]any
		if err := json.Unmarshal(line, &msg); err != nil {
			t.Fatalf("decode codex rpc line %q: %v", line, err)
		}
		if gotID, ok := msg["id"].(float64); !ok || int(gotID) != c.id {
			continue
		}
		if rpcErr, ok := msg["error"]; ok {
			t.Fatalf("codex %s returned error: %#v", method, rpcErr)
		}
		result, ok := msg["result"].(map[string]any)
		if !ok {
			t.Fatalf("codex %s result = %#v, want object", method, msg["result"])
		}
		return result
	}
}

func (c *codexRPCClient) close() {
	_ = c.stdin.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	_ = c.cmd.Wait()
}

func assertCodexHookEvents(t *testing.T, hooks []codexHookMetadata, events []string) {
	t.Helper()
	got := map[string]bool{}
	for _, hook := range hooks {
		if hook.Enabled {
			got[canonicalCodexHookEvent(hook.EventName)] = true
		}
	}
	for _, event := range events {
		if !got[event] {
			t.Fatalf("hooks/list missing enabled %s hook; got %#v", event, hooks)
		}
	}
}

func assertCodexHookCommandContains(t *testing.T, hooks []codexHookMetadata, event, commandPart string) {
	t.Helper()
	if slices.ContainsFunc(hooks, func(h codexHookMetadata) bool {
		return h.Enabled && canonicalCodexHookEvent(h.EventName) == event && strings.Contains(h.Command, commandPart)
	}) {
		return
	}
	t.Fatalf("hooks/list missing enabled %s command containing %q; got %#v", event, commandPart, hooks)
}

func canonicalCodexHookEvent(event string) string {
	switch event {
	case "sessionStart", "SessionStart":
		return "SessionStart"
	case "preToolUse", "PreToolUse":
		return "PreToolUse"
	case "postToolUse", "PostToolUse":
		return "PostToolUse"
	case "stop", "Stop":
		return "Stop"
	default:
		return event
	}
}

func fireHookFixture(t *testing.T, path string, payload string) {
	t.Helper()
	assertFileExists(t, path)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path) //nolint:gosec // path is test-generated hook path
	if strings.HasSuffix(path, ".py") {
		cmd = exec.CommandContext(ctx, "python3", path) //nolint:gosec // path is test-generated hook path
	}
	cmd.Stdin = strings.NewReader(payload)
	cmd.Env = append(os.Environ(), "ORO_HOOK_PARITY_TEST=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hook fixture %s failed: %v\n%s", path, err, out)
	}
}

func runSearchHookFixture(t *testing.T, hookPath, targetPath string) string {
	t.Helper()
	assertFileExists(t, hookPath)
	payload := fmt.Sprintf(`{"tool_name":"str_replace_based_edit_tool","tool_input":{"command":"view","path":%q}}`, targetPath)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, hookPath) //nolint:gosec // path is test-generated hook path
	cmd.Stdin = strings.NewReader(payload)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("oro-search-hook fixture failed: %v\n%s", err, out)
	}
	return string(out)
}

func codexSessionStartPayload(cwd string) string {
	return fmt.Sprintf(`{"hook_event_name":"SessionStart","cwd":%q,"session_id":"oro-test","transcript_path":%q}`, cwd, filepath.Join(cwd, ".oro", "codex.jsonl"))
}

func codexPreToolUsePayload(cwd, tool string) string {
	return fmt.Sprintf(`{"hook_event_name":"PreToolUse","cwd":%q,"tool_name":%q,"tool_input":{"command":"true"}}`, cwd, tool)
}

func codexPostToolUsePayload(cwd, tool string) string {
	return fmt.Sprintf(`{"hook_event_name":"PostToolUse","cwd":%q,"tool_name":%q,"tool_input":{"command":"true"},"tool_response":{"stdout":"ok"}}`, cwd, tool)
}

func codexStopPayload(cwd string) string {
	return fmt.Sprintf(`{"hook_event_name":"Stop","cwd":%q,"session_id":"oro-test"}`, cwd)
}

func largeGoFixture() string {
	var b strings.Builder
	b.WriteString("package parity\n\n")
	for i := 0; i < 140; i++ {
		fmt.Fprintf(&b, "func Exported%d() int { return %d }\n\n", i, i)
	}
	return b.String()
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func runGitForCodexParity(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...) //nolint:gosec // fixed test helper command
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func buildSearchHookForCodexParity(t *testing.T, outPath string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		t.Fatalf("mkdir search hook dir: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	cmd := exec.Command("go", "build", "-o", outPath, "./cmd/oro-search-hook") //nolint:gosec // fixed test build command
	cmd.Dir = walkUpForGoMod(cwd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build oro-search-hook fixture: %v\n%s", err, out)
	}
}

func assertFileExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(data), want) {
		t.Fatalf("%s missing %q:\n%s", path, want, data)
	}
}

func withChdir(t *testing.T, dir string, fn func()) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	defer func() {
		if err := os.Chdir(old); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("restore cwd %s: %v", old, err)
		}
	}()
	fn()
}
