// Package seed holds the starting files of a new agent's home, which the
// daemon writes at admission.
package seed

import _ "embed"

//go:embed README.md
var README string

//go:embed TASK.md
var TASK string
