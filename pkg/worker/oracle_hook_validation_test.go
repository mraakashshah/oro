package worker

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateManagedOracleHook(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	valid := filepath.Join(dir, "oracle-hook")
	if err := os.WriteFile(valid, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	nonExecutable := filepath.Join(dir, "not-executable")
	if err := os.WriteFile(nonExecutable, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	groupWritable := filepath.Join(dir, "group-writable")
	if err := os.WriteFile(groupWritable, []byte("#!/bin/sh\n"), 0o720); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(groupWritable, 0o720); err != nil {
		t.Fatal(err)
	}

	worldWritable := filepath.Join(dir, "world-writable")
	if err := os.WriteFile(worldWritable, []byte("#!/bin/sh\n"), 0o702); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(worldWritable, 0o702); err != nil {
		t.Fatal(err)
	}

	symlink := filepath.Join(dir, "symlink")
	if err := os.Symlink(valid, symlink); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		path string
		kind managedOracleHookErrorKind
	}{
		{name: "missing", path: filepath.Join(dir, "missing"), kind: managedOracleHookMissing},
		{name: "canonicalization failure", path: "\x00", kind: managedOracleHookCanonicalize},
		{name: "symlink", path: symlink, kind: managedOracleHookSymlink},
		{name: "directory", path: dir, kind: managedOracleHookNotRegular},
		{name: "non executable", path: nonExecutable, kind: managedOracleHookNotExecutable},
		{name: "group writable", path: groupWritable, kind: managedOracleHookGroupWritable},
		{name: "world writable", path: worldWritable, kind: managedOracleHookWorldWritable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ValidateManagedOracleHook(tt.path)
			assertManagedOracleHookErrorKind(t, err, tt.kind)
		})
	}

	t.Run("returns an absolute canonical regular executable", func(t *testing.T) {
		got, err := ValidateManagedOracleHook(valid)
		if err != nil {
			t.Fatal(err)
		}
		want, err := filepath.EvalSymlinks(valid)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("ValidateManagedOracleHook() = %q, want %q", got, want)
		}
	})

	t.Run("resolves symlinked parent directories", func(t *testing.T) {
		realDir := filepath.Join(dir, "real")
		if err := os.Mkdir(realDir, 0o700); err != nil {
			t.Fatal(err)
		}
		realHook := filepath.Join(realDir, "oracle-hook")
		if err := os.WriteFile(realHook, []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		aliasDir := filepath.Join(dir, "alias")
		if err := os.Symlink(realDir, aliasDir); err != nil {
			t.Fatal(err)
		}

		got, err := ValidateManagedOracleHook(filepath.Join(aliasDir, "oracle-hook"))
		if err != nil {
			t.Fatal(err)
		}
		want, err := filepath.EvalSymlinks(realHook)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("ValidateManagedOracleHook() = %q, want %q", got, want)
		}
	})

	t.Run("wrong owner is rejected by the facts seam", func(t *testing.T) {
		err := validateManagedOracleHookFacts(managedHookFileFacts{
			Mode: 0o700,
			UID:  2,
		}, 1)
		assertManagedOracleHookErrorKind(t, err, managedOracleHookWrongOwner)
	})
}

func assertManagedOracleHookErrorKind(t *testing.T, err error, want managedOracleHookErrorKind) {
	t.Helper()
	var hookErr *ManagedOracleHookError
	if !errors.As(err, &hookErr) {
		t.Fatalf("error = %v, want ManagedOracleHookError", err)
	}
	if hookErr.Kind != want {
		t.Fatalf("error kind = %q, want %q", hookErr.Kind, want)
	}
}
