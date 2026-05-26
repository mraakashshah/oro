package dispatcher //nolint:testpackage // white-box test: needs access to reranker, rerankerErr, rerankerFactory

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"oro/pkg/memory"
	"oro/pkg/memory/testhelpers"
	"oro/pkg/protocol"
)

// fakeReranker implements Reranker. Returns zero scores without loading any model.
type fakeReranker struct {
	scores []float64 // optional fixed scores; nil → zero-fill
}

func (r *fakeReranker) Rerank(_ string, docs []string) []float64 {
	if r.scores != nil {
		return r.scores
	}
	return make([]float64, len(docs))
}

func TestRerankHandlerDoesNotImportMemory(t *testing.T) {
	src, err := os.ReadFile("rerank_handler.go")
	if err != nil {
		t.Fatalf("read rerank_handler.go: %v", err)
	}
	if strings.Contains(string(src), `"oro/pkg/memory"`) {
		t.Fatal("rerank_handler.go must depend on dispatcher.Reranker, not memory.Reranker")
	}
}

func TestHandleRerankByIDsWithResponseWritesRerankResponse(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}

	d := &Dispatcher{
		rerankerFactory: func(_ string) (Reranker, error) {
			return &fakeReranker{scores: []float64{0.7}}, nil
		},
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		d.handleRerankByIDsWithResponse(context.Background(), server, protocol.Message{
			RerankReq: &protocol.RerankByIDsRequest{Query: "query"},
		})
	}()

	var got protocol.Message
	if err := json.NewDecoder(client).Decode(&got); err != nil {
		t.Fatalf("decode rerank response: %v", err)
	}
	<-done

	if got.Type != protocol.MsgRerankByIDsResponse {
		t.Fatalf("Type = %q, want %q", got.Type, protocol.MsgRerankByIDsResponse)
	}
	if got.RerankResp == nil {
		t.Fatal("RerankResp is nil")
	}
	if len(got.RerankResp.Scores) != 1 || got.RerankResp.Scores[0] != 0.7 {
		t.Fatalf("Scores = %#v, want [0.7]", got.RerankResp.Scores)
	}
}

// TestRerankByIDsNotLoadedAtWarmup asserts that warmupEmbedder does NOT touch the
// reranker field. The dispatcher must be nil-reranker immediately after warmup.
func TestRerankByIDsNotLoadedAtWarmup(t *testing.T) {
	fakeEmb := testhelpers.NewFakeEmbedder(0)
	readyCh := make(chan struct{})
	d := &Dispatcher{
		embedderReady: readyCh,
		embedderFactory: func(_ string) (Embedder, error) {
			return fakeEmb, nil
		},
		cfg: Config{SemanticModelDir: "/fake/models"},
	}

	d.warmupEmbedder(context.Background())

	if d.reranker != nil {
		t.Error("reranker must be nil immediately after warmupEmbedder — lazy-load only")
	}
}

// TestRerankByIDsLazyLoad covers the full lazy-load contract:
//  1. reranker is nil before the first request
//  2. first request triggers exactly one factory call and sets reranker
//  3. subsequent requests reuse the same instance (factory not called again)
//  4. concurrent requests all block on the same sync.Once — no duplicate loads
//  5. factory returning an error caches the failure (no retry on next call)
//  6. nil factory → "reranker unavailable" without panic
func TestRerankByIDsLazyLoad(t *testing.T) {
	t.Run("loads on first request", func(t *testing.T) {
		fakeR := &fakeReranker{}
		var calls atomic.Int32
		d := &Dispatcher{
			rerankerFactory: func(_ string) (Reranker, error) {
				calls.Add(1)
				return fakeR, nil
			},
		}

		if d.reranker != nil {
			t.Fatal("reranker should be nil before first request")
		}

		resp := d.handleRerankByIDs(context.Background(), protocol.RerankByIDsRequest{
			Query: "test", MemoryIDs: nil,
		})

		if resp.Err != "" {
			t.Fatalf("unexpected error on first request: %s", resp.Err)
		}
		if d.reranker == nil {
			t.Error("reranker should be non-nil after first request")
		}
		if calls.Load() != 1 {
			t.Errorf("factory called %d times after first request, want 1", calls.Load())
		}
	})

	t.Run("reuses same instance on subsequent requests", func(t *testing.T) {
		fakeR := &fakeReranker{}
		var calls atomic.Int32
		d := &Dispatcher{
			rerankerFactory: func(_ string) (Reranker, error) {
				calls.Add(1)
				return fakeR, nil
			},
		}

		req := protocol.RerankByIDsRequest{Query: "test", MemoryIDs: nil}
		d.handleRerankByIDs(context.Background(), req)
		d.handleRerankByIDs(context.Background(), req)
		d.handleRerankByIDs(context.Background(), req)

		if calls.Load() != 1 {
			t.Errorf("factory called %d times, want exactly 1 (should reuse instance)", calls.Load())
		}
		if d.reranker != fakeR {
			t.Error("reranker field should be the exact instance returned by the factory")
		}
	})

	t.Run("concurrent requests block on same sync.Once — no duplicate loads", func(t *testing.T) {
		fakeR := &fakeReranker{}
		var calls atomic.Int32

		block := make(chan struct{}) // blocks inside the factory until we release
		factoryEntered := make(chan struct{}, 1)

		d := &Dispatcher{
			rerankerFactory: func(_ string) (Reranker, error) {
				calls.Add(1)
				select {
				case factoryEntered <- struct{}{}:
				default:
				}
				<-block
				return fakeR, nil
			},
		}

		const concurrency = 5
		results := make([]protocol.RerankByIDsResponse, concurrency)
		var wg sync.WaitGroup
		for i := range concurrency {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				results[idx] = d.handleRerankByIDs(context.Background(),
					protocol.RerankByIDsRequest{Query: "q", MemoryIDs: nil})
			}(i)
		}

		// Wait for the factory to actually be entered by one goroutine, then release.
		<-factoryEntered
		close(block)
		wg.Wait()

		if calls.Load() != 1 {
			t.Errorf("factory called %d times, want 1 (sync.Once must deduplicate concurrent calls)", calls.Load())
		}
		for i, r := range results {
			if r.Err != "" {
				t.Errorf("result[%d] has unexpected error: %s", i, r.Err)
			}
		}
	})

	t.Run("factory error caches failure — no retry", func(t *testing.T) {
		var calls atomic.Int32
		d := &Dispatcher{
			rerankerFactory: func(_ string) (Reranker, error) {
				calls.Add(1)
				return nil, errors.New("model missing")
			},
		}

		resp1 := d.handleRerankByIDs(context.Background(), protocol.RerankByIDsRequest{})
		resp2 := d.handleRerankByIDs(context.Background(), protocol.RerankByIDsRequest{})

		if resp1.Err != "reranker unavailable" {
			t.Errorf("resp1.Err = %q, want %q", resp1.Err, "reranker unavailable")
		}
		if resp2.Err != "reranker unavailable" {
			t.Errorf("resp2.Err = %q, want %q", resp2.Err, "reranker unavailable")
		}
		if calls.Load() != 1 {
			t.Errorf("factory called %d times, want 1 (error must be cached, not retried)", calls.Load())
		}
	})

	t.Run("nil factory returns unavailable without panic", func(t *testing.T) {
		d := &Dispatcher{} // rerankerFactory is nil

		resp := d.handleRerankByIDs(context.Background(), protocol.RerankByIDsRequest{
			Query: "test", MemoryIDs: nil,
		})

		if resp.Err != "reranker unavailable" {
			t.Errorf("resp.Err = %q, want %q", resp.Err, "reranker unavailable")
		}
	})

	t.Run("handler resolves IDs from Store — missing ID gets empty string", func(t *testing.T) {
		db := newTestDB(t)

		fakeR := &fakeReranker{scores: []float64{0.9, 0.1}}
		d := &Dispatcher{
			memories: memory.NewStore(db),
			rerankerFactory: func(_ string) (Reranker, error) {
				return fakeR, nil
			},
		}

		// Insert one real memory; the second ID does not exist.
		ctx := context.Background()
		realID, err := d.memories.Insert(ctx, memory.InsertParams{
			Content: "relevant doc", Type: "summary",
		})
		if err != nil {
			t.Fatalf("insert memory: %v", err)
		}

		const missingID = 99999
		resp := d.handleRerankByIDs(ctx, protocol.RerankByIDsRequest{
			Query:     "query",
			MemoryIDs: []int64{realID, missingID},
		})

		if resp.Err != "" {
			t.Fatalf("unexpected error: %s", resp.Err)
		}
		if len(resp.Scores) != 2 {
			t.Fatalf("expected 2 scores, got %d", len(resp.Scores))
		}
		// fakeR returns fakeR.scores directly, so we should get [0.9, 0.1]
		if resp.Scores[0] != 0.9 {
			t.Errorf("Scores[0] = %v, want 0.9", resp.Scores[0])
		}
		if resp.Scores[1] != 0.1 {
			t.Errorf("Scores[1] = %v, want 0.1", resp.Scores[1])
		}
	})
}
