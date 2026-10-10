// Package plugins embeds the release index: each provider plugin's SHA-256
// per architecture. `agent provider install NAME` trusts only these sums.
package plugins

import (
	_ "embed"
	"encoding/json"
)

//go:embed index.json
var indexJSON []byte

type Index struct {
	Version string                       `json:"version"`
	Plugins map[string]map[string]string `json:"plugins"` // name → "linux-<arch>" → sha256
}

func Load() (Index, error) {
	var idx Index
	err := json.Unmarshal(indexJSON, &idx)
	return idx, err
}
