package main

// InsightsModel holds the insights view state.
type InsightsModel struct {
	graph *DependencyGraph
}

// NewInsightsModel creates a new insights model from beads.
func NewInsightsModel(beads []BeadWithDeps) *InsightsModel {
	return &InsightsModel{
		graph: NewDependencyGraph(beads),
	}
}
