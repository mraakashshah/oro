package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

const tombstoneDirectory = ".tombstones"

var namespaceTokenPattern = regexp.MustCompile(`^[a-f0-9]{32,64}$`)

// RetirementReason identifies the lifecycle event that permits scratch cleanup.
//
//oro:testonly — dispatcher and standalone lifecycle wiring lands in a dependent task.
type RetirementReason string

const (
	// RetirementPostMerge retires scratch after a successful merge.
	RetirementPostMerge RetirementReason = "postmerge"
	// RetirementNonOperative retires scratch after a safe no-op completion.
	RetirementNonOperative RetirementReason = "nonoperative"
)

// NamespaceRetirer asynchronously removes completed runtime namespaces.
//
//oro:testonly — dispatcher and standalone lifecycle wiring lands in a dependent task.
type NamespaceRetirer struct {
	catalog      *Catalog
	root         string
	pollInterval time.Duration
	mu           sync.Mutex
	jobs         map[string]*retirementJob
}

type retirementJob struct {
	done chan struct{}
	err  error
}

// NewNamespaceRetirer creates a lease-aware namespace retirement worker.
//
//oro:testonly — dispatcher and standalone lifecycle wiring lands in a dependent task.
func NewNamespaceRetirer(catalog *Catalog, root string) *NamespaceRetirer {
	return &NamespaceRetirer{
		catalog:      catalog,
		root:         root,
		pollInterval: time.Second,
		jobs:         make(map[string]*retirementJob),
	}
}

// Retire records retirement and returns without waiting for active leases.
//
//oro:testonly — dispatcher and standalone lifecycle wiring lands in a dependent task.
func (r *NamespaceRetirer) Retire(ctx context.Context, namespace string, reason RetirementReason) error {
	if err := r.validate(namespace, reason); err != nil {
		return err
	}
	owned, err := r.catalogOwnsNamespace(ctx, namespace)
	if err != nil {
		return err
	}
	if !owned {
		return fmt.Errorf("namespace %s is not catalog-owned", namespace)
	}
	if err := r.catalog.UpsertTombstone(ctx, Tombstone{
		ID:        namespace,
		Namespace: namespace,
		Reason:    string(reason),
		State:     "retiring",
		RetiredAt: time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("record namespace retirement %s: %w", namespace, err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.jobs[namespace]; exists {
		return nil
	}
	job := &retirementJob{done: make(chan struct{})}
	r.jobs[namespace] = job
	go r.run(namespace, reason, job)
	return nil
}

// Wait blocks until every retirement scheduled before the call completes.
//
//oro:testonly — dispatcher and standalone lifecycle wiring lands in a dependent task.
func (r *NamespaceRetirer) Wait(ctx context.Context) error {
	r.mu.Lock()
	jobs := make([]*retirementJob, 0, len(r.jobs))
	for _, job := range r.jobs {
		jobs = append(jobs, job)
	}
	r.mu.Unlock()

	for _, job := range jobs {
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for namespace retirement: %w", ctx.Err())
		case <-job.done:
			if job.err != nil {
				return job.err
			}
		}
	}
	return nil
}

func (r *NamespaceRetirer) run(namespace string, reason RetirementReason, job *retirementJob) {
	job.err = r.retire(namespace, reason)
	r.mu.Lock()
	delete(r.jobs, namespace)
	r.mu.Unlock()
	close(job.done)
}

func (r *NamespaceRetirer) retire(namespace string, reason RetirementReason) error {
	for {
		active, err := r.activeLease(namespace)
		if err != nil {
			return err
		}
		if !active {
			break
		}
		time.Sleep(r.pollInterval)
	}

	tombstone, present, err := r.tombstone(namespace, reason)
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	evidence, err := r.deleteTombstone(tombstone)
	if err != nil {
		return r.recordState(namespace, reason, "tombstoned", err)
	}
	if err := r.catalog.recordTombstoneDeletion(context.Background(), namespace, evidence.beforeBytes, evidence.afterBytes); err != nil {
		return r.recordState(namespace, reason, "tombstoned", err)
	}
	return r.recordState(namespace, reason, "deleted", nil)
}

func (r *NamespaceRetirer) activeLease(namespace string) (bool, error) {
	var active bool
	err := r.catalog.db.QueryRowContext(context.Background(), `SELECT EXISTS(SELECT 1 FROM runtime_leases WHERE namespace=? AND released_at IS NULL)`, namespace).Scan(&active)
	if err != nil {
		return false, fmt.Errorf("check active lease for namespace %s: %w", namespace, err)
	}
	return active, nil
}

func (r *NamespaceRetirer) catalogOwnsNamespace(ctx context.Context, namespace string) (bool, error) {
	var owned bool
	err := r.catalog.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_leases WHERE namespace=?)`, namespace).Scan(&owned)
	if err != nil {
		return false, fmt.Errorf("check namespace ownership for %s: %w", namespace, err)
	}
	return owned, nil
}

func (r *NamespaceRetirer) tombstone(namespace string, reason RetirementReason) (tombstone string, present bool, err error) {
	if _, err := safeDirectory(r.root); err != nil {
		return "", false, fmt.Errorf("validate scratch root: %w", err)
	}
	source := filepath.Join(r.root, namespace)
	info, err := os.Lstat(source)
	if errors.Is(err, os.ErrNotExist) {
		tombstone = filepath.Join(r.root, tombstoneDirectory, namespace)
		tombstoneInfo, tombstoneErr := os.Lstat(tombstone)
		if errors.Is(tombstoneErr, os.ErrNotExist) {
			return "", false, r.recordState(namespace, reason, "deleted", nil)
		}
		if tombstoneErr != nil {
			return "", false, fmt.Errorf("inspect tombstone %s: %w", namespace, tombstoneErr)
		}
		if !tombstoneInfo.IsDir() || tombstoneInfo.Mode()&os.ModeSymlink != 0 {
			return "", false, fmt.Errorf("unsafe tombstone %s", namespace)
		}
		return tombstone, true, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("inspect namespace %s: %w", namespace, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", false, fmt.Errorf("unsafe namespace %s", namespace)
	}

	directory := filepath.Join(r.root, tombstoneDirectory)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", false, fmt.Errorf("create tombstone directory: %w", err)
	}
	if _, err := safeDirectory(directory); err != nil {
		return "", false, fmt.Errorf("validate tombstone directory: %w", err)
	}
	tombstone = filepath.Join(directory, namespace)
	if err := r.recordState(namespace, reason, "tombstoning", nil); err != nil {
		return "", false, err
	}
	if err := os.Rename(source, tombstone); err != nil {
		return "", false, fmt.Errorf("tombstone namespace %s: %w", namespace, err)
	}
	if err := r.recordState(namespace, reason, "tombstoned", nil); err != nil {
		return "", false, err
	}
	return tombstone, true, nil
}

func (r *NamespaceRetirer) recordState(namespace string, reason RetirementReason, state string, cause error) error {
	tombstone := Tombstone{
		ID:        namespace,
		Namespace: namespace,
		Reason:    string(reason),
		State:     state,
		RetiredAt: time.Now().UTC(),
	}
	if cause != nil {
		retryAt := time.Now().UTC().Add(time.Minute)
		tombstone.RetryAt = &retryAt
		tombstone.Attempts = 1
	}
	if err := r.catalog.UpsertTombstone(context.Background(), tombstone); err != nil {
		return fmt.Errorf("record namespace retirement state %s: %w", namespace, err)
	}
	if cause != nil {
		return fmt.Errorf("remove tombstone for namespace %s: %w", namespace, cause)
	}
	return nil
}

func (r *NamespaceRetirer) validate(namespace string, reason RetirementReason) error {
	if r == nil || r.catalog == nil || r.root == "" {
		return fmt.Errorf("invalid namespace retirer")
	}
	if !namespaceTokenPattern.MatchString(namespace) {
		return fmt.Errorf("invalid namespace %q", namespace)
	}
	if reason != RetirementPostMerge && reason != RetirementNonOperative {
		return fmt.Errorf("invalid retirement reason %q", reason)
	}
	return nil
}
