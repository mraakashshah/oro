package codex_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"oro/pkg/agentruntime"
	codexruntime "oro/pkg/agentruntime/codex"
	"oro/pkg/ops"
	"oro/pkg/protocol"
	"oro/pkg/worker"
)

func TestCodexRuntimeImplementsInterface(t *testing.T) {
	var _ agentruntime.Runtime = (*codexruntime.Runtime)(nil)
}

func TestCodexRuntimeDescriptors(t *testing.T) {
	runtime := codexruntime.NewRuntime()

	if got := runtime.ID(); got != agentruntime.RuntimeIDCodex {
		t.Fatalf("ID() = %q, want %q", got, agentruntime.RuntimeIDCodex)
	}
	if got := runtime.StreamFormat(); got != agentruntime.StreamFormatLineText {
		t.Fatalf("StreamFormat() = %q, want %q", got, agentruntime.StreamFormatLineText)
	}
	if runtime.SupportsHooks() {
		t.Fatal("SupportsHooks() = true, want false")
	}
	if runtime.SupportsProjectSkillInstall() {
		t.Fatal("SupportsProjectSkillInstall() = true, want false")
	}
	for _, tc := range []struct {
		role string
		tier protocol.Tier
	}{
		{role: "", tier: ""},
		{role: "worker", tier: protocol.TierFast},
		{role: "spec_challenger", tier: protocol.TierDeep},
	} {
		if got := runtime.DefaultTierModel(tc.role, tc.tier); got != "" {
			t.Fatalf("DefaultTierModel(%q, %q) = %q, want empty string", tc.role, tc.tier, got)
		}
	}
	if got := runtime.InstructionLayout(); !reflect.DeepEqual(got, agentruntime.InstructionLayout{}) {
		t.Fatalf("InstructionLayout() = %#v, want zero value", got)
	}
}

func TestCodexRuntimeSpawnContract(t *testing.T) {
	t.Parallel()

	args := codexruntimeTestBuildExecArgs("gpt-5.5", "finish the bead")
	want := []string{
		"exec",
		"--skip-git-repo-check",
		"--sandbox", "workspace-write",
		"--model", "gpt-5.5",
		"finish the bead",
	}
	if len(args) != len(want) {
		t.Fatalf("args len = %d, want %d: %v", len(args), len(want), args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q (full args: %v)", i, args[i], want[i], args)
		}
	}

	spawner := codexruntime.NewWorkerSpawner()
	if got := spawner.StreamFormat(); got != worker.StreamFormatLineText {
		t.Fatalf("StreamFormat() = %q, want %q", got, worker.StreamFormatLineText)
	}

	withoutModel := codexruntimeTestBuildExecArgs("", "plain prompt")
	joined := strings.Join(withoutModel, " ")
	if strings.Contains(joined, "--json") {
		t.Fatalf("Codex v1 worker contract must default to plain text, got args %v", withoutModel)
	}
	if strings.Contains(joined, "claude") {
		t.Fatalf("Codex contract must not contain Claude-specific flags or paths, got args %v", withoutModel)
	}
}

func TestCodexWorkerSpawnerSetsPWDToWorkdir(t *testing.T) {
	t.Setenv("PWD", "/wrong/root")
	t.Setenv("GIT_DIR", "/wrong/root/.git")
	workdir := t.TempDir()
	binDir := t.TempDir()
	report := filepath.Join(t.TempDir(), "pwd.txt")
	fakeCodex := filepath.Join(binDir, "codex")
	script := "#!/bin/sh\nprintf '%s|%s' \"$PWD\" \"${GIT_DIR-unset}\" > \"$ORO_TEST_REPORT\"\n"
	if err := os.WriteFile(fakeCodex, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ORO_TEST_REPORT", report)

	spawner := codexruntime.NewWorkerSpawner()
	proc, stdout, _, err := spawner.Spawn(context.Background(), "gpt-5.5", "finish the bead", workdir)
	if err != nil {
		t.Fatalf("Spawn() error = %v", err)
	}
	defer stdout.Close()
	if err := proc.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	gotBytes, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if got := string(gotBytes); got != workdir+"|unset" {
		t.Fatalf("env report = %q, want %q", got, workdir+"|unset")
	}
}

func TestCodexWorkerSpawnerAddsGitDirsForWorktree(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "oro-test@example.invalid")
	runGit(t, repo, "config", "user.name", "Oro Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	worktree := filepath.Join(t.TempDir(), "wt")
	runGit(t, repo, "worktree", "add", "-b", "agent/test", worktree)

	binDir := t.TempDir()
	report := filepath.Join(t.TempDir(), "args.txt")
	fakeCodex := filepath.Join(binDir, "codex")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ORO_TEST_ARGS\"\n"
	if err := os.WriteFile(fakeCodex, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ORO_TEST_ARGS", report)

	spawner := codexruntime.NewWorkerSpawner()
	proc, stdout, _, err := spawner.Spawn(context.Background(), "gpt-5.5", "finish the bead", worktree)
	if err != nil {
		t.Fatalf("Spawn() error = %v", err)
	}
	defer stdout.Close()
	if err := proc.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}

	gotBytes, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	gotArgs := strings.Split(strings.TrimSpace(string(gotBytes)), "\n")
	wantCommonDir := strings.TrimSpace(runGitOutput(t, worktree, "rev-parse", "--path-format=absolute", "--git-common-dir"))
	wantGitDir := strings.TrimSpace(runGitOutput(t, worktree, "rev-parse", "--path-format=absolute", "--git-dir"))
	for _, wantDir := range []string{wantCommonDir, wantGitDir} {
		if !argPairPresent(gotArgs, "--add-dir", wantDir) {
			t.Fatalf("codex args missing --add-dir %q: %v", wantDir, gotArgs)
		}
	}
}

func TestCodexWorkerSpawnerUsesFullAccessSandbox(t *testing.T) {
	workdir := t.TempDir()
	binDir := t.TempDir()
	report := filepath.Join(t.TempDir(), "args.txt")
	fakeCodex := filepath.Join(binDir, "codex")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ORO_TEST_ARGS\"\n"
	if err := os.WriteFile(fakeCodex, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ORO_TEST_ARGS", report)

	spawner := codexruntime.NewWorkerSpawner()
	proc, stdout, _, err := spawner.Spawn(context.Background(), "gpt-5.5", "finish the bead", workdir)
	if err != nil {
		t.Fatalf("Spawn() error = %v", err)
	}
	defer stdout.Close()
	if err := proc.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}

	gotBytes, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	gotArgs := strings.Split(strings.TrimSpace(string(gotBytes)), "\n")
	if !argPairPresent(gotArgs, "--sandbox", "danger-full-access") {
		t.Fatalf("codex worker args must use full-access sandbox for git/state writes, got %v", gotArgs)
	}
}

func TestCodexWorkerSpawnerStreamsPromptViaStdin(t *testing.T) {
	t.Setenv("ORO_HOME", "")
	t.Setenv("ORO_PROJECT", "")
	workdir := t.TempDir()
	binDir := t.TempDir()
	argsReport := filepath.Join(t.TempDir(), "args.txt")
	stdinReport := filepath.Join(t.TempDir(), "stdin.txt")
	fakeCodex := filepath.Join(binDir, "codex")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ORO_TEST_ARGS\"\ncat > \"$ORO_TEST_STDIN\"\n"
	if err := os.WriteFile(fakeCodex, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ORO_TEST_ARGS", argsReport)
	t.Setenv("ORO_TEST_STDIN", stdinReport)

	validPrompt := "first line\n第二行\nlast line\n" + strings.Repeat("x", 1_500_000)
	invalidPrompt := "before\n" + string([]byte{0xff}) + "\nafter"
	for _, tc := range []struct {
		name   string
		prompt string
		want   string
	}{
		{name: "large valid multiline", prompt: validPrompt, want: validPrompt},
		{name: "invalid UTF-8", prompt: invalidPrompt, want: "before\n�\nafter"},
		{name: "empty", prompt: "", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spawner := codexruntime.NewWorkerSpawner()
			proc, stdout, _, err := spawner.Spawn(context.Background(), "gpt-5.5", tc.prompt, workdir)
			if err != nil {
				t.Fatalf("Spawn() error = %v", err)
			}
			defer stdout.Close()
			if err := proc.Wait(); err != nil {
				t.Fatalf("Wait() error = %v", err)
			}

			gotArgs, err := os.ReadFile(argsReport)
			if err != nil {
				t.Fatalf("read args: %v", err)
			}
			if got := bytes.Count(gotArgs, []byte("-\n")); got != 1 {
				t.Fatalf("literal dash argv count = %d, want 1: %q", got, gotArgs)
			}
			if tc.want != "" && bytes.Contains(gotArgs, []byte(tc.want)) {
				t.Fatal("assembled prompt unexpectedly present in argv")
			}
			gotStdin, err := os.ReadFile(stdinReport)
			if err != nil {
				t.Fatalf("read stdin: %v", err)
			}
			if got := string(gotStdin); got != tc.want {
				t.Fatalf("stdin = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildBootstrapPromptInstructionSources(t *testing.T) {
	prompt := "preserve\nall task bytes\n"
	localInstructions := "\n  # Local instructions\n\nFollow local rules.\n"
	homeInstructions := "\n  # Home instructions\n\nFollow home rules.\n"

	for _, tc := range []struct {
		name              string
		localInstructions string
		homeInstructions  string
		want              string
	}{
		{
			name:              "worktree local instructions prepend",
			localInstructions: localInstructions,
			want:              "# Local instructions\n\nFollow local rules.\n\n## Task\n\n" + prompt,
		},
		{
			name:             "home instructions are fallback",
			homeInstructions: homeInstructions,
			want:             "# Home instructions\n\nFollow home rules.\n\n## Task\n\n" + prompt,
		},
		{
			name:              "worktree instructions take precedence over home",
			localInstructions: localInstructions,
			homeInstructions:  homeInstructions,
			want:              "# Local instructions\n\nFollow local rules.\n\n## Task\n\n" + prompt,
		},
		{
			name: "neither source leaves prompt unchanged",
			want: prompt,
		},
		{
			name:              "blank sources leave prompt unchanged",
			localInstructions: " \n\t ",
			homeInstructions:  "\n",
			want:              prompt,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workdir := t.TempDir()
			oroHome := t.TempDir()
			t.Setenv("ORO_HOME", oroHome)
			if tc.localInstructions != "" {
				if err := os.WriteFile(filepath.Join(workdir, "ORO_AGENT.md"), []byte(tc.localInstructions), 0o644); err != nil {
					t.Fatalf("write worktree instructions: %v", err)
				}
			}
			if tc.homeInstructions != "" {
				if err := os.WriteFile(filepath.Join(oroHome, "ORO_AGENT.md"), []byte(tc.homeInstructions), 0o644); err != nil {
					t.Fatalf("write home instructions: %v", err)
				}
			}

			if got := codexruntime.BuildBootstrapPrompt(prompt, workdir); got != tc.want {
				t.Fatalf("BuildBootstrapPrompt() = %q, want %q", got, tc.want)
			}
		})
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...) //nolint:gosec // test helper uses fixed git binary
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func runGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...) //nolint:gosec // test helper uses fixed git binary
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v failed: %v", args, err)
	}
	return string(out)
}

func argPairPresent(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

func codexruntimeTestBuildExecArgs(model, prompt string) []string {
	args := []string{"exec", "--skip-git-repo-check", "--sandbox", "workspace-write"}
	if model != "" {
		args = append(args, "--model", model)
	}
	return append(args, prompt)
}

// TestCodexUnsupportedModel verifies that when the Codex CLI exits with an
// unsupported-model error whose output contains "approved" incidentally
// (e.g. "model not approved for Codex"), the ops spawner returns VerdictFailed
// and does NOT misread the error text as an approval verdict.
func TestCodexUnsupportedModel(t *testing.T) {
	// Use sh -c to simulate a Codex process that exits nonzero with an error
	// message whose text happens to contain "approved" (the failure scenario).
	spawner := ops.NewExecSpawner(ops.RuntimeSpec{
		Command: "sh",
		BuildArgs: func(_, _ string) []string {
			return []string{"-c", "echo 'Error: model codex-opus-4-9 is not approved for Codex runtime'; exit 1"}
		},
	})
	s := ops.NewSpawner(spawner)
	ch := s.Review(context.Background(), ops.ReviewOpts{BeadID: "test-unsupported-model"})
	result := <-ch

	if result.Verdict == ops.VerdictApproved {
		t.Fatal("unsupported model error must NOT yield VerdictApproved; Codex runtime errors must fail closed")
	}
	if result.Verdict != ops.VerdictFailed {
		t.Fatalf("expected VerdictFailed for unsupported model error, got %q", result.Verdict)
	}
	if result.Err == nil {
		t.Fatal("expected non-nil Err for nonzero exit")
	}
}

func TestCodexOpsErrorPayloadFailsClosed(t *testing.T) {
	spawner := ops.NewExecSpawner(ops.RuntimeSpec{
		Command: "sh",
		BuildArgs: func(_, _ string) []string {
			return []string{"-c", "echo 'ERROR: {\"type\":\"error\",\"message\":\"unsupported model\"}'; exit 1"}
		},
	})
	s := ops.NewSpawner(spawner)
	ch := s.Review(context.Background(), ops.ReviewOpts{BeadID: "test-codex-error"})
	result := <-ch

	if result.Verdict != ops.VerdictFailed {
		t.Fatalf("Codex ops error payload verdict = %q, want failed", result.Verdict)
	}
	if result.Err == nil {
		t.Fatal("Codex ops error payload should preserve non-nil Err")
	}
}
