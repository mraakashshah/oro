//nolint:testpackage // The contract test verifies repository-owned CI assets.
package github

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"
)

const aggregateJobName = "oro-portable-qg"

var portableJobNames = []string{"go", "cgo-free", "shell", "docs", "python"}

func TestPortableAggregateWorkflowContract(t *testing.T) {
	t.Parallel()

	repoRoot := repositoryRoot(t)
	workflow := readWorkflow(t, filepath.Join(repoRoot, ".github", "workflows", "ci.yml"))
	assertPullRequestAllowsEveryBase(t, workflow)
	aggregate := assertSingleAggregateJob(t, workflow)
	assertAggregateDependsOnPortableJobs(t, aggregate)
	assertAggregateAlwaysRuns(t, aggregate)
	assertNeedsSuccessHelper(t, repoRoot)
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

func assertAggregateDependsOnPortableJobs(t *testing.T, aggregate map[string]any) {
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
	want := slices.Clone(portableJobNames)
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

func assertNeedsSuccessHelper(t *testing.T, repoRoot string) {
	t.Helper()
	script := filepath.Join(repoRoot, "scripts", "ci", "require-needs-success.sh")
	t.Run("accepts all successful dependencies", func(t *testing.T) {
		runNeedsHelper(t, script, needsJSON(t, "success", "success", "success", "success", "success"), false)
	})
	for _, conclusion := range []string{"missing", "skipped", "cancelled", "timed_out", "action_required", "stale", "failure"} {
		t.Run("rejects "+conclusion, func(t *testing.T) {
			if conclusion == "missing" {
				runNeedsHelper(t, script, `{}`, true)
				return
			}
			runNeedsHelper(t, script, needsJSON(t, conclusion, "success", "success", "success", "success"), true)
		})
	}
}

func needsJSON(t *testing.T, conclusions ...string) string {
	t.Helper()
	needs := make(map[string]map[string]string, len(portableJobNames))
	for index, jobName := range portableJobNames {
		needs[jobName] = map[string]string{"result": conclusions[index]}
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
