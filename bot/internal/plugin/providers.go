package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

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
		return nil, fmt.Errorf("no provider cache yet (run as root once): %w", err)
	}
	var cache ProvidersCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, err
	}
	return cache.Providers, nil
}

// ValidateCached checks a config.json against the providers cache, as each
// run checks it against the plugins themselves.
func ValidateCached(data []byte, providers map[string]proto.DescribeResponse) (Config, error) {
	config, err := ParseConfig(data)
	if err != nil {
		return config, err
	}
	installed := make([]string, 0, len(providers))
	for p := range providers {
		installed = append(installed, p)
	}
	sort.Strings(installed)
	return config, Validate(config, installed, providers[config.Provider])
}
