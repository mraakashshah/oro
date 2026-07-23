// Package taskcontract validates the executable-work contract persisted on beads.
package taskcontract

import (
	"fmt"
	"strings"

	"oro/pkg/protocol"
)

// ValidationMode identifies the producer and review path requesting validation.
type ValidationMode string

const (
	// ValidationModeExecutable validates a bead before it becomes executable work.
	ValidationModeExecutable ValidationMode = "executable"
	// ValidationModeTaskcraft additionally enforces taskcraft-declared review requirements.
	ValidationModeTaskcraft ValidationMode = "taskcraft"
	// ValidationModeExempt is for explicitly non-executable producers.
	ValidationModeExempt ValidationMode = "exempt"
)

const (
	// MetadataCallableAPI marks a taskcraft-reviewed task as adding a callable API.
	MetadataCallableAPI = "taskcontract.callable_api"
	// MetadataNonTrivialBoundary marks a taskcraft-reviewed task as having boundary or error behavior.
	MetadataNonTrivialBoundary = "taskcontract.non_trivial_boundary"
)

// Validate checks whether bead satisfies the contract appropriate for its persisted version and producer mode.
func Validate(bead protocol.BeadDetail, mode ValidationMode) error {
	if bead.ContractVersion < 2 || mode == ValidationModeExempt {
		return nil
	}

	fields := acceptanceFields(bead.AcceptanceCriteria)
	if bead.Type == "epic" {
		return validateEpic(fields)
	}
	return validateExecutableTask(bead, fields, mode)
}

func validateExecutableTask(bead protocol.BeadDetail, fields map[string]string, mode ValidationMode) error {
	if bead.Type != "task" && bead.Type != "bug" {
		return fmt.Errorf("task contract: type must be task or bug, got %q", bead.Type)
	}
	if err := validateTaskShape(bead); err != nil {
		return err
	}
	if err := requireAcceptanceFields(fields, "Test", "Cmd", "Assert", "Read"); err != nil {
		return err
	}
	return validateTaskcraftFields(bead.Metadata, fields, mode)
}

func validateTaskShape(bead protocol.BeadDetail) error {
	if strings.TrimSpace(bead.Title) == "" {
		return fmt.Errorf("task contract: title is required")
	}
	if bead.Priority < 0 || bead.Priority > 4 {
		return fmt.Errorf("task contract: priority must be between 0 and 4")
	}
	if bead.EstimatedMinutes < 1 || bead.EstimatedMinutes > 7 {
		return fmt.Errorf("task contract: estimate must be between 1 and 7 minutes")
	}
	return nil
}

func requireAcceptanceFields(fields map[string]string, required ...string) error {
	for _, field := range required {
		if fields[field] == "" {
			return fmt.Errorf("task contract: %s is required", field)
		}
	}
	return nil
}

func validateTaskcraftFields(metadata map[string]any, fields map[string]string, mode ValidationMode) error {
	if mode != ValidationModeTaskcraft {
		return nil
	}
	if metadataFlag(metadata, MetadataCallableAPI, "callable_api") && fields["Signature"] == "" {
		return fmt.Errorf("task contract: Signature is required for callable APIs")
	}
	if metadataFlag(metadata, MetadataNonTrivialBoundary, "non_trivial_boundary") && fields["Edges"] == "" {
		return fmt.Errorf("task contract: Edges is required for non-trivial boundaries")
	}
	return nil
}

func validateEpic(fields map[string]string) error {
	command := fields["Cmd"]
	if command == "" {
		return fmt.Errorf("task contract: Cmd is required for epics")
	}
	if !referencesMainBranch(command) {
		return fmt.Errorf("task contract: epic Cmd must reference the main branch")
	}
	if fields["Assert"] == "" {
		return fmt.Errorf("task contract: Assert is required for epics")
	}
	return nil
}

func acceptanceFields(acceptance string) map[string]string {
	fields := make(map[string]string)
	for _, line := range strings.Split(acceptance, "\n") {
		name, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		name = strings.TrimSpace(name)
		if _, exists := fields[name]; !exists {
			fields[name] = strings.TrimSpace(value)
		}
	}
	return fields
}

func metadataFlag(metadata map[string]any, keys ...string) bool {
	for _, key := range keys {
		value, ok := metadata[key]
		if !ok {
			continue
		}
		flag, ok := value.(bool)
		if ok && flag {
			return true
		}
	}
	return false
}

func referencesMainBranch(command string) bool {
	return strings.Contains(" "+strings.NewReplacer("/", " ", "=", " ", "'", " ", "\"", " ").Replace(command)+" ", " main ")
}
