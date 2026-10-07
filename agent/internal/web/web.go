// Package web holds the dashboard's static files, embedded in the binary
// so the agent ships as a single file with no build toolchain or CDN.
package web

import "embed"

//go:embed static
var Static embed.FS
