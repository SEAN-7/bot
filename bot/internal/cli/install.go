package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/SEAN-7/bot/internal/layout"
	"github.com/SEAN-7/bot/internal/plugin"
	"github.com/SEAN-7/bot/internal/proto"
)

// fetchLimit bounds a plugin binary fetched from a URL.
const fetchLimit = 64 << 20

type InstallOptions struct {
	Layout        layout.Layout
	ProviderGroup string // group allowed to run plugins; "" leaves ownership alone
	Prompt        func(proto.Setting) (string, error)
	Runner        *plugin.Runner
}

// Install installs the plugin binary at source, a file path or an http(s)
// URL, as name for the provider user, collects its settings into a
// root-only credentials file and refreshes the provider cache. Root trusts
// what it installs, as it trusts any package. Nothing is written if the
// fetch fails.
func Install(ctx context.Context, name, source string, o InstallOptions) error {
	if !plugin.ValidName.MatchString(name) {
		return fmt.Errorf("invalid plugin name %q", name)
	}
	data, err := fetch(ctx, source)
	if err != nil {
		return err
	}
	if err := writePlugin(o, name, data); err != nil {
		return err
	}
	d, err := o.Runner.Describe(ctx, name)
	if err != nil && len(d.Settings) == 0 {
		return fmt.Errorf("describe %s: %w", name, err)
	}
	credPath := filepath.Join(o.Layout.Credentials(), name+".env")
	existing, _ := os.ReadFile(credPath)
	creds, err := plugin.ParseEnv(existing)
	if err != nil {
		return err
	}
	for _, s := range d.Settings {
		if creds[s.Name] != "" {
			continue
		}
		value := os.Getenv(s.Name)
		if value == "" && o.Prompt != nil {
			if value, err = o.Prompt(s); err != nil {
				return err
			}
		}
		if value != "" {
			creds[s.Name] = value
		}
	}
	if err := os.MkdirAll(o.Layout.Credentials(), 0o700); err != nil {
		return err
	}
	temp := credPath + ".tmp"
	if err := os.WriteFile(temp, plugin.FormatEnv(creds), 0o600); err != nil {
		return err
	}
	if err := os.Rename(temp, credPath); err != nil {
		return err
	}
	_, err = RefreshCache(ctx, o.Layout, o.Runner)
	return err
}

// fetch reads the plugin binary from a path, or from an http(s) URL.
func fetch(ctx context.Context, source string) ([]byte, error) {
	if !strings.HasPrefix(source, "http://") && !strings.HasPrefix(source, "https://") {
		return os.ReadFile(source)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("fetch %s: HTTP %d", source, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, fetchLimit+1))
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", source, err)
	}
	if len(data) > fetchLimit {
		return nil, errors.New("fetch " + source + ": larger than 64 MiB")
	}
	return data, nil
}

// writePlugin installs the binary root-owned, executable by the provider
// group only, replacing any previous version atomically.
func writePlugin(o InstallOptions, name string, data []byte) error {
	dir := o.Layout.Plugins()
	gid := -1
	if o.ProviderGroup != "" {
		g, err := user.LookupGroup(o.ProviderGroup)
		if err != nil {
			return err
		}
		if gid, err = strconv.Atoi(g.Gid); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	temp := filepath.Join(dir, "."+name+".tmp")
	if err := os.WriteFile(temp, data, 0o750); err != nil {
		return err
	}
	for _, p := range []string{dir, temp} {
		if gid >= 0 {
			if err := os.Chown(p, 0, gid); err != nil {
				return err
			}
		}
		if err := os.Chmod(p, 0o750); err != nil {
			return err
		}
	}
	if err := os.Rename(temp, filepath.Join(dir, name)); err != nil {
		os.Remove(temp)
		return err
	}
	return nil
}
