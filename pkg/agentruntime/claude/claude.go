// Package claude implements Oro runtime descriptors for the Claude CLI.
package claude

import (
	"oro/pkg/agentruntime"
	"oro/pkg/protocol"
)

// Runtime describes Claude runtime capabilities and defaults.
type Runtime struct{}

// New creates a Claude runtime descriptor.
func New() *Runtime {
	return &Runtime{}
}

// ID reports the stable Claude runtime identifier.
func (r *Runtime) ID() agentruntime.RuntimeID {
	return agentruntime.RuntimeIDClaude
}

// DefaultTierModel leaves model resolution to the configured role/tier resolver.
func (r *Runtime) DefaultTierModel(role string, tier protocol.Tier) string {
	return ""
}

// StreamFormat reports Claude's JSON stdout event stream contract.
func (r *Runtime) StreamFormat() agentruntime.StreamFormat {
	return agentruntime.StreamFormatClaudeJSON
}

// InstructionLayout returns the default instruction layout placeholder.
func (r *Runtime) InstructionLayout() agentruntime.InstructionLayout {
	return agentruntime.InstructionLayout{}
}

// SupportsHooks reports that Claude supports project hook configuration.
func (r *Runtime) SupportsHooks() bool {
	return true
}

// SupportsProjectSkillInstall reports that Claude supports project skill installs.
func (r *Runtime) SupportsProjectSkillInstall() bool {
	return true
}
