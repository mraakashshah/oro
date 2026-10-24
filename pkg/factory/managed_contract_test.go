package factory //nolint:testpackage // Managed admission validates private contract seams.

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func managedContractFixture() ManagedSpec {
	return ManagedSpec{
		ID: "calculator", Objective: "Implement addition", Repository: "/private/tmp/repository", TargetBranch: "factory/calculator",
		RequiredChecks: []string{"addition"}, MaxAttempts: 3, TimeoutSeconds: 300, StepTimeoutSeconds: 60,
		Recovery: RecoveryPolicy{MaxControllerStarts: 5, MaxSupervisorStarts: 8, RestartDelaySeconds: 1,
			CleanupTimeoutSeconds: 10, ReconciliationSeconds: 15, MaxReconciliationInvocations: 4, MaxCleanupInvocations: 4},
		Runtime: RuntimePolicy{DockerContext: "colima-oro-factory", ImageDigest: "sha256:" + strings.Repeat("a", 64),
			MemoryBytes: 256 << 20, ScratchBytes: 64 << 20, SourceBytes: 16 << 20, HostRequiredBytes: 1 << 30, HostReserveBytes: 128 << 20,
			CPUs: 1.5, PIDs: 64, MaxFiles: 100, SourceInodes: 200, ScratchInodes: 200,
			MaxModelCalls: 10, MaxModelTurns: 20, MaxToolCalls: 50, MaxHostInvocations: 100,
			Model: "claude-sonnet-4-6", ModelExecutable: "/usr/local/bin/claude", GitExecutable: "/usr/bin/git", MaxModelInvocationUSD: "1.25"},
		Acceptance:         []Check{{ID: "addition", Args: []string{"./calculator", "2", "3"}, Stdin: "", ExpectedStdout: "5\n", ExpectedExit: 0}},
		ReviewInstructions: "Independently verify the implementation and exact acceptance results.",
	}
}

func TestManagedSpecContract(t *testing.T) {
	t.Run("valid contract and immutable image forms", testManagedValidContract)
	t.Run("strict JSON", testManagedStrictJSON)
	t.Run("bounded complete JSON stream", testManagedBoundedJSON)
	t.Run("unsafe identity and checks", testManagedInvalidIdentity)
	t.Run("bounded resources and recovery", testManagedInvalidBudgets)
	t.Run("exact decimal cents", testManagedDecimalCents)
	t.Run("complete stable policy identity", testManagedPolicyIdentity)
	t.Run("durable host reservation tuple", testManagedReservationIdentity)
	t.Run("legacy contract unchanged", func(t *testing.T) {
		spec := contractSpec()
		before, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateSpec(spec); err != nil {
			t.Fatal(err)
		}
		after, err := json.Marshal(spec)
		if err != nil || string(before) != string(after) {
			t.Fatalf("legacy contract changed: %s, %v", after, err)
		}
		if strings.Contains(string(after), "runtime") || strings.Contains(string(after), "recovery") {
			t.Fatal("managed policy leaked into legacy JSON")
		}
	})
}

func TestManagedCleanupPolicy(t *testing.T) {
	t.Run("checked record and storage bounds", testManagedCleanupRecordBounds)
	t.Run("required finite cleanup count", func(t *testing.T) {
		for _, value := range []string{"", "0", "-1", strconv.Itoa(math.MaxInt)} {
			if _, err := decodeManagedSpec(strings.NewReader(managedCleanupPolicyJSON(t, value))); err == nil {
				t.Errorf("unbounded or missing cleanup count %q accepted", value)
			}
		}
	})
	t.Run("large representable count has no arbitrary ceiling", func(t *testing.T) {
		if _, err := decodeManagedSpec(strings.NewReader(managedCleanupPolicyJSON(t, "1048576"))); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("cleanup count changes identity without extending lifetime", func(t *testing.T) {
		one, err := decodeManagedSpec(strings.NewReader(managedCleanupPolicyJSON(t, "1")))
		if err != nil {
			t.Fatal(err)
		}
		two, err := decodeManagedSpec(strings.NewReader(managedCleanupPolicyJSON(t, "2")))
		if err != nil {
			t.Fatal(err)
		}
		digests := map[string]string{"git": strings.Repeat("a", 64), "model": strings.Repeat("b", 64)}
		first, err := managedPolicyHash(one, digests)
		if err != nil {
			t.Fatal(err)
		}
		second, err := managedPolicyHash(two, digests)
		if err != nil || first == second {
			t.Fatalf("cleanup count omitted from identity: %s %s %v", first, second, err)
		}
		if one.Runtime != two.Runtime || one.TimeoutSeconds != two.TimeoutSeconds || one.StepTimeoutSeconds != two.StepTimeoutSeconds ||
			one.Recovery.CleanupTimeoutSeconds != two.Recovery.CleanupTimeoutSeconds || one.Recovery.ReconciliationSeconds != two.Recovery.ReconciliationSeconds ||
			one.Recovery.MaxReconciliationInvocations != two.Recovery.MaxReconciliationInvocations || one.Recovery.MaxSupervisorStarts != two.Recovery.MaxSupervisorStarts ||
			one.Recovery.MaxControllerStarts != two.Recovery.MaxControllerStarts {
			t.Fatal("cleanup count changed another allowance or original lifetime")
		}
		count, err := journalRecordLimit(managedJournalAdmission{Recovery: one.Recovery, Runtime: one.Runtime})
		if err != nil || count != 196 {
			t.Fatalf("cleanup scope and epoch missing from journal bound: %d %v", count, err)
		}
	})
}

func testManagedCleanupRecordBounds(t *testing.T) {
	t.Helper()
	spec := hostRetentionFixture()
	bounds, err := managedRecordLimits(spec.Recovery, spec.Runtime)
	want := managedRecordBounds{Epochs: 4, Invocations: 5, Scopes: 11, Events: 104, CleanupPermits: 1, CleanupPermitBytes: 16384}
	if err != nil || bounds != want {
		t.Fatalf("cleanup record bounds = %+v, %v; want %+v", bounds, err, want)
	}
	if ManagedCleanupPermitBytes != 16384 || ManagedStateStorageBytes != 256<<20 || managedJournalMaxDatabaseBytes != 64<<20 {
		t.Fatal("cleanup changed its permit cap or managed bootstrap storage caps")
	}
	for name, mutate := range map[string]func(*ManagedSpec){
		"zero cleanup":        func(s *ManagedSpec) { s.Recovery.MaxCleanupInvocations = 0 },
		"negative cleanup":    func(s *ManagedSpec) { s.Recovery.MaxCleanupInvocations = -1 },
		"epoch sum":           func(s *ManagedSpec) { s.Recovery.MaxCleanupInvocations = math.MaxInt },
		"work sum":            func(s *ManagedSpec) { s.Runtime.MaxHostInvocations = math.MaxInt },
		"scope product":       func(s *ManagedSpec) { s.Recovery.MaxCleanupInvocations = math.MaxInt / 2 },
		"event product":       func(s *ManagedSpec) { s.Recovery.MaxControllerStarts = math.MaxInt / 16 },
		"permit byte product": func(s *ManagedSpec) { s.Recovery.MaxCleanupInvocations = math.MaxInt/16384 + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := spec
			mutate(&changed)
			if got, err := managedRecordLimits(changed.Recovery, changed.Runtime); err == nil || got != (managedRecordBounds{}) {
				t.Fatalf("invalid record bounds returned usable counts: %+v, %v", got, err)
			}
			if _, err := journalRecordLimit(managedJournalAdmission{Recovery: changed.Recovery, Runtime: changed.Runtime}); err == nil {
				t.Fatal("journal accepted invalid record bounds")
			}
		})
	}
}

func managedCleanupPolicyJSON(t *testing.T, value string) string {
	t.Helper()
	data, err := json.Marshal(managedContractFixture())
	if err != nil {
		t.Fatal(err)
	}
	var spec, recovery map[string]json.RawMessage
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(spec["recovery"], &recovery); err != nil {
		t.Fatal(err)
	}
	delete(recovery, "max_cleanup_invocations")
	if value != "" {
		recovery["max_cleanup_invocations"] = json.RawMessage(value)
	}
	spec["recovery"], err = json.Marshal(recovery)
	if err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func testManagedBoundedJSON(t *testing.T) {
	t.Helper()
	// One MiB covers the complete spec artifact, including all check input/output
	// literals and whitespace; it is independent of admitted source/archive limits.
	const limit = 1 << 20
	data, err := json.Marshal(managedContractFixture())
	if err != nil {
		t.Fatal(err)
	}
	valid := string(data)
	for _, length := range []int{limit - 1, limit} {
		t.Run("within bound "+strconv.Itoa(length), func(t *testing.T) {
			reader := &managedCountingReader{source: strings.NewReader(valid + strings.Repeat(" ", length-len(valid)))}
			if _, err := decodeManagedSpec(reader); err != nil {
				t.Fatalf("valid %d-byte stream rejected: %v", length, err)
			}
			if reader.read != length {
				t.Fatalf("read %d bytes, want complete %d-byte stream", reader.read, length)
			}
		})
	}
	oversize := managedContractFixture()
	oversize.Objective = strings.Repeat("x", limit)
	largeJSON, err := json.Marshal(oversize)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("typed spec artifact bound", func(t *testing.T) {
		if err := validateManagedSpec(oversize); err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("oversized typed artifact must be rejected: %v", err)
		}
	})
	t.Run("typed exact encoded bound", func(t *testing.T) {
		exact := managedContractFixture()
		exact.Objective = strings.Repeat("x", limit-len(valid)+len(exact.Objective))
		if err := validateManagedSpec(exact); err != nil {
			t.Fatalf("exact-limit typed artifact rejected: %v", err)
		}
	})
	t.Run("typed escaped artifact bound", func(t *testing.T) {
		escaped := managedContractFixture()
		escaped.Objective = strings.Repeat("<", limit/2)
		if err := validateManagedSpec(escaped); err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("JSON escape expansion must count toward artifact bound: %v", err)
		}
	})
	for name, input := range map[string]string{
		"JSON object":               string(largeJSON),
		"one extra whitespace byte": valid + strings.Repeat(" ", limit+1-len(valid)),
		"long whitespace tail":      valid + strings.Repeat(" ", 2*limit),
		"long whitespace prefix":    strings.Repeat(" ", 2*limit) + valid,
	} {
		t.Run(name, func(t *testing.T) {
			reader := &managedCountingReader{source: strings.NewReader(input)}
			_, err := decodeManagedSpec(reader)
			if err == nil || !strings.Contains(err.Error(), "exceeds") || errors.Is(err, io.EOF) {
				t.Errorf("oversize stream must report overflow, got %v", err)
			}
			if reader.read != limit+1 {
				t.Errorf("read %d bytes, want exactly cap+1 (%d)", reader.read, limit+1)
			}
		})
	}
	t.Run("empty EOF remains distinct", func(t *testing.T) {
		if _, err := decodeManagedSpec(strings.NewReader("")); !errors.Is(err, io.EOF) {
			t.Fatalf("empty input must retain EOF, got %v", err)
		}
	})
	t.Run("nil reader", func(t *testing.T) {
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Errorf("nil reader panicked: %v", recovered)
			}
		}()
		if _, err := decodeManagedSpec(nil); err == nil {
			t.Fatal("nil reader accepted")
		}
	})
}

type managedCountingReader struct {
	source io.Reader
	read   int
}

func (reader *managedCountingReader) Read(buffer []byte) (int, error) {
	count, err := reader.source.Read(buffer)
	reader.read += count
	return count, err
}

func testManagedValidContract(t *testing.T) {
	t.Helper()
	for _, image := range []string{"sha256:" + strings.Repeat("a", 64), "golang@sha256:" + strings.Repeat("b", 64), "registry.example:5000/team/image@sha256:" + strings.Repeat("c", 64)} {
		spec := managedContractFixture()
		spec.Runtime.ImageDigest = image
		if err := validateManagedSpec(spec); err != nil {
			t.Fatalf("immutable image %q: %v", image, err)
		}
		data, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		got, err := decodeManagedSpec(strings.NewReader(string(data)))
		if err != nil || !reflect.DeepEqual(got, spec) {
			t.Fatalf("managed JSON round trip: %#v, %v", got, err)
		}
	}
}

func testManagedStrictJSON(t *testing.T) {
	t.Helper()
	data, err := json.Marshal(managedContractFixture())
	if err != nil {
		t.Fatal(err)
	}
	valid := string(data)
	cases := map[string]string{
		"unknown root":      strings.Replace(valid, `"id":`, `"unknown":true,"id":`, 1),
		"legacy executable": strings.Replace(valid, `"id":`, `"worker":{"path":"/bin/sh"},"id":`, 1),
		"unknown nested":    strings.Replace(valid, `"runtime":{`, `"runtime":{"unknown":true,`, 1),
		"duplicate field":   strings.Replace(valid, `"max_attempts":3`, `"max_attempts":0,"max_attempts":3`, 1),
		"duplicate nested":  strings.Replace(valid, `"pids":64`, `"pids":0,"pids":64`, 1),
		"case alias":        strings.Replace(valid, `"id":`, `"ID":`, 1),
		"trailing JSON":     valid + `{}`, "truncated": valid[:len(valid)-1], "null": "null",
		"numeric cost":     strings.Replace(valid, `"max_model_invocation_usd":"1.25"`, `"max_model_invocation_usd":1.25`, 1),
		"integer overflow": strings.Replace(valid, `"max_attempts":3`, `"max_attempts":9223372036854775808`, 1),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if input == valid {
				t.Fatal("fixture mutation did not apply")
			}
			if _, err := decodeManagedSpec(strings.NewReader(input)); err == nil {
				t.Fatal("invalid managed JSON accepted")
			}
		})
	}
}

func testManagedInvalidIdentity(t *testing.T) {
	t.Helper()
	cases := map[string]func(*ManagedSpec){
		"objective ID":                func(s *ManagedSpec) { s.ID = "../other" },
		"empty objective":             func(s *ManagedSpec) { s.Objective = " " },
		"relative repository":         func(s *ManagedSpec) { s.Repository = "repository" },
		"unclean repository":          func(s *ManagedSpec) { s.Repository += "/../other" },
		"root repository":             func(s *ManagedSpec) { s.Repository = "/" },
		"nul path":                    func(s *ManagedSpec) { s.Repository += "\x00" },
		"branch traversal":            func(s *ManagedSpec) { s.TargetBranch = "foo/../bar" },
		"branch option":               func(s *ManagedSpec) { s.TargetBranch = "--help" },
		"branch lock":                 func(s *ManagedSpec) { s.TargetBranch = "foo.lock/bar" },
		"branch reflog":               func(s *ManagedSpec) { s.TargetBranch = "foo@{1}" },
		"branch space":                func(s *ManagedSpec) { s.TargetBranch = "foo bar" },
		"empty docker context":        func(s *ManagedSpec) { s.Runtime.DockerContext = "" },
		"context option":              func(s *ManagedSpec) { s.Runtime.DockerContext = "--host" },
		"mutable image":               func(s *ManagedSpec) { s.Runtime.ImageDigest = "golang:latest" },
		"short image hash":            func(s *ManagedSpec) { s.Runtime.ImageDigest = "sha256:abcd" },
		"invalid image repository":    func(s *ManagedSpec) { s.Runtime.ImageDigest = "--bad@sha256:" + strings.Repeat("a", 64) },
		"image with mutable tag":      func(s *ManagedSpec) { s.Runtime.ImageDigest = "golang:latest@sha256:" + strings.Repeat("a", 64) },
		"empty model":                 func(s *ManagedSpec) { s.Runtime.Model = "" },
		"model option":                func(s *ManagedSpec) { s.Runtime.Model = "--dangerous" },
		"relative model executable":   func(s *ManagedSpec) { s.Runtime.ModelExecutable = "claude" },
		"model executable in source":  func(s *ManagedSpec) { s.Runtime.ModelExecutable = s.Repository + "/claude" },
		"git executable in source":    func(s *ManagedSpec) { s.Runtime.GitExecutable = s.Repository + "/git" },
		"no checks":                   func(s *ManagedSpec) { s.RequiredChecks = nil; s.Acceptance = nil },
		"duplicate required":          func(s *ManagedSpec) { s.RequiredChecks = append(s.RequiredChecks, "addition") },
		"missing check":               func(s *ManagedSpec) { s.RequiredChecks = append(s.RequiredChecks, "missing") },
		"extra check":                 func(s *ManagedSpec) { s.Acceptance = append(s.Acceptance, Check{ID: "extra", Args: []string{"true"}}) },
		"duplicate check":             func(s *ManagedSpec) { s.Acceptance = append(s.Acceptance, s.Acceptance[0]) },
		"malformed check ID":          func(s *ManagedSpec) { s.RequiredChecks[0] = "a/b"; s.Acceptance[0].ID = "a/b" },
		"missing argv":                func(s *ManagedSpec) { s.Acceptance[0].Args = nil },
		"empty program":               func(s *ManagedSpec) { s.Acceptance[0].Args[0] = " " },
		"nul argv":                    func(s *ManagedSpec) { s.Acceptance[0].Args = append(s.Acceptance[0].Args, "a\x00b") },
		"negative exit":               func(s *ManagedSpec) { s.Acceptance[0].ExpectedExit = -1 },
		"unrepresentable exit":        func(s *ManagedSpec) { s.Acceptance[0].ExpectedExit = 256 },
		"empty reviewer instructions": func(s *ManagedSpec) { s.ReviewInstructions = " " },
	}
	assertManagedRejected(t, cases)
}

func testManagedInvalidBudgets(t *testing.T) {
	t.Helper()
	cases := map[string]func(*ManagedSpec){
		"zero attempts":          func(s *ManagedSpec) { s.MaxAttempts = 0 },
		"negative timeout":       func(s *ManagedSpec) { s.TimeoutSeconds = -1 },
		"zero step":              func(s *ManagedSpec) { s.StepTimeoutSeconds = 0 },
		"step exceeds objective": func(s *ManagedSpec) { s.StepTimeoutSeconds = 301 },
		"duration overflow":      func(s *ManagedSpec) { s.TimeoutSeconds = math.MaxInt },
		"hard stop overflow": func(s *ManagedSpec) {
			s.TimeoutSeconds = int(math.MaxInt64 / int64(1000000000))
			s.Recovery.CleanupTimeoutSeconds = 100
		},
		"NaN CPUs":                 func(s *ManagedSpec) { s.Runtime.CPUs = math.NaN() },
		"infinite CPUs":            func(s *ManagedSpec) { s.Runtime.CPUs = math.Inf(1) },
		"nano CPU overflow":        func(s *ManagedSpec) { s.Runtime.CPUs = math.MaxFloat64 },
		"VM byte overflow":         func(s *ManagedSpec) { s.Runtime.MemoryBytes = math.MaxInt64 },
		"host reserve overflow":    func(s *ManagedSpec) { s.Runtime.HostReserveBytes = math.MaxInt64 },
		"retained source overflow": func(s *ManagedSpec) { s.Runtime.SourceBytes = math.MaxInt64 / 4; s.MaxAttempts = 5 },
		"too few source inodes":    func(s *ManagedSpec) { s.Runtime.SourceInodes = 1 },
		"cost total overflow": func(s *ManagedSpec) {
			s.Runtime.MaxModelInvocationUSD = "92233720368547758.07"
			s.Runtime.MaxModelCalls = 2
		},
	}
	for _, field := range []string{"MaxControllerStarts", "MaxSupervisorStarts", "RestartDelaySeconds", "CleanupTimeoutSeconds", "ReconciliationSeconds", "MaxReconciliationInvocations", "MaxCleanupInvocations"} {
		cases["zero recovery "+field] = func(s *ManagedSpec) { reflect.ValueOf(&s.Recovery).Elem().FieldByName(field).SetInt(0) }
	}
	for _, field := range []string{"MemoryBytes", "ScratchBytes", "SourceBytes", "HostRequiredBytes", "HostReserveBytes", "PIDs", "MaxFiles", "SourceInodes", "ScratchInodes", "MaxModelCalls", "MaxModelTurns", "MaxToolCalls", "MaxHostInvocations"} {
		cases["zero runtime "+field] = func(s *ManagedSpec) { reflect.ValueOf(&s.Runtime).Elem().FieldByName(field).SetInt(0) }
	}
	cases["zero CPUs"] = func(s *ManagedSpec) { s.Runtime.CPUs = 0 }
	assertManagedRejected(t, cases)
}

func assertManagedRejected(t *testing.T, cases map[string]func(*ManagedSpec)) {
	t.Helper()
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			spec := managedContractFixture()
			change(&spec)
			if err := validateManagedSpec(spec); err == nil {
				t.Fatal("invalid managed contract accepted")
			}
		})
	}
}

func testManagedDecimalCents(t *testing.T) {
	t.Helper()
	for input, want := range map[string]int64{"0.01": 1, "1": 100, "1.2": 120, "1.25": 125, "92233720368547758.07": math.MaxInt64} {
		got, err := managedInvocationCents(input)
		if err != nil || got != want {
			t.Errorf("cents(%q) = %d, %v; want %d", input, got, err, want)
		}
	}
	for _, input := range []string{"", "0", "0.00", "-1", "+1", "1e2", "NaN", "Inf", " 1", "01", ".1", "1.", "1.001", "92233720368547758.08"} {
		if _, err := managedInvocationCents(input); err == nil {
			t.Errorf("invalid exact cost %q accepted", input)
		}
	}
}

func testManagedPolicyIdentity(t *testing.T) {
	t.Helper()
	spec := managedContractFixture()
	digests := map[string]string{"model": strings.Repeat("a", 64), "git": strings.Repeat("b", 64)}
	want, err := managedPolicyHash(spec, digests)
	if err != nil || len(want) != 64 {
		t.Fatalf("policy hash = %q, %v", want, err)
	}
	again, err := managedPolicyHash(spec, map[string]string{"git": digests["git"], "model": digests["model"]})
	if err != nil || again != want {
		t.Fatalf("policy identity unstable: %s, %v", again, err)
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	for path := range managedLeafPaths(body, "") {
		t.Run(path, func(t *testing.T) {
			var changed ManagedSpec
			if err := json.Unmarshal(data, &changed); err != nil {
				t.Fatal(err)
			}
			mutateManagedLeaf(reflect.ValueOf(&changed).Elem(), strings.Split(path, "."))
			got, err := managedPolicyHash(changed, digests)
			if err != nil || got == want {
				t.Fatalf("policy identity omitted %s: %s, %v", path, got, err)
			}
		})
	}
	digests["model"] = strings.Repeat("c", 64)
	if got, err := managedPolicyHash(spec, digests); err != nil || got == want {
		t.Fatalf("policy identity omitted executable digest: %s, %v", got, err)
	}
	digests["model"] = strings.Repeat("a", 64)
	digests["git"] = strings.Repeat("c", 64)
	if got, err := managedPolicyHash(spec, digests); err != nil || got == want {
		t.Fatalf("policy identity omitted git digest: %s, %v", got, err)
	}
	for _, field := range []string{"ID", "Args", "Stdin", "ExpectedStdout", "ExpectedExit"} {
		changed := managedContractFixture()
		check := reflect.ValueOf(&changed.Acceptance[0]).Elem().FieldByName(field)
		switch check.Kind() {
		case reflect.String:
			check.SetString(check.String() + "changed")
		case reflect.Slice:
			check.Index(0).SetString("different-command")
		case reflect.Int:
			check.SetInt(check.Int() + 1)
		}
		got, err := managedPolicyHash(changed, map[string]string{"model": strings.Repeat("a", 64), "git": strings.Repeat("b", 64)})
		if err != nil || got == want {
			t.Errorf("policy identity omitted acceptance %s: %s, %v", field, got, err)
		}
	}
	for _, invalid := range []map[string]string{nil, {"model": "bad", "git": digests["git"]}, {"model": digests["model"], "other": digests["git"]}} {
		if _, err := managedPolicyHash(spec, invalid); err == nil {
			t.Errorf("invalid executable digest set accepted: %v", invalid)
		}
	}
}

func managedLeafPaths(value map[string]any, prefix string) map[string]bool {
	paths := make(map[string]bool)
	for key, child := range value {
		path := prefix + key
		if nested, ok := child.(map[string]any); ok {
			for leaf := range managedLeafPaths(nested, path+".") {
				paths[leaf] = true
			}
		} else {
			paths[path] = true
		}
	}
	return paths
}

func mutateManagedLeaf(value reflect.Value, path []string) {
	for i := range value.NumField() {
		if value.Type().Field(i).Tag.Get("json") != path[0] {
			continue
		}
		field := value.Field(i)
		if len(path) > 1 {
			mutateManagedLeaf(field, path[1:])
			return
		}
		switch field.Kind() {
		case reflect.String:
			field.SetString(field.String() + "changed")
		case reflect.Int, reflect.Int64:
			field.SetInt(field.Int() + 1)
		case reflect.Float64:
			field.SetFloat(field.Float() + 0.25)
		case reflect.Slice:
			field.Set(reflect.Append(field, field.Index(0)))
		}
		return
	}
	panic("missing managed field " + path[0])
}

func testManagedReservationIdentity(t *testing.T) {
	t.Helper()
	spec := managedContractFixture()
	reservation := HostReservation{ID: "reservation-123", StateRoot: "/private/tmp/state", ObjectiveID: spec.ID, Bytes: spec.Runtime.HostRequiredBytes}
	if err := validateHostReservation(reservation, spec, reservation.StateRoot); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*HostReservation){
		func(r *HostReservation) { r.ID = "" }, func(r *HostReservation) { r.ID = "../other" },
		func(r *HostReservation) { r.ObjectiveID = "other" }, func(r *HostReservation) { r.StateRoot += "/other" },
		func(r *HostReservation) { r.Bytes-- }, func(r *HostReservation) { r.Bytes = -1 },
	} {
		changed := reservation
		mutate(&changed)
		if err := validateHostReservation(changed, spec, reservation.StateRoot); err == nil {
			t.Fatalf("mismatched persisted reservation accepted: %#v", changed)
		}
	}
}
