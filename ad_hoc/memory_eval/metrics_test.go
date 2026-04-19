package memoryeval

import (
	"strings"
	"testing"
)

func TestCheckGateZeroBaselineFails(t *testing.T) {
	result := CheckGate(0, 1, 1)
	if result.Pass {
		t.Errorf("CheckGate(0, 1, 1).Pass = true; want false")
	}
	if !strings.Contains(result.Reason, "baseline MRR is 0") {
		t.Errorf("CheckGate(0, 1, 1).Reason = %q; want to contain 'baseline MRR is 0'", result.Reason)
	}
}

func TestCheckGatePassesWhenRatiosMet(t *testing.T) {
	// CheckGate(0.5, 0.65, 0.60).Pass == true
	// warm ratio: 0.65 / 0.5 = 1.30 (meets 1.30x requirement)
	// cold ratio: 0.60 / 0.5 = 1.20 (meets 1.20x requirement)
	result := CheckGate(0.5, 0.65, 0.60)
	if !result.Pass {
		t.Errorf("CheckGate(0.5, 0.65, 0.60).Pass = false; want true. Reason: %s", result.Reason)
	}
}

func TestCheckGateWarmBelowThreshold(t *testing.T) {
	// CheckGate(0.5, 0.60, 0.60).Pass == false
	// warm ratio: 0.60 / 0.5 = 1.20 (< 1.30x requirement)
	// cold ratio: 0.60 / 0.5 = 1.20 (meets 1.20x requirement)
	result := CheckGate(0.5, 0.60, 0.60)
	if result.Pass {
		t.Errorf("CheckGate(0.5, 0.60, 0.60).Pass = true; want false")
	}
	if !strings.Contains(result.Reason, "warm MRR") {
		t.Errorf("CheckGate(0.5, 0.60, 0.60).Reason = %q; want to contain 'warm MRR'", result.Reason)
	}
}

func TestCheckGateColdBelowThreshold(t *testing.T) {
	// CheckGate(0.5, 0.70, 0.55).Pass == false
	// warm ratio: 0.70 / 0.5 = 1.40 (meets 1.30x requirement)
	// cold ratio: 0.55 / 0.5 = 1.10 (< 1.20x requirement)
	result := CheckGate(0.5, 0.70, 0.55)
	if result.Pass {
		t.Errorf("CheckGate(0.5, 0.70, 0.55).Pass = true; want false")
	}
	if !strings.Contains(result.Reason, "cold MRR") {
		t.Errorf("CheckGate(0.5, 0.70, 0.55).Reason = %q; want to contain 'cold MRR'", result.Reason)
	}
}
