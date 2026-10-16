package plugin

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/SEAN-7/bot/internal/proto"
)

// ProvidersCache is providers.json: each installed plugin's describe
// response, without its settings.
type ProvidersCache struct {
	Providers map[string]proto.DescribeResponse `json:"providers"`
}

// LoadProviders reads the providers cache at path.
func LoadProviders(path string) (map[string]proto.DescribeResponse, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("no providers cache: %w", err)
	}
	var cache ProvidersCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, fmt.Errorf("providers cache %s: %w", path, err)
	}
	return cache.Providers, nil
}
