// Package docs embeds the manual, which the daemon writes to
// /var/lib/agent/manual for agents to read.
package docs

import _ "embed"

//go:embed manual.md
var Manual string
