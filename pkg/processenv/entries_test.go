package processenv_test

import (
	"encoding/binary"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"oro/pkg/processenv"
)

func TestReadEntriesPreservesOwnershipEntryBoundaries(t *testing.T) {
	if os.Getenv("ORO_PROCESSENV_ENTRY_HELPER") == "1" {
		for {
			time.Sleep(time.Hour)
		}
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("delimiter-preserving process environment reader is unsupported")
	}

	markers := processenv.WorkerOwnershipMarkers("/tmp/processenv.sock", "processenv-worker")
	note := "NOTE=foreign " + strings.Join(markers, " ") + " text"
	cmd := exec.Command(os.Args[0], "-test.run=^TestReadEntriesPreservesOwnershipEntryBoundaries$") //nolint:gosec // re-executes this test binary as a helper
	cmd.Env = append(processenv.WithWorkerOwnership(os.Environ(), "/tmp/processenv.sock", "processenv-worker"),
		"ORO_PROCESSENV_ENTRY_HELPER=1", note)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start environment helper: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	entries, err := processenv.ReadEntries(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("read helper environment entries: %v", err)
	}
	if !processenv.CommandContainsAllMarkers(entries, markers) {
		t.Fatalf("entries = %#v, want exact ownership markers %v", entries, markers)
	}
	if !slices.Contains(entries, note) {
		t.Fatalf("entries = %#v, want space-containing note %q preserved", entries, note)
	}
	if processenv.CommandContainsAllMarkers([]string{note}, markers) {
		t.Fatal("marker-shaped text inside one foreign value must not prove ownership")
	}
}

func TestParseDarwinEntriesPreservesEnvironmentSuffix(t *testing.T) {
	markers := processenv.WorkerOwnershipMarkers("/tmp/processenv.sock", "processenv-worker")
	raw := append([]byte{2, 0, 0, 0}, []byte("/bin/helper\x00helper\x00--flag\x00\x00PATH=/bin\x00NOTE=value with spaces\x00"+markers[0]+"\x00"+markers[1]+"\x00")...)

	entries, err := processenv.ParseDarwinEntries(raw)
	if err != nil {
		t.Fatalf("parse Darwin entries: %v", err)
	}
	want := []string{"PATH=/bin", "NOTE=value with spaces", markers[0], markers[1]}
	if !slices.Equal(entries, want) {
		t.Fatalf("entries = %#v, want %#v", entries, want)
	}
}

func TestParseDarwinEntriesRejectsMalformedPayloads(t *testing.T) {
	argcOne := make([]byte, 4)
	binary.LittleEndian.PutUint32(argcOne, 1)

	for name, raw := range map[string][]byte{
		"short header":       {0, 0, 0},
		"missing executable": {0, 0, 0, 0},
		"truncated argv":     append(argcOne, []byte("/bin/helper\x00\x00")...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := processenv.ParseDarwinEntries(raw); err == nil {
				t.Fatal("ParseDarwinEntries succeeded for malformed payload")
			}
		})
	}
}

func TestReadEntriesFailsClosedForMissingProcess(t *testing.T) {
	if _, err := processenv.ReadEntries(1 << 30); err == nil {
		t.Fatal("ReadEntries succeeded for a missing process")
	}
}
