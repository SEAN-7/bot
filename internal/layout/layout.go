// Package layout names the harness's fixed paths on the VM.
package layout

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

type Layout struct {
	Home string // main agent directory
	Etc  string // root-only configuration, plugins and credentials
	Lib  string // daemon state
	Log  string // journals
}

func Default() Layout {
	return Layout{Home: "/home/agent", Etc: "/etc/agent", Lib: "/var/lib/agent", Log: "/var/log/agent"}
}

func (l Layout) Plugins() string        { return filepath.Join(l.Etc, "plugins") }
func (l Layout) Credentials() string    { return filepath.Join(l.Etc, "credentials") }
func (l Layout) SettingsFile() string   { return filepath.Join(l.Etc, "settings.json") }
func (l Layout) OwnerSpool() string     { return filepath.Join(l.Lib, "owner") }
func (l Layout) DeliveredFile() string  { return filepath.Join(l.Lib, "delivered.jsonl") }
func (l Layout) ProvidersCache() string { return filepath.Join(l.Lib, "providers.json") }

// JournalDir mirrors the agent's absolute path under the log root.
func (l Layout) JournalDir(agentDir string) string { return filepath.Join(l.Log, agentDir) }

type Settings struct {
	MaxAgents  int    `json:"max_agents"`
	Repository string `json:"repository"`
}

// LoadSettings reads settings.json, filling defaults for a missing file or field.
func LoadSettings(path string) (Settings, error) {
	var s Settings
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return s, err
	}
	if err == nil {
		if err := json.Unmarshal(data, &s); err != nil {
			return s, err
		}
	}
	if s.MaxAgents <= 0 {
		s.MaxAgents = 16
	}
	if s.Repository == "" {
		s.Repository = "SEAN-7/bot"
	}
	return s, nil
}
