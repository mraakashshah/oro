package storage

import (
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// OroHomeLogMaxAge is the maximum age retained for inactive known logs.
	OroHomeLogMaxAge = 7 * 24 * time.Hour
	// OroHomeLogMaxBytes is the maximum total size retained for inactive known logs.
	OroHomeLogMaxBytes int64 = 512 << 20
)

// HomeLog is one observed log beneath the Oro home directory.
type HomeLog struct {
	Path       string
	ModifiedAt time.Time
	Size       int64
}

// ActiveLogRegistry tracks canonical worker and hook logs that must not be
// selected for retention cleanup while their owners are running.
type ActiveLogRegistry struct {
	mu     sync.RWMutex
	active map[string]int
}

// NewActiveLogRegistry creates an empty active log registry.
//
//oro:testonly — production worker and hook lifecycle wiring lands in a subsequent retention task.
func NewActiveLogRegistry() *ActiveLogRegistry {
	return &ActiveLogRegistry{active: make(map[string]int)}
}

// Register marks a known Oro worker or hook log active. The returned function
// releases that registration and is safe to call more than once.
//
//oro:testonly — production worker and hook lifecycle wiring lands in a subsequent retention task.
func (r *ActiveLogRegistry) Register(logPath string) func() {
	canonical, ok := canonicalOroLogPath(logPath)
	if r == nil || !ok {
		return func() {}
	}

	r.mu.Lock()
	r.active[canonical]++
	r.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.active[canonical] <= 1 {
				delete(r.active, canonical)
				return
			}
			r.active[canonical]--
		})
	}
}

// IsActive reports whether a log has a live worker or hook registration.
//
//oro:testonly — production retention execution wiring lands in a subsequent task.
func (r *ActiveLogRegistry) IsActive(logPath string) bool {
	canonical, ok := canonicalOroLogPath(logPath)
	if r == nil || !ok {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.active[canonical] > 0
}

// PlanOroHomeLogRetention selects only inactive known logs. It first selects
// logs older than seven days, then selects the oldest remaining inactive logs
// only until their retained total is at most 512 MiB.
//
//oro:testonly — production retention execution wiring lands in a subsequent task.
func PlanOroHomeLogRetention(now time.Time, logs []HomeLog, active *ActiveLogRegistry) []HomeLog {
	candidates := inactiveKnownLogs(logs, active)
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].ModifiedAt.Equal(candidates[j].ModifiedAt) {
			return candidates[i].Path < candidates[j].Path
		}
		return candidates[i].ModifiedAt.Before(candidates[j].ModifiedAt)
	})

	selected := make([]HomeLog, 0, len(candidates))
	selectedPaths := make(map[string]struct{}, len(candidates))
	remaining := logBytes(candidates)
	cutoff := now.Add(-OroHomeLogMaxAge)
	for _, log := range candidates {
		if !log.ModifiedAt.Before(cutoff) {
			continue
		}
		selected = append(selected, log)
		selectedPaths[log.Path] = struct{}{}
		remaining -= nonNegativeSize(log.Size)
	}
	for _, log := range candidates {
		if remaining <= OroHomeLogMaxBytes {
			break
		}
		if _, selectedAlready := selectedPaths[log.Path]; selectedAlready {
			continue
		}
		selected = append(selected, log)
		remaining -= nonNegativeSize(log.Size)
	}
	return selected
}

func inactiveKnownLogs(logs []HomeLog, active *ActiveLogRegistry) []HomeLog {
	candidates := make([]HomeLog, 0, len(logs))
	for _, log := range logs {
		canonical, ok := canonicalOroLogPath(log.Path)
		if !ok || active.IsActive(canonical) {
			continue
		}
		log.Path = canonical
		candidates = append(candidates, log)
	}
	return candidates
}

func canonicalOroLogPath(logPath string) (string, bool) {
	canonical := path.Clean(strings.TrimSpace(logPath))
	if canonical == "." || strings.HasPrefix(canonical, "../") || strings.HasPrefix(canonical, "/") || !isOroLog(canonical) {
		return "", false
	}
	return canonical, true
}

func logBytes(logs []HomeLog) int64 {
	var total int64
	for _, log := range logs {
		total += nonNegativeSize(log.Size)
	}
	return total
}

func nonNegativeSize(size int64) int64 {
	if size < 0 {
		return 0
	}
	return size
}
