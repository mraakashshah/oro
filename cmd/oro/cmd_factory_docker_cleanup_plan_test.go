package main

import (
	"bytes"
	"encoding/json"
	"maps"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"oro/pkg/factory"
)

func TestFactoryDockerCleanupPlan(t *testing.T) {
	t.Run("fixed commands for exact lifetime", testFactoryDockerCleanupCommands)
	t.Run("observed immutable ownership", testFactoryDockerCleanupOwnership)
	t.Run("native exact absence is observation only", testFactoryDockerCleanupMissing)
	t.Run("bounded unambiguous inspection", testFactoryDockerCleanupMalformed)
	t.Run("invalid permits cannot select commands", testFactoryDockerCleanupPermitShape)
	t.Run("consistent lifecycle states", testFactoryDockerCleanupStates)
	t.Run("descriptor and output boundaries", testFactoryDockerCleanupBounds)
}

func factoryDockerCleanupPlanFixture(kind string) factory.DockerCleanupPermit {
	objective := strings.Repeat("a", 16)
	target := factory.ScopeHandle{ID: strings.Repeat("b", 32), Nonce: strings.Repeat("c", 32), Kind: kind, Backend: "docker", Generation: 1}
	target.Name = "oro-factory-" + objective + "-" + kind + "-" + target.ID + "-" + target.Nonce
	scope := factory.ScopeHandle{ID: strings.Repeat("d", 32), Nonce: strings.Repeat("e", 32), Kind: "cleanup", Backend: "launchd", Generation: 2}
	scope.Name = "dev.getoro.factory." + objective + ".cleanup." + scope.ID + "." + scope.Nonce
	binding := factory.DockerCleanupBinding{Kind: "container", Context: "colima-oro-factory", Name: target.Name, ContainerID: strings.Repeat("f", 64), CreatedAt: "2026-10-24T01:02:03.456Z", ImageID: "sha256:" + strings.Repeat("1", 64), Labels: map[string]string{}}
	for key, value := range map[string]string{"objective": objective, "scope": target.ID, "nonce": target.Nonce, "generation": strconv.FormatUint(target.Generation, 10), "kind": kind, "policy": strings.Repeat("2", 64), "extra": "admitted image label"} {
		binding.Labels["dev.getoro.factory."+key] = value
	}
	if kind == "volume" {
		binding.Kind, binding.ContainerID, binding.ImageID, binding.VolumeDriver = "volume", "", "", "local"
		binding.VolumeOptions = map[string]string{"type": "tmpfs", "device": "tmpfs", "o": "size=1024,nr_inodes=200,uid=1000,gid=1000"}
	}
	return factory.DockerCleanupPermit{Scope: scope, Target: target, Operation: factory.DockerCleanupInspect, Binding: binding, Deadline: time.Date(2026, 10, 24, 2, 0, 0, 0, time.UTC)}
}

func factoryDockerCleanupObject(p factory.DockerCleanupPermit) map[string]any {
	b := p.Binding
	if b.Kind == "volume" {
		return map[string]any{"Name": b.Name, "CreatedAt": b.CreatedAt, "Driver": b.VolumeDriver, "Options": maps.Clone(b.VolumeOptions), "Labels": maps.Clone(b.Labels), "Scope": "local"}
	}
	return map[string]any{
		"Id": b.ContainerID, "Name": "/" + b.Name, "Created": b.CreatedAt, "Image": b.ImageID,
		"Config": map[string]any{"Labels": maps.Clone(b.Labels), "User": "65534:65534", "Entrypoint": []string{"/usr/local/bin/oro-factory-guard"}},
		"State":  map[string]any{"Status": "running", "Running": true, "Pid": 321, "Paused": false, "Restarting": false, "Dead": false, "StartedAt": "2026-10-24T01:02:04Z"},
	}
}

func factoryDockerCleanupInspectionResult(t *testing.T, value any) factory.HostResult {
	t.Helper()
	data, err := json.Marshal([]any{value})
	if err != nil {
		t.Fatal(err)
	}
	return factory.HostResult{Stdout: data}
}

func testFactoryDockerCleanupCommands(t *testing.T) {
	for _, kind := range []string{"keeper", "container", "volume"} {
		t.Run(kind, func(t *testing.T) {
			p := factoryDockerCleanupPlanFixture(kind)
			want := []string{"inspect", "--type", "container", p.Binding.ContainerID}
			if kind == "volume" {
				want = []string{"volume", "inspect", p.Binding.Name}
			}
			args, err := factoryDockerCleanupInspectArgs(p)
			if err != nil || !reflect.DeepEqual(args, want) {
				t.Fatalf("inspect args=%v err=%v", args, err)
			}
			if _, err := factoryDockerCleanupMutationArgs(p, p.Binding); err == nil {
				t.Fatal("inspect-only permit gained mutation")
			}
			p.Operation = factory.DockerCleanupRemove
			want = []string{"rm", "--force", p.Binding.ContainerID}
			if kind == "volume" {
				want = []string{"volume", "rm", p.Binding.Name}
			}
			args, err = factoryDockerCleanupMutationArgs(p, p.Binding)
			if err != nil || !reflect.DeepEqual(args, want) {
				t.Fatalf("remove args=%v err=%v", args, err)
			}
			p.Operation = factory.DockerCleanupStop
			args, err = factoryDockerCleanupMutationArgs(p, p.Binding)
			if kind == "volume" {
				if err == nil {
					t.Fatal("volume stop accepted")
				}
				return
			}
			want = []string{"stop", "--signal", "SIGKILL", "--timeout", "0", p.Binding.ContainerID}
			if err != nil || !reflect.DeepEqual(args, want) {
				t.Fatalf("stop args=%v err=%v", args, err)
			}
			observed := p.Binding
			observed.CreatedAt = "2026-10-24T01:02:04Z"
			if _, err := factoryDockerCleanupMutationArgs(p, observed); err == nil {
				t.Fatal("changed lifetime gained mutation")
			}
		})
	}
	p := factoryDockerCleanupPlanFixture("container")
	p.Binding.ContainerID, p.Binding.CreatedAt = "", ""
	args, err := factoryDockerCleanupInspectArgs(p)
	if err != nil || !reflect.DeepEqual(args, []string{"inspect", "--type", "container", p.Binding.Name}) {
		t.Fatalf("unknown inspection=%v %v", args, err)
	}
	p.Operation = factory.DockerCleanupRemove
	if _, err := factoryDockerCleanupInspectArgs(p); err == nil {
		t.Fatal("unknown mutation permit accepted")
	}
}

func testFactoryDockerCleanupOwnership(t *testing.T) {
	for _, kind := range []string{"keeper", "container", "volume"} {
		t.Run(kind, func(t *testing.T) {
			p := factoryDockerCleanupPlanFixture(kind)
			object := factoryDockerCleanupObject(p)
			binding, observation, err := parseFactoryDockerCleanupInspection(p, factoryDockerCleanupInspectionResult(t, object))
			if err != nil || observation != "present" || !reflect.DeepEqual(binding, p.Binding) {
				t.Fatalf("owned object=%+v %q %v", binding, observation, err)
			}
			// Discover only the creation identity(s); all remaining fields stay bound.
			p.Binding.CreatedAt = ""
			if kind != "volume" {
				p.Binding.ContainerID = ""
			}
			binding, observation, err = parseFactoryDockerCleanupInspection(p, factoryDockerCleanupInspectionResult(t, object))
			if err != nil || observation != "present" || binding.CreatedAt == "" || (kind != "volume" && binding.ContainerID == "") {
				t.Fatalf("unknown discovery=%+v %q %v", binding, observation, err)
			}
			if _, err := factoryDockerCleanupMutationArgs(p, binding); err == nil {
				t.Fatal("discovery silently promoted an inspect permit")
			}
		})
	}
	p := factoryDockerCleanupPlanFixture("container")
	object := factoryDockerCleanupObject(p)
	state := object["State"].(map[string]any)
	state["StartedAt"] = "2026-10-24T01:59:59Z"
	config := object["Config"].(map[string]any)
	config["User"], config["Entrypoint"], config["Cmd"] = "changed", []string{"changed"}, []string{"changed"}
	if _, got, err := parseFactoryDockerCleanupInspection(p, factoryDockerCleanupInspectionResult(t, object)); err != nil || got != "present" {
		t.Fatalf("configuration drift disowned exact lifetime: %q %v", got, err)
	}
	state["Status"], state["Running"], state["Pid"] = "exited", false, 0
	if _, got, err := parseFactoryDockerCleanupInspection(p, factoryDockerCleanupInspectionResult(t, object)); err != nil || got != "stopped" {
		t.Fatalf("stopped ownership=%q %v", got, err)
	}
	for _, field := range []string{"Id", "Name", "Created", "Image"} {
		t.Run("changed "+field, func(t *testing.T) {
			changed := factoryDockerCleanupObject(p)
			changed[field] = "foreign"
			if _, _, err := parseFactoryDockerCleanupInspection(p, factoryDockerCleanupInspectionResult(t, changed)); err == nil {
				t.Fatal("foreign container accepted")
			}
		})
	}
	for _, field := range []string{"Name", "CreatedAt", "Driver", "Options", "Labels"} {
		t.Run("volume "+field, func(t *testing.T) {
			v := factoryDockerCleanupPlanFixture("volume")
			changed := factoryDockerCleanupObject(v)
			changed[field] = "foreign"
			if field == "Options" || field == "Labels" {
				changed[field] = map[string]string{"foreign": "x"}
			}
			if _, _, err := parseFactoryDockerCleanupInspection(v, factoryDockerCleanupInspectionResult(t, changed)); err == nil {
				t.Fatal("foreign volume accepted")
			}
		})
	}
	for _, mode := range []string{"changed", "missing", "extra"} {
		t.Run("complete labels "+mode, func(t *testing.T) {
			changed := factoryDockerCleanupObject(p)
			labels := changed["Config"].(map[string]any)["Labels"].(map[string]string)
			switch mode {
			case "changed":
				labels["dev.getoro.factory.extra"] = "different"
			case "missing":
				delete(labels, "dev.getoro.factory.policy")
			case "extra":
				labels["unbound"] = "extra"
			}
			if _, _, err := parseFactoryDockerCleanupInspection(p, factoryDockerCleanupInspectionResult(t, changed)); err == nil {
				t.Fatal("unbound labels accepted")
			}
		})
	}
}

func testFactoryDockerCleanupMissing(t *testing.T) {
	for _, kind := range []string{"container", "volume", "unknown-container", "unknown-volume"} {
		t.Run(kind, func(t *testing.T) {
			p := factoryDockerCleanupPlanFixture("container")
			if kind == "volume" || kind == "unknown-volume" {
				p = factoryDockerCleanupPlanFixture("volume")
			}
			if kind == "unknown-container" {
				p.Binding.ContainerID, p.Binding.CreatedAt = "", ""
			}
			if kind == "unknown-volume" {
				p.Binding.CreatedAt = ""
			}
			identity := p.Binding.ContainerID
			if identity == "" {
				identity = p.Binding.Name
			}
			message := "Error response from daemon: No such container: " + identity + "\n"
			if p.Binding.Kind == "volume" {
				message = "Error response from daemon: get " + identity + ": no such volume\n"
			}
			result := factory.HostResult{ExitCode: 1, Stdout: []byte("[]\n"), Stderr: []byte(message)}
			got, state, err := parseFactoryDockerCleanupInspection(p, result)
			if err != nil || state != "observed_absent" || !reflect.DeepEqual(got, p.Binding) {
				t.Fatalf("missing=%+v %q %v", got, state, err)
			}
			for _, change := range []func(*factory.HostResult){
				func(r *factory.HostResult) { r.ExitCode = 0 }, func(r *factory.HostResult) { r.ExitCode = 125 },
				func(r *factory.HostResult) { r.Stdout = []byte("[]") }, func(r *factory.HostResult) { r.Stdout = nil },
				func(r *factory.HostResult) { r.Stderr = []byte("daemon unavailable\n") }, func(r *factory.HostResult) { r.Stderr = append(bytes.Clone(r.Stderr), '\n') },
				func(r *factory.HostResult) {
					r.Stderr = bytes.ReplaceAll(r.Stderr, []byte(identity), []byte("foreign"))
				},
			} {
				changed := result
				change(&changed)
				if _, _, err := parseFactoryDockerCleanupInspection(p, changed); err == nil {
					t.Fatal("uncertain error certified absence")
				}
			}
		})
	}
}

func testFactoryDockerCleanupMalformed(t *testing.T) {
	p := factoryDockerCleanupPlanFixture("container")
	valid := factoryDockerCleanupInspectionResult(t, factoryDockerCleanupObject(p))
	for name, data := range map[string][]byte{
		"invalid": []byte("invalid"), "empty": nil, "null": []byte("null"), "zero objects": []byte("[]"), "object": []byte(`{}`),
		"multiple":  append(append(bytes.Clone(valid.Stdout[:len(valid.Stdout)-1]), ','), valid.Stdout[1:]...),
		"truncated": valid.Stdout[:len(valid.Stdout)-1], "trailing": append(bytes.Clone(valid.Stdout), []byte("{}")...),
		"oversize":        bytes.Repeat([]byte(" "), (1<<20)+1),
		"duplicate root":  bytes.Replace(valid.Stdout, []byte(`"Id":`), []byte(`"Id":"foreign","Id":`), 1),
		"duplicate label": bytes.Replace(valid.Stdout, []byte(`"dev.getoro.factory.policy":`), []byte(`"dev.getoro.factory.policy":"foreign","dev.getoro.factory.policy":`), 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := parseFactoryDockerCleanupInspection(p, factory.HostResult{Stdout: data}); err == nil {
				t.Fatal("ambiguous bounded output accepted")
			}
		})
	}
	for _, result := range []factory.HostResult{{Stdout: valid.Stdout, ExitCode: 1}, {Stdout: valid.Stdout, Stderr: []byte("warning")}, {Stdout: valid.Stdout, Stderr: bytes.Repeat([]byte("x"), 4097)}} {
		if _, _, err := parseFactoryDockerCleanupInspection(p, result); err == nil {
			t.Fatal("non-success or unbounded diagnostics accepted")
		}
	}
	for name, change := range map[string]func(map[string]any){
		"PID missing": func(s map[string]any) { delete(s, "Pid") }, "running contradiction": func(s map[string]any) { s["Running"] = false },
		"zero running PID": func(s map[string]any) { s["Pid"] = 0 }, "negative PID": func(s map[string]any) { s["Pid"] = -1 },
		"unknown state": func(s map[string]any) { s["Status"] = "other" }, "paused contradiction": func(s map[string]any) { s["Paused"] = true },
		"removing": func(s map[string]any) { s["Status"] = "removing" }, "null bool": func(s map[string]any) { s["Dead"] = nil },
	} {
		t.Run(name, func(t *testing.T) {
			object := factoryDockerCleanupObject(p)
			change(object["State"].(map[string]any))
			if _, _, err := parseFactoryDockerCleanupInspection(p, factoryDockerCleanupInspectionResult(t, object)); err == nil {
				t.Fatal("contradictory state accepted")
			}
		})
	}
}

func testFactoryDockerCleanupPermitShape(t *testing.T) {
	for name, change := range map[string]func(*factory.DockerCleanupPermit){
		"operation":             func(p *factory.DockerCleanupPermit) { p.Operation = "prune" },
		"context flags":         func(p *factory.DockerCleanupPermit) { p.Binding.Context = "--host=evil" },
		"name flags":            func(p *factory.DockerCleanupPermit) { p.Binding.Name = "--all" },
		"scope":                 func(p *factory.DockerCleanupPermit) { p.Scope.Kind = "host" },
		"scope identity":        func(p *factory.DockerCleanupPermit) { p.Scope.ID = "short" },
		"target":                func(p *factory.DockerCleanupPermit) { p.Target.Backend = "launchd" },
		"generation":            func(p *factory.DockerCleanupPermit) { p.Target.Generation = 0 },
		"future generation":     func(p *factory.DockerCleanupPermit) { p.Target.Generation = p.Scope.Generation + 1 },
		"scope mismatch":        func(p *factory.DockerCleanupPermit) { p.Scope.Name = "foreign" },
		"target mismatch":       func(p *factory.DockerCleanupPermit) { p.Target.Name = "foreign" },
		"ID prefix":             func(p *factory.DockerCleanupPermit) { p.Binding.ContainerID = "abc" },
		"missing creation":      func(p *factory.DockerCleanupPermit) { p.Binding.CreatedAt = "" },
		"noncanonical creation": func(p *factory.DockerCleanupPermit) { p.Binding.CreatedAt = "2026-10-24T00:00:00+00:00" },
		"mutable image":         func(p *factory.DockerCleanupPermit) { p.Binding.ImageID = "latest" },
		"label": func(p *factory.DockerCleanupPermit) {
			p.Binding.Labels["dev.getoro.factory.scope"] = strings.Repeat("0", 32)
		},
		"oversize label":   func(p *factory.DockerCleanupPermit) { p.Binding.Labels["extra"] = strings.Repeat("x", 1025) },
		"missing deadline": func(p *factory.DockerCleanupPermit) { p.Deadline = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			p := factoryDockerCleanupPlanFixture("container")
			change(&p)
			if _, err := factoryDockerCleanupInspectArgs(p); err == nil {
				t.Fatal("unsafe permit selected Docker command")
			}
		})
	}
	for name, change := range map[string]func(*factory.DockerCleanupPermit){
		"driver":          func(p *factory.DockerCleanupPermit) { p.Binding.VolumeDriver = "plugin" },
		"device":          func(p *factory.DockerCleanupPermit) { p.Binding.VolumeOptions["device"] = "/host" },
		"options":         func(p *factory.DockerCleanupPermit) { p.Binding.VolumeOptions["extra"] = "value" },
		"missing options": func(p *factory.DockerCleanupPermit) { p.Binding.VolumeOptions["o"] = "" },
		"image":           func(p *factory.DockerCleanupPermit) { p.Binding.ImageID = "sha256:" + strings.Repeat("f", 64) },
		"unknown removal": func(p *factory.DockerCleanupPermit) {
			p.Binding.CreatedAt = ""
			p.Operation = factory.DockerCleanupRemove
		},
	} {
		t.Run("volume "+name, func(t *testing.T) {
			p := factoryDockerCleanupPlanFixture("volume")
			change(&p)
			if _, err := factoryDockerCleanupInspectArgs(p); err == nil {
				t.Fatal("unsafe volume permit accepted")
			}
		})
	}
}

func testFactoryDockerCleanupStates(t *testing.T) {
	for _, status := range []string{"running", "paused", "restarting", "created", "exited", "dead"} {
		t.Run(status, func(t *testing.T) {
			p := factoryDockerCleanupPlanFixture("container")
			p.Operation = factory.DockerCleanupStop
			object := factoryDockerCleanupObject(p)
			state := object["State"].(map[string]any)
			state["Status"] = status
			want := "present"
			switch status {
			case "paused":
				state["Paused"] = true
			case "restarting":
				state["Restarting"], state["Pid"] = true, 0
			case "created", "exited", "dead":
				state["Running"], state["Pid"], state["Dead"] = false, 0, status == "dead"
				want = "stopped"
			}
			binding, observed, err := parseFactoryDockerCleanupInspection(p, factoryDockerCleanupInspectionResult(t, object))
			if err != nil || observed != want {
				t.Fatalf("%s observation=%q %v", status, observed, err)
			}
			if args, err := factoryDockerCleanupMutationArgs(p, binding); err != nil || args[len(args)-1] != p.Binding.ContainerID {
				t.Fatalf("owned lifecycle cannot retire: %v %v", args, err)
			}
			binding.Labels["changed"] = "copy"
			if _, ok := p.Binding.Labels["changed"]; ok {
				t.Fatal("inspection returned mutable permit aliases")
			}
		})
	}
}

func testFactoryDockerCleanupBounds(t *testing.T) {
	p := factoryDockerCleanupPlanFixture("container")
	valid := factoryDockerCleanupInspectionResult(t, factoryDockerCleanupObject(p))
	valid.Stdout = append(bytes.Repeat([]byte(" "), (1<<20)-len(valid.Stdout)), valid.Stdout...)
	if _, _, err := parseFactoryDockerCleanupInspection(p, valid); err != nil {
		t.Fatalf("exact output limit rejected: %v", err)
	}
	for name, change := range map[string]func(*factory.DockerCleanupPermit){
		"too many labels": func(p *factory.DockerCleanupPermit) {
			for i := 0; i < 17; i++ {
				p.Binding.Labels[strconv.Itoa(i)] = "x"
			}
		},
		"NUL label": func(p *factory.DockerCleanupPermit) { p.Binding.Labels["extra"] = "x\x00y" },
		"encoded permit": func(p *factory.DockerCleanupPermit) {
			for i := 0; i < 9; i++ {
				p.Binding.Labels[strconv.Itoa(i)] = strings.Repeat("\n", 1024)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := factoryDockerCleanupPlanFixture("container")
			change(&changed)
			if _, err := factoryDockerCleanupInspectArgs(changed); err == nil {
				t.Fatal("unbounded permit accepted")
			}
		})
	}
	for _, kind := range []string{"excessive object fields", "null label", "numeric label", "noncanonical creation", "empty creation", "oversize observed labels", "missing config", "missing state", "duplicate state", "case alias"} {
		t.Run(kind, func(t *testing.T) {
			object := factoryDockerCleanupObject(p)
			switch kind {
			case "excessive object fields":
				for i := 0; i < 129; i++ {
					object[strconv.Itoa(i)] = true
				}
			case "null label":
				object["Config"] = map[string]any{"Labels": map[string]any{"dev.getoro.factory.policy": nil}}
			case "numeric label":
				object["Config"] = map[string]any{"Labels": map[string]any{"dev.getoro.factory.policy": 42}}
			case "noncanonical creation":
				object["Created"] = "2026-10-24T01:02:03.456+00:00"
			case "empty creation":
				object["Created"] = ""
			case "oversize observed labels":
				object["Config"].(map[string]any)["Labels"].(map[string]string)["extra"] = strings.Repeat("x", 1025)
			case "missing config":
				delete(object, "Config")
			case "missing state":
				delete(object, "State")
			case "case alias":
				object["id"] = object["Id"]
				delete(object, "Id")
			}
			result := factoryDockerCleanupInspectionResult(t, object)
			if kind == "duplicate state" {
				result.Stdout = bytes.Replace(result.Stdout, []byte(`"Running":true`), []byte(`"Running":false,"Running":true`), 1)
			}
			if _, _, err := parseFactoryDockerCleanupInspection(p, result); err == nil {
				t.Fatal("malformed ownership accepted")
			}
		})
	}
}
