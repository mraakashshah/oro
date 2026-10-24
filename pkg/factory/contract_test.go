package factory //nolint:testpackage // Admission and policy invariants use private package seams.

import (
	"testing"
)

func contractSpec() Spec {
	return Spec{ID: "calculator", Objective: "Implement addition", Repository: "/tmp/repository", TargetBranch: "factory/calculator",
		Worker: Command{Path: "/tmp/worker"}, Acceptance: Command{Path: "/tmp/acceptance"}, Review: Command{Path: "/tmp/review"},
		RequiredChecks: []string{"positive", "negative"}, MaxAttempts: 3, TimeoutSeconds: 300, StepTimeoutSeconds: 60}
}

func TestAdmissionContract(t *testing.T) {
	if err := validateSpec(contractSpec()); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*Spec){
		"empty objective":       func(s *Spec) { s.Objective = "  " },
		"empty ID":              func(s *Spec) { s.ID = "" },
		"invalid ID":            func(s *Spec) { s.ID = "../other" },
		"long ID":               func(s *Spec) { s.ID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" },
		"relative repository":   func(s *Spec) { s.Repository = "repo" },
		"empty target":          func(s *Spec) { s.TargetBranch = "" },
		"relative worker":       func(s *Spec) { s.Worker.Path = "worker" },
		"relative acceptance":   func(s *Spec) { s.Acceptance.Path = "acceptance" },
		"relative review":       func(s *Spec) { s.Review.Path = "review" },
		"no checks":             func(s *Spec) { s.RequiredChecks = nil },
		"empty check":           func(s *Spec) { s.RequiredChecks = []string{" "} },
		"duplicate checks":      func(s *Spec) { s.RequiredChecks = []string{"positive", "positive"} },
		"zero attempts":         func(s *Spec) { s.MaxAttempts = 0 },
		"negative attempts":     func(s *Spec) { s.MaxAttempts = -1 },
		"zero deadline":         func(s *Spec) { s.TimeoutSeconds = 0 },
		"zero step":             func(s *Spec) { s.StepTimeoutSeconds = 0 },
		"step exceeds deadline": func(s *Spec) { s.StepTimeoutSeconds = 301 },
		"duration overflow":     func(s *Spec) { s.TimeoutSeconds = int(^uint(0) >> 1) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			spec := contractSpec()
			change(&spec)
			if err := validateSpec(spec); err == nil {
				t.Fatal("invalid specification accepted")
			}
		})
	}
}
