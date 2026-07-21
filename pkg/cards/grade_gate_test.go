package cards //nolint:testpackage // white-box tests pin the unexported gradeGate contract from the Phase 3 spec.

import "testing"

func TestGradeGate_AcceptsAtThreshold(t *testing.T) {
	cfg := GateConfig{
		AutoApplyConfidence:   []float64{0.95},
		EnsembleMinConfidence: 0.85,
	}

	got := gradeGate([]GradeVerdict{{Verdict: GradeVerdictCorrect, Confidence: 0.96}}, cfg)
	if got.Action != GradeActionApply || got.GradeState != GradeStateApplied {
		t.Fatalf("single high-confidence verdict outcome = %+v, want applied", got)
	}
	if got.Confidence != 0.96 {
		t.Fatalf("Confidence = %v, want 0.96", got.Confidence)
	}

	got = gradeGate([]GradeVerdict{{Verdict: GradeVerdictCorrect, Confidence: 0.94}}, cfg)
	if got.Action != GradeActionQueue || got.Reason != "ensemble_required" {
		t.Fatalf("single borderline verdict outcome = %+v, want queued for ensemble", got)
	}
}

func TestGradeGatePerRungThreshold(t *testing.T) {
	cfg := GateConfig{
		AutoApplyConfidence:   []float64{0.80, 0.95},
		EnsembleMinConfidence: 0.85,
	}

	tests := []struct {
		name       string
		rung       int
		verdict    GradeVerdict
		wantAction GradeAction
		wantState  GradeState
		wantReason string
	}{
		{
			name:       "correct applies at lower rung threshold",
			rung:       0,
			verdict:    GradeVerdict{Verdict: GradeVerdictCorrect, Confidence: 0.80},
			wantAction: GradeActionApply,
			wantState:  GradeStateApplied,
		},
		{
			name:       "correct below higher rung threshold queues",
			rung:       1,
			verdict:    GradeVerdict{Verdict: GradeVerdictCorrect, Confidence: 0.80},
			wantAction: GradeActionQueue,
			wantState:  GradeStateProposed,
			wantReason: "ensemble_required",
		},
		{
			name:       "correct at rung threshold applies",
			rung:       1,
			verdict:    GradeVerdict{Verdict: GradeVerdictCorrect, Confidence: 0.95},
			wantAction: GradeActionApply,
			wantState:  GradeStateApplied,
		},
		{
			name:       "correct below rung threshold queues",
			rung:       1,
			verdict:    GradeVerdict{Verdict: GradeVerdictCorrect, Confidence: 0.94},
			wantAction: GradeActionQueue,
			wantState:  GradeStateProposed,
			wantReason: "ensemble_required",
		},
		{
			name:       "incorrect rejects and retires regardless of rung threshold",
			rung:       1,
			verdict:    GradeVerdict{Verdict: GradeVerdictIncorrect, Confidence: 0.01},
			wantAction: GradeActionRejectAndRetire,
			wantState:  GradeStateRejected,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gradeGate([]GradeVerdict{tt.verdict}, cfg, tt.rung)
			if got.Action != tt.wantAction || got.GradeState != tt.wantState || got.Reason != tt.wantReason {
				t.Fatalf("gradeGate() = %+v, want action %q, state %q, reason %q", got, tt.wantAction, tt.wantState, tt.wantReason)
			}
		})
	}
}

func TestGradeGate_EnsembleUnanimityRequired(t *testing.T) {
	cfg := GateConfig{
		AutoApplyConfidence:   []float64{0.95},
		EnsembleMinConfidence: 0.85,
	}

	got := gradeGate([]GradeVerdict{
		{Verdict: GradeVerdictCorrect, Confidence: 0.9},
		{Verdict: GradeVerdictCorrect, Confidence: 0.91},
		{Verdict: GradeVerdictPartial, Confidence: 0.92},
	}, cfg)
	if got.Action != GradeActionQueue || got.Reason != "ensemble_not_unanimous" {
		t.Fatalf("2/3 correct outcome = %+v, want queued for lack of unanimity", got)
	}

	got = gradeGate([]GradeVerdict{
		{Verdict: GradeVerdictCorrect, Confidence: 0.9},
		{Verdict: GradeVerdictCorrect, Confidence: 0.91},
		{Verdict: GradeVerdictCorrect, Confidence: 0.92},
	}, cfg)
	if got.Action != GradeActionApply || got.GradeState != GradeStateApplied {
		t.Fatalf("3/3 correct outcome = %+v, want applied", got)
	}
	if got.Confidence != 0.9 {
		t.Fatalf("Confidence = %v, want ensemble minimum 0.9", got.Confidence)
	}

	got = gradeGate([]GradeVerdict{
		{Verdict: GradeVerdictCorrect, Confidence: 0.9},
		{Verdict: GradeVerdictCorrect, Confidence: 0.91},
		{Verdict: GradeVerdictUnresolvable, Confidence: 0.99},
	}, cfg)
	if got.Action != GradeActionQueue || got.Reason != "ensemble_unresolvable" {
		t.Fatalf("ensemble with unresolvable outcome = %+v, want queued", got)
	}
}

func TestGradeGate_UnresolvableNeverAccepts(t *testing.T) {
	cfg := GateConfig{
		AutoApplyConfidence:   []float64{0.95},
		EnsembleMinConfidence: 0.85,
	}

	tests := []struct {
		name       string
		verdicts   []GradeVerdict
		wantAction GradeAction
		wantState  GradeState
	}{
		{
			name:       "incorrect rejects and retires",
			verdicts:   []GradeVerdict{{Verdict: GradeVerdictIncorrect, Confidence: 0.99}},
			wantAction: GradeActionRejectAndRetire,
			wantState:  GradeStateRejected,
		},
		{
			name:       "partial queues",
			verdicts:   []GradeVerdict{{Verdict: GradeVerdictPartial, Confidence: 0.99}},
			wantAction: GradeActionQueue,
			wantState:  GradeStateProposed,
		},
		{
			name:       "unresolvable queues",
			verdicts:   []GradeVerdict{{Verdict: GradeVerdictUnresolvable, Confidence: 1}},
			wantAction: GradeActionQueue,
			wantState:  GradeStateProposed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gradeGate(tt.verdicts, cfg)
			if got.Action != tt.wantAction || got.GradeState != tt.wantState {
				t.Fatalf("outcome = %+v, want action %q state %q", got, tt.wantAction, tt.wantState)
			}
		})
	}
}
