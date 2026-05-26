package main

import (
	"context"

	"oro/pkg/ops"
)

type testRuntimeOpsSpawner struct {
	calls      int
	models     []string
	reasonings []string
}

func (s *testRuntimeOpsSpawner) Spawn(_ context.Context, model, _, _ string) (ops.Process, error) {
	s.calls++
	s.models = append(s.models, model)
	s.reasonings = append(s.reasonings, "")
	return nil, nil
}

func (s *testRuntimeOpsSpawner) SpawnWithReasoning(_ context.Context, model, reasoning, _, _ string) (ops.Process, error) {
	s.calls++
	s.models = append(s.models, model)
	s.reasonings = append(s.reasonings, reasoning)
	return nil, nil
}
