package beadstore

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

type rowQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

type depGraph map[string]map[string]struct{}

// Cycle is an ordered dependency cycle with the start node repeated at the end.
type Cycle []string

func loadBlockingGraph(ctx context.Context, q rowQuerier) (depGraph, error) {
	blockingTypes := blockingDepTypes()
	rows, err := q.QueryContext(ctx, `
SELECT b.id, blocker.id
FROM beads b
LEFT JOIN bead_deps d
  ON d.bead_id = b.id
 AND d.type IN (?, ?)
LEFT JOIN beads blocker
  ON blocker.id = d.depends_on_id
 AND blocker.deleted = 0
 AND blocker.status != 'closed'
WHERE b.deleted = 0
  AND b.status != 'closed'
ORDER BY b.id, blocker.id`, blockingTypes[0], blockingTypes[1])
	if err != nil {
		return nil, fmt.Errorf("beadstore: query blocking graph: %w", err)
	}
	defer rows.Close()

	graph := depGraph{}
	for rows.Next() {
		var id string
		var blocker sql.NullString
		if err := rows.Scan(&id, &blocker); err != nil {
			return nil, fmt.Errorf("beadstore: scan blocking graph: %w", err)
		}
		if _, ok := graph[id]; !ok {
			graph[id] = map[string]struct{}{}
		}
		if blocker.Valid && blocker.String != "" {
			graph[id][blocker.String] = struct{}{}
			if _, ok := graph[blocker.String]; !ok {
				graph[blocker.String] = map[string]struct{}{}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("beadstore: iterate blocking graph: %w", err)
	}
	return graph, nil
}

func reachable(g depGraph, from, to string) bool {
	return len(reachablePath(g, from, to)) > 0
}

func reachablePath(g depGraph, from, to string) []string {
	seen := map[string]struct{}{}
	var visit func(string) []string
	visit = func(id string) []string {
		if id == to {
			return []string{id}
		}
		if _, ok := seen[id]; ok {
			return nil
		}
		seen[id] = struct{}{}
		for _, next := range sortedNeighbors(g, id) {
			if path := visit(next); len(path) > 0 {
				return append([]string{id}, path...)
			}
		}
		return nil
	}
	return visit(from)
}

func findCycles(g depGraph) []Cycle {
	if isAcyclic(g) {
		return nil
	}

	seenCycles := map[string]struct{}{}
	var cycles []Cycle
	var path []string
	onPath := map[string]int{}

	var visit func(string)
	visit = func(id string) {
		onPath[id] = len(path)
		path = append(path, id)
		defer func() {
			path = path[:len(path)-1]
			delete(onPath, id)
		}()

		for _, next := range sortedNeighbors(g, id) {
			if start, ok := onPath[next]; ok {
				cycle := canonicalizeCycle(append(append(Cycle{}, path[start:]...), next))
				key := strings.Join(cycle, "\x00")
				if _, ok := seenCycles[key]; !ok {
					seenCycles[key] = struct{}{}
					cycles = append(cycles, cycle)
				}
				continue
			}
			visit(next)
		}
	}

	for _, id := range sortedNodes(g) {
		visit(id)
	}
	sort.Slice(cycles, func(i, j int) bool {
		return strings.Join(cycles[i], "\x00") < strings.Join(cycles[j], "\x00")
	})
	return cycles
}

func isAcyclic(g depGraph) bool {
	const (
		visiting = 1
		visited  = 2
	)
	state := make(map[string]uint8, len(g))
	var visit func(string) bool
	visit = func(id string) bool {
		switch state[id] {
		case visiting:
			return false
		case visited:
			return true
		}
		state[id] = visiting
		for next := range g[id] {
			if !visit(next) {
				return false
			}
		}
		state[id] = visited
		return true
	}
	for id := range g {
		if !visit(id) {
			return false
		}
	}
	return true
}

func canonicalizeCycle(cycle Cycle) Cycle {
	if len(cycle) <= 1 {
		return append(Cycle{}, cycle...)
	}
	nodes := cycle[:len(cycle)-1]
	minIdx := 0
	for i := 1; i < len(nodes); i++ {
		if nodes[i] < nodes[minIdx] {
			minIdx = i
		}
	}
	out := make(Cycle, 0, len(cycle))
	out = append(out, nodes[minIdx:]...)
	out = append(out, nodes[:minIdx]...)
	out = append(out, out[0])
	return out
}

func sortedNodes(g depGraph) []string {
	nodes := make([]string, 0, len(g))
	for id := range g {
		nodes = append(nodes, id)
	}
	sort.Strings(nodes)
	return nodes
}

func sortedNeighbors(g depGraph, id string) []string {
	neighbors := make([]string, 0, len(g[id]))
	for neighbor := range g[id] {
		neighbors = append(neighbors, neighbor)
	}
	sort.Strings(neighbors)
	return neighbors
}

func blockingDepTypes() []string {
	return []string{"blocks", "conditional-blocks"}
}

func isBlockingDepType(depType string) bool {
	for _, blockingType := range blockingDepTypes() {
		if depType == blockingType {
			return true
		}
	}
	return false
}
