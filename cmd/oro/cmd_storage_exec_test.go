package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"oro/pkg/storage"
)

func TestStorageExecCLILeaseEnvelope(t *testing.T) {
	oroHome := t.TempDir()
	workdir := filepath.Join(t.TempDir(), "workdir")
	cacheRoot := filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(workdir, 0o750); err != nil {
		t.Fatalf("create workdir: %v", err)
	}
	t.Setenv("ORO_HOME", oroHome)
	t.Setenv("XDG_CACHE_HOME", cacheRoot)

	paths, err := ResolveStoragePaths(oroHome)
	if err != nil {
		t.Fatalf("ResolveStoragePaths() error = %v", err)
	}
	t.Setenv("ORO_STORAGE_EXEC_CATALOG", paths.CatalogPath)

	root := newRootCmd()
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stdout)
	root.SetArgs([]string{
		"storage", "exec", "--workdir", workdir, "--",
		os.Args[0], "-test.run=^TestStorageExecCLILeaseEnvelopeHelper$", "--",
		"literal;not-a-shell", "two words", "--exit=17",
	})
	err = root.Execute()
	var exitErr interface{ ExitCode() int }
	if !errors.As(err, &exitErr) {
		t.Fatalf("storage exec error = %v, want exit-code error", err)
	}
	if exitErr.ExitCode() != 17 {
		t.Fatalf("storage exec exit code = %d, want 17; child output: %q", exitErr.ExitCode(), stdout.String())
	}

	var got storageExecHelperReport
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode child report %q: %v", stdout.String(), err)
	}
	wantWorkdir, err := filepath.EvalSymlinks(workdir)
	if err != nil {
		t.Fatalf("resolve workdir symlinks: %v", err)
	}
	if got.Workdir != wantWorkdir {
		t.Errorf("child workdir = %q, want %q", got.Workdir, wantWorkdir)
	}
	if !reflect.DeepEqual(got.Args, []string{"literal;not-a-shell", "two words", "--exit=17"}) {
		t.Errorf("child argv = %#v, want literal argv", got.Args)
	}
	if !got.LeaseActive || got.ScratchDir == "" {
		t.Errorf("child lifecycle = %+v, want active lease and scratch", got)
	}
	if filepath.Base(filepath.Dir(got.ScratchDir)) != "oro-subprocess" {
		t.Errorf("child scratch = %q, want Oro runtime scratch", got.ScratchDir)
	}

	catalog, err := storage.OpenCatalog(t.Context(), paths.CatalogPath)
	if err != nil {
		t.Fatalf("open catalog after exec: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	var active int
	if err := catalog.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM runtime_leases WHERE released_at IS NULL`).Scan(&active); err != nil {
		t.Fatalf("count active leases: %v", err)
	}
	if active != 0 {
		t.Errorf("active leases after child exit = %d, want 0", active)
	}
}

type storageExecHelperReport struct {
	Workdir     string   `json:"workdir"`
	Args        []string `json:"args"`
	LeaseActive bool     `json:"lease_active"`
	ScratchDir  string   `json:"scratch_dir"`
}

func TestStorageExecCLILeaseEnvelopeHelper(t *testing.T) {
	if os.Getenv("ORO_STORAGE_EXEC_CATALOG") == "" {
		return
	}
	args := os.Args
	separator := 0
	for i, arg := range args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator == 0 {
		fmt.Fprintln(os.Stderr, "missing child argv separator")
		os.Exit(2)
	}
	workdir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "get child workdir: %v\n", err)
		os.Exit(2)
	}
	scratchDir := os.Getenv("TMPDIR")
	if _, err := os.Stat(scratchDir); err != nil {
		fmt.Fprintf(os.Stderr, "stat child scratch %q: %v\n", scratchDir, err)
		os.Exit(2)
	}
	db, err := sql.Open("sqlite", os.Getenv("ORO_STORAGE_EXEC_CATALOG"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "open child catalog: %v\n", err)
		os.Exit(2)
	}
	defer func() { _ = db.Close() }()
	var active int
	if err := db.QueryRow(`SELECT COUNT(*) FROM runtime_leases WHERE released_at IS NULL`).Scan(&active); err != nil {
		fmt.Fprintf(os.Stderr, "query child lease: %v\n", err)
		os.Exit(2)
	}
	report, err := json.Marshal(storageExecHelperReport{
		Workdir:     workdir,
		Args:        args[separator+1:],
		LeaseActive: active == 1,
		ScratchDir:  scratchDir,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "encode child report: %v\n", err)
		os.Exit(2)
	}
	_, _ = os.Stdout.Write(report)
	os.Exit(17)
}
