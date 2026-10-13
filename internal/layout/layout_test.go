package layout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSettingsDefaults(t *testing.T) {
	s, err := LoadSettings(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if s.MaxAgents != 16 || s.Repository != "SEAN-7/bot" {
		t.Fatalf("got %+v", s)
	}
}

func TestLoadSettingsOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"max_agents":3}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.MaxAgents != 3 || s.Repository != "SEAN-7/bot" {
		t.Fatalf("got %+v", s)
	}
}

func TestJournalDir(t *testing.T) {
	got := Default().JournalDir("main/agents/r")
	if got != "/var/log/agent/main/agents/r" {
		t.Fatalf("got %s", got)
	}
}

func TestPaths(t *testing.T) {
	l := Default()
	for got, want := range map[string]string{
		l.Plugins():        "/etc/agent/plugins",
		l.Credentials():    "/etc/agent/credentials",
		l.SettingsFile():   "/etc/agent/settings.json",
		l.RootHome():       "/etc/agent",
		l.DeliveredFile():  "/var/lib/agent/delivered.jsonl",
		l.ProvidersCache(): "/var/lib/agent/providers.json",
	} {
		if got != want {
			t.Errorf("got %s want %s", got, want)
		}
	}
}

func TestPoolUsers(t *testing.T) {
	u := PoolUsers(16)
	if len(u) != 16 || u[0] != "agent-1" || u[15] != "agent-16" {
		t.Fatal(u)
	}
	if len(PoolUsers(0)) != 0 {
		t.Fatal("PoolUsers(0) should be empty")
	}
}

// TestPoolUsersCoverMaxAgents: max_agents bounds the whole tree, main
// included, so the pool has one user per agent.
func TestPoolUsersCoverMaxAgents(t *testing.T) {
	if u := PoolUsers(3); strings.Join(u, " ") != "agent-1 agent-2 agent-3" {
		t.Fatal(u)
	}
}

func TestPoolPaths(t *testing.T) {
	l := Default()
	for got, want := range map[string]string{
		l.Homes:               "/home",
		l.PoolFile():          "/var/lib/agent/pool.json",
		l.PoolHome("agent-3"): "/home/agent-3",
	} {
		if got != want {
			t.Errorf("got %s want %s", got, want)
		}
	}
}

func TestConversationFile(t *testing.T) {
	l := Default()
	if got := l.ConversationFile("main"); got != "/var/log/agent/main/messages.jsonl" {
		t.Fatalf("main: %s", got)
	}
	if got := l.ConversationFile("main/agents/x"); got != "/var/log/agent/main/agents/x/messages.jsonl" {
		t.Fatalf("child: %s", got)
	}
}
