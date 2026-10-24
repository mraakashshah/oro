package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RecoveryPolicy bounds all incarnations, cleanup and read-only reconciliation.
type RecoveryPolicy struct {
	MaxControllerStarts          int `json:"max_controller_starts"`
	MaxSupervisorStarts          int `json:"max_supervisor_starts"`
	RestartDelaySeconds          int `json:"restart_delay_seconds"`
	CleanupTimeoutSeconds        int `json:"cleanup_timeout_seconds"`
	ReconciliationSeconds        int `json:"reconciliation_seconds"`
	MaxReconciliationInvocations int `json:"max_reconciliation_invocations"`
	MaxCleanupInvocations        int `json:"max_cleanup_invocations"`
}

// ManagedCleanupPermitBytes caps the canonical target-bound cleanup permit.
const ManagedCleanupPermitBytes = 16 << 10

type managedRecordBounds struct {
	Epochs, Invocations, Scopes, Events int64
	CleanupPermits, CleanupPermitBytes  int64
}

// managedRecordLimits shares checked journal and retention counts. Each cleanup
// helper has one invocation, one scope, one epoch and one bounded permit. The
// conservative scope allowance also covers supervisor/controller reservations.
func managedRecordLimits(recovery RecoveryPolicy, runtime RuntimePolicy) (managedRecordBounds, error) {
	for _, count := range []int{recovery.MaxSupervisorStarts, recovery.MaxControllerStarts, recovery.MaxReconciliationInvocations,
		recovery.MaxCleanupInvocations, runtime.MaxHostInvocations, runtime.MaxModelCalls, runtime.MaxToolCalls} {
		if count <= 0 {
			return managedRecordBounds{}, errors.New("managed record limits must be positive")
		}
	}
	var arithmetic hostRetentionArithmetic
	bounds := managedRecordBounds{CleanupPermits: int64(recovery.MaxCleanupInvocations)}
	bounds.Epochs = arithmetic.sum(int64(recovery.MaxSupervisorStarts), int64(recovery.MaxControllerStarts),
		int64(recovery.MaxReconciliationInvocations), bounds.CleanupPermits)
	work := arithmetic.sum(int64(runtime.MaxHostInvocations), int64(runtime.MaxModelCalls), int64(runtime.MaxToolCalls))
	bounds.Invocations = arithmetic.sum(work, int64(recovery.MaxReconciliationInvocations), bounds.CleanupPermits)
	bounds.Scopes = arithmetic.sum(arithmetic.product(2, bounds.Epochs), work)
	bounds.Events = arithmetic.sum(arithmetic.product(8, bounds.Scopes), 16)
	bounds.CleanupPermitBytes = arithmetic.product(bounds.CleanupPermits, ManagedCleanupPermitBytes)
	if arithmetic.err != nil {
		return managedRecordBounds{}, fmt.Errorf("managed record limits: %w", arithmetic.err)
	}
	return bounds, nil
}

// RuntimePolicy freezes execution identities and finite resource allowances.
type RuntimePolicy struct {
	DockerContext         string  `json:"docker_context"`
	ImageDigest           string  `json:"image_digest"`
	MemoryBytes           int64   `json:"memory_bytes"`
	ScratchBytes          int64   `json:"scratch_bytes"`
	SourceBytes           int64   `json:"source_bytes"`
	HostRequiredBytes     int64   `json:"host_required_bytes"`
	HostReserveBytes      int64   `json:"host_reserve_bytes"`
	CPUs                  float64 `json:"cpus"`
	PIDs                  int     `json:"pids"`
	MaxFiles              int     `json:"max_files"`
	SourceInodes          int     `json:"source_inodes"`
	ScratchInodes         int     `json:"scratch_inodes"`
	MaxModelCalls         int     `json:"max_model_calls"`
	MaxModelTurns         int     `json:"max_model_turns"`
	MaxToolCalls          int     `json:"max_tool_calls"`
	MaxHostInvocations    int     `json:"max_host_invocations"`
	Model                 string  `json:"model"`
	ModelExecutable       string  `json:"model_executable"`
	GitExecutable         string  `json:"git_executable"`
	MaxModelInvocationUSD string  `json:"max_model_invocation_usd"`
}

// Check describes a literal container command and the trusted host comparison.
type Check struct {
	ID             string   `json:"id"`
	Args           []string `json:"args"`
	Stdin          string   `json:"stdin"`
	ExpectedStdout string   `json:"expected_stdout"`
	ExpectedExit   int      `json:"expected_exit"`
}

// ManagedSpec is the immutable managed objective contract, separate from Spec.
type ManagedSpec struct {
	ID                 string         `json:"id"`
	Objective          string         `json:"objective"`
	Repository         string         `json:"repository"`
	TargetBranch       string         `json:"target_branch"`
	RequiredChecks     []string       `json:"required_checks"`
	MaxAttempts        int            `json:"max_attempts"`
	TimeoutSeconds     int            `json:"timeout_seconds"`
	StepTimeoutSeconds int            `json:"step_timeout_seconds"`
	Recovery           RecoveryPolicy `json:"recovery"`
	Runtime            RuntimePolicy  `json:"runtime"`
	Acceptance         []Check        `json:"acceptance"`
	ReviewInstructions string         `json:"review_instructions"`
}

// HostReservation identifies a durable allocation in one canonical state root.
type HostReservation struct {
	ID          string `json:"id"`
	StateRoot   string `json:"state_root"`
	ObjectiveID string `json:"objective_id"`
	Bytes       int64  `json:"bytes"`
}

// ScopeHandle identifies one journaled execution scope and ownership epoch.
type ScopeHandle struct {
	ID         string `json:"id"`
	Nonce      string `json:"nonce"`
	Kind       string `json:"kind"`
	Backend    string `json:"backend"`
	Name       string `json:"name"`
	Generation uint64 `json:"generation"`
}

// HostInvocation supplies an admitted command to the trusted host runtime.
type HostInvocation struct {
	Scope       ScopeHandle `json:"scope"`
	Path        string      `json:"path"`
	Args        []string    `json:"args"`
	Env         []string    `json:"env"`
	Dir         string      `json:"dir"`
	Stdin       []byte      `json:"stdin"`
	Deadline    time.Time   `json:"deadline"`
	OutputLimit int64       `json:"output_limit"`
}

// HostResult is bounded command output consumed only after scope quiescence.
type HostResult struct {
	Stdout   []byte `json:"stdout"`
	Stderr   []byte `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// HostRuntime owns bounded execution; success requires exact-scope quiescence.
type HostRuntime interface {
	Invoke(context.Context, HostInvocation) (HostResult, error)
	Retire(context.Context, ScopeHandle) error
}

// ScopeAuthority binds authenticated handshakes to one admitted objective.
type ScopeAuthority interface {
	RecordHello(context.Context, ScopeHandle, int, int) error
	Granted(context.Context, ScopeHandle) (bool, error)
	CleanupPermit(context.Context, ScopeHandle) (DockerCleanupPermit, error)
	Observe(context.Context, ScopeHandle) (ScopeObservation, error)
	MarkQuiescent(context.Context, ScopeHandle) error
	Close() error
}

// DockerCleanupOp is one fixed inspection or retirement operation.
type DockerCleanupOp string

// Supported cleanup operations never create or execute candidate resources.
const (
	DockerCleanupInspect DockerCleanupOp = "inspect"
	DockerCleanupStop    DockerCleanupOp = "stop"
	DockerCleanupRemove  DockerCleanupOp = "remove"
)

// DockerCleanupBinding is the trusted storage snapshot of one exact object.
// It selects no host executable, path, endpoint, environment or command flags.
type DockerCleanupBinding struct {
	Kind          string            `json:"kind"`
	Context       string            `json:"context"`
	Name          string            `json:"name"`
	ContainerID   string            `json:"container_id"`
	CreatedAt     string            `json:"created_at"`
	ImageID       string            `json:"image_id"`
	VolumeDriver  string            `json:"volume_driver"`
	Labels        map[string]string `json:"labels"`
	VolumeOptions map[string]string `json:"volume_options"`
}

// DockerCleanupPermit binds one counted helper invocation to one owned target.
type DockerCleanupPermit struct {
	Scope     ScopeHandle          `json:"scope"`
	Target    ScopeHandle          `json:"target"`
	Operation DockerCleanupOp      `json:"operation"`
	Binding   DockerCleanupBinding `json:"binding"`
	Deadline  time.Time            `json:"deadline"`
}

// ScopeObservation exposes persisted ownership for exact-scope retirement.
// Reading a revoked epoch never authorizes execution in that epoch.
type ScopeObservation struct {
	Phase    string
	PID      int
	PGID     int
	Deadline time.Time
}

// SupervisorBackend owns controllers, reconciliation, and durable release.
type SupervisorBackend interface {
	HostRuntime
	ReleaseHostReservation(context.Context, string) error
	StartController(context.Context, ScopeHandle) error
	Retire(context.Context, ScopeHandle) error
	Quiescent(context.Context, ScopeHandle) (bool, error)
}

// managedSpecMaxBytes bounds the complete frozen spec artifact, including check
// stdin/expected-output literals and leading/trailing whitespace. One MiB leaves
// room for ordinary policy/check fixtures without borrowing source/archive or
// command-output budgets. Larger specifications must be reduced before admission.
const managedSpecMaxBytes = 1 << 20

// decodeManagedSpec bounds the entire input, then rejects aliases, duplicate
// keys, unknown fields and trailing JSON. It performs no admission side effects.
func decodeManagedSpec(reader io.Reader) (ManagedSpec, error) {
	var spec ManagedSpec
	if reader == nil {
		return spec, errors.New("managed specification reader must not be nil")
	}
	data, err := io.ReadAll(io.LimitReader(reader, managedSpecMaxBytes+1))
	if len(data) > managedSpecMaxBytes {
		return spec, fmt.Errorf("managed specification exceeds %d-byte limit", managedSpecMaxBytes)
	}
	if err != nil {
		return spec, fmt.Errorf("read managed specification: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return spec, fmt.Errorf("decode managed specification: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return spec, errors.New("managed specification must contain exactly one JSON value")
	}
	if err := checkManagedJSON(json.NewDecoder(bytes.NewReader(raw)), reflect.TypeOf(spec)); err != nil {
		return spec, err
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		return ManagedSpec{}, fmt.Errorf("decode managed fields: %w", err)
	}
	if err := validateManagedSpec(spec); err != nil {
		return ManagedSpec{}, err
	}
	return spec, nil
}

func checkManagedJSON(decoder *json.Decoder, kind reflect.Type) error {
	if kind.Kind() != reflect.Struct && kind.Kind() != reflect.Slice {
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return fmt.Errorf("decode managed JSON value: %w", err)
		}
		if bytes.Equal(value, []byte("null")) {
			return errors.New("managed JSON fields must not be null")
		}
		return nil
	}
	opening := json.Delim('{')
	if kind.Kind() == reflect.Slice {
		opening = '['
	}
	token, err := decoder.Token()
	if err != nil || token != opening {
		return fmt.Errorf("managed JSON must contain %s", kind.Kind())
	}
	if err := checkManagedJSONMembers(decoder, kind); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("close managed JSON value: %w", err)
	}
	return nil
}

func checkManagedJSONMembers(decoder *json.Decoder, kind reflect.Type) error {
	seen := make(map[string]bool)
	fields := make(map[string]reflect.Type)
	if kind.Kind() == reflect.Struct {
		for i := range kind.NumField() {
			field := kind.Field(i)
			fields[field.Tag.Get("json")] = field.Type
		}
	}
	for decoder.More() {
		if kind.Kind() == reflect.Slice {
			if err := checkManagedJSON(decoder, kind.Elem()); err != nil {
				return err
			}
			continue
		}
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("read managed JSON key: %w", err)
		}
		key, ok := token.(string)
		field, exists := fields[key]
		if !ok || !exists || seen[key] {
			return fmt.Errorf("unknown or duplicate managed JSON field %q", token)
		}
		seen[key] = true
		if err := checkManagedJSON(decoder, field); err != nil {
			return err
		}
	}
	return nil
}

func validateManagedSpec(spec ManagedSpec) error {
	if err := validateManagedArtifactSize(spec); err != nil {
		return err
	}
	if !managedIdentifier(spec.ID) || strings.TrimSpace(spec.Objective) == "" || strings.TrimSpace(spec.ReviewInstructions) == "" {
		return errors.New("managed objective ID, objective and review instructions are required")
	}
	if !managedAbsolutePath(spec.Repository) || !managedBranch(spec.TargetBranch) {
		return errors.New("managed repository or target branch identity is invalid")
	}
	if err := validateManagedRecovery(spec); err != nil {
		return err
	}
	if err := validateManagedRuntime(spec); err != nil {
		return err
	}
	if _, err := managedRecordLimits(spec.Recovery, spec.Runtime); err != nil {
		return err
	}
	return validateManagedChecks(spec.RequiredChecks, spec.Acceptance)
}

func validateManagedArtifactSize(spec ManagedSpec) error {
	if managedJSONBudget(reflect.ValueOf(spec), managedSpecMaxBytes) < 0 {
		return fmt.Errorf("managed specification exceeds %d-byte limit", managedSpecMaxBytes)
	}
	// The lower bound caps traversal and raw string bytes first. JSON escaping
	// can expand strings by at most sixfold, so this allocation is bounded too.
	encoded, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("encode managed specification: %w", err)
	}
	if len(encoded) > managedSpecMaxBytes {
		return fmt.Errorf("managed specification exceeds %d-byte limit", managedSpecMaxBytes)
	}
	return nil
}

// managedJSONBudget subtracts a lower bound for JSON size without allocating or
// walking beyond the budget. Scalars need at least one byte; strings, field
// names, separators and containers contribute their unescaped encoded sizes.
func managedJSONBudget(value reflect.Value, remaining int) int {
	if remaining < 0 {
		return -1
	}
	switch value.Kind() {
	case reflect.String:
		if value.Len() > remaining-2 {
			return -1
		}
		remaining -= value.Len() + 2
	case reflect.Slice:
		remaining -= 2
		for i := 0; i < value.Len() && remaining >= 0; i++ {
			if i > 0 {
				remaining--
			}
			remaining = managedJSONBudget(value.Index(i), remaining)
		}
	case reflect.Struct:
		remaining -= 2
		for i := 0; i < value.NumField() && remaining >= 0; i++ {
			remaining -= len(value.Type().Field(i).Tag.Get("json")) + 3
			if i > 0 {
				remaining--
			}
			remaining = managedJSONBudget(value.Field(i), remaining)
		}
	default:
		remaining--
	}
	return remaining
}

func managedIdentifier(value string) bool {
	return regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`).MatchString(value)
}

func managedAbsolutePath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && filepath.Dir(path) != path && !strings.ContainsRune(path, 0)
}

func managedBranch(branch string) bool {
	if branch == "" || branch == "@" || branch == "HEAD" || strings.HasPrefix(branch, "-") ||
		strings.ContainsAny(branch, "~^:?*[\\") || strings.Contains(branch, "..") || strings.Contains(branch, "@{") || strings.HasSuffix(branch, ".") ||
		regexp.MustCompile(`[\x00-\x20\x7f]`).MatchString(branch) {
		return false
	}
	for _, component := range strings.Split(branch, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

func validateManagedRecovery(spec ManagedSpec) error {
	r := spec.Recovery
	counts := []int{spec.MaxAttempts, r.MaxControllerStarts, r.MaxSupervisorStarts, r.MaxReconciliationInvocations, r.MaxCleanupInvocations}
	for _, count := range counts {
		if count <= 0 {
			return errors.New("managed attempt, start, reconciliation and cleanup limits must be positive")
		}
	}
	seconds := []int{spec.TimeoutSeconds, spec.StepTimeoutSeconds, r.RestartDelaySeconds, r.CleanupTimeoutSeconds, r.ReconciliationSeconds}
	for _, duration := range seconds {
		if duration <= 0 || int64(duration) > math.MaxInt64/int64(time.Second) {
			return errors.New("managed durations must be positive and representable")
		}
	}
	hardStop := int64(spec.TimeoutSeconds) + int64(r.CleanupTimeoutSeconds) + int64(r.ReconciliationSeconds)
	if spec.StepTimeoutSeconds > spec.TimeoutSeconds || hardStop > math.MaxInt64/int64(time.Second) {
		return errors.New("managed step or hard-stop duration exceeds its representable bound")
	}
	return nil
}

func validateManagedRuntime(spec ManagedSpec) error {
	r := spec.Runtime
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`).MatchString(r.DockerContext) ||
		!regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:/-]*$`).MatchString(r.Model) || !managedImageIdentity(r.ImageDigest) {
		return errors.New("managed context, model or immutable image identity is invalid")
	}
	for _, executable := range []string{r.ModelExecutable, r.GitExecutable} {
		if !managedAbsolutePath(executable) || containedPath(spec.Repository, executable) {
			return errors.New("managed executable must be an absolute path outside source")
		}
	}
	if err := validateManagedResources(r); err != nil {
		return err
	}
	return validateManagedProducts(spec)
}

func managedImageIdentity(image string) bool {
	digest := regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	if digest.MatchString(image) {
		return true
	}
	repository, hash, found := strings.Cut(image, "@")
	if !found || !digest.MatchString(hash) {
		return false
	}
	parts := strings.Split(repository, "/")
	component := regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
	for i, part := range parts {
		if i == 0 && len(parts) > 1 && strings.Contains(part, ":") {
			if !regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*:\d+$`).MatchString(part) {
				return false
			}
			continue
		}
		if !component.MatchString(part) {
			return false
		}
	}
	return true
}

func validateManagedResources(r RuntimePolicy) error {
	values := []int64{
		r.MemoryBytes, r.SourceBytes, r.ScratchBytes, r.HostRequiredBytes, r.HostReserveBytes,
		int64(r.PIDs), int64(r.MaxFiles), int64(r.SourceInodes), int64(r.ScratchInodes),
		int64(r.MaxModelCalls), int64(r.MaxModelTurns), int64(r.MaxToolCalls), int64(r.MaxHostInvocations),
	}
	for _, value := range values {
		if value <= 0 {
			return errors.New("managed runtime resource limits must be positive")
		}
	}
	if math.IsNaN(r.CPUs) || math.IsInf(r.CPUs, 0) || r.CPUs < 1e-9 || r.CPUs >= float64(math.MaxInt64)/1e9 {
		return errors.New("managed CPU limit must be finite and representable in nano CPUs")
	}
	if r.SourceInodes < r.MaxFiles {
		return errors.New("managed source inode budget cannot be below the file limit")
	}
	if err := managedBudgetSum(r.MemoryBytes, r.SourceBytes, r.ScratchBytes); err != nil {
		return err
	}
	return managedBudgetSum(r.HostRequiredBytes, r.HostReserveBytes)
}

func validateManagedProducts(spec ManagedSpec) error {
	r := spec.Runtime
	cents, err := managedInvocationCents(r.MaxModelInvocationUSD)
	if err != nil {
		return err
	}
	for _, pair := range [][2]int64{
		{cents, int64(r.MaxModelCalls)},
		{r.SourceBytes, int64(spec.MaxAttempts)},
		{int64(r.MaxModelCalls), int64(r.MaxModelTurns)},
	} {
		if pair[0] > math.MaxInt64/pair[1] {
			return errors.New("managed aggregate resource budget overflows")
		}
	}
	return managedBudgetSum(r.SourceBytes*int64(spec.MaxAttempts), r.SourceBytes)
}

func managedBudgetSum(values ...int64) error {
	var total int64
	for _, value := range values {
		if value > math.MaxInt64-total {
			return errors.New("managed aggregate resource budget overflows")
		}
		total += value
	}
	return nil
}

func managedInvocationCents(value string) (int64, error) {
	if !regexp.MustCompile(`^(0|[1-9]\d*)(\.\d{1,2})?$`).MatchString(value) {
		return 0, errors.New("managed model cost must be a positive decimal with at most two fractional digits")
	}
	whole, fraction, _ := strings.Cut(value, ".")
	fraction += strings.Repeat("0", 2-len(fraction))
	cents, err := strconv.ParseInt(whole+fraction, 10, 64)
	if err != nil || cents <= 0 {
		return 0, errors.New("managed model cost must be positive and representable in integer cents")
	}
	return cents, nil
}

func validateManagedChecks(required []string, checks []Check) error {
	if len(required) == 0 || len(required) != len(checks) {
		return errors.New("managed acceptance must cover every required check exactly once")
	}
	remaining := make(map[string]bool, len(required))
	for _, id := range required {
		if !managedIdentifier(id) || remaining[id] {
			return errors.New("managed required check IDs must be valid and unique")
		}
		remaining[id] = true
	}
	for _, check := range checks {
		if !remaining[check.ID] || len(check.Args) == 0 || strings.TrimSpace(check.Args[0]) == "" || check.ExpectedExit < 0 || check.ExpectedExit > 255 {
			return errors.New("managed acceptance check has invalid ID, command or exit status")
		}
		for _, arg := range check.Args {
			if strings.ContainsRune(arg, 0) {
				return errors.New("managed acceptance argv cannot contain NUL")
			}
		}
		delete(remaining, check.ID)
	}
	return nil
}

// managedPolicyHash binds the complete logical contract and both frozen program
// digests. Admission supplies canonical repository and executable identities;
// this pure helper does not read mutable filesystem paths or normalize policy.
func managedPolicyHash(spec ManagedSpec, executableDigests map[string]string) (string, error) {
	if len(executableDigests) != 2 {
		return "", errors.New("managed policy requires model and git executable digests")
	}
	for _, name := range []string{"model", "git"} {
		if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(executableDigests[name]) {
			return "", fmt.Errorf("managed %s executable digest is invalid", name)
		}
	}
	encoded, err := json.Marshal(struct {
		Spec    ManagedSpec       `json:"spec"`
		Digests map[string]string `json:"executable_digests"`
	}{spec, executableDigests})
	if err != nil {
		return "", fmt.Errorf("encode managed policy identity: %w", err)
	}
	return contentHash(encoded), nil
}

func validateHostReservation(reservation HostReservation, spec ManagedSpec, stateRoot string) error {
	if !managedIdentifier(reservation.ID) || !managedAbsolutePath(stateRoot) || reservation.StateRoot != stateRoot ||
		reservation.ObjectiveID != spec.ID || reservation.Bytes <= 0 || reservation.Bytes != spec.Runtime.HostRequiredBytes {
		return errors.New("host reservation does not match admitted root, objective and byte budget")
	}
	return nil
}
