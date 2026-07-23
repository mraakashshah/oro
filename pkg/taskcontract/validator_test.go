package taskcontract

import (
	"strings"
	"testing"

	"oro/pkg/protocol"
)

func TestValidateExecutableTaskV2(t *testing.T) {
	t.Parallel()

	validTask := protocol.BeadDetail{
		Title:            "Validate executable task contracts",
		ContractVersion:  2,
		Type:             "task",
		Priority:         0,
		EstimatedMinutes: 7,
		AcceptanceCriteria: strings.Join([]string{
			"Test: pkg/taskcontract/validator_test.go:TestValidateExecutableTaskV2",
			"Cmd: go test ./pkg/taskcontract -run '^TestValidateExecutableTaskV2$' -count=1",
			"Assert: v2 executable tasks satisfy their complete contract",
			"Read: pkg/taskcontract/validator.go:Validate",
		}, "\n"),
	}

	tests := []struct {
		name string
		bead protocol.BeadDetail
		mode ValidationMode
		want string
	}{
		{name: "valid task", bead: validTask, mode: ValidationModeExecutable},
		{name: "missing title", bead: without(validTask, func(bead *protocol.BeadDetail) { bead.Title = "" }), mode: ValidationModeExecutable, want: "title"},
		{name: "missing test", bead: withoutAcceptanceField(validTask, "Test"), mode: ValidationModeExecutable, want: "Test"},
		{name: "missing command", bead: withoutAcceptanceField(validTask, "Cmd"), mode: ValidationModeExecutable, want: "Cmd"},
		{name: "missing assertion", bead: withoutAcceptanceField(validTask, "Assert"), mode: ValidationModeExecutable, want: "Assert"},
		{name: "missing read", bead: withoutAcceptanceField(validTask, "Read"), mode: ValidationModeExecutable, want: "Read"},
		{name: "estimate below range", bead: without(validTask, func(bead *protocol.BeadDetail) { bead.EstimatedMinutes = 0 }), mode: ValidationModeExecutable, want: "estimate"},
		{name: "estimate above range", bead: without(validTask, func(bead *protocol.BeadDetail) { bead.EstimatedMinutes = 8 }), mode: ValidationModeExecutable, want: "estimate"},
		{name: "invalid type", bead: without(validTask, func(bead *protocol.BeadDetail) { bead.Type = "feature" }), mode: ValidationModeExecutable, want: "type"},
		{name: "invalid priority", bead: without(validTask, func(bead *protocol.BeadDetail) { bead.Priority = 5 }), mode: ValidationModeExecutable, want: "priority"},
		{name: "callable API lacks signature", bead: without(validTask, func(bead *protocol.BeadDetail) { bead.Metadata = map[string]any{MetadataCallableAPI: true} }), mode: ValidationModeTaskcraft, want: "Signature"},
		{name: "boundary lacks edges", bead: without(validTask, func(bead *protocol.BeadDetail) { bead.Metadata = map[string]any{MetadataNonTrivialBoundary: true} }), mode: ValidationModeTaskcraft, want: "Edges"},
		{name: "epic needs main branch command", bead: protocol.BeadDetail{ContractVersion: 2, Type: "epic", AcceptanceCriteria: "Cmd: go test ./...\nAssert: passes"}, mode: ValidationModeExecutable, want: "main"},
		{name: "epic needs assertion", bead: protocol.BeadDetail{ContractVersion: 2, Type: "epic", AcceptanceCriteria: "Cmd: git merge-base --is-ancestor main HEAD"}, mode: ValidationModeExecutable, want: "Assert"},
		{name: "epic valid", bead: protocol.BeadDetail{ContractVersion: 2, Type: "epic", AcceptanceCriteria: "Cmd: git merge-base --is-ancestor main HEAD\nAssert: main is an ancestor"}, mode: ValidationModeExecutable},
		{name: "historical version is compatible", bead: protocol.BeadDetail{ContractVersion: 0, Type: "task"}, mode: ValidationModeExecutable},
		{name: "exempt producer is compatible", bead: protocol.BeadDetail{ContractVersion: 2, Type: "task"}, mode: ValidationModeExempt},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.bead, tc.mode)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() error = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func without(bead protocol.BeadDetail, mutate func(*protocol.BeadDetail)) protocol.BeadDetail {
	mutate(&bead)
	return bead
}

func withoutAcceptanceField(bead protocol.BeadDetail, field string) protocol.BeadDetail {
	lines := strings.Split(bead.AcceptanceCriteria, "\n")
	filtered := make([]string, 0, len(lines)-1)
	for _, line := range lines {
		if !strings.HasPrefix(line, field+":") {
			filtered = append(filtered, line)
		}
	}
	bead.AcceptanceCriteria = strings.Join(filtered, "\n")
	return bead
}
