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
	Home  string // main agent directory
	Homes string // parent of the pool users' home directories
	Etc   string // root-only configuration, plugins and credentials
	Lib   string // daemon state
	Log   string // journals
}

func Default() Layout {
	return Layout{Home: "/home/agent", Homes: "/home", Etc: "/etc/agent", Lib: "/var/lib/agent", Log: "/var/log/agent"}
}

func (l Layout) Plugins() string        { return filepath.Join(l.Etc, "plugins") }
func (l Layout) Credentials() string    { return filepath.Join(l.Etc, "credentials") }
func (l Layout) SettingsFile() string   { return filepath.Join(l.Etc, "settings.json") }
func (l Layout) OwnerSpool() string     { return filepath.Join(l.Lib, "owner") }
func (l Layout) DeliveredFile() string  { return filepath.Join(l.Lib, "delivered.jsonl") }
func (l Layout) ProvidersCache() string { return filepath.Join(l.Lib, "providers.json") }

// PoolFile records which pool user each subagent holds.
func (l Layout) PoolFile() string { return filepath.Join(l.Lib, "pool.json") }

// OwnerFile is the owner's description, root-owned and readable by every agent.
func (l Layout) OwnerFile() string { return filepath.Join(l.Etc, "OWNER.md") }

// PoolHome is the home directory of a pool user.
func (l Layout) PoolHome(user string) string { return filepath.Join(l.Homes, user) }

// PoolUsers names the subagent users for a max_agents setting: the main agent
// takes one slot, so agent-1 through agent-<maxAgents-1>.
func PoolUsers(maxAgents int) []string {
	var users []string
	for i := 1; i < maxAgents; i++ {
		users = append(users, fmt.Sprintf("agent-%d", i))
	}
	return users
}

// MigratingFile marks a migration of subagents that stopped part way:
// install.sh writes it, and the daemon does not start while it exists.
func (l Layout) MigratingFile() string { return filepath.Join(l.Lib, "migrating") }

// VersionFile records the last version whose release notes the agent got.
func (l Layout) VersionFile() string { return filepath.Join(l.Lib, "version") }

// MessagesFile is the owner conversation, written only by the daemon.
func (l Layout) MessagesFile() string { return filepath.Join(l.Log, "messages.jsonl") }

// ConversationFile is an agent's own conversation log: the owner conversation
// for the main agent, a file in its journal directory for a subagent.
func (l Layout) ConversationFile(agentID string) string {
	if agentID == l.Home {
		return l.MessagesFile()
	}
	return filepath.Join(l.JournalDir(agentID), "messages.jsonl")
}

// JournalDir mirrors the agent's absolute path under the log root.
func (l Layout) JournalDir(agentDir string) string { return filepath.Join(l.Log, agentDir) }

type Settings struct {
	MaxAgents  int    `json:"max_agents"`
	Repository string `json:"repository"`
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
	if s.Repository == "" {
		s.Repository = "SEAN-7/bot"
	}
	return s, err
}
