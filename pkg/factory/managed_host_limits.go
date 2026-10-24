package factory

import (
	"errors"
	"math"
)

const (
	// ManagedHostRequestBytes caps the complete encoded host request, including
	// base64 stdin. Host wrappers and request publishers share this bound.
	ManagedHostRequestBytes = 64 << 20
	// ManagedHostResultMetadataBytes covers scope identity and escaped errors in
	// addition to the pending result's four-times-output allowance.
	ManagedHostResultMetadataBytes = 16384
)

// HostRetentionLimits is the shared per-objective writer and retention contract.
// Writers must enforce these caps before writing, including encoded transcripts,
// staging artifacts and command metadata. They cannot introduce extra retained
// copies or uncounted invocations. MaxFiles in ManagedSpec includes both regular
// entries and their unique implicit directories, with paths at most MaxPathBytes.
type HostRetentionLimits struct {
	MinimumBytes       int64
	ArchiveBytes       int64
	HostOutputBytes    int64
	RequestBytes       int64
	PendingResultBytes int64
	TranscriptBytes    int64
	ScopeMetadataBytes int64
	RuntimeConfigBytes int64
	ProgramBytes       int64
	LedgerBytes        int64
	SourceVersions     int64
	InvocationRecords  int64
	ScopeRecords       int64
	MaxPathBytes       int
}

// ManagedHostRetention computes a conservative retained-byte bound without I/O.
// The CLI must reject HostRequiredBytes below MinimumBytes before reserving space.
// Models' turn counts remain inside each invocation's bounded transcript, while
// every model, tool, host and recovery invocation is accounted for explicitly.
func ManagedHostRetention(spec ManagedSpec) (HostRetentionLimits, error) {
	records, err := managedRecordLimits(spec.Recovery, spec.Runtime)
	if err != nil {
		return HostRetentionLimits{}, err
	}
	if err := validateManagedSpec(spec); err != nil {
		return HostRetentionLimits{}, err
	}
	var arithmetic hostRetentionArithmetic
	runtime := spec.Runtime
	limits := HostRetentionLimits{
		RequestBytes: ManagedHostRequestBytes, RuntimeConfigBytes: 1 << 20,
		ProgramBytes: managedExecutableMaxBytes, LedgerBytes: 32 << 20,
		ScopeMetadataBytes: 1 << 20, MaxPathBytes: 255,
	}
	// USTAR needs one 512-byte header and up to 511 padding bytes per entry,
	// plus two end blocks and Docker's extra './' root directory header. Count
	// source directories as entries too; the transport root is not a source inode.
	limits.ArchiveBytes = arithmetic.sum(runtime.SourceBytes, arithmetic.product(1024, int64(runtime.MaxFiles)), 1536)
	encoded := arithmetic.sum(arithmetic.product(4, arithmetic.sum(limits.ArchiveBytes, 2)/3), 1<<20)
	if arithmetic.err != nil {
		return HostRetentionLimits{}, arithmetic.err
	}
	if encoded > limits.RequestBytes {
		return HostRetentionLimits{}, errors.New("managed archive and base64 request metadata exceed the host transfer bound")
	}
	limits.HostOutputBytes = max(limits.ArchiveBytes, 1<<20)
	limits.PendingResultBytes = arithmetic.sum(arithmetic.product(4, limits.HostOutputBytes), ManagedHostResultMetadataBytes)
	limits.TranscriptBytes = limits.PendingResultBytes
	limits.InvocationRecords = records.Invocations
	// Each cleanup adds two 1 MiB metadata allowances (scope and epoch). Its
	// <=16 KiB permit fits within these allowances without a second byte charge.
	limits.ScopeRecords = records.Scopes
	limits.SourceVersions = arithmetic.sum(2, arithmetic.product(int64(spec.MaxAttempts), arithmetic.sum(int64(len(spec.Acceptance)), 3)))
	limits.MinimumBytes = retainedHostBytes(runtime, limits, &arithmetic)
	if arithmetic.err != nil {
		return HostRetentionLimits{}, arithmetic.err
	}
	return limits, nil
}

func retainedHostBytes(runtime RuntimePolicy, limits HostRetentionLimits, arithmetic *hostRetentionArithmetic) int64 {
	// Each bounded source version retains archive/staging plus blob data. The
	// 128 KiB per entry conservatively covers two 64 KiB loose-object/tree/path
	// overhead allowances; raw bytes and USTAR bytes are accounted separately.
	// The transfer cap also bounds compression overhead for each individual blob.
	source := arithmetic.sum(arithmetic.product(2, limits.ArchiveBytes), arithmetic.product(2, runtime.SourceBytes),
		arithmetic.product(128<<10, int64(runtime.MaxFiles)), 1<<20)
	// Requests include their staging copy. Pending results include base64 output;
	// a separate encoded transcript is capped at the same bound, plus raw streams.
	invocation := arithmetic.sum(arithmetic.product(2, limits.RequestBytes), arithmetic.product(2, limits.PendingResultBytes), arithmetic.product(2, limits.HostOutputBytes))
	fixed := arithmetic.sum(arithmetic.product(3, limits.ProgramBytes), arithmetic.product(4, managedManifestMaxBytes),
		arithmetic.product(2, limits.RuntimeConfigBytes), ManagedStateStorageBytes, limits.LedgerBytes, 1<<20)
	return arithmetic.sum(fixed, arithmetic.product(limits.SourceVersions, source), arithmetic.product(limits.InvocationRecords, invocation),
		arithmetic.product(limits.ScopeRecords, limits.ScopeMetadataBytes))
}

type hostRetentionArithmetic struct{ err error }

func (arithmetic *hostRetentionArithmetic) sum(values ...int64) int64 {
	if arithmetic.err != nil {
		return 0
	}
	var total int64
	for _, value := range values {
		if value < 0 || value > math.MaxInt64-total {
			arithmetic.err = errors.New("managed retained-byte sum overflows")
			return 0
		}
		total += value
	}
	return total
}

func (arithmetic *hostRetentionArithmetic) product(left, right int64) int64 {
	if arithmetic.err != nil {
		return 0
	}
	if left < 0 || right < 0 || (right != 0 && left > math.MaxInt64/right) {
		arithmetic.err = errors.New("managed retained-byte product overflows")
		return 0
	}
	return left * right
}
