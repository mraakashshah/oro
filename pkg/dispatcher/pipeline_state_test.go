package dispatcher //nolint:testpackage // needs access to unexported transitionPipeline + ErrInvalidTransition

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"oro/pkg/beadstore"
)

// stubTransitioner records calls to TransitionPipelineStage for assertion.
type stubTransitioner struct {
	calls []pipelineCall
	err   error
}

type pipelineCall struct {
	beadID string
	from   beadstore.PipelineStage
	to     beadstore.PipelineStage
}

func (s *stubTransitioner) TransitionPipelineStage(_ context.Context, beadID string, from, to beadstore.PipelineStage) error {
	s.calls = append(s.calls, pipelineCall{beadID: beadID, from: from, to: to})
	return s.err
}

// TestPipelineStateMachine verifies the dispatcher's pipeline state machine (§11.9):
//   - every valid transition delegates to TransitionPipelineStage
//   - invalid transitions return ErrInvalidTransition without touching the store
//   - no dispatcher source file contains a direct SQL UPDATE of pipeline_stage
func TestPipelineStateMachine(t *testing.T) {
	ctx := context.Background()

	t.Run("valid transitions call TransitionPipelineStage", func(t *testing.T) {
		cases := []struct {
			from beadstore.PipelineStage
			to   beadstore.PipelineStage
		}{
			// Initial entry
			{beadstore.StageNone, beadstore.StageAssess},
			// §11.9 forward path
			{beadstore.StageAssess, beadstore.StagePlan},
			{beadstore.StagePlan, beadstore.StagePremortem},
			{beadstore.StagePlan, beadstore.StagePrepare},
			{beadstore.StagePremortem, beadstore.StagePrepare},
			{beadstore.StagePremortem, beadstore.StagePlan}, // replan
			{beadstore.StagePrepare, beadstore.StageExecute},
			{beadstore.StageExecute, beadstore.StageValidate},
			{beadstore.StageExecute, beadstore.StageExecute}, // retry loop
			{beadstore.StageValidate, beadstore.StageEvolve},
			{beadstore.StageValidate, beadstore.StageExecute}, // needs-more
			{beadstore.StageEvolve, beadstore.StageAssess},
		}
		for _, tc := range cases {
			stub := &stubTransitioner{}
			err := transitionPipeline(ctx, stub, "b1", tc.from, tc.to)
			if err != nil {
				t.Errorf("transitionPipeline(%s → %s): unexpected error: %v", tc.from, tc.to, err)
				continue
			}
			if len(stub.calls) != 1 {
				t.Errorf("transitionPipeline(%s → %s): want 1 store call, got %d", tc.from, tc.to, len(stub.calls))
				continue
			}
			c := stub.calls[0]
			if c.from != tc.from || c.to != tc.to || c.beadID != "b1" {
				t.Errorf("store call mismatch: got (%q, %s→%s), want (b1, %s→%s)",
					c.beadID, c.from, c.to, tc.from, tc.to)
			}
		}
	})

	t.Run("invalid transitions return ErrInvalidTransition without calling store", func(t *testing.T) {
		invalid := []struct {
			from beadstore.PipelineStage
			to   beadstore.PipelineStage
		}{
			{beadstore.StageNone, beadstore.StagePlan},       // must go through assess
			{beadstore.StageAssess, beadstore.StageExecute},  // skip stages
			{beadstore.StagePrepare, beadstore.StageAssess},  // no backward arc
			{beadstore.StageEvolve, beadstore.StagePlan},     // must go through assess
			{beadstore.StageValidate, beadstore.StageAssess}, // no direct arc
			{beadstore.StageExecute, beadstore.StagePlan},    // not in spec
		}
		for _, tc := range invalid {
			stub := &stubTransitioner{}
			err := transitionPipeline(ctx, stub, "b1", tc.from, tc.to)
			if !errors.Is(err, ErrInvalidTransition) {
				t.Errorf("transitionPipeline(%s → %s): want ErrInvalidTransition, got %v", tc.from, tc.to, err)
			}
			if len(stub.calls) != 0 {
				t.Errorf("transitionPipeline(%s → %s): store must not be called on invalid transition", tc.from, tc.to)
			}
		}
	})

	t.Run("store error propagates", func(t *testing.T) {
		storeErr := errors.New("db unavailable")
		stub := &stubTransitioner{err: storeErr}
		err := transitionPipeline(ctx, stub, "b1", beadstore.StageNone, beadstore.StageAssess)
		if !errors.Is(err, storeErr) {
			t.Errorf("want store error to propagate, got: %v", err)
		}
	})

	// Lint check: no dispatcher source file (non-test) may contain a direct SQL
	// SET pipeline_stage expression. All mutations must go through
	// TransitionPipelineStage so the state machine gate is never bypassed.
	t.Run("no direct pipeline_stage UPDATE in dispatcher source", func(t *testing.T) {
		entries, err := os.ReadDir(".")
		if err != nil {
			t.Fatalf("read dispatcher dir: %v", err)
		}
		for _, e := range entries {
			if e.IsDir() ||
				!strings.HasSuffix(e.Name(), ".go") ||
				strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			src, err := os.ReadFile(e.Name())
			if err != nil {
				t.Fatalf("read %s: %v", e.Name(), err)
			}
			lower := strings.ToLower(string(src))
			idx := strings.Index(lower, "set pipeline_stage")
			if idx == -1 {
				continue
			}
			lineNum := strings.Count(string(src)[:idx], "\n") + 1
			t.Errorf("%s:%d: direct SQL SET pipeline_stage detected in dispatcher source; "+
				"use TransitionPipelineStage to enforce the §11.9 state machine",
				e.Name(), lineNum)
		}
	})
}
