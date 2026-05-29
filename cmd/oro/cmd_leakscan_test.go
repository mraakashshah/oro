package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type leakscanJSON struct {
	Decision string `json:"decision"`
	Matches  []struct {
		Pattern string `json:"pattern"`
		Masked  string `json:"masked"`
	} `json:"matches"`
}

func TestLeakscanCmd(t *testing.T) {
	const secret = "sk-abcdefghijklmnopqrstuvwxyz123456"

	t.Run("stdin blocks with masked JSON", func(t *testing.T) {
		out, err := runLeakscanCmd(t, strings.NewReader("token="+secret), "--stdin")
		if err == nil {
			t.Fatal("expected block error")
		}
		got := decodeLeakscanJSON(t, out)
		if got.Decision != "Block" {
			t.Fatalf("decision = %q, want Block", got.Decision)
		}
		if len(got.Matches) != 1 {
			t.Fatalf("matches len = %d, want 1: %#v", len(got.Matches), got.Matches)
		}
		if got.Matches[0].Masked == "" || strings.Contains(got.Matches[0].Masked, secret) {
			t.Fatalf("match must contain masked preview only: %#v", got.Matches[0])
		}
		if strings.Contains(out, secret) {
			t.Fatalf("output leaked raw secret: %s", out)
		}
	})

	t.Run("file clean exits zero", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "plain.txt")
		if err := os.WriteFile(path, []byte("no credentials here"), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		out, err := runLeakscanCmd(t, nil, "--file", path)
		if err != nil {
			t.Fatalf("expected clean scan: %v\n%s", err, out)
		}
		got := decodeLeakscanJSON(t, out)
		if got.Decision != "Clean" {
			t.Fatalf("decision = %q, want Clean", got.Decision)
		}
		if len(got.Matches) != 0 {
			t.Fatalf("matches len = %d, want 0", len(got.Matches))
		}
	})

	t.Run("allowlist suppresses literal", func(t *testing.T) {
		dir := t.TempDir()
		input := filepath.Join(dir, "input.txt")
		allow := filepath.Join(dir, "allow.yaml")
		if err := os.WriteFile(input, []byte("token="+secret), 0o600); err != nil {
			t.Fatalf("write input: %v", err)
		}
		if err := os.WriteFile(allow, []byte("literals:\n  - "+secret+"\n"), 0o600); err != nil {
			t.Fatalf("write allowlist: %v", err)
		}
		out, err := runLeakscanCmd(t, nil, "--file", input, "--allowlist", allow)
		if err != nil {
			t.Fatalf("expected allowlisted clean scan: %v\n%s", err, out)
		}
		got := decodeLeakscanJSON(t, out)
		if got.Decision != "Clean" || len(got.Matches) != 0 {
			t.Fatalf("allowlisted result = %#v", got)
		}
	})

	t.Run("min entropy controls warnings", func(t *testing.T) {
		token := "qwertyuiopASDFGHJKLzxcvbnm123456"
		out, err := runLeakscanCmd(t, strings.NewReader("session="+token), "--stdin", "--min-entropy", "6.0")
		if err != nil {
			t.Fatalf("warn-only entropy result should exit zero: %v\n%s", err, out)
		}
		got := decodeLeakscanJSON(t, out)
		if got.Decision != "Clean" {
			t.Fatalf("decision = %q, want Clean", got.Decision)
		}
		if len(got.Matches) != 0 {
			t.Fatalf("matches len = %d, want 0 with high entropy threshold", len(got.Matches))
		}
	})

	t.Run("diff scans added lines", func(t *testing.T) {
		repo := initLeakscanGitRepo(t)
		path := filepath.Join(repo, "README.md")
		if err := os.WriteFile(path, []byte("clean\ntoken="+secret+"\n"), 0o600); err != nil {
			t.Fatalf("write modified file: %v", err)
		}
		out, err := runLeakscanCmdInDir(t, repo, nil, "--diff", "HEAD")
		if err == nil {
			t.Fatal("expected block error for diff secret")
		}
		got := decodeLeakscanJSON(t, out)
		if got.Decision != "Block" || len(got.Matches) == 0 {
			t.Fatalf("diff result = %#v", got)
		}
		if strings.Contains(out, secret) {
			t.Fatalf("diff output leaked raw secret: %s", out)
		}
	})
}

func runLeakscanCmd(t *testing.T, in *strings.Reader, args ...string) (string, error) {
	t.Helper()
	return runLeakscanCmdInDir(t, "", in, args...)
}

func runLeakscanCmdInDir(t *testing.T, dir string, in *strings.Reader, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := newLeakscanCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if in != nil {
		cmd.SetIn(in)
	}
	cmd.SetArgs(args)
	if dir != "" {
		t.Chdir(dir)
	}
	err := cmd.Execute()
	return out.String(), err
}

func decodeLeakscanJSON(t *testing.T, out string) leakscanJSON {
	t.Helper()
	var got leakscanJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode leakscan JSON: %v\n%s", err, out)
	}
	return got
}

func initLeakscanGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("clean\n"), 0o600); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, data)
	}
}
