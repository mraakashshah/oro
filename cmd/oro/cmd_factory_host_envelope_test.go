package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// This proves the wrapper codec's complete envelope bound. Core admission also
// enforces conservative source/base64 and command-metadata allowances separately.
func TestFactoryHostEnvelopeBoundaries(t *testing.T) {
	t.Run("complete 64 MiB and one byte over", testFactoryHostEnvelopeExactLimit)
	t.Run("string escaping and binary input", testFactoryHostEnvelopeEscaping)
	t.Run("invalid UTF8 strings have explicit canonical semantics", testFactoryHostEnvelopeInvalidUTF8)
	t.Run("digest canonicality and destination are immutable", testFactoryHostEnvelopeRejections)
	t.Run("metadata alone cannot exceed complete bound", testFactoryHostEnvelopeMetadata)
}

func factoryHostEnvelopeRequest(t *testing.T) factoryHostExecRequest {
	t.Helper()
	request := factoryHostExecFixtureRequest(t, "literal")
	// Large race-instrumented JSON operations must not accidentally test the
	// unrelated five-second fixture deadline. No command is ever executed here.
	request.Invocation.Deadline = time.Now().Add(5 * time.Minute).UTC()
	request.Invocation.Args = []string{"quoted\"\\\n\t\x01<>&\u2028", ""}
	request.Invocation.Env = []string{"VALUE=quoted\"\\\n\t\x01<>&\u2029"}
	request.Invocation.Stdin = []byte{}
	return request
}

func testFactoryHostEnvelopeExactLimit(t *testing.T) {
	t.Helper()
	const completeLimit = 64 << 20
	request := factoryHostEnvelopeRequest(t)
	metadata := marshalFactoryHostEnvelope(t, request)
	// Base64 contributes four bytes per three input bytes. An ordinary argument
	// fills the remaining zero to three bytes without changing JSON overhead.
	rawBytes := ((completeLimit - len(metadata)) / 4) * 3
	padding := completeLimit - len(metadata) - base64.StdEncoding.EncodedLen(rawBytes)
	if rawBytes <= 24379392 || padding < 0 || padding > 3 {
		t.Fatalf("invalid large envelope fixture: raw=%d padding=%d", rawBytes, padding)
	}
	pattern := []byte{0, 0xff, 0xfe, '\n', '"', '\\', 0x80, 'x'}
	request.Invocation.Stdin = bytes.Repeat(pattern, (rawBytes+len(pattern)-1)/len(pattern))[:rawBytes]
	request.Invocation.Args[1] = strings.Repeat("p", padding)
	data, err := encodeFactoryHostExecRequest(request)
	if err != nil || len(data) != completeLimit {
		t.Fatalf("exact complete envelope rejected: bytes=%d, %v", len(data), err)
	}
	path := writeFactoryHostEnvelope(t, request, data)
	raw, err := readFactoryHostExecBytes(path)
	if err != nil || !bytes.Equal(raw, data) {
		t.Fatalf("bounded reader changed exact-limit bytes: read=%d, %v", len(raw), err)
	}
	got, err := readFactoryHostEnvelope(request, path, factoryHostDigest(data))
	if err != nil || !reflect.DeepEqual(got, request) {
		t.Fatalf("exact-limit binary envelope failed canonical roundtrip: stdin=%d, %v", len(got.Invocation.Stdin), err)
	}
	request.Invocation.Args[1] += "x"
	oversized := marshalFactoryHostEnvelope(t, request)
	if len(oversized) != completeLimit+1 {
		t.Fatalf("one-byte overflow fixture is %d bytes", len(oversized))
	}
	if output, err := encodeFactoryHostExecRequest(request); err == nil || output != nil {
		t.Fatalf("encoder accepted canonical limit+1: bytes=%d, %v", len(output), err)
	}
	writeFactoryHostEnvelope(t, request, oversized)
	if output, err := readFactoryHostExecBytes(path); err == nil || output != nil {
		t.Fatalf("bounded reader accepted canonical limit+1: bytes=%d, %v", len(output), err)
	}
	if _, err := readFactoryHostEnvelope(request, path, factoryHostDigest(oversized)); err == nil {
		t.Fatal("correctly hashed canonical limit+1 request accepted")
	}
	assertFactoryHostEnvelopeNoEffects(t, request)
}

func testFactoryHostEnvelopeEscaping(t *testing.T) {
	t.Helper()
	request := factoryHostEnvelopeRequest(t)
	request.Invocation.Stdin = []byte{0, 0xff, 0xfe, '\n', '\\', '"', 0x80}
	data, err := encodeFactoryHostExecRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, escape := range []string{`\"`, `\\`, `\n`, `\t`, `\u0001`, `\u003c`, `\u003e`, `\u0026`, `\u2028`, `\u2029`} {
		if !bytes.Contains(data, []byte(escape)) {
			t.Fatalf("encoded envelope lacks actual JSON escape %q", escape)
		}
	}
	encodedInput := base64.StdEncoding.EncodeToString(request.Invocation.Stdin)
	if !bytes.Contains(data, []byte(`"stdin":"`+encodedInput+`"`)) {
		t.Fatal("binary stdin did not use exact base64 representation")
	}
	path := writeFactoryHostEnvelope(t, request, data)
	got, err := readFactoryHostEnvelope(request, path, factoryHostDigest(data))
	if err != nil || !reflect.DeepEqual(got, request) {
		t.Fatalf("allowed escaped strings or binary bytes changed: %v", err)
	}
	assertFactoryHostEnvelopeNoEffects(t, request)
}

func testFactoryHostEnvelopeInvalidUTF8(t *testing.T) {
	t.Helper()
	request := factoryHostEnvelopeRequest(t)
	request.Invocation.Args = []string{"x\xffy\xfe"}
	request.Invocation.Env = []string{"VALUE=\x80"}
	request.Invocation.Stdin = []byte{0, 0xff, 0xfe, 0x80}
	data, err := encodeFactoryHostExecRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`x\ufffdy\ufffd`)) || !bytes.Contains(data, []byte(`VALUE=\ufffd`)) {
		t.Fatal("encoding/json invalid UTF8 replacement behavior changed")
	}
	var normalized factoryHostExecRequest
	if err := json.Unmarshal(data, &normalized); err != nil {
		t.Fatal(err)
	}
	if normalized.Invocation.Args[0] != "x\ufffdy\ufffd" || normalized.Invocation.Env[0] != "VALUE=\ufffd" ||
		!bytes.Equal(normalized.Invocation.Stdin, request.Invocation.Stdin) {
		t.Fatal("invalid string bytes and binary stdin were not treated separately")
	}
	// Marshal replaces invalid string bytes with escaped U+FFFD. Re-encoding
	// the now-valid rune uses literal UTF8, so the strict reader rejects that
	// original noncanonical spelling instead of silently executing changed argv.
	path := writeFactoryHostEnvelope(t, request, data)
	if _, err := readFactoryHostEnvelope(request, path, factoryHostDigest(data)); err == nil {
		t.Fatal("lossy invalid UTF8 string envelope became executable")
	}
	canonical, err := encodeFactoryHostExecRequest(normalized)
	if err != nil || bytes.Equal(canonical, data) || !bytes.Contains(canonical, []byte("x\ufffdy\ufffd")) {
		t.Fatalf("valid replacement rune did not have a distinct canonical representation: %v", err)
	}
	writeFactoryHostEnvelope(t, normalized, canonical)
	got, err := readFactoryHostEnvelope(normalized, path, factoryHostDigest(canonical))
	if err != nil || !reflect.DeepEqual(got, normalized) {
		t.Fatalf("explicit valid U+FFFD input rejected: %v", err)
	}
	assertFactoryHostEnvelopeNoEffects(t, request)
}

func testFactoryHostEnvelopeRejections(t *testing.T) {
	t.Helper()
	for _, name := range []string{"wrong digest", "truncated digest", "truncated JSON", "whitespace", "unknown field", "unknown nested field", "duplicate field", "duplicate nested field", "different destination", "missing file"} {
		t.Run(name, func(t *testing.T) {
			request := factoryHostEnvelopeRequest(t)
			path := filepath.Join(filepath.Dir(request.ResultPath), "request.json")
			if name == "different destination" {
				request.ResultPath = filepath.Join(request.Invocation.Dir, "result.pending.json")
			}
			data := marshalFactoryHostEnvelope(t, request)
			switch name {
			case "truncated JSON":
				data = data[:len(data)-1]
			case "whitespace":
				data = append(data, '\n')
			case "unknown field":
				data = append([]byte(`{"unexpected":true,`), data[1:]...)
			case "unknown nested field":
				data = bytes.Replace(data, []byte(`"output_limit":`), []byte(`"unexpected":true,"output_limit":`), 1)
			case "duplicate field":
				data = append([]byte(`{"result_path":"discarded",`), data[1:]...)
			case "duplicate nested field":
				data = bytes.Replace(data, []byte(`"output_limit":`), []byte(`"output_limit":4096,"output_limit":`), 1)
			}
			// Keep the actual request file in its original private directory so a
			// syntactically valid alternate result.pending.json cannot relocate it.
			if name != "missing file" {
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			digest := factoryHostDigest(data)
			switch name {
			case "wrong digest":
				digest = strings.Repeat("0", 64)
			case "truncated digest":
				digest = digest[:63]
			}
			if _, err := readFactoryHostEnvelope(request, path, digest); err == nil {
				t.Fatalf("%s accepted", name)
			}
			assertFactoryHostEnvelopeNoEffects(t, request)
		})
	}
}

func testFactoryHostEnvelopeMetadata(t *testing.T) {
	t.Helper()
	for _, fixture := range []struct {
		name, value string
	}{
		{name: "raw metadata exceeds limit", value: strings.Repeat("a", 64<<20)},
		{name: "escaped metadata exceeds limit", value: strings.Repeat("\x01", (64<<20)/6+1)},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			request := factoryHostEnvelopeRequest(t)
			request.Invocation.Args = []string{fixture.value}
			if err := validateFactoryHostExecRequest(request); err != nil {
				t.Fatalf("metadata fixture is otherwise invalid: %v", err)
			}
			if output, err := encodeFactoryHostExecRequest(request); err == nil || output != nil {
				t.Fatalf("oversized metadata accepted: bytes=%d, %v", len(output), err)
			}
			assertFactoryHostEnvelopeNoEffects(t, request)
		})
	}
}

func marshalFactoryHostEnvelope(t *testing.T, request factoryHostExecRequest) []byte {
	t.Helper()
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeFactoryHostEnvelope(t *testing.T, request factoryHostExecRequest, data []byte) string {
	t.Helper()
	path := filepath.Join(filepath.Dir(request.ResultPath), "request.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFactoryHostEnvelope(request factoryHostExecRequest, path, digest string) (factoryHostExecRequest, error) {
	return readFactoryHostExecRequest(path, digest, request.Invocation.Deadline.Format(time.RFC3339Nano), request.Invocation.OutputLimit)
}

func assertFactoryHostEnvelopeNoEffects(t *testing.T, request factoryHostExecRequest) {
	t.Helper()
	factoryHostExecAssertAbsent(t, request.ResultPath)
	factoryHostExecAssertAbsent(t, request.ResultPath+".inflight")
	factoryHostExecAssertAbsent(t, filepath.Join(request.Invocation.Dir, "child-started"))
}
