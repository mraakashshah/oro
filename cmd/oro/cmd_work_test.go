package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/worker"
)

func TestRunWorkResolvesRuntimeIdentityBeforeSpawnerConstruction(t *testing.T) {
	type observedEnv struct {
		oroHome string
		project string
	}

	ctx := context.Background()
	repoRoot := t.TempDir()
	homeDir := t.TempDir()
	project := "runtime-identity"
	oroHome := filepath.Join(homeDir, ".oro")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".oro"), 0o750); err != nil {
		t.Fatalf("create project config directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".oro", "config.yaml"), []byte("project: "+project+"\n"), 0o600); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	t.Chdir(repoRoot)
	t.Setenv("HOME", homeDir)
	t.Setenv("ORO_HOME", "")
	t.Setenv("ORO_PROJECT", "")
	t.Setenv(agentRuntimeEnvVar, runtimeClaude)
	t.Setenv("ORO_BEADSOURCE_MODE", "sqlite")
	if err := os.MkdirAll(filepath.Join(oroHome, "projects", project), 0o750); err != nil {
		t.Fatalf("create project state directory: %v", err)
	}
	db, err := openStateDB(filepath.Join(oroHome, "projects", project, "state.db"))
	if err != nil {
		t.Fatalf("open state database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := beadstore.NewSQLiteStore(db)
	if _, err := store.Create(ctx, beadstore.CreateParams{
		ID:                 "runtime-identity",
		Title:              "Verify runtime identity",
		Type:               "task",
		AcceptanceCriteria: "identity is restored",
	}); err != nil {
		t.Fatalf("create bead: %v", err)
	}

	previousClaude := newClaudeWorkerSpawner
	previousCodex := newCodexWorkerSpawner
	defer func() {
		newClaudeWorkerSpawner = previousClaude
		newCodexWorkerSpawner = previousCodex
	}()
	newClaudeWorkerSpawner = func() worker.StreamingSpawner { return &workerRouterTestSpawner{} }

	var constructedEnv observedEnv
	newCodexWorkerSpawner = func() worker.StreamingSpawner {
		constructedEnv = observedEnv{oroHome: os.Getenv("ORO_HOME"), project: os.Getenv("ORO_PROJECT")}
		return &workerRouterTestSpawner{}
	}

	if err := runWork(nil, &workConfig{beadID: "runtime-identity", dryRun: true, reviewTimeout: time.Second}); err != nil {
		t.Fatalf("runWork: %v", err)
	}
	if constructedEnv != (observedEnv{oroHome: oroHome, project: project}) {
		t.Fatalf("runtime spawner saw %+v, want ORO_HOME=%q and ORO_PROJECT=%q", constructedEnv, oroHome, project)
	}

	t.Setenv("ORO_HOME", "")
	t.Setenv("ORO_PROJECT", "")
	if err := executeWork(ctx, &workConfig{beadID: "runtime-identity", dryRun: true}, &workDeps{beadSrc: store, repoRoot: repoRoot}); err != nil {
		t.Fatalf("executeWork: %v", err)
	}
	if got := (observedEnv{oroHome: os.Getenv("ORO_HOME"), project: os.Getenv("ORO_PROJECT")}); got != (observedEnv{oroHome: oroHome, project: project}) {
		t.Fatalf("executeWork environment = %+v, want ORO_HOME=%q and ORO_PROJECT=%q", got, oroHome, project)
	}
}
