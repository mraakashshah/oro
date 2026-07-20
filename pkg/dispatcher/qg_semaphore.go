package dispatcher

import (
	"context"
	"sync"
)

// QGSemaphore limits concurrent presubmit actions independently by resource
// class. A zero capacity intentionally queues work until its context ends.
type QGSemaphore struct {
	classes map[ResourceClass]chan struct{}
}

// NewQGSemaphore constructs a per-resource-class semaphore.
func NewQGSemaphore(capacities map[ResourceClass]int) *QGSemaphore {
	classes := make(map[ResourceClass]chan struct{}, len(capacities))
	for class, capacity := range capacities {
		if capacity < 0 {
			capacity = 0
		}
		classes[class] = make(chan struct{}, capacity)
	}
	return &QGSemaphore{classes: classes}
}

// Acquire waits for capacity in class and returns an idempotent release
// function. Classes without a declared capacity are not limited.
func (s *QGSemaphore) Acquire(ctx context.Context, class ResourceClass) (func(), error) {
	if s == nil || s.classes[class] == nil {
		return func() {}, nil
	}
	permits := s.classes[class]
	select {
	case permits <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			<-permits
		})
	}, nil
}
