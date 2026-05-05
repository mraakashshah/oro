package codestruct

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeImpactFixtureFile writes content to path, creating parent dirs as needed.
func writeImpactFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

// buildImpactFixture creates a 4-package Go module on disk:
//
//	cmd/app/main.go         calls caller.StartAll
//	pkg/caller/caller.go    calls dispatcher.Dispatcher.Run         (direct caller, depth 1)
//	pkg/dispatcher/dispatcher.go  defines Run, calls worker.Assemble + stdlib
//	pkg/worker/worker.go    defines Assemble                        (cross-package callee)
//
// Returns the project root and the absolute path to dispatcher.go.
func buildImpactFixture(t *testing.T) (root, dispatcherFile string) {
	t.Helper()
	root = t.TempDir()

	writeImpactFixtureFile(t, filepath.Join(root, "go.mod"), "module fixture\n\ngo 1.21\n")

	dispatcherFile = filepath.Join(root, "pkg", "dispatcher", "dispatcher.go")
	writeImpactFixtureFile(t, dispatcherFile, `package dispatcher

import (
	"context"
	"fmt"

	"fixture/pkg/worker"
)

// Dispatcher orchestrates workers.
type Dispatcher struct{}

// Run executes the dispatcher loop.
func (d *Dispatcher) Run(ctx context.Context) error {
	msg := worker.Assemble()
	ctx, cancel := context.WithTimeout(ctx, 0)
	defer cancel()
	fmt.Println(msg)
	return nil
}
`)

	writeImpactFixtureFile(t, filepath.Join(root, "pkg", "caller", "caller.go"), `package caller

import (
	"context"

	"fixture/pkg/dispatcher"
)

// StartAll runs the given dispatcher.
func StartAll(d *dispatcher.Dispatcher) {
	_ = d.Run(context.Background())
}
`)

	writeImpactFixtureFile(t, filepath.Join(root, "pkg", "worker", "worker.go"), `package worker

// Assemble builds a work message.
func Assemble() string {
	return "work"
}
`)

	writeImpactFixtureFile(t, filepath.Join(root, "cmd", "app", "main.go"), `package main

import (
	"fixture/pkg/caller"
	"fixture/pkg/dispatcher"
)

// main is the fixture entry point.
func main() {
	d := &dispatcher.Dispatcher{}
	caller.StartAll(d)
}
`)

	return root, dispatcherFile
}

func TestComputeImpactBlastRadius(t *testing.T) {
	root, dispatcherFile := buildImpactFixture(t)

	got, err := ComputeImpact(root, dispatcherFile, "Run")
	require.NoError(t, err)

	assert.Equal(t, "Run", got.Symbol)
	assert.Equal(t, filepath.Join("pkg", "dispatcher", "dispatcher.go"), got.File)

	assert.Equal(t,
		[]string{filepath.Join("pkg", "caller", "caller.go") + ":StartAll"},
		got.DirectCallers,
		"direct_callers must contain caller.StartAll only",
	)

	assert.Equal(t,
		[]string{filepath.Join("cmd", "app", "main.go") + ":main"},
		got.TransitiveCallers,
		"transitive_callers must contain main only (depth 2)",
	)

	assert.Equal(t,
		[]string{filepath.Join("pkg", "worker") + ".Assemble"},
		got.CrossPkgCallees,
	)

	assert.ElementsMatch(t,
		[]string{"context.WithTimeout", "fmt.Println"},
		got.ExternalCallees,
	)
}

func TestComputeImpactDirectCallersAreSortedAndDeduped(t *testing.T) {
	root := t.TempDir()
	writeImpactFixtureFile(t, filepath.Join(root, "go.mod"), "module dedup\n\ngo 1.21\n")

	target := filepath.Join(root, "pkg", "target", "target.go")
	writeImpactFixtureFile(t, target, `package target

// Hit is the symbol under inspection.
func Hit() {}
`)

	// Two callers in package zee whose names sort after package aaa.
	writeImpactFixtureFile(t, filepath.Join(root, "pkg", "zee", "zee.go"), `package zee

import "dedup/pkg/target"

// CallZ1 calls Hit.
func CallZ1() { target.Hit() }

// CallZ2 also calls Hit.
func CallZ2() { target.Hit() }

// CallZ1Again calls Hit twice from the same caller (must dedupe).
func CallZ1Again() {
	target.Hit()
	target.Hit()
}
`)

	writeImpactFixtureFile(t, filepath.Join(root, "pkg", "aaa", "aaa.go"), `package aaa

import "dedup/pkg/target"

// CallA calls Hit.
func CallA() { target.Hit() }
`)

	got, err := ComputeImpact(root, target, "Hit")
	require.NoError(t, err)

	wantDirect := []string{
		filepath.Join("pkg", "aaa", "aaa.go") + ":CallA",
		filepath.Join("pkg", "zee", "zee.go") + ":CallZ1",
		filepath.Join("pkg", "zee", "zee.go") + ":CallZ1Again",
		filepath.Join("pkg", "zee", "zee.go") + ":CallZ2",
	}
	assert.Equal(t, wantDirect, got.DirectCallers, "direct callers must be sorted and deduped")
}

func TestComputeImpactWalkError(t *testing.T) {
	_, err := ComputeImpact(filepath.Join(t.TempDir(), "does-not-exist"), "ignored.go", "Run")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "impact:")
}

func TestFindGoModDir(t *testing.T) {
	root := t.TempDir()
	writeImpactFixtureFile(t, filepath.Join(root, "go.mod"), "module find\n\ngo 1.21\n")
	nested := filepath.Join(root, "a", "b", "c")
	require.NoError(t, os.MkdirAll(nested, 0o755))

	got, err := FindGoModDir(nested)
	require.NoError(t, err)
	gotResolved, err := filepath.EvalSymlinks(got)
	require.NoError(t, err)
	rootResolved, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	assert.Equal(t, rootResolved, gotResolved)
}

func TestFindGoModDirNotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := FindGoModDir(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no go.mod found")
}
