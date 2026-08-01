//nolint:testpackage // The contract test verifies repository-owned CI assets.
package github

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const aggregateJobName = "oro-portable-qg"

func TestPortableAggregateWorkflowContract(t *testing.T) {
	t.Parallel()

	repoRoot := repositoryRoot(t)
	workflow := readWorkflow(t, filepath.Join(repoRoot, ".github", "workflows", "ci.yml"))
	assertPullRequestAllowsEveryBase(t, workflow)
	aggregate := assertSingleAggregateJob(t, workflow)
	assertAggregateDependsOnPortableJobs(t, workflow, aggregate)
	assertAggregateAlwaysRuns(t, aggregate)
	assertAggregateChecksOutRepository(t, aggregate)
	assertNeedsSuccessHelper(t, repoRoot, portableJobNamesFromWorkflow(t, workflow))
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate workflow contract test")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
}

func readWorkflow(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read workflow: %v", err)
	}
	var workflow map[string]any
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse workflow: %v", err)
	}
	return workflow
}

func assertPullRequestAllowsEveryBase(t *testing.T, workflow map[string]any) {
	t.Helper()
	triggers, ok := workflow["on"].(map[string]any)
	if !ok {
		t.Fatalf("workflow on = %#v, want mapping with pull_request", workflow["on"])
	}
	if _, ok := triggers["pull_request"]; !ok {
		t.Fatalf("workflow pull_request trigger missing: %#v", triggers)
	}
	if pullRequest, ok := triggers["pull_request"].(map[string]any); ok {
		if _, filtered := pullRequest["branches"]; filtered {
			t.Fatalf("pull_request must accept every base, found branches filter: %#v", pullRequest)
		}
	}
}

func assertSingleAggregateJob(t *testing.T, workflow map[string]any) map[string]any {
	t.Helper()
	jobs, ok := workflow["jobs"].(map[string]any)
	if !ok {
		t.Fatalf("workflow jobs = %#v, want mapping", workflow["jobs"])
	}
	aggregate, ok := jobs[aggregateJobName].(map[string]any)
	if !ok {
		t.Fatalf("aggregate job %q missing", aggregateJobName)
	}
	if occurrences := countAggregateJobs(workflow); occurrences != 1 {
		t.Fatalf("aggregate job %q occurs %d times, want 1", aggregateJobName, occurrences)
	}
	return aggregate
}

func countAggregateJobs(workflow map[string]any) int {
	jobs, _ := workflow["jobs"].(map[string]any)
	count := 0
	for jobName := range jobs {
		if jobName == aggregateJobName {
			count++
		}
	}
	return count
}

// portableJobNamesFromWorkflow derives the expected gate set from the workflow
// itself - every job except the aggregate - so a newly added CI job cannot
// silently escape the required aggregate. This mirrors portable_job_names() in
// scripts/ci/workflow_contract_test.sh, which already derives it dynamically.
// This Go copy used to hardcode the list and went stale when 9dab8d8c added the
// blocking incremental-mutation job and updated only the shell contract.
func portableJobNamesFromWorkflow(t *testing.T, workflow map[string]any) []string {
	t.Helper()
	jobs, ok := workflow["jobs"].(map[string]any)
	if !ok {
		t.Fatalf("workflow jobs = %#v, want map", workflow["jobs"])
	}
	names := make([]string, 0, len(jobs))
	for name := range jobs {
		if name == aggregateJobName {
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		t.Fatal("workflow declares no jobs besides the aggregate")
	}
	return names
}

func assertAggregateDependsOnPortableJobs(t *testing.T, workflow, aggregate map[string]any) {
	t.Helper()
	needs, ok := aggregate["needs"].([]any)
	if !ok {
		t.Fatalf("aggregate needs = %#v, want list", aggregate["needs"])
	}
	actual := make([]string, 0, len(needs))
	for _, need := range needs {
		name, ok := need.(string)
		if !ok {
			t.Fatalf("aggregate need = %#v, want string", need)
		}
		actual = append(actual, name)
	}
	slices.Sort(actual)
	want := portableJobNamesFromWorkflow(t, workflow)
	slices.Sort(want)
	if !slices.Equal(actual, want) {
		t.Fatalf("aggregate needs = %v, want every portable job %v", actual, want)
	}
}

func assertAggregateAlwaysRuns(t *testing.T, aggregate map[string]any) {
	t.Helper()
	if condition, _ := aggregate["if"].(string); condition != "${{ always() }}" {
		t.Fatalf("aggregate if = %q, want ${{ always() }}", condition)
	}
}

func assertAggregateChecksOutRepository(t *testing.T, aggregate map[string]any) {
	t.Helper()
	steps, ok := aggregate["steps"].([]any)
	if !ok {
		t.Fatalf("aggregate steps = %#v, want list", aggregate["steps"])
	}
	checkoutIndex := -1
	helperIndex := -1
	for index, rawStep := range steps {
		step, ok := rawStep.(map[string]any)
		if !ok {
			t.Fatalf("aggregate step = %#v, want mapping", rawStep)
		}
		if uses, _ := step["uses"].(string); uses == "actions/checkout@v4" {
			checkoutIndex = index
		}
		if command, _ := step["run"].(string); strings.Contains(command, "scripts/ci/require-needs-success.sh") {
			helperIndex = index
		}
	}
	if checkoutIndex < 0 {
		t.Fatal("aggregate must check out the repository before running its helper script")
	}
	if helperIndex < 0 {
		t.Fatal("aggregate needs-success helper step missing")
	}
	if checkoutIndex > helperIndex {
		t.Fatal("aggregate must check out the repository before running its helper script")
	}
}

func assertNeedsSuccessHelper(t *testing.T, repoRoot string, jobNames []string) {
	t.Helper()
	script := filepath.Join(repoRoot, "scripts", "ci", "require-needs-success.sh")
	assertHelperRequiresEveryJob(t, script, jobNames)
	t.Run("accepts all successful dependencies", func(t *testing.T) {
		runNeedsHelper(t, script, needsJSON(t, jobNames, "success"), false)
	})
	for _, conclusion := range []string{"missing", "skipped", "cancelled", "timed_out", "action_required", "stale", "failure"} {
		t.Run("rejects "+conclusion, func(t *testing.T) {
			if conclusion == "missing" {
				runNeedsHelper(t, script, `{}`, true)
				return
			}
			runNeedsHelper(t, script, needsJSON(t, jobNames, conclusion), true)
		})
	}
}

// assertHelperRequiresEveryJob keeps require-needs-success.sh's hardcoded
// required_jobs array in sync with ci.yml. That array is a third copy of the job
// list alongside this test and scripts/ci/workflow_contract_test.sh; when
// 9dab8d8c added incremental-mutation it updated the shell contract and the helper
// but not this test, so the aggregate could have gone green while a required job
// was silently absent from the gate.
func assertHelperRequiresEveryJob(t *testing.T, script string, jobNames []string) {
	t.Helper()
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatalf("read needs helper: %v", err)
	}
	for _, name := range jobNames {
		if !strings.Contains(string(data), name) {
			t.Errorf("require-needs-success.sh does not require job %q; a job in ci.yml is missing from required_jobs", name)
		}
	}
}

// needsJSON builds a needs fixture where the first job carries firstConclusion
// and every other required job succeeds.
func needsJSON(t *testing.T, jobNames []string, firstConclusion string) string {
	t.Helper()
	// The helper validates dependencies BY NAME, so the fixture must use the real
	// job names. Deriving them from ci.yml means a newly added job is covered
	// automatically instead of silently shrinking this fixture.
	needs := make(map[string]map[string]string, len(jobNames))
	for index, name := range jobNames {
		result := "success"
		if index == 0 {
			result = firstConclusion
		}
		needs[name] = map[string]string{"result": result}
	}
	encoded, err := json.Marshal(needs)
	if err != nil {
		t.Fatalf("encode needs fixture: %v", err)
	}
	return string(encoded)
}

func runNeedsHelper(t *testing.T, script, needs string, wantFailure bool) {
	t.Helper()
	command := exec.Command("bash", script, needs)
	output, err := command.CombinedOutput()
	if wantFailure && err == nil {
		t.Fatalf("helper accepted %s:\n%s", needs, output)
	}
	if !wantFailure && err != nil {
		t.Fatalf("helper rejected %s: %v\n%s", needs, err, output)
	}
}
