package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"oro/pkg/merge"
)

// funcMergeGitRunner implements merge.GitRunner via a user-supplied function.
type funcMergeGitRunner struct {
	fn func(ctx context.Context, dir string, args ...string) (string, string, error)
}

func (f *funcMergeGitRunner) Run(ctx context.Context, dir string, args ...string) (string, string, error) {
	return f.fn(ctx, dir, args...)
}

// TestMergeFFOnlyAfterParallelAdvance verifies that when W1 advances the
// primary repo HEAD while W2 targets a non-checked-out epic branch, W2 advances
// the target ref directly instead of trying to ff-merge into the primary HEAD.
//
// Regression test for cascading exit-128 failures observed 2026-04-17
// Regression coverage for the 2026-05-10 merge churn: approved epic-targeted
// branches were rebased correctly but then reopened because the coordinator
// tried to merge them into the primary worktree HEAD rather than advancing the
// epic branch ref.
func TestMergeFFOnlyAfterParallelAdvance(t *testing.T) {
	// Race window:
	//   W2 (target="epic/feat") rebases agent/w2 onto epic/feat@M0.
	//   W1 (target="main") acquires ffLock first, ff-merges agent/w1 →
	//     primary repo HEAD advances from M0 to M1.
	//   W2 acquires ffLock and must update refs/heads/epic/feat to agent/w2,
	//     not try to ff-merge agent/w2 into primary HEAD.

	// Synchronization channels enforce the exact race ordering.
	w2RebaseDone := make(chan struct{}) // W2 signals initial rebase done
	w1Done := make(chan struct{})       // W1 signals it advanced main (ff-only complete)

	var (
		mu              sync.Mutex
		w2FFCount       int // how many ff-only attempts W2 made
		w2UpdateRef     int
		w2RebaseIsRetry bool   // set to true after W2's first ff-only fails
		retryRebaseBase string // the base arg of W2's retry rebase call
	)

	runner := &funcMergeGitRunner{fn: func(_ context.Context, dir string, args ...string) (string, string, error) {
		switch {
		case len(args) >= 1 && args[0] == "rev-list":
			return "1\n", "", nil // not merged yet

		case len(args) == 2 && args[0] == "rev-parse" && args[1] == "--git-common-dir":
			return "/repo/.git\n", "", nil

		case len(args) == 2 && args[0] == "rev-parse" && args[1] == "HEAD":
			// Returns a commit SHA representing primary repo's current HEAD (M1 after W1 merges).
			return "primary-head-M1\n", "", nil

		case len(args) >= 2 && args[0] == "rebase" && args[1] != "--abort":
			if dir == "/tmp/wt-w2" {
				mu.Lock()
				isRetry := w2RebaseIsRetry
				mu.Unlock()

				if isRetry {
					// Retry rebase — record the base used (args[1]) for later assertion.
					mu.Lock()
					retryRebaseBase = args[1]
					mu.Unlock()
					return "", "", nil
				}
				// Initial rebase: signal W2 is ready, wait for W1 to advance main.
				close(w2RebaseDone)
				select {
				case <-w1Done:
				case <-time.After(5 * time.Second):
					return "", "", fmt.Errorf("timeout waiting for W1 to advance main")
				}
				return "", "", nil
			}
			// W1's rebase: instant success.
			return "", "", nil

		case len(args) >= 2 && args[0] == "merge" && args[1] == "--ff-only":
			branch := args[len(args)-1]

			if strings.HasSuffix(branch, "w1") {
				// W1's ff-only succeeded → main advanced to M1. Notify W2.
				select {
				case <-w1Done: // already closed
				default:
					close(w1Done)
				}
				return "", "", nil
			}

			if strings.HasSuffix(branch, "w2") {
				mu.Lock()
				w2FFCount++
				count := w2FFCount
				mu.Unlock()

				if count == 1 {
					// First attempt: fails because primary HEAD (M1) ≠ W2's base (M0 via epic/feat).
					mu.Lock()
					w2RebaseIsRetry = true
					mu.Unlock()
					return "", "fatal: Not possible to fast-forward, aborting.", fmt.Errorf("exit status 128")
				}

				// Second attempt: succeeds only if the retry rebase used the primary HEAD
				// (not the stale effectiveTarget "epic/feat").
				mu.Lock()
				base := retryRebaseBase
				mu.Unlock()
				if base == "epic/feat" {
					// Bug: retry rebase used stale target — primary HEAD still not ancestor.
					return "", "fatal: Not possible to fast-forward, aborting.", fmt.Errorf("exit status 128")
				}
				// Fix: retry rebase used primary HEAD (e.g., "primary-head-M1") → success.
				return "", "", nil
			}

		case len(args) >= 3 && args[0] == "merge-base" && args[1] == "--is-ancestor":
			return "", "", nil

		case len(args) >= 3 && args[0] == "update-ref":
			if args[1] == "refs/heads/epic/feat" && args[2] == "agent/w2" {
				mu.Lock()
				w2UpdateRef++
				mu.Unlock()
			}
			return "", "", nil

		case len(args) >= 3 && args[0] == "worktree" && args[1] == "remove":
			return "", "", nil
		}
		return "", "", nil
	}}

	coord := merge.NewCoordinator(runner)

	var wg sync.WaitGroup
	var w1Err, w2Err error

	// W2: targets "epic/feat" — different target from W1, so rebases run in parallel.
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, w2Err = coord.Merge(context.Background(), merge.Opts{
			Branch:       "agent/w2",
			Worktree:     "/tmp/wt-w2",
			BeadID:       "oro-w2",
			TargetBranch: "epic/feat",
		})
	}()

	// W1: targets "main" — starts after W2's initial rebase (enforcing the race).
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-w2RebaseDone:
		case <-time.After(5 * time.Second):
			t.Errorf("W2 did not signal rebase-done within 5s")
			return
		}
		_, w1Err = coord.Merge(context.Background(), merge.Opts{
			Branch:       "agent/w1",
			Worktree:     "/tmp/wt-w1",
			BeadID:       "oro-w1",
			TargetBranch: "main",
		})
	}()

	wg.Wait()

	if w1Err != nil {
		t.Errorf("W1 merge failed: %v", w1Err)
	}
	if w2Err != nil {
		t.Errorf("W2 merge should succeed via retry-rebase onto primary HEAD, got: %v", w2Err)
	}

	mu.Lock()
	defer mu.Unlock()

	if w2FFCount != 0 {
		t.Errorf("expected no ff-only attempts for non-HEAD W2 target, got %d", w2FFCount)
	}
	if w2UpdateRef != 1 {
		t.Errorf("expected one update-ref for W2 epic target, got %d", w2UpdateRef)
	}
	if retryRebaseBase != "" {
		t.Errorf("expected no retry rebase for non-HEAD target, got base %q", retryRebaseBase)
	}
}
