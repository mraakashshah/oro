//nolint:testpackage // These tests pin the unexported pure cycle-detection core.
package beadstore

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

func TestFindCycles_AcyclicDiamondReturnsEmpty(t *testing.T) {
	graph := depGraph{
		"A": {"B": {}, "C": {}},
		"B": {"D": {}},
		"C": {"D": {}},
		"D": {},
	}

	if got := findCycles(graph); len(got) != 0 {
		t.Fatalf("findCycles() = %#v, want empty", got)
	}
}

func TestFindCycles_DenseAcyclicGraphReturnsPromptly(t *testing.T) {
	const nodes = 28
	graph := make(depGraph, nodes)
	for from := 0; from < nodes; from++ {
		id := fmt.Sprintf("N%02d", from)
		graph[id] = map[string]struct{}{}
		for to := from + 1; to < nodes; to++ {
			graph[id][fmt.Sprintf("N%02d", to)] = struct{}{}
		}
	}

	if got := findCycles(graph); len(got) != 0 {
		t.Fatalf("findCycles() = %#v, want empty", got)
	}
}

func TestFindCycles_TwoNodeCycle(t *testing.T) {
	graph := depGraph{
		"A": {"B": {}},
		"B": {"A": {}},
	}

	got := findCycles(graph)
	want := []Cycle{{"A", "B", "A"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findCycles() = %#v, want %#v", got, want)
	}
}

func TestFindCycles_ThreeNodeCycleOrdered(t *testing.T) {
	graph := depGraph{
		"A": {"B": {}},
		"B": {"C": {}},
		"C": {"A": {}},
	}

	got := findCycles(graph)
	want := []Cycle{{"A", "B", "C", "A"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findCycles() = %#v, want %#v", got, want)
	}
}

func TestFindCycles_SelfLoop(t *testing.T) {
	graph := depGraph{
		"A": {"A": {}},
	}

	got := findCycles(graph)
	want := []Cycle{{"A", "A"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findCycles() = %#v, want %#v", got, want)
	}
}

func TestFindCycles_TwoDisjointCyclesDeduped(t *testing.T) {
	graph := depGraph{
		"A": {"B": {}},
		"B": {"A": {}},
		"C": {"D": {}},
		"D": {"C": {}},
	}

	got := findCycles(graph)
	want := []Cycle{{"A", "B", "A"}, {"C", "D", "C"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findCycles() = %#v, want %#v", got, want)
	}
}

func TestReachable(t *testing.T) {
	graph := depGraph{
		"A": {"B": {}, "C": {}},
		"B": {"D": {}},
		"C": {"D": {}},
		"D": {},
	}

	if !reachable(graph, "A", "D") {
		t.Fatal("reachable(A, D) = false, want true")
	}
	if reachable(graph, "D", "A") {
		t.Fatal("reachable(D, A) = true, want false")
	}
}

func TestLoadBlockingGraph_ExcludesNonBlockingAndClosed(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)
	mustCreate(t, store, CreateParams{ID: "A", Title: "A"})
	mustCreate(t, store, CreateParams{ID: "B", Title: "B"})
	mustCreate(t, store, CreateParams{ID: "C", Title: "C"})
	mustCreate(t, store, CreateParams{ID: "D", Title: "D"})
	mustCreate(t, store, CreateParams{ID: "E", Title: "E"})
	mustCreate(t, store, CreateParams{ID: "F", Title: "F"})
	mustClose(t, store, "C", "done")

	mustExec(t, store.db, `INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES (?, ?, ?)`, "A", "B", "blocks")
	mustExec(t, store.db, `INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES (?, ?, ?)`, "D", "E", "conditional-blocks")
	mustExec(t, store.db, `INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES (?, ?, ?)`, "A", "C", "blocks")
	mustExec(t, store.db, `INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES (?, ?, ?)`, "A", "D", "parent-child")
	mustExec(t, store.db, `INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES (?, ?, ?)`, "A", "F", "related")

	got, err := loadBlockingGraph(ctx, store.db)
	if err != nil {
		t.Fatalf("loadBlockingGraph: %v", err)
	}
	want := depGraph{
		"A": {"B": {}},
		"B": {},
		"D": {"E": {}},
		"E": {},
		"F": {},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loadBlockingGraph() = %#v, want %#v", got, want)
	}
}
