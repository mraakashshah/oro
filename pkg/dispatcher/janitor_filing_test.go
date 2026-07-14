//nolint:testpackage // white-box test exercises the unexported janitor result handler
package dispatcher

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"oro/pkg/beadstore"
	"oro/pkg/ops"
	"oro/pkg/protocol"
)

func TestJanitorFindingAcceptanceSource(t *testing.T) {
	finding := ops.Finding{
		ID:       "fnd_source",
		Severity: ops.SevMinor,
		Title:    "source-aware finding",
		Detail:   "detector-backed cleanup",
		Sources:  []string{"todo", "todo", "missing-tool"},
	}

	fileAcceptance := func(t *testing.T, projectScript bool) string {
		t.Helper()
		d, store, _, _, _, _ := newTestDispatcher(t)
		feedback, err := json.Marshal(struct {
			Findings      []ops.Finding `json:"findings"`
			RanDetectors  []string      `json:"ran_detectors"`
			ProjectScript bool          `json:"project_script"`
		}{
			Findings:      []ops.Finding{finding},
			RanDetectors:  []string{"todo"},
			ProjectScript: projectScript,
		})
		if err != nil {
			t.Fatalf("marshal janitor result: %v", err)
		}
		d.handleJanitorResult(t.Context(), ops.Result{
			Type: ops.OpsJanitor, BeadID: "oro-janitor-role", Feedback: string(feedback),
		})
		store.mu.Lock()
		defer store.mu.Unlock()
		if len(store.created) != 1 {
			t.Fatalf("created beads = %d, want 1", len(store.created))
		}
		return store.created[0].acceptanceCriteria
	}

	t.Run("project detector script is rerunnable", func(t *testing.T) {
		acceptance := fileAcceptance(t, true)
		if got := strings.Count(acceptance, "oro janitor:detect --project-script --detector 'todo'"); got != 1 {
			t.Fatalf("project acceptance detector commands = %d, want 1: %q", got, acceptance)
		}
		if !strings.Contains(acceptance, "./scripts/quality_gate.sh") {
			t.Fatalf("project acceptance missing quality gate: %q", acceptance)
		}
	})

	t.Run("built-in fallback never references the absent script", func(t *testing.T) {
		acceptance := fileAcceptance(t, false)
		if strings.Contains(acceptance, "scripts/janitor_detect.sh") {
			t.Fatalf("fallback acceptance references absent detector script: %q", acceptance)
		}
		if got := strings.Count(acceptance, "oro janitor:detect --detector 'todo'"); got != 1 {
			t.Fatalf("fallback acceptance detector commands = %d, want 1: %q", got, acceptance)
		}
		if strings.Contains(acceptance, "missing-tool") {
			t.Fatalf("fallback acceptance includes detector that did not run: %q", acceptance)
		}
		if !strings.Contains(acceptance, "'todo' && ./scripts/quality_gate.sh") {
			t.Fatalf("fallback acceptance missing detector-to-gate sequence: %q", acceptance)
		}
	})

	t.Run("built-in detector arguments are shell safe", func(t *testing.T) {
		const hostile = "todo'; printf pwned"
		acceptance := janitorFindingAcceptance(ops.Finding{
			ID:      "fnd_hostile",
			Sources: []string{hostile},
		}, []string{hostile}, false, "")
		if !strings.Contains(acceptance, `--detector 'todo'\''; printf pwned'`) {
			t.Fatalf("fallback acceptance argument is not shell quoted: %q", acceptance)
		}
	})

	t.Run("CI detector receives the configured target branch", func(t *testing.T) {
		const targetBranch = "release/v1'; printf pwned"
		acceptance := janitorFindingAcceptance(ops.Finding{
			ID:      "fnd_ci",
			Sources: []string{"ci"},
		}, []string{"ci"}, false, targetBranch)
		if !strings.Contains(acceptance, `--detector 'ci' --target-branch 'release/v1'\''; printf pwned'`) {
			t.Fatalf("CI acceptance target branch is missing or unsafe: %q", acceptance)
		}
	})
}

func TestJanitorFindingAcceptanceSourceShellSafe(t *testing.T) {
	worktree := t.TempDir()
	writeJanitorAcceptanceScript(t, worktree, "scripts/janitor_detect.sh", "#!/bin/sh\nprintf '%s' \"$2\" > detector-arg\n")
	writeJanitorAcceptanceScript(t, worktree, "scripts/quality_gate.sh", "#!/bin/sh\n: > quality-gate-ran\n")
	writeJanitorAcceptanceScript(t, worktree, "bin/oro", `#!/bin/sh
if [ "$2" = --project-script ]; then
  printf '%s' "$4" > detector-arg
else
  printf '%s' "$3" > detector-arg
fi
`)

	for _, projectScript := range []bool{true, false} {
		mode := "built-in"
		if projectScript {
			mode = "project-script"
		}
		for _, detector := range []string{
			"detector with spaces; touch injected",
			"detector'quoted",
			"detector\nwith\nnewlines\n",
		} {
			t.Run(mode+"/"+strings.ReplaceAll(detector, "\n", "newline"), func(t *testing.T) {
				_ = os.Remove(filepath.Join(worktree, "detector-arg"))
				_ = os.Remove(filepath.Join(worktree, "quality-gate-ran"))
				command := janitorDetectorRerunCommand(detector, projectScript, "") + " && ./scripts/quality_gate.sh"
				if strings.Contains(command, "\n") {
					t.Fatalf("generated command contains a literal newline: %q", command)
				}
				cmd := exec.Command("sh", "-c", command) //nolint:gosec // executes generated acceptance only in an isolated fixture.
				cmd.Dir = worktree
				cmd.Env = append(os.Environ(), "PATH="+filepath.Join(worktree, "bin")+":"+os.Getenv("PATH"))
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("run generated command: %v\n%s\n%s", err, output, command)
				}
				got, err := os.ReadFile(filepath.Join(worktree, "detector-arg"))
				if err != nil {
					t.Fatalf("read captured detector: %v", err)
				}
				if string(got) != detector {
					t.Fatalf("captured detector = %q, want exact %q", got, detector)
				}
				if _, err := os.Stat(filepath.Join(worktree, "injected")); !os.IsNotExist(err) {
					t.Fatalf("detector argument injected a command: %v", err)
				}
				if _, err := os.Stat(filepath.Join(worktree, "quality-gate-ran")); err != nil {
					t.Fatalf("quality gate did not run: %v", err)
				}
			})
		}
	}
}

func TestJanitorProjectAcceptanceRequiresClear(t *testing.T) {
	worktree := t.TempDir()
	writeJanitorAcceptanceScript(t, worktree, "scripts/janitor_detect.sh", "#!/bin/sh\n: > direct-script-ran\nexit 0\n")
	writeJanitorAcceptanceScript(t, worktree, "scripts/quality_gate.sh", "#!/bin/sh\n: > quality-gate-ran\n")
	writeJanitorAcceptanceScript(t, worktree, "bin/oro", `#!/bin/sh
[ "$1" = janitor:detect ] && [ "$2" = --project-script ] && [ "$3" = --detector ] || exit 9
printf '%s' "$4" > detector-arg
[ "$(cat detector-state)" = remaining ] && exit 3
exit 0
`)
	const detector = "project'; touch injected"
	command := janitorDetectorRerunCommand(detector, true, "") + " && ./scripts/quality_gate.sh"
	run := func() error {
		cmd := exec.Command("sh", "-c", command) //nolint:gosec // exercises generated acceptance in an isolated fixture.
		cmd.Dir = worktree
		cmd.Env = append(os.Environ(), "PATH="+filepath.Join(worktree, "bin")+":"+os.Getenv("PATH"))
		return cmd.Run()
	}

	if err := os.WriteFile(filepath.Join(worktree, "detector-state"), []byte("remaining\n"), 0o600); err != nil {
		t.Fatalf("write remaining detector state: %v", err)
	}
	if err := run(); err == nil {
		t.Fatal("project acceptance passed while the named candidate remained")
	}
	if _, err := os.Stat(filepath.Join(worktree, "quality-gate-ran")); !os.IsNotExist(err) {
		t.Fatalf("quality gate ran before detector cleared: %v", err)
	}

	if err := os.WriteFile(filepath.Join(worktree, "detector-state"), []byte("clear\n"), 0o600); err != nil {
		t.Fatalf("write clear detector state: %v", err)
	}
	if err := run(); err != nil {
		t.Fatalf("project acceptance after detector cleared: %v", err)
	}
	argument, err := os.ReadFile(filepath.Join(worktree, "detector-arg"))
	if err != nil || string(argument) != detector {
		t.Fatalf("project detector argument = %q, %v; want %q", argument, err, detector)
	}
	if _, err := os.Stat(filepath.Join(worktree, "direct-script-ran")); !os.IsNotExist(err) {
		t.Fatalf("acceptance invoked the project script directly: %v", err)
	}
	if _, err := os.Stat(filepath.Join(worktree, "injected")); !os.IsNotExist(err) {
		t.Fatalf("project detector argument injected a command: %v", err)
	}
	if _, err := os.Stat(filepath.Join(worktree, "quality-gate-ran")); err != nil {
		t.Fatalf("quality gate did not run after detector cleared: %v", err)
	}
}

func writeJanitorAcceptanceScript(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("create scripts directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatalf("write acceptance script: %v", err)
	}
}

func TestJanitorTopKConfig(t *testing.T) {
	const roleBeadID = "oro-janitor-role"
	findings := []ops.Finding{
		{ID: "critical-1", Severity: ops.SevCritical, Title: "critical one"},
		{ID: "suppressed", Severity: ops.SevCritical, Title: "suppressed critical"},
		{ID: "wont-fix", Severity: ops.SevCritical, Status: "wont-fix", Title: "wont-fix critical"},
		{ID: "critical-2", Severity: ops.SevCritical, Title: "critical two"},
		{ID: "important-1", Severity: ops.SevImportant, Title: "important one"},
		{ID: "important-2", Severity: ops.SevImportant, Title: "important two"},
		{ID: "important-3", Severity: ops.SevImportant, Title: "important three"},
		{ID: "minor-1", Severity: ops.SevMinor, Title: "minor one"},
		{ID: "minor-2", Severity: ops.SevMinor, Title: "minor two"},
		{ID: "minor-3", Severity: ops.SevMinor, Title: "minor three"},
		{ID: "minor-4", Severity: ops.SevMinor, Title: "minor four"},
		{ID: "minor-5", Severity: ops.SevMinor, Title: "minor five"},
	}
	wantOrder := []string{
		"critical one", "critical two",
		"important one", "important two", "important three",
		"minor one", "minor two", "minor three", "minor four", "minor five",
	}

	for _, tc := range []struct {
		name  string
		limit int
		want  int
	}{
		{name: "configured two", limit: 2, want: 2},
		{name: "configured nine", limit: 9, want: 9},
		{name: "zero uses natural limit", limit: 0, want: janitorTopFindings},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, store, _, _, _, _ := newTestDispatcher(t)
			d.cfg.JanitorTopK = tc.limit
			store.journeys = make(map[string][]beadstore.JourneyEvent)
			store.metadataMatches = []*protocol.Bead{
				{ID: roleBeadID, Status: "closed", Metadata: map[string]any{cleanlinessRoleMetadataKey: "janitor"}},
				{ID: "oro-existing", Status: "open", Metadata: map[string]any{auditFindingMetadataKey: "suppressed"}},
			}
			appendSuppressionFixture(t, store, roleBeadID, findings[1], "janitor_finding")

			feedback, err := json.Marshal(janitorResultPayload{Findings: findings})
			if err != nil {
				t.Fatalf("marshal janitor result: %v", err)
			}
			d.handleJanitorResult(t.Context(), ops.Result{
				Type: ops.OpsJanitor, BeadID: roleBeadID, Feedback: string(feedback),
			})

			store.mu.Lock()
			created := append([]createCall(nil), store.created...)
			store.mu.Unlock()
			if len(created) != tc.want {
				t.Fatalf("created beads = %d, want %d", len(created), tc.want)
			}
			for i, call := range created {
				if call.title != wantOrder[i] {
					t.Fatalf("created bead %d title = %q, want %q", i, call.title, wantOrder[i])
				}
			}
		})
	}

	original := append([]ops.Finding(nil), findings...)
	_ = janitorTopFindingsBySeverity(findings, 0)
	if !reflect.DeepEqual(findings, original) {
		t.Fatalf("janitor severity selection mutated input\n got: %#v\nwant: %#v", findings, original)
	}
}

func TestJanitorFilingTopFive(t *testing.T) {
	ctx := context.Background()
	d, store, _, _, _, _ := newTestDispatcher(t)
	const roleBeadID = "oro-janitor-role"

	findings := []ops.Finding{
		{ID: "minor-1", Severity: ops.SevMinor, Title: "minor one", Detail: "minor detail", Sources: []string{"todo"}},
		{ID: "critical-1", Severity: ops.SevCritical, Title: "critical one", Detail: "critical detail", Sources: []string{"golangci-lint", "missing-tool"}},
		{ID: "important-1", Severity: ops.SevImportant, Title: "important one", Detail: "important detail", Sources: []string{"todo"}},
		{ID: "minor-2", Severity: ops.SevMinor, Title: "minor two", Detail: "minor detail", Sources: []string{"todo"}},
		{ID: "important-2", Severity: ops.SevImportant, Title: "important two", Detail: "important detail", Sources: []string{"golangci-lint"}},
		{ID: "minor-3", Severity: ops.SevMinor, Title: "minor three", Detail: "minor detail", Sources: []string{"todo"}},
		{ID: "minor-4", Severity: ops.SevMinor, Title: "minor four", Detail: "minor detail", Sources: []string{"todo"}},
	}
	feedback, err := json.Marshal(struct {
		Findings     []ops.Finding `json:"findings"`
		RanDetectors []string      `json:"ran_detectors"`
	}{Findings: findings, RanDetectors: []string{"todo", "golangci-lint"}})
	if err != nil {
		t.Fatalf("marshal feedback: %v", err)
	}

	d.handleJanitorResult(ctx, ops.Result{Type: ops.OpsJanitor, BeadID: roleBeadID, Feedback: string(feedback)})

	store.mu.Lock()
	created := append([]createCall(nil), store.created...)
	journey := append([]beadstore.JourneyEvent(nil), store.journeys[roleBeadID]...)
	store.mu.Unlock()
	if len(created) != 5 {
		t.Fatalf("created beads = %d, want 5: %#v", len(created), created)
	}
	if created[0].title != "critical one" || created[1].title != "important one" || created[2].title != "important two" {
		t.Fatalf("created severity ordering = [%q %q %q], want critical then important", created[0].title, created[1].title, created[2].title)
	}
	for _, call := range created {
		if call.priority != 2 || call.beadType != "task" {
			t.Errorf("created bead = priority %d type %q, want low-priority task", call.priority, call.beadType)
		}
		if call.metadata["meta_finding_id"] == "" {
			t.Errorf("created bead missing meta_finding_id: %#v", call.metadata)
		}
		if !strings.Contains(call.description, "wont-fix:") || !strings.Contains(call.description, "reopen") {
			t.Errorf("description missing wont-fix/reopen contract: %q", call.description)
		}
		if strings.Contains(call.acceptanceCriteria, "missing-tool") {
			t.Errorf("acceptance includes detector that did not run: %q", call.acceptanceCriteria)
		}
		if !strings.Contains(call.acceptanceCriteria, "Cmd:") {
			t.Errorf("acceptance missing rerun command: %q", call.acceptanceCriteria)
		}
	}
	if len(journey) != len(findings)+1 {
		t.Fatalf("janitor journey events = %d, want %d", len(journey), len(findings)+1)
	}
	for _, event := range journey[:len(findings)] {
		if event.Actor != "ops_janitor" || event.Event != "janitor_finding" {
			t.Errorf("journey finding event = %#v, want ops_janitor janitor_finding", event)
		}
	}
}

func TestJanitorFilingMalformedJSONRecordsJourneyNote(t *testing.T) {
	ctx := context.Background()
	d, store, _, _, _, _ := newTestDispatcher(t)
	const roleBeadID = "oro-janitor-role"

	d.handleJanitorResult(ctx, ops.Result{Type: ops.OpsJanitor, BeadID: roleBeadID, Feedback: "not json"})

	store.mu.Lock()
	created := len(store.created)
	journey := append([]beadstore.JourneyEvent(nil), store.journeys[roleBeadID]...)
	store.mu.Unlock()
	if created != 0 {
		t.Fatalf("created beads = %d, want 0", created)
	}
	if len(journey) != 1 || journey[0].Actor != "ops_janitor" || journey[0].Event != "note" {
		t.Fatalf("malformed JSON journey = %#v, want one ops_janitor note", journey)
	}
}
