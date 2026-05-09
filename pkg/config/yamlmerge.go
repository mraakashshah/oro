// Package config provides utilities for reading and updating YAML config files
// while preserving user-edited content and best-effort comments.
package config

import (
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// MergeKey reads the YAML file at path, updates the top-level key to value,
// and writes the result back. The Node tree is used so comments and formatting
// are preserved best-effort. Edge cases:
//   - missing file: created containing only key/value
//   - malformed YAML: returns parse error without writing
//   - key absent: inserted at end of mapping
func MergeKey(path, key string, value any) error {
	raw, err := os.ReadFile(path) //nolint:gosec // path accepted from caller
	if errors.Is(err, os.ErrNotExist) {
		return createWithKey(path, key, value)
	}
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}

	valueNode, err := marshalToNode(value)
	if err != nil {
		return fmt.Errorf("encoding value: %w", err)
	}

	if doc.Kind == 0 {
		// Empty document — build a minimal mapping.
		doc = yaml.Node{
			Kind: yaml.DocumentNode,
			Content: []*yaml.Node{
				{Kind: yaml.MappingNode, Tag: "!!map"},
			},
		}
	}

	if err := setMappingKey(doc.Content[0], key, valueNode); err != nil {
		return err
	}

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("encoding yaml: %w", err)
	}

	if err := os.WriteFile(path, out, 0o600); err != nil { //nolint:gosec // path accepted from caller
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// setMappingKey replaces or appends key in a YAML mapping node.
func setMappingKey(mapping *yaml.Node, key string, value *yaml.Node) error {
	if mapping.Kind != yaml.MappingNode {
		return fmt.Errorf("expected mapping node, got kind %v", mapping.Kind)
	}
	// Walk pairs: Content[i] = key, Content[i+1] = value.
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1] = value
			return nil
		}
	}
	// Key not found — append.
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	mapping.Content = append(mapping.Content, keyNode, value)
	return nil
}

// marshalToNode converts any Go value to a yaml.Node by round-tripping through
// Marshal/Unmarshal, preserving the correct YAML type tags.
func marshalToNode(value any) (*yaml.Node, error) {
	b, err := yaml.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshaling value: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("parsing marshaled value: %w", err)
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return doc.Content[0], nil
	}
	return &doc, nil
}

// createWithKey creates a new file containing only the given key/value pair.
func createWithKey(path, key string, value any) error {
	m := map[string]any{key: value}
	out, err := yaml.Marshal(m)
	if err != nil {
		return fmt.Errorf("encoding yaml: %w", err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil { //nolint:gosec // path accepted from caller
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
