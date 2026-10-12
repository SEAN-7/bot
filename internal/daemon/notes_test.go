package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testNotes = `# Release notes

## 0.2.1
- Upgrade notes like this one arrive in your inbox.

## 0.2.0
- Follow-ups continue the conversation.

## 0.1.2
- The daily heartbeat is gone.
`

func harnessUpdates(t *testing.T, dir string) []string {
	t.Helper()
	var bodies []string
	for _, sub := range []string{"inbox", "inbox/processed"} {
		matches, _ := filepath.Glob(filepath.Join(dir, sub, "*-harness-update.md"))
		for _, m := range matches {
			data, _ := os.ReadFile(m)
			bodies = append(bodies, string(data))
		}
	}
	return bodies
}

func notesHarness(t *testing.T, version, recorded string) *harness {
	h := newHarness(t, done())
	h.cfg.Version, h.cfg.ReleaseNotes = version, testNotes
	if recorded != "" {
		os.WriteFile(h.cfg.VersionFile(), []byte(recorded+"\n"), 0o644)
	}
	return h
}

func TestReleaseNotesDeliveredOnUpgrade(t *testing.T) {
	h := notesHarness(t, "0.2.1", "0.2.0")
	h.start()
	waitFor(t, "release notes", func() bool { return len(harnessUpdates(t, h.cfg.Home)) == 1 })
	body := harnessUpdates(t, h.cfg.Home)[0]
	if !strings.Contains(body, "0.2.1") || !strings.Contains(body, "Upgrade notes like this") || strings.Contains(body, "Follow-ups continue") {
		t.Fatalf("wrong notes delivered:\n%s", body)
	}
	if data, _ := os.ReadFile(h.cfg.VersionFile()); strings.TrimSpace(string(data)) != "0.2.1" {
		t.Fatalf("version record %q", data)
	}
}

func TestReleaseNotesAllWhenNoRecord(t *testing.T) {
	h := notesHarness(t, "0.2.1", "")
	h.start()
	waitFor(t, "release notes", func() bool { return len(harnessUpdates(t, h.cfg.Home)) == 1 })
	body := harnessUpdates(t, h.cfg.Home)[0]
	for _, want := range []string{"Upgrade notes like this", "Follow-ups continue", "heartbeat is gone"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q:\n%s", want, body)
		}
	}
}

func TestNoReleaseNotesWhenUnchanged(t *testing.T) {
	h := notesHarness(t, "0.2.1", "0.2.1")
	h.start()
	h.drop(h.cfg.Home, "go.md", "go") // something else happens; no update note appears
	h.waitEnds(h.cfg.Home, 1)
	if n := len(harnessUpdates(t, h.cfg.Home)); n != 0 {
		t.Fatalf("%d update notes for an unchanged version", n)
	}
}

func TestNoReleaseNotesForDevBuild(t *testing.T) {
	h := notesHarness(t, "dev", "")
	h.start()
	h.drop(h.cfg.Home, "go.md", "go")
	h.waitEnds(h.cfg.Home, 1)
	if n := len(harnessUpdates(t, h.cfg.Home)); n != 0 {
		t.Fatalf("dev build delivered %d update notes", n)
	}
	if _, err := os.Stat(h.cfg.VersionFile()); !os.IsNotExist(err) {
		t.Fatal("dev build wrote a version record")
	}
}
