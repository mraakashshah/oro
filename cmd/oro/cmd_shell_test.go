package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// capturedShellInvocation holds what the stub runner captured.
type capturedShellInvocation struct {
	claudePath string
	args       []string
	env        []string
}

// stubShellRunner returns a shellRunner that records its invocation.
func stubShellRunner(out *capturedShellInvocation) shellRunner {
	return func(claudePath string, args []string, env []string) error {
		out.claudePath = claudePath
		out.args = args
		out.env = env
		return nil
	}
}

// fakeLookPath returns a lookPath stub that always resolves to fakePath.
func fakeLookPath(fakePath string) func(string) (string, error) {
	return func(string) (string, error) {
		return fakePath, nil
	}
}

// envGet extracts the value for key from an env slice (KEY=value format).
func envGet(env []string, key string) string {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return strings.TrimPrefix(e, prefix)
		}
	}
	return ""
}

// makeShellTestDir creates a project root with .oro/config.yaml and a settings.json.
func makeShellTestDir(t *testing.T, projectName string) (projectRoot, oroHome string) {
	t.Helper()
	tmp := t.TempDir()
	projectRoot = tmp
	oroHome = filepath.Join(tmp, "oro-home")

	configPath := filepath.Join(tmp, ".oro", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("project: "+projectName+"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	settingsDir := filepath.Join(oroHome, "projects", projectName)
	if err := os.MkdirAll(settingsDir, 0o700); err != nil {
		t.Fatalf("mkdir settings dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "settings.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	return projectRoot, oroHome
}

// argPairPresent checks that the arg pair (flag, value) appears in args.
func argPairPresent(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestShellResolvesProject(t *testing.T) {
	const projectName = "myproject"

	t.Run("resolves project, sets ORO_HOME and ORO_PROJECT, constructs claude args", func(t *testing.T) {
		projectRoot, oroHome := makeShellTestDir(t, projectName)
		t.Setenv("ORO_HOME", oroHome)

		var invoc capturedShellInvocation
		deps := shellDeps{
			runner:   stubShellRunner(&invoc),
			lookPath: fakeLookPath("/usr/local/bin/claude"),
		}

		if err := runShell(projectRoot, nil, false, deps); err != nil {
			t.Fatalf("runShell: %v", err)
		}

		if v := envGet(invoc.env, "ORO_PROJECT"); v != projectName {
			t.Errorf("ORO_PROJECT: want %q, got %q", projectName, v)
		}
		if v := envGet(invoc.env, "ORO_HOME"); v != oroHome {
			t.Errorf("ORO_HOME: want %q, got %q", oroHome, v)
		}

		settingsPath := filepath.Join(oroHome, "projects", projectName, "settings.json")
		if !argPairPresent(invoc.args, "--add-dir", oroHome) {
			t.Errorf("expected --add-dir %s in claude args, got %v", oroHome, invoc.args)
		}
		if !argPairPresent(invoc.args, "--settings", settingsPath) {
			t.Errorf("expected --settings %s in claude args, got %v", settingsPath, invoc.args)
		}
	})

	t.Run("forwards --resume flag to claude", func(t *testing.T) {
		projectRoot, oroHome := makeShellTestDir(t, projectName)
		t.Setenv("ORO_HOME", oroHome)

		var invoc capturedShellInvocation
		deps := shellDeps{
			runner:   stubShellRunner(&invoc),
			lookPath: fakeLookPath("/usr/local/bin/claude"),
		}

		if err := runShell(projectRoot, nil, true, deps); err != nil {
			t.Fatalf("runShell: %v", err)
		}

		found := false
		for _, a := range invoc.args {
			if a == "--resume" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected --resume in claude args, got %v", invoc.args)
		}
	})

	t.Run("passes extra args through to claude", func(t *testing.T) {
		projectRoot, oroHome := makeShellTestDir(t, projectName)
		t.Setenv("ORO_HOME", oroHome)

		var invoc capturedShellInvocation
		deps := shellDeps{
			runner:   stubShellRunner(&invoc),
			lookPath: fakeLookPath("/usr/local/bin/claude"),
		}

		extra := []string{"--dangerously-skip-permissions", "--verbose"}
		if err := runShell(projectRoot, extra, false, deps); err != nil {
			t.Fatalf("runShell: %v", err)
		}

		for _, want := range extra {
			found := false
			for _, a := range invoc.args {
				if a == want {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected %q in claude args, got %v", want, invoc.args)
			}
		}
	})

	t.Run("missing .oro/config.yaml errors with oro init hint", func(t *testing.T) {
		tmp := t.TempDir()

		deps := shellDeps{
			runner:   stubShellRunner(&capturedShellInvocation{}),
			lookPath: fakeLookPath("/usr/local/bin/claude"),
		}

		err := runShell(tmp, nil, false, deps)
		if err == nil {
			t.Fatal("expected error for missing .oro/config.yaml")
		}
		if !strings.Contains(err.Error(), "oro init") {
			t.Errorf("error should mention 'oro init', got: %v", err)
		}
	})

	t.Run("empty project field in config errors", func(t *testing.T) {
		tmp := t.TempDir()
		oroHome := filepath.Join(tmp, "oro-home")
		t.Setenv("ORO_HOME", oroHome)

		configPath := filepath.Join(tmp, ".oro", "config.yaml")
		if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configPath, []byte("project:\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		deps := shellDeps{
			runner:   stubShellRunner(&capturedShellInvocation{}),
			lookPath: fakeLookPath("/usr/local/bin/claude"),
		}

		err := runShell(tmp, nil, false, deps)
		if err == nil {
			t.Fatal("expected error for empty project field")
		}
	})

	t.Run("missing settings.json errors with oro setup hint", func(t *testing.T) {
		tmp := t.TempDir()
		oroHome := filepath.Join(tmp, "oro-home")
		t.Setenv("ORO_HOME", oroHome)

		configPath := filepath.Join(tmp, ".oro", "config.yaml")
		if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configPath, []byte("project: myproject\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		// No settings.json created in oroHome

		deps := shellDeps{
			runner:   stubShellRunner(&capturedShellInvocation{}),
			lookPath: fakeLookPath("/usr/local/bin/claude"),
		}

		err := runShell(tmp, nil, false, deps)
		if err == nil {
			t.Fatal("expected error for missing settings.json")
		}
		if !strings.Contains(err.Error(), "oro setup") {
			t.Errorf("error should mention 'oro setup', got: %v", err)
		}
	})

	t.Run("claude not in PATH errors", func(t *testing.T) {
		projectRoot, oroHome := makeShellTestDir(t, projectName)
		t.Setenv("ORO_HOME", oroHome)

		deps := shellDeps{
			runner: stubShellRunner(&capturedShellInvocation{}),
			lookPath: func(string) (string, error) {
				return "", exec.ErrNotFound
			},
		}

		err := runShell(projectRoot, nil, false, deps)
		if err == nil {
			t.Fatal("expected error when claude not in PATH")
		}
	})
}

func TestShellRegisteredInRoot(t *testing.T) {
	root := newRootCmd()
	for _, sub := range root.Commands() {
		if sub.Name() == "shell" {
			return
		}
	}
	t.Fatal("expected 'shell' subcommand in root")
}
