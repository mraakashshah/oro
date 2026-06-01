package ops //nolint:testpackage // selectPersonas is an internal review orchestration helper

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"oro/pkg/agentmodel"
)

func TestSelectPersonas(t *testing.T) {
	t.Run("normal diff selects persona team", func(t *testing.T) {
		worktree := testReviewRepo(t)
		writeFile(t, filepath.Join(worktree, "pkg", "worker.go"), "package pkg\n\nfunc Worker() string { return \"changed\" }\n")

		personas := selectPersonas(ReviewOpts{Worktree: worktree, BaseBranch: "main"})
		wantIDs := []string{"correctness", "security", "adversarial", "design", "test", "architecture"}
		if len(personas) != len(wantIDs) {
			t.Fatalf("selectPersonas normal diff returned %d personas, want %d: %#v", len(personas), len(wantIDs), personas)
		}
		for i, wantID := range wantIDs {
			p := personas[i]
			if p.ID != wantID {
				t.Fatalf("persona[%d].ID = %q, want %q", i, p.ID, wantID)
			}
			if p.Role != "ops_review_"+wantID {
				t.Fatalf("persona[%d].Role = %q, want agentmodel role %q", i, p.Role, "ops_review_"+wantID)
			}
			if p.Fragment == "" {
				t.Fatalf("persona[%d].Fragment is empty", i)
			}
			runtime, model, _ := agentmodel.ResolveForRole(p.Role)
			if runtime == "" || model == "" {
				t.Fatalf("persona[%d].Role %q did not resolve through agentmodel: runtime=%q model=%q", i, p.Role, runtime, model)
			}
		}
	})

	t.Run("docs only diff selects no personas", func(t *testing.T) {
		worktree := testReviewRepo(t)
		writeFile(t, filepath.Join(worktree, "docs", "plan.md"), "# changed\n")

		if personas := selectPersonas(ReviewOpts{Worktree: worktree, BaseBranch: "main"}); len(personas) != 0 {
			t.Fatalf("selectPersonas docs-only diff returned %#v, want empty slice", personas)
		}
	})

	t.Run("trivial empty diff selects no personas", func(t *testing.T) {
		worktree := testReviewRepo(t)

		if personas := selectPersonas(ReviewOpts{Worktree: worktree, BaseBranch: "main"}); len(personas) != 0 {
			t.Fatalf("selectPersonas empty diff returned %#v, want empty slice", personas)
		}
	})
}

func TestReviewMultiPersona_SpawnsPerPersona(t *testing.T) {
	worktree := testReviewRepo(t)
	t.Chdir(worktree)
	writePersonaAgentConfig(t, worktree)
	writeFile(t, filepath.Join(worktree, "pkg", "worker.go"), "package pkg\n\nfunc Worker() string { return \"changed\" }\n")

	personas := selectPersonas(ReviewOpts{Worktree: worktree, BaseBranch: "main"})
	spawner := &recordingReviewSpawner{
		stdout: structuredReviewOutput(t, ReviewReport{Reviewer: "reviewer", Verdict: VerdictApproved}),
	}
	s := NewSpawner(spawner)

	result := waitResult(t, s.Review(context.Background(), ReviewOpts{
		BeadID:       "oro-review",
		Worktree:     worktree,
		BaseBranch:   "main",
		MultiPersona: true,
		MaxReviewers: 2,
	}))

	if result.Verdict != VerdictApproved {
		t.Fatalf("Review verdict = %q, want approved; feedback=%s err=%v", result.Verdict, result.Feedback, result.Err)
	}
	calls := spawner.getCalls()
	if len(calls) != len(personas) {
		t.Fatalf("spawn calls = %d, want %d", len(calls), len(personas))
	}
	seenRoles := make(map[string]bool, len(calls))
	for _, call := range calls {
		seenRoles[call.role] = true
		if !strings.Contains(call.prompt, "Structured Review Output") {
			t.Fatalf("prompt for role %q did not include structured review schema", call.role)
		}
	}
	for _, persona := range personas {
		if !seenRoles[persona.Role] {
			t.Fatalf("missing spawn for persona role %q; saw %#v", persona.Role, seenRoles)
		}
	}
}

func TestReviewSinglePass_WhenMultiPersonaFalse(t *testing.T) {
	worktree := testReviewRepo(t)
	t.Chdir(worktree)
	writePersonaAgentConfig(t, worktree)
	writeFile(t, filepath.Join(worktree, "pkg", "worker.go"), "package pkg\n\nfunc Worker() string { return \"changed\" }\n")

	spawner := &recordingReviewSpawner{stdout: "legacy review\nVERDICT: APPROVED\n"}
	s := NewSpawner(spawner)

	result := waitResult(t, s.Review(context.Background(), ReviewOpts{
		BeadID:       "oro-review",
		Worktree:     worktree,
		BaseBranch:   "main",
		MultiPersona: false,
	}))

	if result.Verdict != VerdictApproved {
		t.Fatalf("Review verdict = %q, want approved; feedback=%s err=%v", result.Verdict, result.Feedback, result.Err)
	}
	calls := spawner.getCalls()
	if len(calls) != 1 {
		t.Fatalf("spawn calls = %d, want single legacy review pass", len(calls))
	}
	if calls[0].role != "ops_review" {
		t.Fatalf("spawn role = %q, want ops_review", calls[0].role)
	}
	if strings.Contains(calls[0].prompt, "Persona focus:") {
		t.Fatalf("single-pass prompt unexpectedly included persona fragment: %q", calls[0].prompt)
	}
}

func TestReviewMultiPersona_AllFailFallsBackToSinglePass(t *testing.T) {
	worktree := testReviewRepo(t)
	t.Chdir(worktree)
	writePersonaAgentConfig(t, worktree)
	writeFile(t, filepath.Join(worktree, "pkg", "worker.go"), "package pkg\n\nfunc Worker() string { return \"changed\" }\n")

	personas := selectPersonas(ReviewOpts{Worktree: worktree, BaseBranch: "main"})
	outputs := make([]string, 0, len(personas)+1)
	for range personas {
		outputs = append(outputs, "reviewer crashed without a verdict")
	}
	outputs = append(outputs, "legacy fallback\nVERDICT: APPROVED\n")
	spawner := &recordingReviewSpawner{outputs: outputs}
	s := NewSpawner(spawner)

	result := waitResult(t, s.Review(context.Background(), ReviewOpts{
		BeadID:       "oro-review",
		Worktree:     worktree,
		BaseBranch:   "main",
		MultiPersona: true,
	}))

	if result.Verdict != VerdictApproved {
		t.Fatalf("Review verdict = %q, want approved from legacy fallback; feedback=%s err=%v", result.Verdict, result.Feedback, result.Err)
	}
	calls := spawner.getCalls()
	if len(calls) != len(personas)+1 {
		t.Fatalf("spawn calls = %d, want %d persona calls plus one fallback", len(calls), len(personas)+1)
	}
	if calls[len(calls)-1].role != "ops_review" {
		t.Fatalf("fallback role = %q, want ops_review", calls[len(calls)-1].role)
	}
}

func testReviewRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "pkg", "worker.go"), "package pkg\n\nfunc Worker() string { return \"ok\" }\n")
	writeFile(t, filepath.Join(dir, "docs", "plan.md"), "# plan\n")
	git(t, dir, "init", "-b", "main")
	git(t, dir, "config", "user.email", "test@example.com")
	git(t, dir, "config", "user.name", "Test User")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "initial")
	return dir
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...) //nolint:gosec // fixed test helper command
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func writePersonaAgentConfig(t *testing.T, dir string) {
	t.Helper()
	body := `agent:
  roles:
    ops_review:
      transport: cli
      runtime: ops_review
      model: review-model
    ops_review_correctness:
      transport: cli
      runtime: ops_review_correctness
      model: review-model
    ops_review_security:
      transport: cli
      runtime: ops_review_security
      model: review-model
    ops_review_adversarial:
      transport: cli
      runtime: ops_review_adversarial
      model: review-model
    ops_review_design:
      transport: cli
      runtime: ops_review_design
      model: review-model
    ops_review_test:
      transport: cli
      runtime: ops_review_test
      model: review-model
    ops_review_architecture:
      transport: cli
      runtime: ops_review_architecture
      model: review-model
`
	writeFile(t, filepath.Join(dir, ".oro", "config.yaml"), body)
}

type recordingReviewSpawner struct {
	mu      sync.Mutex
	calls   []recordingReviewCall
	stdout  string
	outputs []string
}

type recordingReviewCall struct {
	role   string
	prompt string
}

func (r *recordingReviewSpawner) SpawnRuntime(_ context.Context, runtime, _, _, prompt, _ string) (Process, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, recordingReviewCall{role: runtime, prompt: prompt})
	return newReadyMockProcess(r.nextOutputLocked(), nil), nil
}

func (r *recordingReviewSpawner) Spawn(_ context.Context, _, prompt, _ string) (Process, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, recordingReviewCall{role: "legacy", prompt: prompt})
	return newReadyMockProcess(r.nextOutputLocked(), nil), nil
}

func (r *recordingReviewSpawner) getCalls() []recordingReviewCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordingReviewCall, len(r.calls))
	copy(out, r.calls)
	return out
}

func structuredReviewOutput(t *testing.T, report ReviewReport) string {
	t.Helper()
	b, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	return "```json\n" + string(b) + "\n```\nVERDICT: APPROVED\n"
}

func (r *recordingReviewSpawner) nextOutputLocked() string {
	if len(r.outputs) == 0 {
		return r.stdout
	}
	out := r.outputs[0]
	r.outputs = r.outputs[1:]
	return out
}
