package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"

	"github.com/SEAN-7/bot/internal/layout"
	"github.com/SEAN-7/bot/internal/plugin"
	"github.com/SEAN-7/bot/internal/proto"
	"github.com/SEAN-7/bot/plugins"
)

type InstallOptions struct {
	Layout        layout.Layout
	Arch          string
	Index         *plugins.Index // nil: the index compiled into this binary
	ProviderGroup string         // group allowed to run plugins; "" leaves ownership alone
	Prompt        func(proto.Setting) (string, error)
	Runner        *plugin.Runner
}

// Install verifies the plugin binary at file, which install.sh downloaded,
// against the release index, installs it as name for the provider user,
// collects its settings into a root-only credentials file and refreshes the
// provider cache. Nothing is written if verification fails.
func Install(ctx context.Context, name, file string, o InstallOptions) error {
	data, err := verify(name, file, o)
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

// verify reads the plugin binary at file and checks it against the SHA-256
// the index records for name on this architecture.
func verify(name, file string, o InstallOptions) ([]byte, error) {
	if !plugin.ValidName.MatchString(name) {
		return nil, fmt.Errorf("invalid plugin name %q", name)
	}
	idx := o.Index
	if idx == nil {
		loaded, err := plugins.Load()
		if err != nil {
			return nil, err
		}
		idx = &loaded
	}
	want := idx.Plugins[name]["linux-"+o.Arch]
	if want == "" {
		return nil, fmt.Errorf("no plugin %q for linux-%s in release %s", name, o.Arch, idx.Version)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != want {
		return nil, fmt.Errorf("checksum mismatch for %s: got %s, want %s; nothing installed", name, got, want)
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
