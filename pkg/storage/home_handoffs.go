package storage

import (
	"path"
	"sort"
	"strings"
	"time"
)

const (
	// OroHomeHandoffMaxAge is the maximum age retained for rendered handoffs
	// beyond each project's newest handoffs.
	OroHomeHandoffMaxAge = 30 * 24 * time.Hour
	// OroHomeHandoffKeepNewest is the number of newest rendered handoffs kept
	// for every project regardless of age.
	OroHomeHandoffKeepNewest = 10
)

// HomeHandoff is one observed rendered handoff beneath the Oro home directory.
type HomeHandoff struct {
	Path       string
	ModifiedAt time.Time
}

// PlanOroHomeHandoffRetention selects only known rendered handoffs that are
// older than thirty days and outside their project's newest ten handoffs.
// Unknown or malformed paths are deliberately preserved.
//
//oro:testonly — production retention execution wiring lands in a subsequent task.
func PlanOroHomeHandoffRetention(now time.Time, handoffs []HomeHandoff) []HomeHandoff {
	byProject := knownHandoffsByProject(handoffs)
	selected := make([]HomeHandoff, 0, len(handoffs))
	cutoff := now.Add(-OroHomeHandoffMaxAge)
	for _, projectHandoffs := range byProject {
		sort.Slice(projectHandoffs, func(i, j int) bool {
			if projectHandoffs[i].ModifiedAt.Equal(projectHandoffs[j].ModifiedAt) {
				return projectHandoffs[i].Path < projectHandoffs[j].Path
			}
			return projectHandoffs[i].ModifiedAt.After(projectHandoffs[j].ModifiedAt)
		})
		for i, handoff := range projectHandoffs {
			if i >= OroHomeHandoffKeepNewest && handoff.ModifiedAt.Before(cutoff) {
				selected = append(selected, handoff)
			}
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Path < selected[j].Path })
	return selected
}

func knownHandoffsByProject(handoffs []HomeHandoff) map[string][]HomeHandoff {
	byProject := make(map[string][]HomeHandoff)
	for _, handoff := range handoffs {
		canonical, project, ok := canonicalOroHandoffPath(handoff.Path)
		if !ok {
			continue
		}
		handoff.Path = canonical
		byProject[project] = append(byProject[project], handoff)
	}
	return byProject
}

func canonicalOroHandoffPath(handoffPath string) (canonical, project string, ok bool) {
	canonical = path.Clean(strings.TrimSpace(handoffPath))
	parts := strings.Split(canonical, "/")
	if canonical == "." || strings.HasPrefix(canonical, "../") || strings.HasPrefix(canonical, "/") ||
		len(parts) != 3 || parts[0] != "handoffs" || parts[1] == "" || !strings.HasSuffix(parts[2], ".md") {
		return "", "", false
	}
	return canonical, parts[1], true
}
