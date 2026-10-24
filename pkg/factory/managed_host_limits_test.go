package factory //nolint:testpackage // The shared limits are checked against private validated contract fixtures.

import (
	"archive/tar"
	"bytes"
	"encoding/base64"
	"math"
	"testing"
)

func TestManagedHostRetentionDockerRoot(t *testing.T) {
	spec := hostRetentionFixture()
	spec.Runtime.SourceBytes = 1048577
	limits, err := ManagedHostRetention(spec)
	if err != nil {
		t.Fatal(err)
	}
	archive := candidateArchive(t, archiveFixtureEntry{header: tar.Header{Name: "./", Mode: 0o700, Typeflag: tar.TypeDir}},
		archiveFixtureEntry{header: tar.Header{Name: "./file", Mode: 0o644, Typeflag: tar.TypeReg}, data: string(bytes.Repeat([]byte{'x'}, int(spec.Runtime.SourceBytes)))})
	if len(archive) != 1051136 {
		t.Fatalf("Docker shape fixture bytes=%d", len(archive))
	}
	if limits.ArchiveBytes != 1051137 || limits.HostOutputBytes < int64(len(archive)) {
		t.Fatalf("Docker root exceeds shared allowance: archive=%d bounds=%+v", len(archive), limits)
	}
}

func hostRetentionFixture() ManagedSpec {
	spec := managedContractFixture()
	spec.MaxAttempts = 1
	spec.Runtime.SourceBytes, spec.Runtime.MaxFiles = 1024, 1
	spec.Runtime.MaxHostInvocations, spec.Runtime.MaxModelCalls, spec.Runtime.MaxToolCalls = 1, 1, 1
	spec.Recovery.MaxSupervisorStarts, spec.Recovery.MaxControllerStarts, spec.Recovery.MaxReconciliationInvocations = 1, 1, 1
	spec.Recovery.MaxCleanupInvocations = 1
	return spec
}

func TestManagedHostRetention(t *testing.T) {
	t.Run("exact complete retained bound", func(t *testing.T) {
		limits, err := ManagedHostRetention(hostRetentionFixture())
		if err != nil {
			t.Fatal(err)
		}
		// Independently calculated: fixed=1,924,136,960; source=7,133,184;
		// invocation=723,681,280; scope metadata=11,534,336 bytes.
		if limits.MinimumBytes != 2666485760 || limits.ArchiveBytes != 3584 || limits.HostOutputBytes != 1<<20 || limits.PendingResultBytes != 4210688 {
			t.Fatalf("retained bounds = %+v", limits)
		}
		if limits.SourceVersions != 6 || limits.InvocationRecords != 5 || limits.ScopeRecords != 11 || limits.MaxPathBytes != 255 {
			t.Fatalf("writer/count contracts = %+v", limits)
		}
		if ManagedHostRequestBytes != 64<<20 || limits.RequestBytes != 64<<20 || limits.TranscriptBytes != limits.PendingResultBytes || limits.RuntimeConfigBytes != 1<<20 ||
			limits.ProgramBytes != 512<<20 || limits.ScopeMetadataBytes != 1<<20 || limits.LedgerBytes != 32<<20 {
			t.Fatalf("fixed writer caps = %+v", limits)
		}
	})
	t.Run("both retained request copies grow", testHostRetentionRequestCopies)
	t.Run("full Oro source allowance fits", testHostRetentionFullSource)
	t.Run("every retained budget family contributes", testHostRetentionFamilies)
	t.Run("exact base64 transfer boundary", func(t *testing.T) {
		spec := hostRetentionFixture()
		// A=49,545,216 encodes to 63 MiB, leaving 1 MiB of request metadata.
		spec.Runtime.SourceBytes = 49545216 - 2560
		limits, err := ManagedHostRetention(spec)
		if err != nil || limits.ArchiveBytes != 49545216 || limits.HostOutputBytes != limits.ArchiveBytes {
			t.Fatalf("exact transport bound rejected: %+v, %v", limits, err)
		}
		spec.Runtime.SourceBytes++
		if _, err := ManagedHostRetention(spec); err == nil {
			t.Fatal("archive whose base64 exceeds request budget accepted")
		}
	})
	t.Run("arithmetic overflow and invalid policy", testHostRetentionOverflow)
	t.Run("turns stay within one capped model transcript", func(t *testing.T) {
		spec := hostRetentionFixture()
		before, err := ManagedHostRetention(spec)
		if err != nil {
			t.Fatal(err)
		}
		spec.Runtime.MaxModelTurns++
		after, err := ManagedHostRetention(spec)
		if err != nil || after != before {
			t.Fatalf("per-call transcript cap changed with model turns: %+v, %v", after, err)
		}
	})
}

func testHostRetentionRequestCopies(t *testing.T) {
	t.Helper()
	for _, fixture := range []struct {
		hostCalls    int
		priorMinimum int64
	}{
		{hostCalls: 1, priorMinimum: 2330941440},
		// Six more host calls previously retained 78,675,968 bytes each,
		// including their separate scope metadata.
		{hostCalls: 7, priorMinimum: 2802997248},
	} {
		spec := hostRetentionFixture()
		spec.Runtime.MaxHostInvocations = fixture.hostCalls
		limits, err := ManagedHostRetention(spec)
		if err != nil {
			t.Fatal(err)
		}
		calls := int64(fixture.hostCalls + 4) // One model, tool, reconciliation and cleanup.
		wantDelta := calls * (64 << 20)       // Two copies each grow by 32 MiB.
		if limits.InvocationRecords != calls || limits.MinimumBytes-fixture.priorMinimum != wantDelta {
			t.Fatalf("H=%d request-copy growth: minimum=%d, prior=%d, Q=%d, want delta=%d",
				fixture.hostCalls, limits.MinimumBytes, fixture.priorMinimum, limits.InvocationRecords, wantDelta)
		}
	}
}

func testHostRetentionFullSource(t *testing.T) {
	t.Helper()
	spec := hostRetentionFixture()
	spec.Runtime.SourceBytes = 40 << 20
	spec.Runtime.MaxFiles, spec.Runtime.SourceInodes = 2200, 2200
	limits, err := ManagedHostRetention(spec)
	if err != nil {
		t.Fatal(err)
	}
	if limits.ArchiveBytes != 44197376 || limits.HostOutputBytes != limits.ArchiveBytes {
		t.Fatalf("full source archive allowance = %+v", limits)
	}
	encoded := int64(base64.StdEncoding.EncodedLen(int(limits.ArchiveBytes))) + (1 << 20)
	if encoded != 59978412 || encoded > limits.RequestBytes {
		t.Fatalf("full source encoded request = %d, cap = %d", encoded, limits.RequestBytes)
	}
}

func testHostRetentionFamilies(t *testing.T) {
	t.Helper()
	base, err := ManagedHostRetention(hostRetentionFixture())
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ManagedSpec){
		"source bytes":          func(s *ManagedSpec) { s.Runtime.SourceBytes++ },
		"files and directories": func(s *ManagedSpec) { s.Runtime.MaxFiles++ },
		"attempts":              func(s *ManagedSpec) { s.MaxAttempts++ },
		"checks": func(s *ManagedSpec) {
			s.RequiredChecks = append(s.RequiredChecks, "other")
			s.Acceptance = append(s.Acceptance, Check{ID: "other", Args: []string{"true"}})
		},
		"host calls":        func(s *ManagedSpec) { s.Runtime.MaxHostInvocations++ },
		"model calls":       func(s *ManagedSpec) { s.Runtime.MaxModelCalls++ },
		"tool calls":        func(s *ManagedSpec) { s.Runtime.MaxToolCalls++ },
		"reconciliation":    func(s *ManagedSpec) { s.Recovery.MaxReconciliationInvocations++ },
		"cleanup":           func(s *ManagedSpec) { s.Recovery.MaxCleanupInvocations++ },
		"supervisor starts": func(s *ManagedSpec) { s.Recovery.MaxSupervisorStarts++ },
		"controller starts": func(s *ManagedSpec) { s.Recovery.MaxControllerStarts++ },
	} {
		t.Run(name, func(t *testing.T) {
			spec := hostRetentionFixture()
			mutate(&spec)
			got, err := ManagedHostRetention(spec)
			if err != nil || got.MinimumBytes <= base.MinimumBytes {
				t.Fatalf("retention omitted %s: %+v, %v", name, got, err)
			}
		})
	}
}

func testHostRetentionOverflow(t *testing.T) {
	t.Helper()
	for name, mutate := range map[string]func(*ManagedSpec){
		"archive multiplication":      func(s *ManagedSpec) { s.Runtime.MaxFiles = math.MaxInt; s.Runtime.SourceInodes = math.MaxInt },
		"invocation sum":              func(s *ManagedSpec) { s.Runtime.MaxHostInvocations = math.MaxInt },
		"scope sum":                   func(s *ManagedSpec) { s.Recovery.MaxSupervisorStarts = math.MaxInt },
		"cleanup sum":                 func(s *ManagedSpec) { s.Recovery.MaxCleanupInvocations = math.MaxInt },
		"retained cleanup product":    func(s *ManagedSpec) { s.Recovery.MaxCleanupInvocations = math.MaxInt / (32 << 20) },
		"retained invocation product": func(s *ManagedSpec) { s.Runtime.MaxHostInvocations = math.MaxInt / (32 << 20) },
		"retained source product":     func(s *ManagedSpec) { s.MaxAttempts = math.MaxInt / (1 << 20) },
		"invalid zero":                func(s *ManagedSpec) { s.Runtime.SourceBytes = 0 },
		"invalid negative":            func(s *ManagedSpec) { s.Runtime.MaxFiles = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			spec := hostRetentionFixture()
			mutate(&spec)
			if _, err := ManagedHostRetention(spec); err == nil {
				t.Fatal("overflowing or invalid retained-byte policy accepted")
			}
		})
	}
}
