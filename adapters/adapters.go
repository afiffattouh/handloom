// Package adapters embeds the files each agent adapter installs.
//
// AGENTS.snippet.md and LEAD.snippet.md are the protocol instructions shared
// by every kind. Each kind's directory holds its manifest and, for kinds
// without native command hooks, the shim that forwards lifecycle events to
// `handloom hook <kind>`.
package adapters

import "embed"

//go:embed AGENTS.snippet.md LEAD.snippet.md claude codex pi omp opencode
var FS embed.FS
