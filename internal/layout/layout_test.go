package layout

import (
	"os"
	"path/filepath"
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
