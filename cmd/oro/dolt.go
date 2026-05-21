package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
)

const (
	doltPortBase  = 13307
	doltPortRange = 1000
)

// doltMeta holds the fields from beads metadata relevant to dolt lifecycle.
type doltMeta struct {
	Backend        string `json:"backend"`
	DoltServerPort int    `json:"dolt_server_port"`
	DoltDatabase   string `json:"dolt_database"`
	DoltMode       string `json:"dolt_mode,omitempty"`
}

// DerivePort computes a stable port in [13307, 14306] for the given beads
// directory using FNV-32a hash of the absolute path. Two calls with the same
// resolved absolute path always return the same port.
func DerivePort(beadsDir string) int {
	abs, err := filepath.Abs(beadsDir)
	if err != nil {
		abs = beadsDir
	}
	h := fnv.New32a()
	h.Write([]byte(abs)) //nolint:gosec // G104: hash.Hash.Write never returns an error
	return doltPortBase + int(h.Sum32()%doltPortRange)
}

// readDoltMeta reads beads metadata and returns its contents if the
// backend is "dolt". Returns nil (no error) for missing directories, missing
// metadata.json, or any non-dolt backend.
func readDoltMeta(beadsDir string) (*doltMeta, error) {
	metaPath := filepath.Join(beadsDir, "metadata.json")
	data, err := os.ReadFile(metaPath) //nolint:gosec // beadsDir is caller-controlled
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", metaPath, err)
	}

	var meta doltMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parse %s: %w", metaPath, err)
	}

	if meta.Backend != "dolt" {
		return nil, nil
	}
	return &meta, nil
}
