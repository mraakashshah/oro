package main

import "embed"

// EmbeddedAssets contains oro config assets (skills, hooks, beacons, commands, CLAUDE.md)
// staged into _assets/ by the Makefile stage-assets target.
// The directory is created at build time and cleaned up afterward.
//
//go:embed all:_assets
var EmbeddedAssets embed.FS
