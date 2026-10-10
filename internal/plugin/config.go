package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"

	"github.com/SEAN-7/bot/internal/proto"
)

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

// Validate checks a config against the provider's describe response.
func Validate(c Config, installed []string, d proto.DescribeResponse) error {
	if !contains(installed, c.Provider) {
		return fmt.Errorf("unknown provider %q (installed: %s)", c.Provider, strings.Join(installed, ", "))
	}
	var model *proto.Model
	var ids []string
	for i, m := range d.Models {
		ids = append(ids, m.ID)
		if m.ID == c.Model {
			model = &d.Models[i]
		}
	}
	if model == nil {
		return fmt.Errorf("unknown model %q for provider %q (available: %s)", c.Model, c.Provider, strings.Join(ids, ", "))
	}
	names := make([]string, 0, len(c.Options))
	for name := range c.Options {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := c.Options[name]
		opt, ok := model.Options[name]
		if !ok {
			return fmt.Errorf("unknown option %q for model %q", name, c.Model)
		}
		if opt.Type == "integer" {
			n, isNum := value.(float64)
			if !isNum || n != math.Trunc(n) || (opt.Min != nil && n < float64(*opt.Min)) || (opt.Max != nil && n > float64(*opt.Max)) {
				return fmt.Errorf("option %q: value %v out of range [%s, %s]", name, value, bound(opt.Min), bound(opt.Max))
			}
			continue
		}
		allowed := false
		var shown []string
		for _, v := range opt.Values {
			shown = append(shown, fmt.Sprint(v))
			allowed = allowed || reflect.DeepEqual(normalize(v), normalize(value))
		}
		if !allowed {
			return fmt.Errorf("option %q: value %v not allowed (allowed: %s)", name, value, strings.Join(shown, ", "))
		}
	}
	return nil
}

func bound(p *int) string {
	if p == nil {
		return "any"
	}
	return fmt.Sprint(*p)
}

// normalize gives values from different decoders one representation.
func normalize(v any) any {
	data, _ := json.Marshal(v)
	var out any
	json.Unmarshal(data, &out)
	return out
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// ContextTokens returns the model's context size, or 0 if unknown.
func ContextTokens(d proto.DescribeResponse, model string) int {
	for _, m := range d.Models {
		if m.ID == model {
			return m.ContextTokens
		}
	}
	return 0
}
