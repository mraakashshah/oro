package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/ops"
	"oro/pkg/protocol"
)

const (
	cleanlinessRoleMetadataKey = "meta_role"
	auditRoleActor             = "ops_audit"
)

type cleanlinessRoleSpec struct {
	name  string
	title string
}

func roleSpec(name string) (cleanlinessRoleSpec, error) {
	switch name {
	case "janitor":
		return cleanlinessRoleSpec{name: name, title: "Janitor findings"}, nil
	case "audit":
		return cleanlinessRoleSpec{name: name, title: "Audit findings"}, nil
	default:
		return cleanlinessRoleSpec{}, fmt.Errorf("unknown cleanliness role %q", name)
	}
}

// ensureRoleBead returns the persistent, non-assignable bead ID for name.
func (d *Dispatcher) ensureRoleBead(ctx context.Context, name string) (string, error) {
	spec, err := roleSpec(name)
	if err != nil {
		return "", err
	}

	d.cleanlinessRoleMu.Lock()
	defer d.cleanlinessRoleMu.Unlock()

	beads, err := d.beads.FindByMetadataKey(ctx, cleanlinessRoleMetadataKey)
	if err != nil {
		return "", fmt.Errorf("find %s role bead: %w", name, err)
	}
	matches := matchingRoleBeads(beads, spec.name)
	if len(matches) > 0 {
		if len(matches) > 1 {
			_ = d.logEvent(ctx, "cleanliness_role_duplicate", "dispatcher", matches[0].ID, "",
				fmt.Sprintf("role=%s markers=%d; using oldest", name, len(matches)))
		}
		return matches[0].ID, nil
	}

	role, err := d.beads.Create(ctx, beadstore.CreateParams{
		Title:    spec.title,
		Type:     "task",
		Priority: 2,
		Status:   "closed",
		Metadata: map[string]string{cleanlinessRoleMetadataKey: spec.name},
	})
	if err != nil {
		return "", fmt.Errorf("create %s role bead: %w", name, err)
	}
	if role == nil || role.ID == "" {
		return "", fmt.Errorf("create %s role bead: empty bead", name)
	}
	return role.ID, nil
}

func matchingRoleBeads(beads []*protocol.Bead, name string) []*protocol.Bead {
	matches := make([]*protocol.Bead, 0, len(beads))
	for _, bead := range beads {
		if bead != nil && bead.Metadata[cleanlinessRoleMetadataKey] == name {
			matches = append(matches, bead)
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].CreatedAt != matches[j].CreatedAt {
			return matches[i].CreatedAt < matches[j].CreatedAt
		}
		return matches[i].ID < matches[j].ID
	})
	return matches
}

func (d *Dispatcher) cleanlinessRoleBeadIDs(ctx context.Context, requiredID string) ([]string, error) {
	beads, err := d.beads.FindByMetadataKey(ctx, cleanlinessRoleMetadataKey)
	if err != nil {
		return nil, fmt.Errorf("find cleanliness role beads: %w", err)
	}
	seen := make(map[string]bool, len(beads)+1)
	ids := make([]string, 0, len(beads)+1)
	for _, bead := range beads {
		if bead == nil || bead.ID == "" || seen[bead.ID] {
			continue
		}
		role, _ := bead.Metadata[cleanlinessRoleMetadataKey].(string)
		if role != "janitor" && role != "audit" {
			continue
		}
		seen[bead.ID] = true
		ids = append(ids, bead.ID)
	}
	if requiredID != "" && !seen[requiredID] {
		ids = append(ids, requiredID)
	}
	return ids, nil
}

// deriveSuppressed resolves wont-fixed finding beads against the union of the
// janitor and audit role journeys so line-stable evidence remains available.
func (d *Dispatcher) deriveSuppressed(ctx context.Context, roleBeadIDs []string) ([]ops.Finding, error) {
	beads, err := d.beads.FindByMetadataKey(ctx, auditFindingMetadataKey)
	if err != nil {
		return nil, fmt.Errorf("find cleanliness finding beads: %w", err)
	}
	wontFixIDs := make(map[string]bool)
	for _, bead := range beads {
		if bead == nil || bead.Status != "closed" || !isWontFixReason(bead.CloseReason) {
			continue
		}
		findingID, _ := bead.Metadata[auditFindingMetadataKey].(string)
		if findingID != "" {
			wontFixIDs[findingID] = true
		}
	}
	return d.resolveRoleFindings(ctx, roleBeadIDs, wontFixIDs, "wont-fix")
}

func (d *Dispatcher) deriveActiveFindings(ctx context.Context, roleBeadIDs []string) ([]ops.Finding, error) {
	beads, err := d.beads.FindByMetadataKey(ctx, auditFindingMetadataKey)
	if err != nil {
		return nil, fmt.Errorf("find active cleanliness finding beads: %w", err)
	}
	activeIDs := make(map[string]bool)
	for _, bead := range beads {
		if bead == nil || bead.Status == "closed" {
			continue
		}
		findingID, _ := bead.Metadata[auditFindingMetadataKey].(string)
		if findingID != "" {
			activeIDs[findingID] = true
		}
	}
	return d.resolveRoleFindings(ctx, roleBeadIDs, activeIDs, "open")
}

func (d *Dispatcher) resolveRoleFindings(
	ctx context.Context,
	roleBeadIDs []string,
	targetIDs map[string]bool,
	status string,
) ([]ops.Finding, error) {
	findings := make(map[string]ops.Finding, len(targetIDs))
	for _, roleBeadID := range roleBeadIDs {
		events, journeyErr := d.beads.Journey(ctx, roleBeadID, time.Time{})
		if journeyErr != nil {
			return nil, fmt.Errorf("load cleanliness role journey %s: %w", roleBeadID, journeyErr)
		}
		for _, event := range events {
			if !isCleanlinessFindingEvent(event) || event.Payload == "" {
				continue
			}
			var finding ops.Finding
			if err := json.Unmarshal([]byte(event.Payload), &finding); err != nil {
				return nil, fmt.Errorf("parse %s finding journey: %w", event.Event, err)
			}
			if targetIDs[finding.ID] {
				finding.Status = status
				findings[finding.ID] = finding
			}
		}
	}

	for findingID := range targetIDs {
		if _, ok := findings[findingID]; !ok {
			findings[findingID] = ops.Finding{ID: findingID, Status: status}
		}
	}
	result := make([]ops.Finding, 0, len(findings))
	for _, finding := range findings {
		result = append(result, finding)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func isCleanlinessFindingEvent(event beadstore.JourneyEvent) bool {
	return event.Actor == janitorRoleActor && event.Event == "janitor_finding" ||
		event.Actor == auditRoleActor && event.Event == "audit_finding"
}

func findingSuppressed(candidate ops.Finding, suppressed []ops.Finding) bool {
	for _, prior := range suppressed {
		if candidate.ID != "" && candidate.ID == prior.ID {
			return true
		}
		if candidate.Title != "" && prior.Title != "" && ops.SameFindingBucket(candidate, prior) {
			return true
		}
	}
	return false
}
