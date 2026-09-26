// Package webui holds the built dashboard. Node is a build-time
// dependency only; the shipped binary contains these files.
package webui

import "embed"

//go:embed all:dist
var Dist embed.FS
