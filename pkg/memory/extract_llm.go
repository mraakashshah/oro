package memory

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"time"
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

// Inserter abstracts memory insertion.
// *Store satisfies this interface via its Insert method (Go structural typing).
type Inserter interface {
	Insert(ctx context.Context, m InsertParams) (int64, error)
}

// CLISpawner is the production Spawner that invokes `claude -p`.
type CLISpawner struct{}

// Spawn starts a `claude -p` subprocess with the given model and prompt.
// Stdin is set to /dev/null to prevent the process from inheriting parent stdin
// and hanging (see pkg/worker/worker.go:1249-1256 for full rationale).
func (c CLISpawner) Spawn(ctx context.Context, model, prompt string) (io.ReadCloser, error) {
	args := []string{"-p", prompt, "--model", model}
	cmd := exec.CommandContext(ctx, "claude", args...) //nolint:gosec // args constructed internally

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
		return nil, fmt.Errorf("start claude: %w", err)
	}

	return stdout, nil
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

	reader, err := spawner.Spawn(extractCtx, extractionModel, prompt)
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

	return nil
}
