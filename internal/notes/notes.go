// Package notes embeds the agent-facing release notes.
package notes

import _ "embed"

//go:embed notes.md
var Text string
