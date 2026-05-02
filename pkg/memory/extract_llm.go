package memory

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"oro/pkg/agentruntime"
	"oro/pkg/processenv"
)

// maxSessionBytes is the maximum number of bytes taken from sessionText.
// The tail is used because learnings cluster near the end of a session.
const maxSessionBytes = 50000

// extractTimeout is the per-call timeout for LLM extraction.
const extractTimeout = 30 * time.Second

// extractionModel is the model used for lightweight memory extraction.
const extractionModel = "haiku"

// extractionPrompt is sent to the LLM to extract [MEMORY] markers from session text.
//
//nolint:gochecknoglobals // compile-once constant prompt, safe as package-level var
const extractionPrompt = `You are a learning extractor. Given a worker session log, identify 0-5 genuine
discoveries worth remembering for future sessions. Only extract non-obvious
insights — things a developer working on this codebase would benefit from knowing.

Categories:
- lesson: something that worked or a technique discovered
- gotcha: something surprising or counterintuitive
- decision: an architectural choice and why it was made
- pattern: a reusable approach that emerged

For each discovery, output exactly one line in this format:
[MEMORY] type=<type> tags=<comma-separated>: <concise description>

If the session contains no genuine learnings (routine coding, straightforward
fixes), output nothing. Most sessions will have 0-2 learnings. Do not fabricate.

Session log (last ~12K tokens):
`

// Spawner abstracts subprocess creation for testability.
// Production code uses CLISpawner; tests use a mock.
type Spawner interface {
	Spawn(ctx context.Context, model, prompt string) (io.ReadCloser, error)
}

// WorkdirSpawner is implemented by subprocess spawners that can bind runtime
// execution to a specific worktree.
type WorkdirSpawner interface {
	SpawnInWorkdir(ctx context.Context, model, prompt, workdir string) (io.ReadCloser, error)
}

// Inserter abstracts memory insertion.
// *Store satisfies this interface via its Insert method (Go structural typing).
type Inserter interface {
	Insert(ctx context.Context, m InsertParams) (int64, error)
}

const codexExtractionModel = "gpt-5-codex"

// CLISpawner is the production Spawner that invokes the configured runtime CLI.
type CLISpawner struct{}

// waitCloser wraps a pipe reader and calls cmd.Wait() on Close to reap the child process.
type waitCloser struct {
	io.ReadCloser
	cmd *exec.Cmd
}

// Close closes the pipe and reaps the child process to prevent zombies.
func (w *waitCloser) Close() error {
	_ = w.ReadCloser.Close()
	if err := w.cmd.Wait(); err != nil {
		return fmt.Errorf("wait for subprocess: %w", err)
	}
	return nil
}

// Spawn starts a runtime subprocess with the given model and prompt.
// Stdin is set to /dev/null to prevent the process from inheriting parent stdin
// and hanging (see pkg/worker/worker.go:1249-1256 for full rationale).
// The returned ReadCloser's Close method reaps the child process via cmd.Wait().
func (c CLISpawner) Spawn(ctx context.Context, model, prompt string) (io.ReadCloser, error) {
	return c.SpawnInWorkdir(ctx, model, prompt, "")
}

// SpawnInWorkdir starts a runtime subprocess from workdir with git environment
// variables normalized to that worktree. This keeps best-effort memory
// extraction from mutating the dispatcher/main checkout when a worker is
// assigned to an isolated worktree.
func (c CLISpawner) SpawnInWorkdir(ctx context.Context, model, prompt, workdir string) (io.ReadCloser, error) {
	args := spawnCommand(model, prompt)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...) //nolint:gosec // args constructed internally
	if workdir != "" {
		cmd.Dir = workdir
	}
	cmd.Env = processenv.ForWorkdir(os.Environ(), workdir)

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return nil, fmt.Errorf("open /dev/null: %w", err)
	}
	defer devNull.Close() // fd is dup'd into child by Start(); safe to close our copy on return
	cmd.Stdin = devNull

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start runtime subprocess: %w", err)
	}

	return &waitCloser{ReadCloser: stdout, cmd: cmd}, nil
}

func spawnCommand(model, prompt string) []string {
	if agentruntime.ReadRuntime() == agentruntime.RuntimeCodex {
		args := []string{"codex", "exec", "--skip-git-repo-check", "--sandbox", "workspace-write"}
		if codexModel := normalizeCodexModel(model); codexModel != "" {
			args = append(args, "--model", codexModel)
		}
		return append(args, prompt)
	}
	return []string{"claude", "-p", prompt, "--model", model}
}

func normalizeCodexModel(model string) string {
	model = strings.TrimSpace(model)
	switch model {
	case "", "haiku", "sonnet", "opus":
		return codexExtractionModel
	default:
		return model
	}
}

// ExtractWithLLM runs a lightweight LLM extraction pass over sessionText and
// inserts any discovered [MEMORY] markers into store with source=llm_extracted.
//
// Edge cases handled:
//   - nil spawner → return nil immediately (no-op)
//   - empty sessionText → return nil without calling spawner
//   - sessionText > 50K → take tail (learnings cluster near end)
//   - spawner error → log and return nil (best-effort)
//   - output with no [MEMORY] lines → 0 inserts, return nil
//   - malformed [MEMORY] line → ParseMarker returns nil, line is skipped
//
// ExtractWithLLM creates its own 30s timeout from context.Background(),
// independent of the caller's context deadline.
//
//oro:testonly — wired into production by subsequent memory-intake beads (worker.go, drain.go)
func ExtractWithLLM(_ context.Context, spawner Spawner, sessionText, beadID string, store Inserter) error {
	return extractWithLLM(spawner, sessionText, beadID, store, "")
}

// ExtractWithLLMInWorkdir runs extraction with a workdir-aware spawner when the
// spawner supports it, falling back to the legacy Spawner contract otherwise.
func ExtractWithLLMInWorkdir(_ context.Context, spawner Spawner, sessionText, beadID string, store Inserter, workdir string) error {
	return extractWithLLM(spawner, sessionText, beadID, store, workdir)
}

func extractWithLLM(spawner Spawner, sessionText, beadID string, store Inserter, workdir string) error {
	if spawner == nil {
		return nil
	}
	if sessionText == "" {
		return nil
	}

	// Cap to tail of session (learnings cluster near end).
	if len(sessionText) > maxSessionBytes {
		sessionText = sessionText[len(sessionText)-maxSessionBytes:]
	}

	// Create own timeout from Background — do NOT inherit caller's deadline.
	extractCtx, cancel := context.WithTimeout(context.Background(), extractTimeout)
	defer cancel()

	prompt := extractionPrompt + sessionText

	reader, err := spawnExtractor(extractCtx, spawner, extractionModel, prompt, workdir)
	if err != nil {
		log.Printf("memory extract: spawn error: %v", err)
		return nil
	}
	defer reader.Close()

	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()
		params := ParseMarker(line)
		if params == nil {
			continue
		}

		// ParseMarker returns source=self_report, confidence=0.8 — override.
		params.Source = "llm_extracted"
		params.Confidence = 0.7
		params.BeadID = beadID

		if _, err := store.Insert(extractCtx, *params); err != nil {
			log.Printf("memory extract: insert error: %v", err)
		}
	}
	if err := scanner.Err(); err != nil {
		log.Printf("memory extract: scan error: %v", err)
	}

	return nil
}

func spawnExtractor(ctx context.Context, spawner Spawner, model, prompt, workdir string) (io.ReadCloser, error) {
	if workdir != "" {
		if workdirSpawner, ok := spawner.(WorkdirSpawner); ok {
			return workdirSpawner.SpawnInWorkdir(ctx, model, prompt, workdir)
		}
	}
	return spawner.Spawn(ctx, model, prompt)
}
