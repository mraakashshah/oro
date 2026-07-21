package storage

import (
	"path"
	"strings"
	"time"
)

// OroHomeTemporaryMaxAge is the maximum age retained for inactive known Oro
// temporary files.
const OroHomeTemporaryMaxAge = 24 * time.Hour

// HomeTemporary is one observed temporary artifact beneath the Oro home
// directory. Symlinks are never selected because their targets may escape the
// Oro home directory.
type HomeTemporary struct {
	Path       string
	ModifiedAt time.Time
	Active     bool
	IsSymlink  bool
}

// PlanOroHomeTemporaryRetention selects only inactive, non-symlinked known
// Oro temporary files older than twenty-four hours. Unknown and malformed
// paths are deliberately preserved.
//
//oro:testonly — production retention execution wiring lands in a subsequent task.
func PlanOroHomeTemporaryRetention(now time.Time, temporaries []HomeTemporary) []HomeTemporary {
	cutoff := now.Add(-OroHomeTemporaryMaxAge)
	selected := make([]HomeTemporary, 0, len(temporaries))
	for _, temporary := range temporaries {
		canonical, ok := canonicalOroTemporaryPath(temporary.Path)
		if !ok || temporary.Active || temporary.IsSymlink || !temporary.ModifiedAt.Before(cutoff) {
			continue
		}
		temporary.Path = canonical
		selected = append(selected, temporary)
	}
	return selected
}

func canonicalOroTemporaryPath(temporaryPath string) (string, bool) {
	canonical := path.Clean(strings.TrimSpace(temporaryPath))
	if canonical == "." || strings.HasPrefix(canonical, "../") || strings.HasPrefix(canonical, "/") || !isKnownTemporary(canonical) {
		return "", false
	}
	return canonical, true
}
