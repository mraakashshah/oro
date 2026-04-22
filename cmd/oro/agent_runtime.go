package main

import (
	"fmt"
	"os"
	"strings"

	codexruntime "oro/pkg/agentruntime/codex"
	"oro/pkg/ops"
	"oro/pkg/worker"
)

const (
	agentRuntimeEnvVar = "ORO_AGENT_RUNTIME"
	runtimeClaude      = "claude"
	runtimeCodex       = "codex"
)

type productionRuntime struct {
	id          string
	workerSpawn worker.StreamingSpawner
	opsSpawn    ops.BatchSpawner
}

var (
	newClaudeWorkerSpawner = func() worker.StreamingSpawner { return &worker.ClaudeSpawner{} }         //nolint:gochecknoglobals // injectable constructor for runtime-specific test seams
	newClaudeOpsSpawner    = func() ops.BatchSpawner { return ops.NewClaudeOpsSpawner() }              //nolint:gochecknoglobals // injectable constructor for runtime-specific test seams
	newCodexWorkerSpawner  = func() worker.StreamingSpawner { return codexruntime.NewWorkerSpawner() } //nolint:gochecknoglobals // injectable constructor for runtime-specific test seams
	newCodexOpsSpawner     = codexruntime.NewOpsSpawner                                                //nolint:gochecknoglobals // injectable constructor for runtime-specific test seams
)

func readAgentRuntime() string {
	runtime := strings.TrimSpace(strings.ToLower(os.Getenv(agentRuntimeEnvVar)))
	if runtime == "" {
		return runtimeClaude
	}
	return runtime
}

func resolveProductionRuntime() (*productionRuntime, error) {
	switch runtime := readAgentRuntime(); runtime {
	case runtimeClaude:
		return &productionRuntime{
			id:          runtimeClaude,
			workerSpawn: newClaudeWorkerSpawner(),
			opsSpawn:    newClaudeOpsSpawner(),
		}, nil
	case runtimeCodex:
		return &productionRuntime{
			id:          runtimeCodex,
			workerSpawn: newCodexWorkerSpawner(),
			opsSpawn:    newCodexOpsSpawner(),
		}, nil
	default:
		return nil, fmt.Errorf("unknown agent runtime %q", runtime)
	}
}
