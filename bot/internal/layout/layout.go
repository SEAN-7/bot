// Package layout names the harness's fixed paths on the VM.
package layout

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

type Layout struct {
	Homes string // parent of the pool users' home directories
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
func (l Layout) DeliveredFile() string  { return filepath.Join(l.Lib, "delivered.jsonl") }
func (l Layout) ProvidersCache() string { return filepath.Join(l.Lib, "providers.json") }

// PoolFile records which pool user each agent holds.
func (l Layout) PoolFile() string { return filepath.Join(l.Lib, "pool.json") }

// RootHome is root's place in the agent tree, where an agent has its home:
// root's control directories are RootHome/agents/NAME and its spool for
// child NAME is RootHome/.messages/agents/NAME.
func (l Layout) RootHome() string { return l.Etc }

// PoolHome is the home directory of a pool user.
func (l Layout) PoolHome(user string) string { return filepath.Join(l.Homes, user) }

// PoolUsers names the agent users for a max_agents setting, which bounds the
// whole tree: agent-1 through agent-<maxAgents>.
func PoolUsers(maxAgents int) []string {
	var users []string
	for i := 1; i <= maxAgents; i++ {
		users = append(users, fmt.Sprintf("agent-%d", i))
	}
	return users
}

// ConversationFile is an agent's conversation with its owner, written only
// by the daemon, in its journal directory.
func (l Layout) ConversationFile(agentID string) string {
	return filepath.Join(l.JournalDir(agentID), "messages.jsonl")
}

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
