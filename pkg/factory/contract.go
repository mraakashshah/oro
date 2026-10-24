// Package factory implements an experimental autonomous delivery lifecycle for trusted command adapters.
package factory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Command identifies an admitted executable and its literal arguments.
type Command struct {
	Path string   `json:"path"`
	Args []string `json:"args"`
}

// Spec fixes an objective, its executable policies, and its lifetime budget.
type Spec struct {
	ID                 string   `json:"id"`
	Objective          string   `json:"objective"`
	Repository         string   `json:"repository"`
	TargetBranch       string   `json:"target_branch"`
	Worker             Command  `json:"worker"`
	Acceptance         Command  `json:"acceptance"`
	Review             Command  `json:"review"`
	RequiredChecks     []string `json:"required_checks"`
	MaxAttempts        int      `json:"max_attempts"`
	TimeoutSeconds     int      `json:"timeout_seconds"`
	StepTimeoutSeconds int      `json:"step_timeout_seconds"`
}

// Status describes durable objective progress or its terminal outcome.
type Status struct {
	ID           string `json:"id"`
	State        string `json:"state"`
	Attempts     int    `json:"attempts"`
	CandidateSHA string `json:"candidate_sha,omitempty"`
	TargetBranch string `json:"target_branch"`
	Reason       string `json:"reason,omitempty"`
}

type evidence struct {
	Stage        string
	CandidateSHA string
	BaseSHA      string
	PolicyHash   string
	OutputHash   string
	ExitCode     int
}

func validateSpec(spec Spec) error {
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`).MatchString(spec.ID) {
		return errors.New("invalid objective ID")
	}
	if strings.TrimSpace(spec.Objective) == "" || strings.TrimSpace(spec.TargetBranch) == "" {
		return errors.New("objective and target branch must be nonempty")
	}
	if !filepath.IsAbs(spec.Repository) {
		return errors.New("repository path must be absolute")
	}
	for name, command := range map[string]Command{"worker": spec.Worker, "acceptance": spec.Acceptance, "review": spec.Review} {
		if !filepath.IsAbs(command.Path) {
			return fmt.Errorf("%s program path must be absolute", name)
		}
	}
	if spec.MaxAttempts <= 0 || spec.TimeoutSeconds <= 0 || spec.StepTimeoutSeconds <= 0 ||
		spec.StepTimeoutSeconds > spec.TimeoutSeconds || int64(spec.TimeoutSeconds) > math.MaxInt64/int64(time.Second) {
		return errors.New("attempt and timeout limits must be positive, representable, and step timeout must not exceed total timeout")
	}
	if len(spec.RequiredChecks) == 0 {
		return errors.New("at least one required check must be specified")
	}
	seen := make(map[string]bool, len(spec.RequiredChecks))
	for _, check := range spec.RequiredChecks {
		if strings.TrimSpace(check) == "" || seen[check] {
			return errors.New("required checks must be nonempty and unique")
		}
		seen[check] = true
	}
	return nil
}

type policySet struct {
	Spec     Spec
	Hash     string
	Digests  map[string]string
	Programs map[string][]byte
	Dir      string
}

// loadPolicies leaves the logical spec unchanged: path spelling and argv are
// admitted identity. Resolved paths are used only for filesystem access and
// containment checks, so symlink aliases never weaken those checks.
func loadPolicies(stateDir string, spec Spec) (policySet, error) {
	if err := validateSpec(spec); err != nil {
		return policySet{}, err
	}
	root, err := filepath.EvalSymlinks(spec.Repository)
	if err != nil {
		return policySet{}, fmt.Errorf("resolve repository: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return policySet{}, errors.New("repository must be an existing directory")
	}
	state, err := resolveAncestors(stateDir)
	if err != nil {
		return policySet{}, err
	}
	if containedPath(root, state) {
		return policySet{}, errors.New("factory state must be outside the repository")
	}
	objectiveDir, err := resolveAncestors(filepath.Join(state, spec.ID))
	if err != nil {
		return policySet{}, err
	}
	if containedPath(root, objectiveDir) {
		return policySet{}, errors.New("objective state must be outside the repository")
	}
	p := policySet{Spec: spec, Dir: objectiveDir, Digests: make(map[string]string), Programs: make(map[string][]byte)}
	for stage, command := range specCommands(spec) {
		program, readErr := readExternalProgram(root, command.Path)
		if readErr != nil {
			return policySet{}, fmt.Errorf("%s policy: %w", stage, readErr)
		}
		p.Programs[stage], p.Digests[stage] = program, contentHash(program)
	}
	identity, err := json.Marshal(struct {
		Spec    Spec
		Digests map[string]string
	}{spec, p.Digests})
	if err != nil {
		return policySet{}, fmt.Errorf("encode policy identity: %w", err)
	}
	p.Hash = contentHash(identity)
	return p, nil
}

func specCommands(spec Spec) map[string]Command {
	return map[string]Command{"worker": spec.Worker, "acceptance": spec.Acceptance, "review": spec.Review}
}

func resolveAncestors(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("factory state path must be absolute")
	}
	ancestor := filepath.Clean(path)
	var missing []string
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("inspect state ancestor: %w", err)
		}
		missing = append(missing, filepath.Base(ancestor))
		ancestor = filepath.Dir(ancestor)
	}
	resolved, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", fmt.Errorf("resolve state ancestor: %w", err)
	}
	for i := len(missing) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, missing[i])
	}
	return resolved, nil
}

func containedPath(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func readExternalProgram(root, path string) ([]byte, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf("resolve program: %w", err)
	}
	if containedPath(root, resolved) {
		return nil, errors.New("policy program must be outside the repository")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("inspect program: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, errors.New("policy program must be a regular executable file")
	}
	program, err := os.ReadFile(resolved) //nolint:gosec // Explicit admitted external executable path.
	if err != nil {
		return nil, fmt.Errorf("read program: %w", err)
	}
	return program, nil
}

func contentHash(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func freezePolicies(p policySet) error {
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return fmt.Errorf("create policy directory: %w", err)
	}
	if err := freezeFile(filepath.Join(p.Dir, "policy.sha256"), []byte(p.Hash), 0o400); err != nil {
		return err
	}
	for stage, program := range p.Programs {
		if err := freezeFile(filepath.Join(p.Dir, stage), program, 0o500); err != nil {
			return err
		}
	}
	return verifyPolicies(p)
}

// freezeFile publishes a complete file without replacing any existing identity.
func freezeFile(path string, data []byte, mode os.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".policy-")
	if err != nil {
		return fmt.Errorf("create frozen policy: %w", err)
	}
	defer os.Remove(temp.Name()) //nolint:errcheck // Removing an unpublished temporary file cannot affect admitted policy.
	defer temp.Close()
	if _, err := temp.Write(data); err != nil {
		return fmt.Errorf("write frozen policy: %w", err)
	}
	if err := temp.Chmod(mode); err != nil {
		return fmt.Errorf("protect frozen policy: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("sync frozen policy: %w", err)
	}
	if err := os.Link(temp.Name(), path); err != nil && !os.IsExist(err) {
		return fmt.Errorf("publish frozen policy: %w", err)
	}
	return verifyFile(path, contentHash(data), mode&0o111 != 0)
}

func verifyPolicies(p policySet) error {
	if err := verifyFile(filepath.Join(p.Dir, "policy.sha256"), contentHash([]byte(p.Hash)), false); err != nil {
		return err
	}
	for stage, digest := range p.Digests {
		if err := verifyFile(filepath.Join(p.Dir, stage), digest, true); err != nil {
			return err
		}
	}
	return nil
}

func verifyFile(path, digest string, executable bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect frozen policy: %w", err)
	}
	if !info.Mode().IsRegular() || (executable && info.Mode().Perm()&0o111 == 0) {
		return errors.New("frozen policy must remain a regular file with its admitted execution permission")
	}
	data, err := os.ReadFile(path) //nolint:gosec // File is in the explicit factory state directory.
	if err != nil {
		return fmt.Errorf("read frozen policy: %w", err)
	}
	if contentHash(data) != digest {
		return errors.New("frozen policy identity mismatch")
	}
	return nil
}

func (p policySet) command(stage string) Command {
	command := specCommands(p.Spec)[stage]
	command.Path = filepath.Join(p.Dir, stage)
	command.Args = append([]string(nil), command.Args...)
	return command
}
