package dispatcher

// isReviewArtifactTerminal reports whether a checkpoint can release its
// artifacts after retention. Approval alone is intentionally not terminal:
// integration can still be pending or require operator action.
func isReviewArtifactTerminal(state ReviewCheckpointState) bool {
	return state == ReviewCheckpointStateIntegrated || state == ReviewCheckpointStateSuperseded
}
