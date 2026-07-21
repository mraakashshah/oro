package storage

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// StoragePolicy supplies cache sharing identities and explicit cleanup authority.
// Cache resolution never infers deletion authority from paths found in an environment.
type StoragePolicy struct { //nolint:revive // name is fixed by the public acceptance contract
	Providers          []CacheProvider
	ProjectID          string
	RepositoryRoot     string
	DeletionAuthorized bool
}

// ResolvedCacheEnv is a normalized subprocess environment and any unsafe
// unknown cache paths observed without changing them.
type ResolvedCacheEnv struct {
	Env      []string
	Findings []CacheEnvFinding
}

// CacheEnvFinding identifies an unknown cache-like path inside a managed boundary.
type CacheEnvFinding struct {
	Variable string
	Path     string
}

// ResolveCacheEnv resolves registered cache variables to external, scope-safe
// locations while preserving unknown environment state unchanged.
func ResolveCacheEnv(env []string, workdir string, policy StoragePolicy) (ResolvedCacheEnv, error) {
	providers, err := cacheProviders(policy)
	if err != nil {
		return ResolvedCacheEnv{}, err
	}
	boundaries, err := cacheBoundaries(env, workdir)
	if err != nil {
		return ResolvedCacheEnv{}, err
	}
	values, order := splitCacheEnv(env)
	registered := registeredVariables(providers)
	findings := unknownInternalFindings(values, registered, boundaries)
	for variable, provider := range registered {
		value := values[variable]
		if value != "" && filepath.IsAbs(value) && !insideAny(value, boundaries) {
			continue
		}
		path, resolveErr := scopedCachePath(provider, policy)
		if resolveErr != nil {
			return ResolvedCacheEnv{}, fmt.Errorf("resolve %s: %w", variable, resolveErr)
		}
		values[variable] = path
		if !contains(order, variable) {
			order = append(order, variable)
		}
	}
	return ResolvedCacheEnv{Env: joinCacheEnv(values, order), Findings: findings}, nil
}

func cacheProviders(policy StoragePolicy) ([]CacheProvider, error) {
	providers := policy.Providers
	if len(providers) == 0 {
		providers = BuiltinProviders()
	}
	for _, provider := range providers {
		if err := provider.Validate(); err != nil {
			return nil, fmt.Errorf("validate cache provider %q: %w", provider.ID, err)
		}
	}
	return providers, nil
}

func cacheBoundaries(env []string, workdir string) ([]string, error) {
	boundaries := make([]string, 0, 3)
	if workdir != "" {
		canonical, err := canonicalCachePath(workdir)
		if err != nil {
			return nil, fmt.Errorf("resolve workdir %q: %w", workdir, err)
		}
		boundaries = append(boundaries, canonical)
	}
	values, _ := splitCacheEnv(env)
	if root := values["ORO_SUBPROCESS_TMP_ROOT"]; root != "" {
		canonical, err := canonicalCachePath(root)
		if err != nil {
			return nil, fmt.Errorf("resolve subprocess temp root %q: %w", root, err)
		}
		boundaries = append(boundaries, canonical)
	}
	return boundaries, nil
}

func splitCacheEnv(env []string) (values map[string]string, order []string) {
	values = make(map[string]string, len(env))
	order = make([]string, 0, len(env))
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if !contains(order, key) {
			order = append(order, key)
		}
		values[key] = value
	}
	return values, order
}

func joinCacheEnv(values map[string]string, order []string) []string {
	env := make([]string, 0, len(order))
	for _, key := range order {
		env = append(env, key+"="+values[key])
	}
	return env
}

func registeredVariables(providers []CacheProvider) map[string]CacheProvider {
	registered := make(map[string]CacheProvider)
	for _, provider := range providers {
		for _, variable := range provider.Variables {
			if _, exists := registered[variable]; !exists {
				registered[variable] = provider
			}
		}
	}
	return registered
}

func unknownInternalFindings(values map[string]string, registered map[string]CacheProvider, boundaries []string) []CacheEnvFinding {
	findings := make([]CacheEnvFinding, 0)
	for variable, path := range values {
		if _, known := registered[variable]; known || path == "" || !filepath.IsAbs(path) || !cacheLikeVariable(variable) {
			continue
		}
		if insideAny(path, boundaries) {
			findings = append(findings, CacheEnvFinding{Variable: variable, Path: path})
		}
	}
	return findings
}

func cacheLikeVariable(variable string) bool {
	return strings.Contains(variable, "CACHE")
}

func scopedCachePath(provider CacheProvider, policy StoragePolicy) (string, error) {
	base := provider.DefaultPath()
	if !filepath.IsAbs(base) {
		return "", fmt.Errorf("provider default path %q is not absolute", base)
	}
	switch provider.Scope {
	case UserScope:
		return base, nil
	case ProjectScope:
		return filepath.Join(base, "project", scopeToken(policy.ProjectID)), nil
	case RepositoryScope:
		identity := policy.RepositoryRoot
		if identity == "" {
			return "", fmt.Errorf("repository scope requires repository root")
		}
		canonical, err := canonicalCachePath(identity)
		if err != nil {
			return "", fmt.Errorf("resolve repository root %q: %w", identity, err)
		}
		return filepath.Join(base, "repository", scopeToken(canonical)), nil
	default:
		return "", fmt.Errorf("unsupported cache scope %q", provider.Scope)
	}
}

func scopeToken(identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("%x", sum[:8])
}

func insideAny(path string, boundaries []string) bool {
	canonical, err := canonicalCachePath(path)
	if err != nil {
		return false
	}
	for _, boundary := range boundaries {
		if pathInside(boundary, canonical) {
			return true
		}
	}
	return false
}

func canonicalCachePath(path string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("make absolute: %w", err)
	}
	for existing := abs; ; existing = filepath.Dir(existing) {
		if _, statErr := os.Lstat(existing); statErr == nil {
			resolved, resolveErr := filepath.EvalSymlinks(existing)
			if resolveErr != nil {
				return "", fmt.Errorf("resolve symlinks: %w", resolveErr)
			}
			rel, relErr := filepath.Rel(existing, abs)
			if relErr != nil {
				return "", fmt.Errorf("resolve suffix: %w", relErr)
			}
			return filepath.Join(resolved, rel), nil
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return abs, nil
		}
	}
}

func pathInside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
