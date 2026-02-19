package main

import (
	"fmt"
	"sort"
)

// BeadWithDeps represents a bead with its dependency information.
type BeadWithDeps struct {
	ID              string
	Priority        int
	Type            string
	DaysSinceUpdate int
	DependsOn       []string
}

// DependencyGraph represents the dependency graph of beads.
type DependencyGraph struct {
	beads []BeadWithDeps
}

// NewDependencyGraph creates a new dependency graph from beads.
func NewDependencyGraph(beads []BeadWithDeps) *DependencyGraph {
	return &DependencyGraph{beads: beads}
}

// CriticalPath returns the longest dependency chain in the graph.
// Returns an error if a circular dependency is detected.
func (g *DependencyGraph) CriticalPath() ([]string, error) {
	if len(g.beads) == 0 {
		return []string{}, nil
	}

	depMap := g.buildDependencyMap()

	if err := g.detectCycles(depMap); err != nil {
		return nil, err
	}

	longestPath := g.findLongestPath(depMap)

	// Return empty slice if no dependencies exist
	if len(longestPath) == 1 {
		return []string{}, nil
	}

	return longestPath, nil
}

func (g *DependencyGraph) buildDependencyMap() map[string][]string {
	depMap := make(map[string][]string)
	for _, bead := range g.beads {
		depMap[bead.ID] = bead.DependsOn
	}
	return depMap
}

func (g *DependencyGraph) detectCycles(depMap map[string][]string) error {
	visited := make(map[string]bool)
	recStack := make(map[string]bool)

	var hasCycle func(string) bool
	hasCycle = func(id string) bool {
		visited[id] = true
		recStack[id] = true

		for _, dep := range depMap[id] {
			if !visited[dep] {
				if hasCycle(dep) {
					return true
				}
			} else if recStack[dep] {
				return true
			}
		}

		recStack[id] = false
		return false
	}

	for _, bead := range g.beads {
		if !visited[bead.ID] && hasCycle(bead.ID) {
			return ErrCircularDependency
		}
	}

	return nil
}

func (g *DependencyGraph) findLongestPath(depMap map[string][]string) []string {
	memo := make(map[string][]string)

	var computePath func(string) []string
	computePath = func(id string) []string {
		if path, exists := memo[id]; exists {
			return path
		}

		deps := depMap[id]
		if len(deps) == 0 {
			memo[id] = []string{id}
			return []string{id}
		}

		longestDep := g.findLongestDependency(deps, computePath)
		result := append([]string{}, longestDep...)
		result = append(result, id)
		memo[id] = result
		return result
	}

	var overallLongest []string
	for _, bead := range g.beads {
		path := computePath(bead.ID)
		if len(path) > len(overallLongest) {
			overallLongest = path
		}
	}

	return overallLongest
}

func (g *DependencyGraph) findLongestDependency(deps []string, computePath func(string) []string) []string {
	var longestPath []string
	for _, dep := range deps {
		path := computePath(dep)
		if len(path) > len(longestPath) {
			longestPath = path
		}
	}
	return longestPath
}

var ErrCircularDependency = circularDependencyError{}

type circularDependencyError struct{}

func (e circularDependencyError) Error() string {
	return "circular dependency detected"
}

// Bottleneck represents a bead that blocks multiple other beads.
type Bottleneck struct {
	BeadID       string
	BlockedCount int
}

// Bottlenecks returns all beads that block at least one other bead,
// sorted by the number of beads they block (descending).
func (g *DependencyGraph) Bottlenecks() []Bottleneck {
	// Count how many beads depend on each bead
	blockedCount := make(map[string]int)

	for _, bead := range g.beads {
		for _, dep := range bead.DependsOn {
			blockedCount[dep]++
		}
	}

	// Build result slice
	var result []Bottleneck
	for beadID, count := range blockedCount {
		if count > 0 {
			result = append(result, Bottleneck{
				BeadID:       beadID,
				BlockedCount: count,
			})
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].BeadID < result[j].BeadID
	})

	return result
}

// TriageFlag represents a bead that needs attention.
type TriageFlag struct {
	BeadID   string
	Reason   string
	Severity string // "high", "medium", "low"
}

// TriageFlags returns beads that need attention based on heuristics:
// - Stale P0 beads (not updated in 7+ days)
// - Bugs with low priority (P3 or P4)
func (g *DependencyGraph) TriageFlags() []TriageFlag {
	var flags []TriageFlag

	for _, bead := range g.beads {
		// Check for stale P0 beads
		if bead.Priority == 0 && bead.DaysSinceUpdate >= 7 {
			flags = append(flags, TriageFlag{
				BeadID:   bead.ID,
				Reason:   formatStaleReason(bead.DaysSinceUpdate),
				Severity: "high",
			})
		}

		// Check for bugs with low priority (P3 or P4)
		if bead.Type == "bug" && bead.Priority >= 3 {
			flags = append(flags, TriageFlag{
				BeadID:   bead.ID,
				Reason:   formatBugPriorityReason(bead.Priority),
				Severity: "medium",
			})
		}
	}

	return flags
}

func formatStaleReason(days int) string {
	return fmt.Sprintf("stale P0 (%d days)", days)
}

func formatBugPriorityReason(priority int) string {
	return fmt.Sprintf("bug with low priority (P%d)", priority)
}

// InDegrees returns a map of beadID -> number of other beads that depend on it
// (i.e. the in-degree in the "dependency points to prerequisite" direction).
// This is Phase 1 computation — O(E) where E is total dependency edges.
func (g *DependencyGraph) InDegrees() map[string]int {
	deg := make(map[string]int, len(g.beads))
	// Initialise every known bead at zero
	for _, b := range g.beads {
		deg[b.ID] = 0
	}
	// For every "A depends on B" edge, increment B's count
	for _, b := range g.beads {
		for _, dep := range b.DependsOn {
			deg[dep]++
		}
	}
	return deg
}

// TopologicalOrder returns the beads in topological order (prerequisites first).
// Uses Kahn's algorithm which runs in O(V+E).
// Returns ErrCircularDependency if a cycle is detected.
// This is Phase 1 computation.
func (g *DependencyGraph) TopologicalOrder() ([]string, error) {
	if len(g.beads) == 0 {
		return []string{}, nil
	}

	// prereqCount[id] = number of prerequisites bead id must wait for.
	// A bead with prereqCount == 0 can be scheduled immediately.
	prereqCount := make(map[string]int, len(g.beads))
	for _, b := range g.beads {
		prereqCount[b.ID] = len(b.DependsOn)
	}

	// successors[B] = list of beads that list B as a prerequisite.
	// When B is scheduled, we decrement each successor's prereqCount.
	successors := make(map[string][]string, len(g.beads))
	for _, b := range g.beads {
		for _, dep := range b.DependsOn {
			successors[dep] = append(successors[dep], b.ID)
		}
	}

	// Seed the queue with nodes that have no prerequisites.
	queue := make([]string, 0, len(g.beads))
	for _, b := range g.beads {
		if prereqCount[b.ID] == 0 {
			queue = append(queue, b.ID)
		}
	}

	order := make([]string, 0, len(g.beads))
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		order = append(order, node)

		for _, succ := range successors[node] {
			prereqCount[succ]--
			if prereqCount[succ] == 0 {
				queue = append(queue, succ)
			}
		}
	}

	if len(order) != len(g.beads) {
		return nil, ErrCircularDependency
	}

	return order, nil
}
