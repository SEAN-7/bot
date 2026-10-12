package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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

// NewOptions are the flags of agent new, and how long to wait for admission.
type NewOptions struct {
	Provider, Model string
	Options         []string // "key=value"
	Wait, Poll      time.Duration
}

// New creates the control directory of a child called name, from the
// caller's own config.json (an agent) or the flags alone (root), and reports
// what the daemon makes of it. It writes the same directory an agent could
// create by hand: the daemon's poll does the rest.
func New(l layout.Layout, root bool, home, name string, o NewOptions, w io.Writer) error {
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
	var config plugin.Config
	if root {
		if o.Model == "" {
			return errors.New("root must choose a model: agent new NAME --model M")
		}
	} else {
		data, err := os.ReadFile(filepath.Join(home, "config.json"))
		switch {
		case os.IsNotExist(err) && o.Provider != "" && o.Model != "":
			// The flags say everything a starting config would.
		case err != nil:
			return fmt.Errorf("no starting config to copy (%v); pass --provider and --model", err)
		default:
			if config, err = plugin.ParseConfig(data); err != nil {
				return fmt.Errorf("%s: %w", filepath.Join(home, "config.json"), err)
			}
		}
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
	// Options belong to a model, so choosing another provider or model starts
	// from none; only --option applies then.
	if o.Provider != "" || o.Model != "" {
		config.Options = nil
	}
	if o.Provider != "" {
		config.Provider = o.Provider
	}
	if o.Model != "" {
		config.Model = o.Model
	}
	if config.Provider == "" {
		if len(installed) != 1 {
			return fmt.Errorf("choose a provider: agent new NAME --provider P (installed: %s)", strings.Join(installed, ", "))
		}
		config.Provider = installed[0]
	}
	describe := providers[config.Provider]
	for _, option := range o.Options {
		key, value, ok := strings.Cut(option, "=")
		if !ok || key == "" {
			return fmt.Errorf("option %q is not key=value (--option k=v)", option)
		}
		var parsed any = value
		for _, m := range describe.Models {
			if m.ID == config.Model && m.Options[key].Type == "integer" {
				n, err := strconv.Atoi(value)
				if err != nil {
					return fmt.Errorf("option %q: %q is not an integer", key, value)
				}
				parsed = float64(n)
			}
		}
		if config.Options == nil {
			config.Options = map[string]any{}
		}
		config.Options[key] = parsed
	}
	if err := plugin.Validate(config, installed, describe); err != nil {
		return err
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	// Root's control directories stay private; an agent's must be readable by
	// the child's user, who has to traverse the parent's home to reach it.
	mode := os.FileMode(0o755)
	if root {
		mode = 0o700
	}
	if err := os.MkdirAll(filepath.Dir(ctrl), mode); err != nil {
		return err
	}
	if err := os.Mkdir(ctrl, mode); err != nil {
		return err
	}
	// The dot-name rename keeps the daemon from reading half a config.
	temp := filepath.Join(ctrl, ".config.json")
	if err := os.WriteFile(temp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(temp, filepath.Join(ctrl, "config.json")); err != nil {
		return err
	}
	return awaitAdmission(ctrl, name, o, w)
}

// awaitAdmission polls the control directory for the home link the daemon
// makes at admission, or the ERROR it writes instead.
func awaitAdmission(ctrl, name string, o NewOptions, w io.Writer) error {
	deadline := time.Now().Add(o.Wait)
	for {
		if target, err := os.Readlink(filepath.Join(ctrl, "home")); err == nil {
			fmt.Fprintf(w, "Created %s: home %s\n", name, target)
			return nil
		}
		if data, err := os.ReadFile(filepath.Join(ctrl, "ERROR")); err == nil && len(strings.TrimSpace(string(data))) > 0 {
			return fmt.Errorf("%s was not started: %s", name, strings.TrimSpace(sanitize(string(data))))
		}
		if !time.Now().Before(deadline) {
			fmt.Fprintf(w, "Created %s; the daemon has not admitted it yet (agent log %s shows why).\n", name, name)
			return nil
		}
		time.Sleep(o.Poll)
	}
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
