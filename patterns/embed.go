// Package patterns embeds the built-in screen heuristic pattern files.
package patterns

import "embed"

// FS holds patterns/*.toml.
//
//go:embed *.toml
var FS embed.FS
