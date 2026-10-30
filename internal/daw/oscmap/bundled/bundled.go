// Package bundled holds built-in DAW maps and the companion OSC extension.
package bundled

import "embed"

// FS contains built-in DAW maps and extension files.
//
//go:embed *.json abletonosc.patch
var FS embed.FS
