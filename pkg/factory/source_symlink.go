package factory

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// validateSourceLinks checks a complete bounded leaf vector without filesystem
// access. Only implicit directories survive an exact Git tree round-trip;
// empty tar-only directories cannot make an otherwise invalid link traversable.
func validateSourceLinks(entries []sourceEntry) error {
	nodes := make(map[string]sourcePathNode)
	modes := make(map[string]uint32, len(entries))
	for _, entry := range entries {
		if entry.Mode != 0o100644 && entry.Mode != 0o100755 && entry.Mode != 0o120000 {
			return errors.New("source links require canonical Git leaf modes")
		}
		if err := validateSourcePath(entry.Path); err != nil {
			return err
		}
		if err := addSourcePath(nodes, entry.Path, false, math.MaxInt); err != nil {
			return err
		}
		modes[entry.Path] = entry.Mode
	}
	for _, entry := range entries {
		if entry.Mode != 0o120000 {
			continue
		}
		if err := validateSourceLink(entry, nodes, modes); err != nil {
			return fmt.Errorf("source link %q: %w", entry.Path, err)
		}
	}
	return nil
}

func validateSourceLink(entry sourceEntry, nodes map[string]sourcePathNode, modes map[string]uint32) error {
	if len(entry.Data) == 0 || len(entry.Data) > 100 {
		return errors.New("target must fit the nonempty USTAR link field")
	}
	parent := strings.Split(entry.Path, "/")
	parent = parent[:len(parent)-1]
	components := strings.Split(string(entry.Data), "/")
	for index, component := range components {
		if component == ".." {
			if len(parent) == 0 {
				return errors.New("target traverses above the source root")
			}
			parent = parent[:len(parent)-1]
			continue
		}
		if err := validateSourcePath(component); err != nil {
			return err
		}
		parent = append(parent, component)
		path := strings.Join(parent, "/")
		node, exists := nodes[strings.ToLower(path)]
		if !exists || node.name != path {
			return errors.New("target traverses a missing or case-aliased source path")
		}
		// Check raw traversal before a later '..' could erase this component.
		if index != len(components)-1 && !node.directory {
			return errors.New("target traverses a non-directory source entry")
		}
	}
	mode := modes[strings.Join(parent, "/")]
	if mode != 0o100644 && mode != 0o100755 {
		return errors.New("target must name an existing regular source file")
	}
	return nil
}
