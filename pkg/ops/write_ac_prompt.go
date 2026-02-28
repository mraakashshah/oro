package ops

// buildWriteACPrompt assembles the prompt for an acceptance-criteria writing agent.
// It delegates to buildACPrompt in ac_prompt.go.
func buildWriteACPrompt(opts WriteACOpts) string {
	return buildACPrompt(opts)
}
