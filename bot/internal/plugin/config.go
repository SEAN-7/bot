package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/SEAN-7/bot/internal/proto"
)

// ConfigLimit is the most bytes of config.json the daemon reads.
const ConfigLimit = 1 << 20

// Config is an agent's config.json.
type Config struct {
	Provider string         `json:"provider"`
	Model    string         `json:"model"`
	Options  map[string]any `json:"options,omitempty"`
}

// ParseConfig rejects unknown keys so a typo cannot pass silently.
func ParseConfig(data []byte) (Config, error) {
	var c Config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("invalid JSON: %w", err)
	}
	if c.Provider == "" || c.Model == "" {
		return c, errors.New(`"provider" and "model" are required`)
	}
	return c, nil
}

// Validate checks that the config names an installed provider and one of
// its models, against the providers cache. Options are passed to the
// provider as they are; the plugin judges them.
func Validate(c Config, providers map[string]proto.DescribeResponse) error {
	d, ok := providers[c.Provider]
	if !ok {
		installed := make([]string, 0, len(providers))
		for p := range providers {
			installed = append(installed, p)
		}
		sort.Strings(installed)
		return fmt.Errorf("unknown provider %q (installed: %s)", c.Provider, strings.Join(installed, ", "))
	}
	var ids []string
	for _, m := range d.Models {
		if m.ID == c.Model {
			return nil
		}
		ids = append(ids, m.ID)
	}
	return fmt.Errorf("unknown model %q for provider %q (available: %s)", c.Model, c.Provider, strings.Join(ids, ", "))
}

// ImageTypes lists the image media types the model accepts, none if unknown.
func ImageTypes(d proto.DescribeResponse, model string) []string {
	for _, m := range d.Models {
		if m.ID == model {
			return m.ImageTypes
		}
	}
	return nil
}
