package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Admission creates the parent's spool for the child, and startup root's
// spools, closed to others.
func TestSpoolsCreated(t *testing.T) {
	h := newHarness(t)
	h.start()
	ctrl := makeChild(t, h, "c", true)
	h.childHome(ctrl)
	for _, dir := range []string{
		filepath.Join(h.mainHome, ".messages", "agents", "c"),
		filepath.Join(h.cfg.RootHome(), ".messages"),
		filepath.Join(h.cfg.RootHome(), ".messages", "agents"),
	} {
		statMode(t, dir, os.ModeDir|0o750)
	}
}

// A message over 64 KiB is delivered cut short, and its sender is told.
func TestTruncatedMessageReported(t *testing.T) {
	h := newHarness(t)
	h.start()
	ctrl := makeChild(t, h, "c", true)
	home := h.childHome(ctrl)
	long := strings.Repeat("x", messageLimit+10)
	h.queue(filepath.Join(home, ".messages"), long)
	waitFor(t, "child told", func() bool { return countNotes(home, "-truncated.md") == 1 })
	if !inboxHas(home, "-truncated.md", MsgTruncatedUp) {
		t.Fatal("child's note")
	}
	h.spoolDown(h.mainHome, "c", long)
	waitFor(t, "parent told", func() bool { return countNotes(h.mainHome, "-truncated.md") == 1 })
	if !inboxHas(h.mainHome, "-truncated.md", "Message to c was truncated to 64 KiB.") {
		t.Fatal("parent's note")
	}
}

// inboxHas reports whether an inbox file of the agent at home whose name
// ends in suffix holds text, whether or not a run has collected it yet (the
// note itself wakes the agent).
func inboxHas(home, suffix, text string) bool {
	for _, dir := range []string{"inbox", "inbox/processed"} {
		entries, _ := os.ReadDir(filepath.Join(home, dir))
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), suffix) {
				data, _ := os.ReadFile(filepath.Join(home, dir, e.Name()))
				if strings.Contains(string(data), text) {
					return true
				}
			}
		}
	}
	return false
}

func TestManualHasVersion(t *testing.T) {
	cfg := DefaultConfig("")
	if v := cfg.ManualFiles["version"]; v != cfg.Version+"\n" {
		t.Fatalf("version file %q", v)
	}
}
