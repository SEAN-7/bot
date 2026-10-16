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
	Homes string // parent of the agent users' home directories
	Etc   string // root-only configuration, plugins, credentials and root's agents
	Lib   string // daemon state
	Log   string // journals
}

func Default() Layout {
	return Layout{Homes: "/home", Etc: "/etc/agent", Lib: "/var/lib/agent", Log: "/var/log/agent"}
}

func (l Layout) Plugins() string        { return filepath.Join(l.Etc, "plugins") }
func (l Layout) Credentials() string    { return filepath.Join(l.Etc, "credentials") }
func (l Layout) SettingsFile() string   { return filepath.Join(l.Etc, "settings.json") }
func (l Layout) ProvidersCache() string { return filepath.Join(l.Lib, "providers.json") }

// Manual is the read-only directory agents read as ~/manual: the manual and
// the release notes of the running version.
func (l Layout) Manual() string { return filepath.Join(l.Lib, "manual") }

// RootHome is root's place in the agent tree, where an agent has its home:
// root's control directories are RootHome/agents/NAME.
func (l Layout) RootHome() string { return l.Etc }

// Home is the home directory of an agent user.
func (l Layout) Home(user string) string { return filepath.Join(l.Homes, user) }

// Inboxes is the root-owned directory holding every agent's inbox, which
// any user may traverse but not list: a subagent reaches its owner's inbox
// without entering the owner's home. It shares a filesystem with the homes,
// so a file staged in a home is renamed in.
func (l Layout) Inboxes() string { return filepath.Join(l.Homes, ".inbox") }

// Inbox is an agent user's inbox directory, which ~/inbox links to.
func (l Layout) Inbox(user string) string { return filepath.Join(l.Inboxes(), user) }

// JournalDir mirrors the agent's id, its path from root (main,
// main/agents/research), under the log root.
func (l Layout) JournalDir(agentID string) string { return filepath.Join(l.Log, agentID) }

type Settings struct {
	MaxAgents int `json:"max_agents"`
}

// LoadSettings reads settings.json, filling defaults for a missing file or
// field. On error it still returns usable defaults alongside the error.
func LoadSettings(path string) (Settings, error) {
	var s Settings
	data, err := os.ReadFile(path)
	if err == nil {
		if jerr := json.Unmarshal(data, &s); jerr != nil {
			s, err = Settings{}, jerr
		}
	} else if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	if s.MaxAgents <= 0 {
		s.MaxAgents = 16
	}
	return s, err
}
