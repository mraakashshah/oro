// Package agentassets generates runtime-specific agent assets from Oro's shared
// asset bundle.
package agentassets

import "io/fs"

// RuleAsset describes one runtime rule file generated from Oro's asset bundle.
type RuleAsset struct {
	Source  string
	Target  string
	Content []byte
}

// ClaudeGenerator generates Claude-specific runtime assets.
type ClaudeGenerator struct {
	Source fs.FS
}

// RuleAssets returns the Oro-owned Claude rule files for the sync plan.
func (g ClaudeGenerator) RuleAssets() ([]RuleAsset, error) {
	return ClaudeRuleAssets(g.Source)
}
