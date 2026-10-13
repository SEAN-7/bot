package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/SEAN-7/bot/internal/layout"
	"github.com/SEAN-7/bot/internal/plugin"
	"github.com/SEAN-7/bot/internal/proto"
)

type providersCache struct {
	Providers map[string]proto.DescribeResponse `json:"providers"`
}

// RefreshCache describes every installed plugin and writes the world-readable
// cache the agent reads. Describe output carries no credentials.
func RefreshCache(ctx context.Context, l layout.Layout, r *plugin.Runner) (providersCache, error) {
	cache := providersCache{Providers: map[string]proto.DescribeResponse{}}
	names, err := r.Installed()
	if err != nil {
		return cache, err
	}
	for _, name := range names {
		d, err := r.Describe(ctx, name)
		if err != nil {
			d = proto.DescribeResponse{Error: &proto.Error{Message: err.Error()}}
		}
		d.Settings = nil
		cache.Providers[name] = d
	}
	data, _ := json.MarshalIndent(cache, "", "  ")
	if err := os.MkdirAll(l.Lib, 0o755); err != nil {
		return cache, err
	}
	temp := filepath.Join(l.Lib, ".providers.json.tmp")
	if err := os.WriteFile(temp, append(data, '\n'), 0o644); err != nil {
		return cache, err
	}
	os.Chmod(temp, 0o644)
	return cache, os.Rename(temp, l.ProvidersCache())
}

// LoadProviders reads the cache of describe responses RefreshCache writes.
func LoadProviders(l layout.Layout) (map[string]proto.DescribeResponse, error) {
	data, err := os.ReadFile(l.ProvidersCache())
	if err != nil {
		return nil, fmt.Errorf("no provider cache yet (run as root once): %w", err)
	}
	var cache providersCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, err
	}
	return cache.Providers, nil
}

// Describe prints providers, models and options: live as root, otherwise
// from the cache.
func Describe(ctx context.Context, l layout.Layout, r *plugin.Runner, root bool, w io.Writer) error {
	var cache providersCache
	if root {
		var err error
		if cache, err = RefreshCache(ctx, l, r); err != nil {
			return err
		}
	} else {
		providers, err := LoadProviders(l)
		if err != nil {
			return err
		}
		cache.Providers = providers
	}
	names := make([]string, 0, len(cache.Providers))
	for name := range cache.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		d := cache.Providers[name]
		fmt.Fprintln(w, name)
		if d.Error != nil {
			fmt.Fprintf(w, "  error: %s\n", d.Error.Message)
		}
		for _, m := range d.Models {
			fmt.Fprintf(w, "  %s (%d tokens)\n", m.ID, m.ContextTokens)
			opts := make([]string, 0, len(m.Options))
			for opt := range m.Options {
				opts = append(opts, opt)
			}
			sort.Strings(opts)
			for _, opt := range opts {
				o := m.Options[opt]
				var allowed string
				if o.Type == "integer" {
					allowed = fmt.Sprintf("integer %s-%s", plugin.Bound(o.Min), plugin.Bound(o.Max))
				} else {
					var vals []string
					for _, v := range o.Values {
						vals = append(vals, fmt.Sprint(v))
					}
					allowed = strings.Join(vals, ", ")
				}
				fmt.Fprintf(w, "    %s: %s\n", opt, allowed)
			}
		}
	}
	return nil
}
