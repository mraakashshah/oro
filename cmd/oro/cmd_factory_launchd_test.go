package main

import (
	"bytes"
	"encoding/xml"
	"math"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"oro/pkg/factory"
)

func TestFactoryLaunchdJob(t *testing.T) {
	valid := func() factory.HostInvocation {
		id, nonce := strings.Repeat("a", 32), strings.Repeat("b", 32)
		dir := "/private/factory/request & $(literal)"
		return factory.HostInvocation{
			Scope: factory.ScopeHandle{
				ID: id, Nonce: nonce, Kind: "host", Backend: "launchd", Generation: 7,
				Name: "dev.getoro.factory.0123456789abcdef.host." + id + "." + nonce,
			},
			Path: "/trusted/Oro & tools/oro", Dir: dir,
			Args:     []string{"factory", "_host-exec", "--request", dir + "/request.json", "--request-sha256", strings.Repeat("c", 64), "--socket", "/private/tmp/factory/socket"},
			Deadline: time.Date(2027, time.January, 2, 3, 4, 5, 123456789, time.UTC), OutputLimit: 1 << 20,
		}
	}
	t.Run("native launchd boolean tokens", func(t *testing.T) {
		invocation := valid()
		invocation.Path = "/trusted/<true></true>/<false></false>/oro"
		data, err := factoryJobPlist(invocation)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("<true></true>")) || bytes.Contains(data, []byte("<false></false>")) ||
			bytes.Count(data, []byte("<true/>")) != 1 || bytes.Count(data, []byte("<false/>")) != 3 {
			t.Fatal("launchd requires self-closing boolean tokens even when plutil accepts expanded tags")
		}
		if got := decodeFactoryJobPlist(t, data)["Program"].Text; got != invocation.Path {
			t.Fatalf("boolean serialization changed literal program path: %q", got)
		}
	})
	t.Run("literal immutable one shot wrapper", func(t *testing.T) {
		invocation := valid()
		before := valid()
		data, err := factoryJobPlist(invocation)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS == "darwin" {
			t.Run("Apple plist parser", func(t *testing.T) {
				cmd := exec.CommandContext(t.Context(), "/usr/bin/plutil", "-lint", "-")
				cmd.Stdin = bytes.NewReader(data)
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("Apple rejected emitted plist: %s, %v", output, err)
				}
			})
		}
		properties := decodeFactoryJobPlist(t, data)
		wantArgs := append([]string{invocation.Path}, invocation.Args...)
		wantArgs = append(wantArgs, "--deadline", "2027-01-02T03:04:05.123456789Z", "--output-limit", "1048576")
		if got := properties["ProgramArguments"]; got.XMLName.Local != "array" || !reflect.DeepEqual(got.Strings, wantArgs) {
			t.Errorf("ProgramArguments = %+v, want literal %q", got, wantArgs)
		}
		for key, want := range map[string]string{
			"Label": invocation.Scope.Name, "Program": invocation.Path, "WorkingDirectory": invocation.Dir,
			"StandardInPath": "/dev/null", "StandardOutPath": "/dev/null", "StandardErrorPath": "/dev/null",
		} {
			if got := properties[key]; got.XMLName.Local != "string" || got.Text != want {
				t.Errorf("%s = %+v, want string %q", key, got, want)
			}
		}
		for _, key := range []string{"KeepAlive", "AbandonProcessGroup", "EnableGlobbing"} {
			if properties[key].XMLName.Local != "false" {
				t.Errorf("%s must be explicitly false", key)
			}
		}
		if properties["RunAtLoad"].XMLName.Local != "true" {
			t.Error("RunAtLoad must be true")
		}
		for key, want := range map[string]string{"ExitTimeOut": "5", "Umask": "63"} {
			if got := properties[key]; got.XMLName.Local != "integer" || got.Text != want {
				t.Errorf("%s = %+v, want integer %s", key, got, want)
			}
		}
		if _, ok := properties["EnvironmentVariables"]; ok {
			t.Error("wrapper must not receive caller environment overrides")
		}
		if len(properties) != 13 {
			t.Errorf("unexpected plist properties: %+v", properties)
		}
		if !reflect.DeepEqual(invocation, before) {
			t.Error("renderer mutated the invocation")
		}
		again, err := factoryJobPlist(invocation)
		if err != nil || string(again) != string(data) {
			t.Fatalf("rendering is not deterministic: %v", err)
		}
	})
	t.Run("deadline keeps its instant in UTC", func(t *testing.T) {
		invocation := valid()
		invocation.Deadline = invocation.Deadline.In(time.FixedZone("other", 3600))
		data, err := factoryJobPlist(invocation)
		if err != nil {
			t.Fatal(err)
		}
		args := decodeFactoryJobPlist(t, data)["ProgramArguments"].Strings
		if args[len(args)-3] != "2027-01-02T03:04:05.123456789Z" {
			t.Errorf("deadline instant changed: %q", args)
		}
	})
	t.Run("distinct scope labels", func(t *testing.T) {
		first, second := valid(), valid()
		second.Scope.Nonce = strings.Repeat("d", 32)
		second.Scope.Name = strings.TrimSuffix(second.Scope.Name, first.Scope.Nonce) + second.Scope.Nonce
		left, leftErr := factoryJobPlist(first)
		right, rightErr := factoryJobPlist(second)
		if leftErr != nil || rightErr != nil {
			t.Fatalf("render separate scopes: %v, %v", leftErr, rightErr)
		}
		if decodeFactoryJobPlist(t, left)["Label"].Text == decodeFactoryJobPlist(t, right)["Label"].Text {
			t.Fatal("different scopes reused a launchd label")
		}
	})
	t.Run("observer scope keeps its explicit purpose", func(t *testing.T) {
		invocation := valid()
		invocation.Scope.Kind = "observer"
		invocation.Scope.Name = strings.Replace(invocation.Scope.Name, ".host.", ".observer.", 1)
		data, err := factoryJobPlist(invocation)
		if err != nil {
			t.Fatalf("observer wrapper rejected: %v", err)
		}
		properties := decodeFactoryJobPlist(t, data)
		if properties["Label"].Text != invocation.Scope.Name || properties["KeepAlive"].XMLName.Local != "false" ||
			properties["AbandonProcessGroup"].XMLName.Local != "false" {
			t.Errorf("observer lost exact identity or one-shot lifetime: %+v", properties)
		}
	})
	for name, mutate := range map[string]func(*factory.HostInvocation){
		"legacy label":  func(i *factory.HostInvocation) { i.Scope.Name = "dev.getoro.oro" },
		"foreign label": func(i *factory.HostInvocation) { i.Scope.Name = "com.other.factory" },
		"objective hash": func(i *factory.HostInvocation) {
			i.Scope.Name = strings.Replace(i.Scope.Name, "0123456789abcdef", "0123456789abcdeG", 1)
		},
		"scope id mismatch":        func(i *factory.HostInvocation) { i.Scope.ID = strings.Repeat("e", 32) },
		"nonce mismatch":           func(i *factory.HostInvocation) { i.Scope.Nonce = strings.Repeat("f", 32) },
		"nonce length":             func(i *factory.HostInvocation) { i.Scope.Nonce = "aa" },
		"zero generation":          func(i *factory.HostInvocation) { i.Scope.Generation = 0 },
		"backend":                  func(i *factory.HostInvocation) { i.Scope.Backend = "docker" },
		"kind":                     func(i *factory.HostInvocation) { i.Scope.Kind = "worker" },
		"observer with host label": func(i *factory.HostInvocation) { i.Scope.Kind = "observer" },
		"host with observer label": func(i *factory.HostInvocation) {
			i.Scope.Name = strings.Replace(i.Scope.Name, ".host.", ".observer.", 1)
		},
		"relative executable":      func(i *factory.HostInvocation) { i.Path = "oro" },
		"unclean executable":       func(i *factory.HostInvocation) { i.Path = "/trusted/../oro" },
		"NUL executable":           func(i *factory.HostInvocation) { i.Path += "\x00" },
		"invalid UTF8 executable":  func(i *factory.HostInvocation) { i.Path += "\xff" },
		"root directory":           func(i *factory.HostInvocation) { i.Dir = "/" },
		"relative directory":       func(i *factory.HostInvocation) { i.Dir = "requests" },
		"unclean directory":        func(i *factory.HostInvocation) { i.Dir += "/.." },
		"direct payload":           func(i *factory.HostInvocation) { i.Args = []string{"-c", "touch /outside"} },
		"wrong command":            func(i *factory.HostInvocation) { i.Args[0] = "other" },
		"wrong wrapper":            func(i *factory.HostInvocation) { i.Args[1] = "run" },
		"wrong request flag":       func(i *factory.HostInvocation) { i.Args[2] = "--other" },
		"foreign request":          func(i *factory.HostInvocation) { i.Args[3] = "/outside/request.json" },
		"wrong hash flag":          func(i *factory.HostInvocation) { i.Args[4] = "--other" },
		"short digest":             func(i *factory.HostInvocation) { i.Args[5] = "aa" },
		"uppercase digest":         func(i *factory.HostInvocation) { i.Args[5] = strings.Repeat("A", 64) },
		"wrong socket flag":        func(i *factory.HostInvocation) { i.Args[6] = "--other" },
		"relative socket":          func(i *factory.HostInvocation) { i.Args[7] = "socket" },
		"unclean socket":           func(i *factory.HostInvocation) { i.Args[7] = "/private/../socket" },
		"long socket":              func(i *factory.HostInvocation) { i.Args[7] = "/" + strings.Repeat("s", 103) },
		"NUL socket":               func(i *factory.HostInvocation) { i.Args[7] += "\x00" },
		"extra argv":               func(i *factory.HostInvocation) { i.Args = append(i.Args, "--deadline", "replacement") },
		"stdin":                    func(i *factory.HostInvocation) { i.Stdin = []byte("payload") },
		"environment":              func(i *factory.HostInvocation) { i.Env = []string{"PATH=/candidate"} },
		"zero deadline":            func(i *factory.HostInvocation) { i.Deadline = time.Time{} },
		"unrepresentable deadline": func(i *factory.HostInvocation) { i.Deadline = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) },
		"zero output limit":        func(i *factory.HostInvocation) { i.OutputLimit = 0 },
		"negative output limit":    func(i *factory.HostInvocation) { i.OutputLimit = -1 },
		"output overflow":          func(i *factory.HostInvocation) { i.OutputLimit = math.MaxInt64 },
	} {
		t.Run("reject "+name, func(t *testing.T) {
			invocation := valid()
			mutate(&invocation)
			if data, err := factoryJobPlist(invocation); err == nil || data != nil {
				t.Fatalf("unsafe invocation rendered: %s, %v", data, err)
			}
		})
	}
}

type factoryJobTestValue struct {
	XMLName xml.Name
	Text    string   `xml:",chardata"`
	Strings []string `xml:"string"`
}

func decodeFactoryJobPlist(t *testing.T, data []byte) map[string]factoryJobTestValue {
	t.Helper()
	var document struct {
		XMLName xml.Name `xml:"plist"`
		Version string   `xml:"version,attr"`
		Dict    struct {
			Values []factoryJobTestValue `xml:",any"`
		} `xml:"dict"`
	}
	if err := xml.Unmarshal(data, &document); err != nil || document.Version != "1.0" {
		t.Fatalf("invalid plist: %s, %v", data, err)
	}
	values := document.Dict.Values
	properties := make(map[string]factoryJobTestValue)
	for index := 0; index < len(values); index += 2 {
		if index+1 >= len(values) || values[index].XMLName.Local != "key" {
			t.Fatalf("invalid plist dictionary: %+v", values)
		}
		key := values[index].Text
		if _, exists := properties[key]; exists {
			t.Fatalf("duplicate plist key %q", key)
		}
		properties[key] = values[index+1]
	}
	return properties
}
