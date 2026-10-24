package factory

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
)

// existingSourceBlobs queries identities computed from the validated candidate,
// in deterministic path order. Presence is only a write optimization; subsequent
// raw export must still verify object contents before acceptance or promotion.
func (g *managedGitRepository) existingSourceBlobs(ctx context.Context, tree sourceTree) (map[string]bool, error) {
	present := make(map[string]bool, len(tree.blobs))
	oids := make([]string, 0, len(tree.blobs))
	sizes := make([]int64, 0, len(tree.blobs))
	for index, oid := range tree.blobs {
		if present[oid] {
			continue
		}
		present[oid] = true
		oids = append(oids, oid)
		sizes = append(sizes, int64(len(tree.entries[index].Data)))
	}
	input := strings.Join(oids, "\n")
	if len(oids) != 0 {
		input += "\n"
	}
	out, err := g.run(ctx, []byte(input), "cat-file", "--batch-check")
	if err != nil {
		return nil, err
	}
	checked, err := decodeSourceBlobPresence(out, oids, sizes)
	if err != nil {
		return nil, err
	}
	for index, oid := range oids {
		present[oid] = checked[index]
	}
	return present, nil
}

// decodeSourceBlobPresence accepts one exact bounded response per unique local
// identity. Comparing canonical rows also rejects alternate size spellings,
// whitespace, object kinds and missing-object forms without permissive parsing.
func decodeSourceBlobPresence(data []byte, oids []string, sizes []int64) ([]bool, error) {
	if len(oids) != len(sizes) {
		return nil, errors.New("source presence identities and sizes differ in length")
	}
	present := make([]bool, len(oids))
	seen := make(map[string]bool, len(oids))
	for index, oid := range oids {
		if !sourceOID(oid) || len(oid) != len(oids[0]) || sizes[index] < 0 || seen[oid] {
			return nil, errors.New("source presence requires unique full-width local blob identities and nonnegative sizes")
		}
		seen[oid] = true
		row, rest, found := bytes.Cut(data, []byte{'\n'})
		if !found || len(row) > 128 {
			return nil, errors.New("source presence response is truncated or oversized")
		}
		switch string(row) {
		case oid + " blob " + strconv.FormatInt(sizes[index], 10):
			present[index] = true
		case oid + " missing":
		default:
			return nil, errors.New("source presence response differs from exact requested identity, blob type or size")
		}
		data = rest
	}
	if len(data) != 0 {
		return nil, errors.New("source presence response has extra rows or trailing data")
	}
	return present, nil
}
