// Package kit carries the project kit (the check, the gate, the presets and the handover) inside
// the handloom program, so `handloom kit init` works on a machine with nothing else installed.
// The kit is also plain files: it works without Handloom (see README.md in this folder).
package kit

import "embed"

// Files holds bin/factory and the kit's modules and templates. Build caches are skipped on use.
//
//go:embed all:bin all:kit docs/contract.md README.md
var Files embed.FS
