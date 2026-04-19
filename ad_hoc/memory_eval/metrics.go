package memoryeval

import "fmt"

const (
	WarmMRRRatio = 1.30
	ColdMRRRatio = 1.20
)

type GateResult struct {
	Pass   bool
	Reason string
}

func CheckGate(baseMRR, warmMRR, coldMRR float64) GateResult {
	if baseMRR == 0 {
		return GateResult{
			Pass:   false,
			Reason: "baseline MRR is 0 — search is broken, cannot compute ratio",
		}
	}
	if warmMRR < WarmMRRRatio*baseMRR {
		return GateResult{
			Pass:   false,
			Reason: fmt.Sprintf("warm MRR %.4f < 1.30×%.4f baseline", warmMRR, baseMRR),
		}
	}
	if coldMRR < ColdMRRRatio*baseMRR {
		return GateResult{
			Pass:   false,
			Reason: fmt.Sprintf("cold MRR %.4f < 1.20×%.4f baseline", coldMRR, baseMRR),
		}
	}
	return GateResult{Pass: true}
}
