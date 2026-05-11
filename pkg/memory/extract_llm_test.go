package memory //nolint:testpackage // white-box tests — same package for interface access

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"oro/pkg/agentmodel"
)

// spawnerMock is a test double for Spawner.
type spawnerMock struct {
	callCount  int
	lastModel  string
	lastPrompt string
	lastCtx    context.Context
	output     string
	err        error
}

func (s *spawnerMock) Spawn(ctx context.Context, model, prompt string) (io.ReadCloser, error) {
	s.callCount++
	s.lastModel = model
	s.lastPrompt = prompt
	s.lastCtx = ctx
	if s.err != nil {
		return nil, s.err
	}
	return io.NopCloser(strings.NewReader(s.output)), nil
}

// inserterMock records Insert calls for verification.
type inserterMock struct {
	inserted []InsertParams
}

func (m *inserterMock) Insert(_ context.Context, p InsertParams) (int64, error) {
	m.inserted = append(m.inserted, p)
	return int64(len(m.inserted)), nil
}

func TestExtractWithLLM(t *testing.T) {
	t.Run("sends prompt with tail-truncated session text and haiku model", func(t *testing.T) {
		// Build 60K text with a head-only region and a tail-unique marker.
		// Tail = last 50K chars = positions 10000-59999.
		headOnly := strings.Repeat("Z", 10000)  // positions 0-9999: head only, 10K Z's
		tailStart := "TAIL_BEGINS_HERE"         // position 10000: start of tail, unique
		rest := strings.Repeat("X", 49984)      // positions 10016-59999
		longText := headOnly + tailStart + rest // 60000 chars

		spawner := &spawnerMock{output: ""}
		inserter := &inserterMock{}

		err := ExtractWithLLM(context.Background(), spawner, longText, "bead-123", inserter)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if spawner.callCount != 1 {
			t.Errorf("expected 1 spawner call, got %d", spawner.callCount)
		}
		if spawner.lastModel != "haiku" {
			t.Errorf("expected model=haiku, got %q", spawner.lastModel)
		}

		// Prompt must contain the tail-unique marker.
		if !strings.Contains(spawner.lastPrompt, "TAIL_BEGINS_HERE") {
			t.Error("prompt should contain tail of session text (TAIL_BEGINS_HERE marker missing)")
		}
		// Prompt must NOT contain the head-only Z block (10000 consecutive Z's).
		if strings.Contains(spawner.lastPrompt, strings.Repeat("Z", 10000)) {
			t.Error("prompt should not contain head-only region (10000 Z's present)")
		}
	})

	t.Run("inserts parsed MEMORY lines with source=llm_extracted confidence=0.7 beadID set", func(t *testing.T) {
		output := "[MEMORY] type=lesson tags=go,test: table-driven tests catch edge cases\n" +
			"[MEMORY] type=gotcha tags=sqlite: WAL mode required for concurrent writes\n"

		spawner := &spawnerMock{output: output}
		inserter := &inserterMock{}

		err := ExtractWithLLM(context.Background(), spawner, "session text content here long enough", "bead-42", inserter)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(inserter.inserted) != 2 {
			t.Fatalf("expected 2 inserts, got %d", len(inserter.inserted))
		}

		for i, mem := range inserter.inserted {
			if mem.Source != "llm_extracted" {
				t.Errorf("[%d] expected source=llm_extracted, got %q", i, mem.Source)
			}
			if mem.Confidence != 0.7 {
				t.Errorf("[%d] expected confidence=0.7, got %f", i, mem.Confidence)
			}
			if mem.BeadID != "bead-42" {
				t.Errorf("[%d] expected beadID=bead-42, got %q", i, mem.BeadID)
			}
		}

		if inserter.inserted[0].Type != "lesson" {
			t.Errorf("expected type=lesson, got %q", inserter.inserted[0].Type)
		}
		if inserter.inserted[1].Type != "gotcha" {
			t.Errorf("expected type=gotcha, got %q", inserter.inserted[1].Type)
		}
	})

	t.Run("empty session text returns nil without calling spawner", func(t *testing.T) {
		spawner := &spawnerMock{}
		inserter := &inserterMock{}

		err := ExtractWithLLM(context.Background(), spawner, "", "bead-123", inserter)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if spawner.callCount != 0 {
			t.Errorf("expected 0 spawner calls, got %d", spawner.callCount)
		}
		if len(inserter.inserted) != 0 {
			t.Errorf("expected 0 inserts, got %d", len(inserter.inserted))
		}
	})

	t.Run("spawner error returns nil with no inserts", func(t *testing.T) {
		spawner := &spawnerMock{err: errors.New("API unavailable")}
		inserter := &inserterMock{}

		err := ExtractWithLLM(context.Background(), spawner, "session text with enough content here", "bead-123", inserter)
		if err != nil {
			t.Errorf("expected nil error on spawner failure, got: %v", err)
		}
		if len(inserter.inserted) != 0 {
			t.Errorf("expected 0 inserts on spawner error, got %d", len(inserter.inserted))
		}
	})

	t.Run("nil spawner returns nil immediately", func(t *testing.T) {
		inserter := &inserterMock{}

		err := ExtractWithLLM(context.Background(), nil, "some session text here long enough", "bead-123", inserter)
		if err != nil {
			t.Errorf("expected nil error for nil spawner, got: %v", err)
		}
		if len(inserter.inserted) != 0 {
			t.Errorf("expected 0 inserts for nil spawner, got %d", len(inserter.inserted))
		}
	})

	t.Run("creates own 30s timeout ignoring expired parent context", func(t *testing.T) {
		before := time.Now()

		// Parent context is already expired.
		expiredCtx, cancel := context.WithDeadline(context.Background(), before.Add(-time.Second))
		defer cancel()

		spawner := &spawnerMock{output: ""}
		inserter := &inserterMock{}

		err := ExtractWithLLM(expiredCtx, spawner, "session text content here", "bead-123", inserter)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Spawner should be called despite expired parent context.
		if spawner.callCount != 1 {
			t.Fatalf("expected 1 spawner call (ignoring expired parent), got %d", spawner.callCount)
		}

		// The spawner's context deadline must be AFTER the expired parent deadline.
		// (Note: we cannot check ctx.Err() here because ExtractWithLLM's defer cancel
		// runs on return, cancelling the context before we can inspect it. The deadline
		// value is immutable and survives cancellation.)
		parentDeadline, _ := expiredCtx.Deadline() // in the past

		deadline, ok := spawner.lastCtx.Deadline()
		if !ok {
			t.Fatal("spawner context should have a deadline")
		}
		if !deadline.After(parentDeadline) {
			t.Errorf("spawner deadline %v should be after expired parent deadline %v", deadline, parentDeadline)
		}
		// Deadline should be ~30s from call time.
		expectedDeadline := before.Add(30 * time.Second)
		diff := expectedDeadline.Sub(deadline)
		if diff < 0 {
			diff = -diff
		}
		if diff > 2*time.Second {
			t.Errorf("deadline should be ~30s after call time; expected ~%v got %v (diff=%v)", expectedDeadline, deadline, diff)
		}
	})

	t.Run("output with no MEMORY lines results in zero inserts", func(t *testing.T) {
		spawner := &spawnerMock{output: "Just regular output\nNo memory markers here\n"}
		inserter := &inserterMock{}

		err := ExtractWithLLM(context.Background(), spawner, "session text content", "bead-123", inserter)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(inserter.inserted) != 0 {
			t.Errorf("expected 0 inserts for output with no MEMORY lines, got %d", len(inserter.inserted))
		}
	})

	t.Run("malformed MEMORY lines are skipped", func(t *testing.T) {
		output := "[MEMORY] this is not valid format\n" +
			"[MEMORY] type=lesson tags=go: valid memory content here\n" +
			"[MEMORY] bad\n"

		spawner := &spawnerMock{output: output}
		inserter := &inserterMock{}

		err := ExtractWithLLM(context.Background(), spawner, "session content here long", "bead-123", inserter)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Only the one valid line should be inserted.
		if len(inserter.inserted) != 1 {
			t.Fatalf("expected 1 insert, got %d", len(inserter.inserted))
		}
		if inserter.inserted[0].Type != "lesson" {
			t.Errorf("expected type=lesson, got %q", inserter.inserted[0].Type)
		}
	})
}

func TestCLISpawnerImplementsSpawner(t *testing.T) {
	// Compile-time check: CLISpawner must implement Spawner.
	var _ Spawner = CLISpawner{}
	var _ WorkdirSpawner = CLISpawner{}
}

func TestCLISpawner_SetsStdinToDevNull(t *testing.T) {
	spawner := CLISpawner{}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	reader, err := spawner.Spawn(ctx, "claude-haiku-4-5-20251001", "test prompt")
	if err != nil {
		// claude is not available in test environment.
		// Verify the error is about the binary, NOT about /dev/null access.
		// This indirectly verifies /dev/null was opened successfully.
		if strings.Contains(err.Error(), "dev/null") {
			t.Errorf("error should not be about /dev/null; got: %v", err)
		}
		return
	}
	// claude ran — clean up.
	if reader != nil {
		_ = reader.Close()
	}
}

func TestCLISpawner_SpawnInWorkdirNormalizesGitEnv(t *testing.T) {
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	workdir := filepath.Join(tmp, "worktree")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	canonicalWorkdir, err := filepath.EvalSymlinks(workdir)
	if err != nil {
		t.Fatalf("canonical workdir: %v", err)
	}
	// Use a fake claude binary — SpawnInWorkdir resolves memory_extractor role to
	// the claude runtime via agentmodel, so we intercept claude (not codex).
	fakeClaude := filepath.Join(binDir, "claude")
	script := "#!/bin/sh\n" +
		"printf 'PWD=%s\\n' \"$PWD\"\n" +
		"printf 'GIT_DIR=%s\\n' \"${GIT_DIR-unset}\"\n" +
		"printf 'GIT_WORK_TREE=%s\\n' \"${GIT_WORK_TREE-unset}\"\n" +
		"printf 'GIT_INDEX_FILE=%s\\n' \"${GIT_INDEX_FILE-unset}\"\n" +
		"printf 'ACTUAL=%s\\n' \"$(pwd -P)\"\n"
	if err := os.WriteFile(fakeClaude, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PWD", "/poisoned/main")
	t.Setenv("GIT_DIR", "/poisoned/main/.git")
	t.Setenv("GIT_WORK_TREE", "/poisoned/main")
	t.Setenv("GIT_INDEX_FILE", "/poisoned/main/.git/index")

	reader, err := CLISpawner{}.SpawnInWorkdir(context.Background(), "gpt-5-codex", "extract", workdir)
	if err != nil {
		t.Fatalf("SpawnInWorkdir() error: %v", err)
	}
	out, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil {
		t.Fatalf("read stdout: %v", readErr)
	}
	if closeErr != nil {
		t.Fatalf("close stdout: %v", closeErr)
	}

	text := string(out)
	for _, want := range []string{
		"PWD=" + workdir,
		"GIT_DIR=unset",
		"GIT_WORK_TREE=unset",
		"GIT_INDEX_FILE=unset",
		"ACTUAL=" + canonicalWorkdir,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("SpawnInWorkdir output missing %q:\n%s", want, text)
		}
	}
}

func TestSpawnCommand_DefaultsToClaude(t *testing.T) {
	t.Setenv("ORO_AGENT_RUNTIME", "")

	got := spawnCommand("haiku", "test prompt", "")
	want := []string{"claude", "-p", "test prompt", "--model", "haiku"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("spawnCommand() = %v, want %v", got, want)
	}
}

func TestSpawnCommand_UsesCodexWhenConfigured(t *testing.T) {
	t.Setenv("ORO_AGENT_RUNTIME", "codex")

	got := spawnCommand("haiku", "test prompt", "")
	want := []string{"codex", "exec", "--skip-git-repo-check", "--sandbox", "workspace-write", "--model", "gpt-5-codex", "test prompt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("spawnCommand() = %v, want %v", got, want)
	}
}

func TestSpawnCommand_UsesCodexModelForClaudeFallbackWhenConfigured(t *testing.T) {
	t.Setenv("ORO_AGENT_RUNTIME", "codex")

	got := spawnCommand("claude-haiku-4-5-20251001", "test prompt", "")
	want := []string{"codex", "exec", "--skip-git-repo-check", "--sandbox", "workspace-write", "--model", "gpt-5-codex", "test prompt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("spawnCommand() = %v, want %v", got, want)
	}
}

// TestMemoryExtractorRoleResolves verifies that spawnCommand resolves the model
// via agentmodel.ResolveForRole when a role is provided, and falls back to
// ReadRuntime() + the model parameter when role is empty.
func TestMemoryExtractorRoleResolves(t *testing.T) {
	t.Run("role resolves model via agentmodel", func(t *testing.T) {
		t.Setenv("ORO_AGENT_RUNTIME", "")

		_, expectedModel, _ := agentmodel.ResolveForRole("memory_extractor")
		args := spawnCommand("haiku", "probe", "memory_extractor")

		gotModel := sliceValue(args, "--model")
		if gotModel != expectedModel {
			t.Errorf("spawnCommand model = %q, want %q (from agentmodel.ResolveForRole)", gotModel, expectedModel)
		}
	})

	t.Run("empty role falls back to ReadRuntime default", func(t *testing.T) {
		t.Setenv("ORO_AGENT_RUNTIME", "")

		args := spawnCommand("my-model", "probe", "")

		if args[0] != "claude" {
			t.Errorf("empty role: command = %q, want claude (ReadRuntime default)", args[0])
		}
		gotModel := sliceValue(args, "--model")
		if gotModel != "my-model" {
			t.Errorf("empty role: model = %q, want my-model (fallback parameter)", gotModel)
		}
	})
}

// sliceValue finds the value following key in a string slice.
func sliceValue(args []string, key string) string {
	for i, arg := range args {
		if arg == key && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
