package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskTerminologyGuard(t *testing.T) {
	repoRoot := terminologyRepoRoot(t)
	script := filepath.Join(repoRoot, "scripts", "check-task-terminology.sh")

	t.Run("current repository passes guard", func(t *testing.T) {
		cmd := exec.Command(script)
		cmd.Dir = repoRoot
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("terminology guard failed:\n%s", string(output))
		}
	})

	t.Run("rejects public oro bead create guidance", func(t *testing.T) {
		badDoc := filepath.Join(t.TempDir(), "bad.md")
		if err := os.WriteFile(badDoc, []byte("Use `oro bead create --title=x` for new public work.\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command(script, badDoc)
		cmd.Dir = repoRoot
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("terminology guard accepted bad public guidance:\n%s", string(output))
		}
		if !strings.Contains(string(output), "oro bead create") {
			t.Fatalf("terminology guard rejection did not cite the bad command:\n%s", string(output))
		}
	})

	t.Run("rejects argv-form public oro bead command", func(t *testing.T) {
		badHook := filepath.Join(t.TempDir(), "bad_hook.py")
		if err := os.WriteFile(badHook, []byte("subprocess.run([\"oro\", \"bead\", \"create\", \"--title=x\"])\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command(script, badHook)
		cmd.Dir = repoRoot
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("terminology guard accepted argv-form bead command:\n%s", string(output))
		}
		if !strings.Contains(string(output), "\"bead\", \"create\"") {
			t.Fatalf("terminology guard rejection did not cite argv-form bead command:\n%s", string(output))
		}
	})

	t.Run("rejects multiline argv-form public oro bead command", func(t *testing.T) {
		badHook := filepath.Join(t.TempDir(), "bad_multiline_hook.py")
		content := strings.Join([]string{
			"subprocess.run([",
			"    \"oro\",",
			"    \"bead\",",
			"    \"create\",",
			"    \"--title=x\",",
			"])",
			"",
		}, "\n")
		if err := os.WriteFile(badHook, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command(script, badHook)
		cmd.Dir = repoRoot
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("terminology guard accepted multiline argv-form bead command:\n%s", string(output))
		}
		if !strings.Contains(string(output), "\"bead\",") || !strings.Contains(string(output), "\"create\",") {
			t.Fatalf("terminology guard rejection did not cite multiline argv-form bead command:\n%s", string(output))
		}
	})

	t.Run("rejects wrapped public oro bead command", func(t *testing.T) {
		badDoc := filepath.Join(t.TempDir(), "wrapped.md")
		content := "Rollback is not executable because `oro bead\nimport` is a stub.\n"
		if err := os.WriteFile(badDoc, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command(script, badDoc)
		cmd.Dir = repoRoot
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("terminology guard accepted wrapped public oro bead command:\n%s", string(output))
		}
		if !strings.Contains(string(output), "oro bead") || !strings.Contains(string(output), "import` is a stub") {
			t.Fatalf("terminology guard rejection did not cite wrapped command:\n%s", string(output))
		}
	})

	t.Run("rejects full normal public oro bead command surface", func(t *testing.T) {
		for _, command := range []string{
			"create",
			"show",
			"update",
			"close",
			"reopen",
			"defer",
			"undefer",
			"list",
			"status",
			"ready",
			"blocked",
			"closed",
			"dep",
			"deps",
			"tag",
			"meta",
			"note",
			"comment",
			"search",
			"export",
			"import",
			"doctor",
			"work",
		} {
			t.Run(command, func(t *testing.T) {
				badDoc := filepath.Join(t.TempDir(), "bad-"+command+".md")
				content := "Use `oro bead " + command + " oro-abc` for normal public task work.\n"
				if err := os.WriteFile(badDoc, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}

				cmd := exec.Command(script, badDoc)
				cmd.Dir = repoRoot
				output, err := cmd.CombinedOutput()
				if err == nil {
					t.Fatalf("terminology guard accepted public oro bead %s guidance:\n%s", command, string(output))
				}
				if !strings.Contains(string(output), "oro bead "+command) {
					t.Fatalf("terminology guard rejection did not cite oro bead %s:\n%s", command, string(output))
				}
			})
		}
	})

	t.Run("rejects argv-form full normal public oro bead command surface", func(t *testing.T) {
		badHook := filepath.Join(t.TempDir(), "bad_reopen_hook.py")
		if err := os.WriteFile(badHook, []byte("subprocess.run([\"oro\", \"bead\", \"reopen\", \"oro-abc\"])\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command(script, badHook)
		cmd.Dir = repoRoot
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("terminology guard accepted argv-form bead reopen command:\n%s", string(output))
		}
		if !strings.Contains(string(output), "\"bead\", \"reopen\"") {
			t.Fatalf("terminology guard rejection did not cite argv-form bead reopen command:\n%s", string(output))
		}
	})

	t.Run("rejects stale active beacon guidance", func(t *testing.T) {
		badBeacon := filepath.Join(t.TempDir(), "manager.md")
		if err := os.WriteFile(badBeacon, []byte("No beads ready. Use `oro task ready`.\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command(script, badBeacon)
		cmd.Dir = repoRoot
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("terminology guard accepted stale beacon guidance:\n%s", string(output))
		}
		if !strings.Contains(string(output), "No beads ready") {
			t.Fatalf("terminology guard rejection did not cite stale beacon guidance:\n%s", string(output))
		}
	})

	t.Run("rejects stale session context heading", func(t *testing.T) {
		badHookOutput := filepath.Join(t.TempDir(), "session_start_extras.py")
		if err := os.WriteFile(badHookOutput, []byte("lines = [\"## Stale Beads (no update in >3 days)\"]\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command(script, badHookOutput)
		cmd.Dir = repoRoot
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("terminology guard accepted stale session context heading:\n%s", string(output))
		}
		if !strings.Contains(string(output), "Stale Beads") {
			t.Fatalf("terminology guard rejection did not cite stale session context heading:\n%s", string(output))
		}
	})

	t.Run("rejects stale public workflow labels", func(t *testing.T) {
		badDoc := filepath.Join(t.TempDir(), "bad-workflow.md")
		content := strings.Join([]string{
			"## BEAD CRAFT",
			"LAUNCH -> OBSERVE -> DETECT -> SPEC/BEAD -> FIX",
			"Use the legacy Beads CLI for normal work.",
			"",
		}, "\n")
		if err := os.WriteFile(badDoc, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command(script, badDoc)
		cmd.Dir = repoRoot
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("terminology guard accepted stale public workflow labels:\n%s", string(output))
		}
		if !strings.Contains(string(output), "BEAD CRAFT") ||
			!strings.Contains(string(output), "SPEC/BEAD") ||
			!strings.Contains(string(output), "Beads CLI") {
			t.Fatalf("terminology guard rejection did not cite stale public workflow labels:\n%s", string(output))
		}
	})

	t.Run("rejects stale default command surfaces", func(t *testing.T) {
		for _, relPath := range []string{
			filepath.Join(".claude", "commands", "bad.md"),
			filepath.Join(".claude", "skills", "bad-skill"),
			filepath.Join("assets", "CLAUDE.md"),
			filepath.Join("assets", "ORO_AGENT.md"),
			filepath.Join("assets", "review-patterns.md"),
			filepath.Join("cmd", "oro", "_assets", "CLAUDE.md"),
			filepath.Join("cmd", "oro", "_assets", "ORO_AGENT.md"),
			filepath.Join("cmd", "oro", "_assets", "commands", "bad.md"),
		} {
			t.Run(relPath, func(t *testing.T) {
				tempRoot := t.TempDir()
				if err := os.MkdirAll(filepath.Join(tempRoot, "scripts"), 0o755); err != nil {
					t.Fatal(err)
				}
				scriptBytes, err := os.ReadFile(script)
				if err != nil {
					t.Fatal(err)
				}
				tempScript := filepath.Join(tempRoot, "scripts", "check-task-terminology.sh")
				if err := os.WriteFile(tempScript, scriptBytes, 0o755); err != nil {
					t.Fatal(err)
				}
				readme := strings.Join([]string{
					"### Task Terminology",
					"- **Task:** preferred public term for an Oro work item.",
					"- **Bead:** legacy/internal term still visible in storage, historical docs, compatibility CLI, and migration artifacts.",
					"- **Task type:** the `type` field, whose values include `task`, `bug`, `epic`, `research`, and `chore`.",
					"",
				}, "\n")
				if err := os.WriteFile(filepath.Join(tempRoot, "README.md"), []byte(readme), 0o644); err != nil {
					t.Fatal(err)
				}
				for _, dir := range []string{
					filepath.Join(tempRoot, "docs", "runbooks"),
					filepath.Join(tempRoot, "assets", "beacons"),
					filepath.Join(tempRoot, "assets", "commands"),
					filepath.Join(tempRoot, "assets", "skills"),
					filepath.Join(tempRoot, "assets", "hooks"),
					filepath.Join(tempRoot, ".claude", "hooks", "beacons"),
					filepath.Join(tempRoot, ".claude", "skills"),
					filepath.Join(tempRoot, "cmd", "oro", "_assets", "beacons"),
					filepath.Join(tempRoot, "cmd", "oro", "_assets", "hooks"),
					filepath.Join(tempRoot, "cmd", "oro", "_assets", "skills"),
				} {
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(tempRoot, "docs", "INSTALL.md"), nil, 0o644); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(tempRoot, relPath)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("Use `oro bead create --title=x` for new public work.\n"), 0o644); err != nil {
					t.Fatal(err)
				}

				cmd := exec.Command(tempScript)
				cmd.Dir = tempRoot
				output, err := cmd.CombinedOutput()
				if err == nil {
					t.Fatalf("terminology guard accepted stale command surface %s:\n%s", relPath, string(output))
				}
				if !strings.Contains(string(output), relPath) {
					t.Fatalf("terminology guard rejection did not cite stale command surface %s:\n%s", relPath, string(output))
				}
			})
		}
	})

	t.Run("rejects stale extensionless skill wording", func(t *testing.T) {
		badSkill := filepath.Join(t.TempDir(), "restart-oro")
		if err := os.WriteFile(badSkill, []byte("Assignment spam -- bead has no AC. Report which worker and bead are stuck.\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command(script, badSkill)
		cmd.Dir = repoRoot
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("terminology guard accepted stale extensionless skill wording:\n%s", string(output))
		}
		if !strings.Contains(string(output), "bead has no AC") || !strings.Contains(string(output), "worker and bead are stuck") {
			t.Fatalf("terminology guard rejection did not cite stale extensionless skill wording:\n%s", string(output))
		}
	})

	t.Run("rejects stale bead metadata export wording", func(t *testing.T) {
		badSkill := filepath.Join(t.TempDir(), "context-checkpoint.md")
		content := strings.Join([]string{
			"The pre-commit hook automatically runs `bead metadata export`, so manual sync is not needed.",
			"Close the task, then export bead metadata before committing.",
			"",
		}, "\n")
		if err := os.WriteFile(badSkill, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command(script, badSkill)
		cmd.Dir = repoRoot
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("terminology guard accepted stale bead metadata export wording:\n%s", string(output))
		}
		if !strings.Contains(string(output), "bead metadata export") || !strings.Contains(string(output), "export bead metadata") {
			t.Fatalf("terminology guard rejection did not cite stale metadata export wording:\n%s", string(output))
		}
	})

	t.Run("rejects stale operator proof wording", func(t *testing.T) {
		badRunbook := filepath.Join(t.TempDir(), "runbook.md")
		content := strings.Join([]string{
			"Stop before assigning worker proof beads.",
			"Record the controlled test bead ID.",
			"Use the migrated id, not the smoke bead.",
			"",
		}, "\n")
		if err := os.WriteFile(badRunbook, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command(script, badRunbook)
		cmd.Dir = repoRoot
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("terminology guard accepted stale operator proof wording:\n%s", string(output))
		}
		if !strings.Contains(string(output), "worker proof beads") ||
			!strings.Contains(string(output), "controlled test bead") ||
			!strings.Contains(string(output), "smoke bead") {
			t.Fatalf("terminology guard rejection did not cite stale operator proof wording:\n%s", string(output))
		}
	})

	t.Run("rejects bead-primary prose in public guidance", func(t *testing.T) {
		badDoc := filepath.Join(t.TempDir(), "bad-prose.md")
		content := strings.Join([]string{
			"Execute one bead at a time. Work is tracked as beads. All beads are visible via `oro task list`.",
			"Workers execute beads concurrently. The dispatcher assigns beads to idle workers.",
			"",
		}, "\n")
		if err := os.WriteFile(badDoc, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command(script, badDoc)
		cmd.Dir = repoRoot
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("terminology guard accepted bead-primary prose:\n%s", string(output))
		}
		if !strings.Contains(string(output), "one bead") ||
			!strings.Contains(string(output), "execute beads") ||
			!strings.Contains(string(output), "assigns beads") {
			t.Fatalf("terminology guard rejection did not cite bead-primary prose:\n%s", string(output))
		}
	})

	t.Run("rejects stale runtime ops prompt wording", func(t *testing.T) {
		badPrompt := filepath.Join(t.TempDir(), "ops.go")
		content := strings.Join([]string{
			"Diagnose why bead oro-abc is stuck.",
			"Create a blocker bead for unresolved dependencies.",
			"",
		}, "\n")
		if err := os.WriteFile(badPrompt, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command(script, badPrompt)
		cmd.Dir = repoRoot
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("terminology guard accepted stale runtime ops prompt wording:\n%s", string(output))
		}
		if !strings.Contains(string(output), "Diagnose why bead") || !strings.Contains(string(output), "blocker bead") {
			t.Fatalf("terminology guard rejection did not cite stale ops prompt wording:\n%s", string(output))
		}
	})

	t.Run("rejects stale current runtime command comments", func(t *testing.T) {
		tempRoot := t.TempDir()
		if err := os.MkdirAll(filepath.Join(tempRoot, "scripts"), 0o755); err != nil {
			t.Fatal(err)
		}
		scriptBytes, err := os.ReadFile(script)
		if err != nil {
			t.Fatal(err)
		}
		tempScript := filepath.Join(tempRoot, "scripts", "check-task-terminology.sh")
		if err := os.WriteFile(tempScript, scriptBytes, 0o755); err != nil {
			t.Fatal(err)
		}
		readme := strings.Join([]string{
			"### Task Terminology",
			"- **Task:** preferred public term for an Oro work item.",
			"- **Bead:** legacy/internal term still visible in storage, historical docs, compatibility CLI, and migration artifacts.",
			"- **Task type:** the `type` field, whose values include `task`, `bug`, `epic`, `research`, and `chore`.",
			"",
		}, "\n")
		if err := os.WriteFile(filepath.Join(tempRoot, "README.md"), []byte(readme), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, dir := range []string{
			filepath.Join(tempRoot, "docs"),
			filepath.Join(tempRoot, "cmd", "oro"),
			filepath.Join(tempRoot, "pkg", "dispatcher"),
		} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(tempRoot, "docs", "INSTALL.md"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tempRoot, "cmd", "oro", "tmux.go"), []byte("// SessionStart hooks (bd list, bd ready, git status, etc.)\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		dispatcherComment := strings.Join([]string{
			"// priority queue from oro bead ready",
			"// Determine the effective repo root for oro bead commands.",
			"",
		}, "\n")
		if err := os.WriteFile(filepath.Join(tempRoot, "pkg", "dispatcher", "dispatcher.go"), []byte(dispatcherComment), 0o644); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command(tempScript)
		cmd.Dir = tempRoot
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("terminology guard accepted stale runtime command comments:\n%s", string(output))
		}
		if !strings.Contains(string(output), "oro bead ready") ||
			!strings.Contains(string(output), "oro bead commands") ||
			!strings.Contains(string(output), "SessionStart hooks") {
			t.Fatalf("terminology guard rejection did not cite stale runtime comments:\n%s", string(output))
		}
	})

	t.Run("allows dot slash runtime comment file arguments", func(t *testing.T) {
		cmd := exec.Command(script, "./pkg/dispatcher/dispatcher.go", "./cmd/oro/tmux.go")
		cmd.Dir = repoRoot
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("terminology guard rejected dot-slash runtime file arguments:\n%s", string(output))
		}
	})

	t.Run("rejects invented task branch naming", func(t *testing.T) {
		badDoc := filepath.Join(t.TempDir(), "bad-branch.md")
		if err := os.WriteFile(badDoc, []byte("Worker worktrees use task/abc branches.\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command(script, badDoc)
		cmd.Dir = repoRoot
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("terminology guard accepted invented task branch naming:\n%s", string(output))
		}
		if !strings.Contains(string(output), "task/abc") {
			t.Fatalf("terminology guard rejection did not cite invented branch naming:\n%s", string(output))
		}
	})

	t.Run("allows storage and migration compatibility terms", func(t *testing.T) {
		allowedDoc := filepath.Join(t.TempDir(), "allowed.md")
		content := strings.Join([]string{
			"Storage keeps bead_id and beadstore names for compatibility.",
			"Migration operators still run `oro bead migrate-from-dolt --help`.",
			"Historical bd/Dolt backups remain audit evidence.",
			"",
		}, "\n")
		if err := os.WriteFile(allowedDoc, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command(script, allowedDoc)
		cmd.Dir = repoRoot
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("terminology guard rejected allowed compatibility terms:\n%s", string(output))
		}
	})

	t.Run("runs under macOS system bash", func(t *testing.T) {
		if _, err := os.Stat("/bin/bash"); err != nil {
			t.Skip("/bin/bash not available")
		}

		cmd := exec.Command("/bin/bash", script)
		cmd.Dir = repoRoot
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("terminology guard failed under /bin/bash:\n%s", string(output))
		}
	})

	t.Run("rejects junk smoke task create placeholders", func(t *testing.T) {
		badRunbook := filepath.Join(t.TempDir(), "bad-smoke.md")
		content := "Smoke: `oro task create --type task --title=t --description=d --acceptance-criteria=ac`.\n"
		if err := os.WriteFile(badRunbook, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command(script, badRunbook)
		cmd.Dir = repoRoot
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("terminology guard accepted junk smoke guidance:\n%s", string(output))
		}
		if !strings.Contains(string(output), "--title=t") {
			t.Fatalf("terminology guard rejection did not cite junk smoke command:\n%s", string(output))
		}
	})
}

func terminologyRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find repo root from %s", wd)
		}
	}
}
