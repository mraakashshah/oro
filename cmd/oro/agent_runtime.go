package main

import (
	"fmt"

	"oro/pkg/agentruntime"
	codexruntime "oro/pkg/agentruntime/codex"
	"oro/pkg/ops"
	"oro/pkg/worker"
)

const (
	agentRuntimeEnvVar = agentruntime.EnvVar
	runtimeClaude      = agentruntime.RuntimeClaude
	runtimeCodex       = agentruntime.RuntimeCodex
)

type productionRuntime struct {
	id             string
	workerSpawn    worker.StreamingSpawner
	opsSpawn       ops.BatchSpawner
	reviewOpsSpawn ops.BatchSpawner
}

var (
	newClaudeWorkerSpawner    = func() worker.StreamingSpawner { return &worker.ClaudeSpawner{} }         //nolint:gochecknoglobals // injectable constructor for runtime-specific test seams
	newClaudeOpsSpawner       = func() ops.BatchSpawner { return ops.NewClaudeOpsSpawner() }              //nolint:gochecknoglobals // injectable constructor for runtime-specific test seams
	newClaudeReviewOpsSpawner = func() ops.BatchSpawner { return ops.NewClaudeReviewOpsSpawner() }        //nolint:gochecknoglobals // injectable constructor for runtime-specific test seams
	newCodexWorkerSpawner     = func() worker.StreamingSpawner { return codexruntime.NewWorkerSpawner() } //nolint:gochecknoglobals // injectable constructor for runtime-specific test seams
	newCodexOpsSpawner        = codexruntime.NewOpsSpawner                                                //nolint:gochecknoglobals // injectable constructor for runtime-specific test seams
)

func readAgentRuntime() string {
	return agentruntime.ReadRuntime()
}

func resolveProductionRuntime() (*productionRuntime, error) {
	switch runtime := readAgentRuntime(); runtime {
	case runtimeClaude:
		return &productionRuntime{
			id:             runtimeClaude,
			workerSpawn:    newClaudeWorkerSpawner(),
			opsSpawn:       ops.NewRuntimeSpawnerRouter(newClaudeOpsSpawner(), newCodexOpsSpawner()),
			reviewOpsSpawn: newClaudeReviewOpsSpawner(),
		}, nil
	case runtimeCodex:
		return &productionRuntime{
			id:             runtimeCodex,
			workerSpawn:    newCodexWorkerSpawner(),
			opsSpawn:       ops.NewRuntimeSpawnerRouter(newClaudeOpsSpawner(), newCodexOpsSpawner()),
			reviewOpsSpawn: newClaudeReviewOpsSpawner(),
		}, nil
	default:
		return nil, fmt.Errorf("unknown agent runtime %q", runtime)
	}
}
