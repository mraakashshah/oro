package dispatcher

import (
	"context"
	"sync"
)

type presubmitCandidate struct {
	actions []PresubmitAction
	done    chan<- struct{}
}

func newPresubmitSemaphore() *QGSemaphore {
	return NewQGSemaphore(map[ResourceClass]int{
		ResourceCPULight:    4,
		ResourceCPUHeavy:    1,
		ResourceMemoryHeavy: 1,
		ResourceEmpty:       1,
	})
}

// runPresubmitScheduler schedules each action independently so a constrained
// resource class cannot serialize unrelated checks.
func (d *Dispatcher) runPresubmitScheduler(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case candidate, ok := <-d.presubmitCandidates:
			if !ok {
				return
			}
			d.safeGo(func() { d.runPresubmitCandidate(ctx, candidate) })
		}
	}
}

func (d *Dispatcher) runPresubmitCandidate(ctx context.Context, candidate presubmitCandidate) {
	defer notifyPresubmitComplete(candidate.done)

	var actions sync.WaitGroup
	for _, action := range candidate.actions {
		action := action
		actions.Add(1)
		d.safeGo(func() {
			defer actions.Done()
			d.runPresubmitAction(ctx, action)
		})
	}
	actions.Wait()
}

func (d *Dispatcher) runPresubmitAction(ctx context.Context, action PresubmitAction) {
	release, err := d.presubmitSemaphore.Acquire(ctx, action.ResourceClass)
	if err != nil {
		return
	}
	defer release()
	if d.presubmitActionRunner != nil {
		_ = d.presubmitActionRunner(ctx, action)
	}
}

func notifyPresubmitComplete(done chan<- struct{}) {
	if done != nil {
		close(done)
	}
}
