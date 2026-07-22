//nolint:testpackage // The collection acceptance test verifies Client's unexported read-only boundary.
package github

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type effectiveRuleCollectionFixture struct {
	items              []effectiveRuleResponse
	evidence           CollectionEvidence
	err                error
	cancelAfterCollect context.CancelFunc
	requests           []CollectionRequest
}

var _ CollectionReader = (*effectiveRuleCollectionFixture)(nil)

func (f *effectiveRuleCollectionFixture) CollectJSON(_ context.Context, request CollectionRequest, dst any) (CollectionEvidence, error) {
	f.requests = append(f.requests, request)
	if f.err != nil {
		return CollectionEvidence{}, f.err
	}
	*(dst.(*[]effectiveRuleResponse)) = append([]effectiveRuleResponse(nil), f.items...)
	if f.cancelAfterCollect != nil {
		f.cancelAfterCollect()
	}
	return f.evidence, nil
}

func TestCollectEffectiveRulePages(t *testing.T) {
	limits := CollectionLimits{MaxPages: 3, MaxItems: 50, MaxBytes: 4096}
	request := CollectionRequest{
		Path:     "/repos/acme/oro/rules/branches/main",
		MaxPages: 3,
		MaxItems: 50,
		MaxBytes: 4096,
	}

	tests := []struct {
		name         string
		fixture      effectiveRuleCollectionFixture
		cancelBefore bool
		wantItems    []int64
		wantEvidence CollectionEvidence
		wantErr      error
		wantRequest  bool
	}{
		{
			name: "complete pages",
			fixture: effectiveRuleCollectionFixture{
				items:    []effectiveRuleResponse{{ID: 101}, {ID: 303}},
				evidence: CollectionEvidence{PageCount: 2, ItemCount: 2},
			},
			wantItems:    []int64{101, 303},
			wantEvidence: CollectionEvidence{PageCount: 2, ItemCount: 2},
			wantRequest:  true,
		},
		{
			name: "proven terminal empty",
			fixture: effectiveRuleCollectionFixture{
				items:    []effectiveRuleResponse{},
				evidence: CollectionEvidence{PageCount: 1, ItemCount: 0},
			},
			wantItems:    []int64{},
			wantEvidence: CollectionEvidence{PageCount: 1, ItemCount: 0},
			wantRequest:  true,
		},
		{
			name:         "canceled before collection",
			cancelBefore: true,
			wantErr:      context.Canceled,
		},
		{
			name: "canceled during collection",
			fixture: effectiveRuleCollectionFixture{
				cancelAfterCollect: func() {},
			},
			wantErr:     context.Canceled,
			wantRequest: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fixture := tt.fixture
			if fixture.cancelAfterCollect != nil {
				fixture.cancelAfterCollect = cancel
			}
			if tt.cancelBefore {
				cancel()
			}

			client := Client{repository: "acme/oro", collection: &fixture, collectionLimits: limits}
			got, err := client.collectEffectiveRuleResponses(ctx, "main")
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("collectEffectiveRuleResponses() error = %v, want cause %v", err, tt.wantErr)
				}
				if got.Items != nil || got.Evidence != (CollectionEvidence{}) {
					t.Fatalf("collection = %+v, want zero collection", got)
				}
				assertEffectiveRuleRequests(t, fixture.requests, request, tt.wantRequest)
				return
			}
			if err != nil {
				t.Fatalf("collectEffectiveRuleResponses() error = %v", err)
			}
			ids := make([]int64, len(got.Items))
			for i, item := range got.Items {
				ids[i] = item.ID
			}
			if !reflect.DeepEqual(ids, tt.wantItems) {
				t.Fatalf("stable IDs = %v, want %v", ids, tt.wantItems)
			}
			if got.Evidence != tt.wantEvidence {
				t.Fatalf("evidence = %+v, want %+v", got.Evidence, tt.wantEvidence)
			}
			if got.Items == nil {
				t.Fatal("items = nil, want non-nil slice")
			}
			assertEffectiveRuleRequests(t, fixture.requests, request, tt.wantRequest)
		})
	}
}

func assertEffectiveRuleRequests(t *testing.T, got []CollectionRequest, want CollectionRequest, wantRequest bool) {
	t.Helper()
	if wantRequest {
		if !reflect.DeepEqual(got, []CollectionRequest{want}) {
			t.Fatalf("requests = %+v, want one bounded collection request %+v", got, want)
		}
		return
	}
	if len(got) != 0 {
		t.Fatalf("requests = %+v, want no collection request", got)
	}
}
