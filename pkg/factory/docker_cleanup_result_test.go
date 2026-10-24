package factory //nolint:testpackage // Test the pure codec against the private canonical permit contract.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDockerCleanupResult(t *testing.T) {
	t.Run("canonical exact result round trip", testCleanupResultRoundTrip)
	t.Run("complete operation observation acknowledgment table", testCleanupResultTable)
	t.Run("unknown lifetime inspection fills only observed identity", testCleanupResultUnknown)
	t.Run("exact immutable permit and binding", testCleanupResultIdentity)
	t.Run("strict canonical JSON", testCleanupResultJSON)
	t.Run("exact encoded size boundary", testCleanupResultSize)
	t.Run("malformed observed lifetime", testCleanupResultMalformedLifetime)
}

func testCleanupResultTable(t *testing.T) {
	t.Helper()
	for _, kind := range []string{"container", "volume"} {
		for _, operation := range []DockerCleanupOp{DockerCleanupInspect, DockerCleanupStop, DockerCleanupRemove} {
			for _, observation := range []string{"present", "stopped", "observed_absent"} {
				for _, ack := range []bool{false, true} {
					t.Run(kind+"/"+string(operation)+"/"+observation+"/"+strconv.FormatBool(ack), func(t *testing.T) {
						permit, _ := cleanupResultFixture(t)
						if kind == "volume" {
							permit = cleanupResultVolume(permit)
						}
						permit.Operation = operation
						result := cleanupResultForPermit(t, permit)
						result.Observation, result.MutationAcknowledged = observation, ack
						valid := false
						switch operation {
						case DockerCleanupInspect:
							valid = !ack && (kind != "volume" || observation != "stopped")
						case DockerCleanupStop:
							valid = kind == "container" && (observation == "observed_absent" || (observation == "stopped" && ack))
						case DockerCleanupRemove:
							valid = observation == "observed_absent"
						}
						encoded, err := EncodeDockerCleanupResult(result, permit)
						if (err == nil) != valid {
							t.Fatalf("valid=%v encoded=%s err=%v", valid, encoded, err)
						}
						body, err := json.Marshal(result)
						if err != nil {
							t.Fatal(err)
						}
						decoded, err := DecodeDockerCleanupResult(body, permit)
						if (err == nil) != valid {
							t.Fatalf("valid=%v decoded=%+v err=%v", valid, decoded, err)
						}
					})
				}
			}
		}
	}
}

func cleanupResultVolume(p DockerCleanupPermit) DockerCleanupPermit {
	p.Target.Kind = "volume"
	p.Target.Name = strings.Replace(p.Target.Name, "-container-", "-volume-", 1)
	p.Binding.Kind, p.Binding.Name, p.Binding.ContainerID, p.Binding.ImageID = "volume", p.Target.Name, "", ""
	p.Binding.VolumeDriver = "local"
	p.Binding.VolumeOptions = map[string]string{"device": "tmpfs", "type": "tmpfs", "o": "size=1024,nr_inodes=16"}
	p.Binding.Labels = maps.Clone(p.Binding.Labels)
	p.Binding.Labels["dev.getoro.factory.kind"] = "volume"
	return p
}

func testCleanupResultUnknown(t *testing.T) {
	t.Helper()
	for _, kind := range []string{"container", "volume"} {
		permit, _ := cleanupResultFixture(t)
		if kind == "volume" {
			permit = cleanupResultVolume(permit)
		}
		permit.Binding.ContainerID, permit.Binding.CreatedAt = "", ""
		result := cleanupResultForPermit(t, permit)
		if _, err := EncodeDockerCleanupResult(result, permit); err == nil {
			t.Fatal("present object lacked a known lifetime")
		}
		result.Observation = "observed_absent"
		if _, err := EncodeDockerCleanupResult(result, permit); err != nil {
			t.Fatal(err)
		}
		result.Observation = "present"
		result.Binding.CreatedAt = "2026-10-24T00:00:00.123Z"
		if kind == "container" {
			if _, err := EncodeDockerCleanupResult(result, permit); err == nil {
				t.Fatal("partial container lifetime accepted")
			}
			result.Binding.ContainerID = strings.Repeat("1", 64)
		}
		body, err := EncodeDockerCleanupResult(result, permit)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeDockerCleanupResult(body, permit); err != nil {
			t.Fatal(err)
		}
		result.Observation = "observed_absent"
		if _, err := EncodeDockerCleanupResult(result, permit); err == nil {
			t.Fatal("absence invented an unobserved lifetime")
		}
		if permit.Binding.CreatedAt != "" || permit.Binding.ContainerID != "" {
			t.Fatal("codec mutated trusted permit")
		}
	}
}

func testCleanupResultIdentity(t *testing.T) {
	t.Helper()
	permit, result := cleanupResultFixture(t)
	for name, mutate := range map[string]func(*DockerCleanupResult){
		"version":        func(r *DockerCleanupResult) { r.Version = 2 },
		"digest":         func(r *DockerCleanupResult) { r.PermitSHA256 = strings.Repeat("0", 64) },
		"scope":          func(r *DockerCleanupResult) { r.Scope.Nonce = strings.Repeat("0", 32) },
		"target":         func(r *DockerCleanupResult) { r.Target.Generation++ },
		"operation":      func(r *DockerCleanupResult) { r.Operation = DockerCleanupRemove },
		"observation":    func(r *DockerCleanupResult) { r.Observation = "quiescent" },
		"resource kind":  func(r *DockerCleanupResult) { r.Binding.Kind = "unknown" },
		"context":        func(r *DockerCleanupResult) { r.Binding.Context = "other" },
		"name":           func(r *DockerCleanupResult) { r.Binding.Name = "other" },
		"image":          func(r *DockerCleanupResult) { r.Binding.ImageID = "sha256:" + strings.Repeat("0", 64) },
		"label":          func(r *DockerCleanupResult) { r.Binding.Labels["dev.getoro.factory.nonce"] = "other" },
		"driver":         func(r *DockerCleanupResult) { r.Binding.VolumeDriver = "other" },
		"options":        func(r *DockerCleanupResult) { r.Binding.VolumeOptions = map[string]string{"type": "tmpfs"} },
		"known id":       func(r *DockerCleanupResult) { r.Binding.ContainerID = strings.Repeat("0", 64) },
		"missing id":     func(r *DockerCleanupResult) { r.Binding.ContainerID = "" },
		"known creation": func(r *DockerCleanupResult) { r.Binding.CreatedAt = "2026-10-24T00:00:01Z" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := result
			changed.Binding.Labels = maps.Clone(result.Binding.Labels)
			mutate(&changed)
			if body, err := EncodeDockerCleanupResult(changed, permit); err == nil || body != nil {
				t.Fatalf("invalid result encoded: %s %v", body, err)
			}
			body, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := DecodeDockerCleanupResult(body, permit); err == nil || !reflect.DeepEqual(got, DockerCleanupResult{}) {
				t.Fatalf("invalid result decoded: %+v %v", got, err)
			}
		})
	}
	changed := permit
	changed.Deadline = permit.Deadline.Add(time.Second)
	if _, err := EncodeDockerCleanupResult(result, changed); err == nil {
		t.Fatal("permit deadline omitted from digest")
	}
	changed = permit
	changed.Deadline = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := EncodeDockerCleanupResult(result, changed); err == nil {
		t.Fatal("unencodable permit accepted")
	}
	volume := cleanupResultVolume(permit)
	volumeResult := cleanupResultForPermit(t, volume)
	volumeResult.Binding.VolumeOptions = maps.Clone(volume.Binding.VolumeOptions)
	volumeResult.Binding.VolumeOptions["o"] = "size=2048,nr_inodes=16"
	if _, err := EncodeDockerCleanupResult(volumeResult, volume); err == nil {
		t.Fatal("observed volume configuration changed")
	}
}

func testCleanupResultMalformedLifetime(t *testing.T) {
	t.Helper()
	for name, mutate := range map[string]func(*DockerCleanupBinding){
		"invalid creation":      func(b *DockerCleanupBinding) { b.CreatedAt = "invalid" },
		"noncanonical creation": func(b *DockerCleanupBinding) { b.CreatedAt = "2026-10-24T00:00:00+00:00" },
		"oversize creation":     func(b *DockerCleanupBinding) { b.CreatedAt = strings.Repeat("x", 31) },
		"short ID":              func(b *DockerCleanupBinding) { b.ContainerID = "abc" },
		"uppercase ID":          func(b *DockerCleanupBinding) { b.ContainerID = strings.Repeat("A", 64) },
		"nonhex ID":             func(b *DockerCleanupBinding) { b.ContainerID = strings.Repeat("g", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			permit, result := cleanupResultFixture(t)
			permit.Binding.ContainerID, permit.Binding.CreatedAt = "", ""
			result.PermitSHA256 = cleanupResultForPermit(t, permit).PermitSHA256
			mutate(&result.Binding)
			if _, err := EncodeDockerCleanupResult(result, permit); err == nil {
				t.Fatal("malformed discovered lifetime accepted")
			}
		})
	}
	permit, _ := cleanupResultFixture(t)
	permit = cleanupResultVolume(permit)
	permit.Binding.CreatedAt = ""
	result := cleanupResultForPermit(t, permit)
	result.Binding.CreatedAt = "2026-10-24T00:00:00Z"
	result.Binding.ContainerID = strings.Repeat("a", 64)
	if _, err := EncodeDockerCleanupResult(result, permit); err == nil {
		t.Fatal("volume discovery gained a container identity")
	}
}

func testCleanupResultJSON(t *testing.T) {
	t.Helper()
	permit, result := cleanupResultFixture(t)
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"malformed": []byte("{"), "null": []byte("null"), "wrong type": []byte("[]"),
		"trailing whitespace":    append(append([]byte(nil), data...), '\n'),
		"second object":          append(append([]byte(nil), data...), data...),
		"unknown field":          []byte(strings.Replace(string(data), "{", `{"quiescent":true,`, 1)),
		"duplicate":              []byte(strings.Replace(string(data), "{", `{"version":1,`, 1)),
		"missing required false": []byte(strings.Replace(string(data), `,"mutation_acknowledged":false`, "", 1)),
		"oversize":               []byte(strings.Repeat(" ", 16385)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeDockerCleanupResult(body, permit); err == nil {
				t.Fatal("noncanonical or invalid result accepted")
			}
		})
	}
}

func testCleanupResultSize(t *testing.T) {
	t.Helper()
	permit, _ := cleanupResultFixture(t)
	for i := range 10 {
		permit.Binding.Labels["padding"+strconv.Itoa(i)] = ""
	}
	result := cleanupResultForPermit(t, permit)
	baseline, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	remaining := 16384 - len(baseline)
	last := ""
	for i := range 10 {
		key := "padding" + strconv.Itoa(i)
		n := min(remaining/2, 1024)
		permit.Binding.Labels[key] = strings.Repeat("\n", n)
		remaining -= 2 * n
		if n < 1024 {
			last = key
		}
	}
	if remaining == 1 {
		permit.Binding.Labels[last] += "x"
		remaining--
	}
	if remaining != 0 || !boundedCleanupMap(permit.Binding.Labels) {
		t.Fatalf("invalid boundary fixture: remaining=%d", remaining)
	}
	result = cleanupResultForPermit(t, permit)
	data, err := EncodeDockerCleanupResult(result, permit)
	if err != nil || len(data) != 16384 {
		t.Fatalf("exact result limit=%d %v", len(data), err)
	}
	if _, err := DecodeDockerCleanupResult(data, permit); err != nil {
		t.Fatal(err)
	}
	permit.Binding.Labels[last] += "x"
	result = cleanupResultForPermit(t, permit)
	if data, err := EncodeDockerCleanupResult(result, permit); err == nil || data != nil {
		t.Fatal("oversize result encoded")
	}
}

func cleanupResultFixture(t *testing.T) (DockerCleanupPermit, DockerCleanupResult) {
	t.Helper()
	a := managedJournalAdmission{ObjectiveID: "result", ManifestHash: strings.Repeat("a", 64), Runtime: RuntimePolicy{DockerContext: "factory"}, Reservation: HostReservation{StateRoot: "/private/tmp/cleanup-result"}, HardStop: time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC)}
	hash := cleanupObjectiveHash(a)
	scope := ScopeHandle{ID: strings.Repeat("b", 32), Nonce: strings.Repeat("c", 32), Kind: "cleanup", Backend: "launchd", Generation: 2}
	scope.Name = "dev.getoro.factory." + hash + ".cleanup." + scope.ID + "." + scope.Nonce
	target := ScopeHandle{ID: strings.Repeat("d", 32), Nonce: strings.Repeat("e", 32), Kind: "container", Backend: "docker", Generation: 1}
	target.Name = "oro-factory-" + hash + "-container-" + target.ID + "-" + target.Nonce
	binding := DockerCleanupBinding{Kind: "container", Context: a.Runtime.DockerContext, Name: target.Name, ContainerID: strings.Repeat("f", 64), CreatedAt: "2026-10-24T00:00:00Z", ImageID: "sha256:" + strings.Repeat("a", 64), Labels: map[string]string{
		"dev.getoro.factory.objective": hash, "dev.getoro.factory.scope": target.ID, "dev.getoro.factory.nonce": target.Nonce, "dev.getoro.factory.kind": target.Kind, "dev.getoro.factory.generation": "1", "dev.getoro.factory.policy": a.ManifestHash,
	}}
	permit := DockerCleanupPermit{Scope: scope, Target: target, Operation: DockerCleanupInspect, Binding: binding, Deadline: a.HardStop}
	if _, err := encodeCleanupPermit(a, permit); err != nil {
		t.Fatal(err)
	}
	return permit, cleanupResultForPermit(t, permit)
}

func cleanupResultForPermit(t *testing.T, p DockerCleanupPermit) DockerCleanupResult {
	t.Helper()
	encoded, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	return DockerCleanupResult{Version: 1, PermitSHA256: hex.EncodeToString(digest[:]), Scope: p.Scope, Target: p.Target, Operation: p.Operation, Binding: p.Binding, Observation: "present"}
}

func testCleanupResultRoundTrip(t *testing.T) {
	t.Helper()
	permit, result := cleanupResultFixture(t)
	before, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeDockerCleanupResult(result, permit)
	if err != nil || string(encoded) != string(before) {
		t.Fatalf("canonical encode=%s %v", encoded, err)
	}
	decoded, err := DecodeDockerCleanupResult(encoded, permit)
	if err != nil || !reflect.DeepEqual(decoded, result) {
		t.Fatalf("decoded result=%+v %v", decoded, err)
	}
	if strings.Contains(string(encoded), "quiescent") {
		t.Fatal("observation invented quiescence")
	}
}
