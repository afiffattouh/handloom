// Package adapters embeds the files each agent adapter installs.
package adapters

import "embed"

//go:embed claude
var FS embed.FS
