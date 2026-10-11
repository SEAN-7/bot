package seed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeedDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("mine"), 0o644)
	if err := Write(dir, []byte(`{"provider":"p","model":"m"}`)); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "README.md")); string(data) != "mine" {
		t.Fatalf("README overwritten: %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "TASK.md")); string(data) != "No task yet.\n" {
		t.Fatalf("TASK.md %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "config.json")); !strings.Contains(string(data), `"model":"m"`) {
		t.Fatalf("config %q", data)
	}
	for _, sub := range []string{"inbox/processed", "agents"} {
		if info, err := os.Stat(filepath.Join(dir, sub)); err != nil || !info.IsDir() {
			t.Fatalf("%s missing", sub)
		}
	}
	os.WriteFile(filepath.Join(dir, "config.json"), []byte("edited"), 0o644)
	Write(dir, []byte(`{}`))
	if data, _ := os.ReadFile(filepath.Join(dir, "config.json")); string(data) != "edited" {
		t.Fatal("config overwritten on reseed")
	}
}

func TestSeedReadmeCoversTopics(t *testing.T) {
	for _, want := range []string{"TASK.md", "archive", "journal", "systemd-run --user", "agent provider describe", `"from": "owner"`, "cron", "~/agents/", "dot", "Never poll", "agent message"} {
		if !strings.Contains(README, want) {
			t.Errorf("README lacks %q", want)
		}
	}
}
