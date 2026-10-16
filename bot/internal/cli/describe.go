package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/SEAN-7/bot/internal/layout"
	"github.com/SEAN-7/bot/internal/plugin"
	"github.com/SEAN-7/bot/internal/proto"
)

// RefreshCache describes every installed plugin and writes the world-readable
// cache the agent reads. Describe output carries no credentials.
func RefreshCache(ctx context.Context, l layout.Layout, r *plugin.Runner) (plugin.ProvidersCache, error) {
	cache := plugin.ProvidersCache{Providers: map[string]proto.DescribeResponse{}}
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
	return plugin.LoadProviders(l.ProvidersCache())
}

// Describe prints the describe responses of the installed providers as
// indented JSON: live as root, otherwise from the cache.
func Describe(ctx context.Context, l layout.Layout, r *plugin.Runner, root bool, w io.Writer) error {
	var cache plugin.ProvidersCache
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
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}
