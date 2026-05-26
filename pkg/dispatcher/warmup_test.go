package dispatcher //nolint:testpackage // white-box test needs internal access

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
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
	fakeEmb := fakeEmbedder{}
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

func TestWarmupEmbedderHappyPath(t *testing.T) {
	fakeEmb := fakeEmbedder{}
	readyCh := make(chan struct{})
	d := &Dispatcher{
		embedderReady: readyCh,
		embedderFactory: func(modelDir string) (Embedder, error) {
			return fakeEmb, nil
		},
		cfg: Config{SemanticModelDir: "/fake/models"},
	}

	d.warmupEmbedder(context.Background())

	select {
	case <-readyCh:
		// good — channel closed
	default:
		t.Fatal("embedderReady not closed after successful warmup")
	}
	if d.embedderErr != nil {
		t.Errorf("expected nil embedderErr, got %v", d.embedderErr)
	}
	if d.embedder != fakeEmb {
		t.Errorf("expected fake embedder, got %v", d.embedder)
	}
}

func TestWarmupWithoutModelDoesNotBlockWorkers(t *testing.T) {
	readyCh := make(chan struct{})
	d := &Dispatcher{
		embedderReady: readyCh,
		embedderFactory: func(modelDir string) (Embedder, error) {
			return nil, &os.PathError{Op: "open", Path: modelDir + "/model.onnx", Err: os.ErrNotExist}
		},
		cfg: Config{SemanticModelDir: "/fake/models"},
	}

	d.warmupEmbedder(context.Background())

	select {
	case <-readyCh:
		// good — channel closed despite error
	default:
		t.Fatal("embedderReady not closed after PathError")
	}
	if !errors.Is(d.embedderErr, ErrEmbedderUnavailable) {
		t.Errorf("expected ErrEmbedderUnavailable, got %v", d.embedderErr)
	}
	if d.embedder != nil {
		t.Errorf("expected nil embedder on error path, got %v", d.embedder)
	}
}

func TestWarmupDisabledWhenSemanticDisabled(t *testing.T) {
	d := &Dispatcher{} // embedderReady nil = semantic disabled

	d.warmupEmbedder(context.Background())

	if d.embedderErr != nil {
		t.Errorf("expected nil embedderErr when disabled, got %v", d.embedderErr)
	}
	if d.embedder != nil {
		t.Errorf("expected nil embedder when disabled, got %v", d.embedder)
	}
}
