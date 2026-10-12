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
	got := Default().JournalDir("/home/agent/agents/r")
	if got != "/var/log/agent/home/agent/agents/r" {
		t.Fatalf("got %s", got)
	}
}

func TestPaths(t *testing.T) {
	l := Default()
	for got, want := range map[string]string{
		l.Plugins():        "/etc/agent/plugins",
		l.Credentials():    "/etc/agent/credentials",
		l.SettingsFile():   "/etc/agent/settings.json",
		l.OwnerSpool():     "/var/lib/agent/owner",
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
	if len(u) != 15 || u[0] != "agent-1" || u[14] != "agent-15" {
		t.Fatal(u)
	}
	if len(PoolUsers(1)) != 0 || len(PoolUsers(0)) != 0 {
		t.Fatal("PoolUsers(1) should be empty")
	}
}

func TestPoolPaths(t *testing.T) {
	l := Default()
	for got, want := range map[string]string{
		l.Homes:               "/home",
		l.PoolFile():          "/var/lib/agent/pool.json",
		l.OwnerFile():         "/etc/agent/OWNER.md",
		l.PoolHome("agent-3"): "/home/agent-3",
	} {
		if got != want {
			t.Errorf("got %s want %s", got, want)
		}
	}
}

func TestConversationFile(t *testing.T) {
	l := Default()
	if got := l.ConversationFile(l.Home); got != l.MessagesFile() {
		t.Fatalf("main: %s", got)
	}
	got := l.ConversationFile("/home/agent-1")
	if got != "/var/log/agent/home/agent-1/messages.jsonl" {
		t.Fatalf("child: %s", got)
	}
}

// TestMigratingFileMatchesInstaller: install.sh writes the marker the daemon
// checks, so both must name the same path.
func TestMigratingFileMatchesInstaller(t *testing.T) {
	path := Default().MigratingFile()
	if path != "/var/lib/agent/migrating" {
		t.Fatalf("got %s", path)
	}
	script, err := os.ReadFile(filepath.Join("..", "..", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "\nMIGRATING="+path+"\n") {
		t.Fatalf("install.sh does not set MIGRATING=%s", path)
	}
}
