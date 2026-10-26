package factory

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
)

type sourceCommit struct {
	tree    string
	parents []string
}

func (g *managedGitRepository) readCommit(ctx context.Context, oid string) (sourceCommit, error) {
	var commit sourceCommit
	if !sourceOID(oid) {
		return commit, errors.New("source requires a complete lowercase commit object ID")
	}
	out, err := g.run(ctx, []byte(oid+"\n"), "cat-file", "--batch")
	if err != nil {
		return commit, err
	}
	objects, err := decodeSourceBatch(out, []string{oid}, "commit", 1<<20)
	if err != nil {
		return commit, err
	}
	headers, _, found := bytes.Cut(objects[0], []byte("\n\n"))
	if !found {
		return commit, errors.New("source commit has no complete header")
	}
	for index, line := range strings.Split(string(headers), "\n") {
		switch {
		case strings.HasPrefix(line, "tree "):
			if index != 0 || commit.tree != "" {
				return commit, errors.New("source commit has ambiguous tree headers")
			}
			commit.tree = strings.TrimPrefix(line, "tree ")
		case strings.HasPrefix(line, "parent "):
			parent := strings.TrimPrefix(line, "parent ")
			if !sourceOID(parent) || len(parent) != len(oid) {
				return commit, errors.New("source commit has an invalid parent")
			}
			commit.parents = append(commit.parents, parent)
		}
	}
	if !sourceOID(commit.tree) || len(commit.tree) != len(oid) {
		return commit, errors.New("source commit has no exact tree ID")
	}
	return commit, nil
}

func (g *managedGitRepository) export(ctx context.Context, oid string) ([]sourceEntry, error) {
	ctx, cancel := g.operation(ctx)
	defer cancel()
	commit, err := g.readCommit(ctx, oid)
	if err != nil {
		return nil, err
	}
	out, err := g.run(ctx, nil, "ls-tree", "-r", "-z", "-l", "--full-tree", oid)
	if err != nil {
		return nil, err
	}
	entries, oids, sizes, err := decodeSourceListing(out, g.source, len(oid))
	if err != nil {
		return nil, err
	}
	if len(oids) != 0 {
		out, err = g.run(ctx, []byte(strings.Join(oids, "\n")+"\n"), "cat-file", "--batch")
		if err != nil {
			return nil, err
		}
		blobs, err := decodeSourceBatch(out, oids, "blob", g.source.MaxBytes)
		if err != nil {
			return nil, err
		}
		for index := range entries {
			if int64(len(blobs[index])) != sizes[index] {
				return nil, errors.New("raw blob size differs from the selected tree entry")
			}
			entries[index].Data = blobs[index]
		}
	}
	tree, err := buildSourceTree(entries, g.source, len(oid))
	if err != nil {
		return nil, err
	}
	// Full identity reconstruction also rejects invisible empty subtrees and
	// noncanonical tree encodings/modes that a recursive listing could hide.
	if tree.oid != commit.tree {
		return nil, errors.New("exported source cannot reproduce the selected commit's exact tree")
	}
	return tree.entries, nil
}

func decodeSourceListing(data []byte, limits sourceLimits, width int) (entries []sourceEntry, oids []string, sizes []int64, err error) {
	nodes := make(map[string]sourcePathNode)
	sizes = make([]int64, 0)
	var total int64
	for len(data) > 0 {
		row, rest, found := bytes.Cut(data, []byte{0})
		if !found || len(row) > limits.MaxPathBytes+128 {
			return nil, nil, nil, errors.New("raw Git tree listing is unterminated or oversized")
		}
		data = rest
		entry, err := decodeSourceListingEntry(row, limits, width, limits.MaxBytes-total)
		if err != nil {
			return nil, nil, nil, err
		}
		if err := validateSourcePath(entry.Path); err != nil {
			return nil, nil, nil, err
		}
		if err := addSourcePath(nodes, entry.Path, false, limits.MaxFiles); err != nil {
			return nil, nil, nil, err
		}
		entries = append(entries, entry.sourceEntry)
		oids, sizes = append(oids, entry.oid), append(sizes, entry.size)
		total += entry.size
	}
	return entries, oids, sizes, nil
}

type sourceListingEntry struct {
	sourceEntry
	oid  string
	size int64
}

func decodeSourceListingEntry(row []byte, limits sourceLimits, width int, remaining int64) (sourceListingEntry, error) {
	header, path, found := bytes.Cut(row, []byte{'\t'})
	fields := strings.Fields(string(header))
	if !found || len(fields) != 4 || fields[1] != "blob" || !sourceOID(fields[2]) || len(fields[2]) != width || (fields[0] != "100644" && fields[0] != "100755" && fields[0] != "120000") {
		return sourceListingEntry{}, errors.New("raw Git tree contains an unsupported mode or object")
	}
	size, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil || size < 0 || size > limits.MaxFileBytes || size > remaining || len(path) > limits.MaxPathBytes {
		return sourceListingEntry{}, errors.New("raw Git tree exceeds admitted source limits")
	}
	mode, _ := strconv.ParseUint(fields[0], 8, 32)
	return sourceListingEntry{sourceEntry: sourceEntry{Path: string(path), Mode: uint32(mode)}, oid: fields[2], size: size}, nil
}

func (g *managedGitRepository) commit(ctx context.Context, base string, entries []sourceEntry) (string, error) {
	if !sourceOID(base) {
		return "", errors.New("candidate requires an exact base commit ID")
	}
	// Validate the whole vector before any host effect or object write.
	tree, err := buildSourceTree(entries, g.source, len(base))
	if err != nil {
		return "", err
	}
	ctx, cancel := g.operation(ctx)
	defer cancel()
	parent, err := g.readCommit(ctx, base)
	if err != nil {
		return "", err
	}
	if parent.tree == tree.oid {
		return base, nil
	}
	if err := g.writeSourceTree(ctx, tree); err != nil {
		return "", err
	}
	out, err := g.run(ctx, nil, "commit-tree", "--no-gpg-sign", tree.oid, "-p", base, "-m", "factory: candidate")
	if err != nil {
		return "", err
	}
	oid, err := parseSourceOIDLine(out)
	if err != nil || len(oid) != len(base) {
		return "", errors.New("raw Git returned an invalid candidate commit ID")
	}
	candidate, err := g.readCommit(ctx, oid)
	if err != nil {
		return "", err
	}
	if candidate.tree != tree.oid || len(candidate.parents) != 1 || candidate.parents[0] != base {
		return "", errors.New("candidate commit differs from the validated tree or exact base parent")
	}
	return oid, nil
}

func (g *managedGitRepository) writeSourceTree(ctx context.Context, tree sourceTree) error {
	written, err := g.existingSourceBlobs(ctx, tree)
	if err != nil {
		return err
	}
	for index, entry := range tree.entries {
		expected := tree.blobs[index]
		if written[expected] {
			continue
		}
		out, err := g.run(ctx, entry.Data, "hash-object", "-w", "--stdin", "--no-filters")
		if err != nil {
			return err
		}
		oid, err := parseSourceOIDLine(out)
		if err != nil || oid != expected {
			return errors.New("raw Git wrote a blob differing from validated candidate bytes")
		}
		written[expected] = true
	}
	out, err := g.run(ctx, tree.input, "mktree", "-z", "--batch")
	if err != nil {
		return err
	}
	if string(out) != strings.Join(tree.trees, "\n")+"\n" {
		return errors.New("raw Git constructed a tree differing from validated candidate modes, paths or bytes")
	}
	return nil
}
