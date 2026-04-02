package web

import "embed"

// Content embeds the web dashboard templates and static assets.
// The templates/ directory contains HTML templates; the static/ directory
// contains CSS and JavaScript files served at /static/.
//
//go:embed templates static
var Content embed.FS
