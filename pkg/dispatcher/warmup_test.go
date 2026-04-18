package dispatcher //nolint:testpackage // white-box test needs internal access

import (
	"context"
	"errors"
	"testing"
	"time"

	"oro/pkg/memory/testhelpers"
)

func TestWaitForEmbedderReturnsSentinelWhenDisabled(t *testing.T) {
	d := &Dispatcher{} // embedderReady nil = semantic disabled

	emb, err := d.WaitForEmbedder(context.Background())
	if emb != nil {
		t.Errorf("expected nil embedder, got %v", emb)
	}
	if !errors.Is(err, ErrSemanticDisabled) {
		t.Errorf("expected ErrSemanticDisabled, got %v", err)
	}
}

func TestWaitForEmbedderBlocksUntilReady(t *testing.T) {
	fakeEmb := testhelpers.NewFakeEmbedder(0)
	readyCh := make(chan struct{})
	d := &Dispatcher{
		embedder:      fakeEmb,
		embedderReady: readyCh,
	}

	type result struct {
		emb interface{}
		err error
	}
	resultCh := make(chan result, 1)

	go func() {
		emb, err := d.WaitForEmbedder(context.Background())
		resultCh <- result{emb, err}
	}()

	// Confirm it is blocked (not yet returned)
	select {
	case <-resultCh:
		t.Fatal("WaitForEmbedder returned before embedderReady was closed")
	case <-time.After(20 * time.Millisecond):
		// good — still blocking
	}

	close(readyCh)

	select {
	case r := <-resultCh:
		if r.err != nil {
			t.Errorf("expected nil error, got %v", r.err)
		}
		if r.emb != fakeEmb {
			t.Errorf("expected fake embedder, got %v", r.emb)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitForEmbedder did not unblock within 1s after channel closed")
	}
}
