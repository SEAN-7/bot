package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/SEAN-7/bot/internal/layout"
	"github.com/SEAN-7/bot/internal/plugin"
	"github.com/SEAN-7/bot/internal/proto"
	"github.com/SEAN-7/bot/internal/release"
	"github.com/SEAN-7/bot/plugins"
)

type InstallOptions struct {
	Layout        layout.Layout
	Release       *release.Client
	Repo, Version string
	Arch          string
	Index         *plugins.Index // nil: the index compiled into this binary
	ProviderGroup string         // group allowed to run plugins; "" leaves ownership alone
	Prompt        func(proto.Setting) (string, error)
	Runner        *plugin.Runner
}

// Install downloads and verifies a plugin, installs it for the provider
// user, collects its settings into a root-only credentials file and refreshes
// the provider cache. Nothing is written if verification fails.
func Install(ctx context.Context, target, sha string, o InstallOptions) error {
	name, data, err := fetch(ctx, target, sha, o)
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

func fetch(ctx context.Context, target, sha string, o InstallOptions) (string, []byte, error) {
	var name, want string
	var data []byte
	var err error
	if strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "http://") {
		if sha == "" {
			return "", nil, errors.New("installing from a URL requires --sha256")
		}
		name = strings.TrimPrefix(path.Base(target), "plugin-")
		name = strings.TrimSuffix(strings.TrimSuffix(name, "-linux-amd64"), "-linux-arm64")
		want = strings.ToLower(sha)
		if plugin.ValidName.MatchString(name) {
			data, err = download(ctx, target)
		}
	} else {
		name = target
		idx := o.Index
		if idx == nil {
			loaded, err := plugins.Load()
			if err != nil {
				return "", nil, err
			}
			idx = &loaded
		}
		want = idx.Plugins[name]["linux-"+o.Arch]
		if want == "" {
			return "", nil, fmt.Errorf("no plugin %q for linux-%s in release %s", name, o.Arch, idx.Version)
		}
		data, err = o.Release.Asset(ctx, o.Repo, "v"+o.Version, "plugin-"+name+"-linux-"+o.Arch)
	}
	if !plugin.ValidName.MatchString(name) {
		return "", nil, fmt.Errorf("invalid plugin name %q", name)
	}
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != want {
		return "", nil, fmt.Errorf("checksum mismatch for %s: got %s, want %s", name, got, want)
	}
	return name, data, nil
}

func download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 256<<20))
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
