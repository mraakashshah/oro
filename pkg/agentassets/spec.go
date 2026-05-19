// Package agentassets generates runtime-specific agent assets from Oro's shared
// asset bundle.
package agentassets

// RuleAsset describes one runtime rule file generated from Oro's asset bundle.
type RuleAsset struct {
	Source  string
	Target  string
	Content []byte
}
