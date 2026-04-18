package memory

import (
	"fmt"
	"sort"
	"sync"
)

// InMemoryVecIndex is a test double for VectorIndex that stores vectors in memory.
// Partitions are keyed by project; within each partition vectors are keyed by id.
type InMemoryVecIndex struct {
	mu         sync.RWMutex
	partitions map[string]map[int64][]float32
}

// NewInMemoryVecIndex returns an empty InMemoryVecIndex.
//
//oro:testonly
func NewInMemoryVecIndex() *InMemoryVecIndex {
	return &InMemoryVecIndex{
		partitions: make(map[string]map[int64][]float32),
	}
}

// Upsert stores vec under (project, id). Returns an error if vec is nil.
func (v *InMemoryVecIndex) Upsert(id int64, vec []float32, project string) error {
	if vec == nil {
		return fmt.Errorf("upsert: nil vec")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.partitions[project] == nil {
		v.partitions[project] = make(map[int64][]float32)
	}
	cp := make([]float32, len(vec))
	copy(cp, vec)
	v.partitions[project][id] = cp
	return nil
}

// Search returns up to k results from the given project partition sorted by
// cosine similarity descending. Ties are broken by MemoryID ascending.
// Returns an empty slice (no error) for an empty partition or k <= 0.
func (v *InMemoryVecIndex) Search(queryVec []float32, project string, k int) ([]ANNResult, error) {
	if k <= 0 {
		return nil, nil
	}
	v.mu.RLock()
	partition := v.partitions[project]
	v.mu.RUnlock()

	if len(partition) == 0 {
		return nil, nil
	}

	results := make([]ANNResult, 0, len(partition))
	for id, vec := range partition {
		results = append(results, ANNResult{
			MemoryID: id,
			Score:    CosineSimilarity(queryVec, vec),
		})
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].MemoryID < results[j].MemoryID
	})

	if k < len(results) {
		results = results[:k]
	}
	return results, nil
}

// Delete removes id from every project partition. No-op if id is not found.
func (v *InMemoryVecIndex) Delete(id int64) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, partition := range v.partitions {
		delete(partition, id)
	}
	return nil
}
