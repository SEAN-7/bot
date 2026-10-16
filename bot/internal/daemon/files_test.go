package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeProvidersCache writes providers.json as root's describe would, with
// the replay plugin's one model.
func writeProvidersCache(t *testing.T, h *harness) {
	t.Helper()
	cache := `{"providers":{"replay":{"models":[{"id":"replay-1"}]}}}`
	if err := os.WriteFile(h.cfg.ProvidersCache(), []byte(cache), 0o644); err != nil {
		t.Fatal(err)
	}
}

// An invalid starting config.json takes no user: the parent is told and can
// fix it in place, and the agent is admitted once it is valid.
func TestInvalidStartingConfigHeldBack(t *testing.T) {
	h := newHarness(t)
	writeProvidersCache(t, h)
	h.start()
	ctrl := filepath.Join(h.mainHome, "agents", "c")
	os.MkdirAll(ctrl, 0o755)
	os.WriteFile(filepath.Join(ctrl, "config.json"), []byte(`{"provider":"replay","model":"nope"}`), 0o644)
	errorFile := filepath.Join(ctrl, "ERROR")
	waitFor(t, "ERROR", func() bool { return fileContains(errorFile, `config.json: unknown model "nope"`) })
	waitFor(t, "parent note", func() bool { return countNotes(h.mainHome, "-subagent-error.md") == 1 })
	time.Sleep(100 * time.Millisecond)
	if n := countNotes(h.mainHome, "-subagent-error.md"); n != 1 {
		t.Fatalf("%d parent notes", n)
	}
	if _, err := os.Lstat(filepath.Join(ctrl, "home")); !os.IsNotExist(err) {
		t.Fatal("admitted with an invalid config")
	}
	if strings.Contains(poolJSON(h), childID("c")) {
		t.Fatalf("a user was assigned: %s", poolJSON(h))
	}
	os.WriteFile(filepath.Join(ctrl, "config.json"), []byte(testConfig), 0o644)
	h.childHome(ctrl)
	// Admission clears it just after writing the home link.
	waitFor(t, "ERROR cleared at admission", func() bool { _, err := os.Stat(errorFile); return os.IsNotExist(err) })
}

// Without a providers cache the starting config is not checked: the first
// run's own check reports a problem.
func TestStartingConfigUncheckedWithoutCache(t *testing.T) {
	h := newHarness(t)
	h.start()
	ctrl := filepath.Join(h.mainHome, "agents", "c")
	os.MkdirAll(ctrl, 0o755)
	os.WriteFile(filepath.Join(ctrl, "config.json"), []byte(`{"provider":"replay","model":"nope"}`), 0o644)
	h.childHome(ctrl)
}

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
