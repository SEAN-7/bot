package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/SEAN-7/bot/internal/layout"
	"github.com/SEAN-7/bot/internal/plugin"
)

// Base is the directory whose agents/ holds the caller's children: root's
// RootHome, or an agent's own home.
func Base(l layout.Layout, root bool, home string) (string, error) {
	if root {
		return l.RootHome(), nil
	}
	if home == "" {
		return "", errors.New("HOME is not set; run this from an agent's shell")
	}
	return home, nil
}

// The file operations that can fail after the control directory exists;
// tests replace them.
var (
	writeFile = os.WriteFile
	rename    = os.Rename
)

// New creates the control directory of a child called name from the config
// in source: a file path, "-" for stdin, or "" for the caller's own
// ~/config.json (root has none). The config is checked against the installed
// providers before anything is created. It writes the same directory an
// agent could create by hand: the daemon's poll does the rest, so New
// reports where the directory is and returns without waiting.
func New(l layout.Layout, root bool, home, name, source string, stdin io.Reader, w io.Writer) error {
	base, err := Base(l, root, home)
	if err != nil {
		return err
	}
	if !cleanName(name) {
		return fmt.Errorf("%q is not a direct subagent name; use one plain name", name)
	}
	ctrl := filepath.Join(base, "agents", name)
	if _, err := os.Lstat(ctrl); err == nil {
		return fmt.Errorf("%s already exists", ctrl)
	}
	if source == "" {
		if root {
			return errors.New("root has no config.json to copy: agent new NAME CONFIG (a file, or - for stdin)")
		}
		source = filepath.Join(home, "config.json")
	}
	data, err := readConfigSource(source, stdin)
	if err != nil {
		return err
	}
	config, err := plugin.ParseConfig(data)
	if err != nil {
		return fmt.Errorf("%s: %w", source, err)
	}
	providers, err := LoadProviders(l)
	if err != nil {
		return err
	}
	installed := make([]string, 0, len(providers))
	for p := range providers {
		installed = append(installed, p)
	}
	sort.Strings(installed)
	if err := plugin.Validate(config, installed, providers[config.Provider]); err != nil {
		return fmt.Errorf("%s: %w", source, err)
	}
	data, err = json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	// Root's control directories stay private. An agent's are open to its
	// home's group, its ancestors, and closed to everyone else: its
	// children, whom a sharing grant may let through its home, never need
	// them.
	mode := os.FileMode(0o750)
	if root {
		mode = 0o700
	}
	if err := os.MkdirAll(filepath.Dir(ctrl), mode); err != nil {
		return err
	}
	if err := os.Mkdir(ctrl, mode); err != nil {
		return err
	}
	// From here the directory is ours: a failure removes it again, so
	// nothing half-made waits for the daemon.
	if err := writeConfig(ctrl, data, mode); err != nil {
		os.RemoveAll(ctrl)
		return err
	}
	fmt.Fprintf(w, "Created %s\n", ctrl)
	fmt.Fprintf(w, "The daemon admits it shortly: its home appears at %s, and a reason it was not started in %s.\n",
		filepath.Join(ctrl, "home"), filepath.Join(ctrl, "ERROR"))
	return nil
}

// readConfigSource reads a config of at most plugin.ConfigLimit bytes, the
// daemon's limit, from path or, for "-", from stdin.
func readConfigSource(source string, stdin io.Reader) ([]byte, error) {
	r := stdin
	if source != "-" {
		f, err := os.Open(source)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = f
	}
	data, err := io.ReadAll(io.LimitReader(r, plugin.ConfigLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > plugin.ConfigLimit {
		return nil, fmt.Errorf("%s is over %d KiB", source, plugin.ConfigLimit>>10)
	}
	return data, nil
}

// writeConfig sets the modes of the new control directory and writes its
// config.json, whatever the umask. The dot-name rename keeps the daemon from
// reading half a config.
func writeConfig(ctrl string, data []byte, mode os.FileMode) error {
	if err := os.Chmod(ctrl, mode); err != nil {
		return err
	}
	temp := filepath.Join(ctrl, ".config.json")
	if err := writeFile(temp, data, 0o640); err != nil {
		return err
	}
	if err := os.Chmod(temp, 0o640); err != nil {
		return err
	}
	return rename(temp, filepath.Join(ctrl, "config.json"))
}

// Remove deletes the control directory of the direct child name, which stops
// it; the daemon reclaims its home. The daemon restores a missing home link
// in a child's control directory every poll, so the directory is first
// renamed out of agents/ (atomically, and the child is gone for the daemon),
// then deleted.
func Remove(l layout.Layout, root bool, home, name string) error {
	base, err := Base(l, root, home)
	if err != nil {
		return err
	}
	if !cleanName(name) {
		return fmt.Errorf("%q is not a direct subagent name; use one plain name", name)
	}
	ctrl := filepath.Join(base, "agents", name)
	if info, err := os.Lstat(ctrl); err != nil || !info.IsDir() {
		return fmt.Errorf("no subagent %q (expected %s/)", name, ctrl)
	}
	gone := filepath.Join(base, ".removed-"+name+"-"+time.Now().UTC().Format("20060102T150405.000000000Z"))
	if err := os.Rename(ctrl, gone); err != nil {
		return err
	}
	return os.RemoveAll(gone)
}
