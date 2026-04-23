// Package agentruntime exposes shared runtime selection helpers.
package agentruntime

import (
	"os"
	"strings"
)

const (
	// EnvVar selects which agent runtime Oro uses for worker-like subprocesses.
	EnvVar = "ORO_AGENT_RUNTIME"

	// RuntimeClaude routes worker-like subprocesses through the Claude CLI.
	RuntimeClaude = "claude"
	// RuntimeCodex routes worker-like subprocesses through the Codex CLI.
	RuntimeCodex = "codex"
)

// ReadRuntime returns the configured runtime, defaulting to Claude.
func ReadRuntime() string {
	runtime := strings.TrimSpace(strings.ToLower(os.Getenv(EnvVar)))
	if runtime == "" {
		return RuntimeClaude
	}
	return runtime
}
