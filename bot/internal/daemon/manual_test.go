package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/SEAN-7/bot/docs"
	"github.com/SEAN-7/bot/internal/journal"
	"github.com/SEAN-7/bot/internal/proto"
)

const testNotes = "# Release notes\n\nintro\n\n## 0.6.0\n- new thing\n\n### detail\nmore\n\n## 0.5.2\n- old thing\n\n## 0.5.1\n- older thing\n"

// upgradeHarness runs version 0.6.0 for a main agent whose previous run was
// recorded by version prev ("" for a run from before versions were recorded,
// "none" for no previous run).
func upgradeHarness(t *testing.T, prev string, steps ...string) *harness {
	t.Helper()
	h := newHarness(t, steps...)
	h.cfg.Version, h.cfg.ReleaseNotes = "0.6.0", testNotes
	if prev != "none" {
		w, err := journal.Create(h.cfg.JournalDir(h.mainID), time.Now().Add(-time.Hour), -1)
		if err != nil {
			t.Fatal(err)
		}
		start := map[string]any{"agent": h.mainID}
		if prev != "" {
			start["version"] = prev
		}
		w.Record("start", start)
		w.End("done")
		// An agent that has run is in pool.json, which a journal without it
		// would make the daemon refuse to start.
		if err := os.WriteFile(h.cfg.PoolFile(), []byte(`[{"user":"agent-1","agent":"main","state":"active"}]`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

// upgradeNoteIn returns the content of the upgrade note among records, and
// its label.
func upgradeNoteIn(records []map[string]any) (string, any) {
	for _, r := range records {
		if f, _ := r["file"].(string); r["type"] == "inbox" && strings.HasSuffix(f, "-harness-upgrade.md") {
			content, _ := r["content"].(string)
			return content, r["from"]
		}
	}
	return "", nil
}

func TestNotesSince(t *testing.T) {
	got := notesSince(testNotes, [3]int{0, 5, 1}, [3]int{0, 6, 0})
	want := []string{"## 0.6.0\n- new thing\n\n### detail\nmore", "## 0.5.2\n- old thing"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
	if got := notesSince(testNotes, [3]int{0, 5, 1}, [3]int{0, 5, 9}); len(got) != 1 || !strings.HasPrefix(got[0], "## 0.5.2") {
		t.Fatalf("capped at the running version: %q", got)
	}
	if got := notesSince(testNotes, [3]int{0, 6, 0}, [3]int{0, 6, 0}); len(got) != 0 {
		t.Fatalf("nothing newer: %q", got)
	}
}

func TestUpgradeNoteWithNextRun(t *testing.T) {
	h := upgradeHarness(t, "0.5.2", done(), done())
	h.start()
	h.sendOwner("wake up")
	records := h.waitEnds(h.mainID, 2)
	note, from := upgradeNoteIn(records)
	if from != "data" {
		t.Fatalf("from %v", from)
	}
	if !strings.HasPrefix(note, "The harness was upgraded from v0.5.2 to v0.6.0. What changed for you, newest first (~/manual/notes.md has every release):\n\n## 0.6.0\n") ||
		!strings.Contains(note, "new thing") || strings.Contains(note, "## 0.5.2") {
		t.Fatalf("note %q", note)
	}
	if !h.sawRecord("-harness-upgrade.md", "## 0.6.0") {
		t.Fatal("the model did not see the note in its first request")
	}
	if records[0]["type"] != "start" || records[0]["version"] != "0.6.0" {
		t.Fatalf("start record %v", records[0])
	}
	// The next run is on the same version: no note.
	h.sendOwner("again")
	if note, _ := upgradeNoteIn(h.waitEnds(h.mainID, 3)); note != "" {
		t.Fatalf("second run got a note: %q", note)
	}
	if n := countNotes(h.mainHome, "-harness-upgrade.md"); n != 1 {
		t.Fatalf("%d notes", n)
	}
}

// A run from before versions were recorded gets every section since 0.5.1,
// the last release that delivered notes.
func TestUpgradeNoteForUnversionedRun(t *testing.T) {
	h := upgradeHarness(t, "", done())
	h.start()
	h.sendOwner("wake up")
	note, _ := upgradeNoteIn(h.waitEnds(h.mainID, 2))
	if !strings.HasPrefix(note, "The harness was upgraded to v0.6.0. What changed for you since v0.5.1, newest first (~/manual/notes.md has every release):\n\n## 0.6.0\n") ||
		!strings.Contains(note, "## 0.5.2\n- old thing") || strings.Contains(note, "## 0.5.1") {
		t.Fatalf("note %q", note)
	}
}

// Nothing wakes an idle agent for an upgrade.
func TestUpgradeNoteWaitsForARun(t *testing.T) {
	h := upgradeHarness(t, "0.5.2")
	h.start()
	time.Sleep(200 * time.Millisecond)
	if n := countNotes(h.mainHome, "-harness-upgrade.md"); n != 0 {
		t.Fatalf("%d notes written for an idle agent", n)
	}
	if runs := h.runs(h.mainID); len(runs) != 1 {
		t.Fatalf("runs %v", runs)
	}
}

func TestNoUpgradeNote(t *testing.T) {
	for _, c := range []struct {
		name, prev, version string
	}{
		{"never ran", "none", "0.6.0"},
		{"same version", "0.6.0", "0.6.0"},
		{"downgrade", "0.7.0", "0.6.0"},
		{"previous run was a dev build", "dev", "0.6.0"},
		{"dev build", "0.5.2", "dev"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := upgradeHarness(t, c.prev, done())
			h.cfg.Version = c.version
			h.start()
			h.sendOwner("hello")
			n := 1
			if c.prev != "none" {
				n = 2
			}
			h.waitEnds(h.mainID, n)
			if n := countNotes(h.mainHome, "-harness-upgrade.md"); n != 0 {
				t.Fatalf("%d notes", n)
			}
		})
	}
}

// The manual is written at startup with exactly the embedded files, and each
// run links ~/manual to it.
func TestManualWrittenAndLinked(t *testing.T) {
	h := newHarness(t, done())
	h.cfg.ManualFiles = map[string]string{"manual.md": "# Manual\n", "notes.md": "# Release notes\n"}
	dir := h.cfg.Manual()
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "stale.md"), []byte("old"), 0o600)
	h.start()
	waitFor(t, "manual written", func() bool {
		data, _ := os.ReadFile(filepath.Join(dir, "manual.md"))
		return string(data) == "# Manual\n"
	})
	if data, _ := os.ReadFile(filepath.Join(dir, "notes.md")); string(data) != "# Release notes\n" {
		t.Fatalf("notes.md %q", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "stale.md")); !os.IsNotExist(err) {
		t.Fatalf("stale file kept: %v", err)
	}
	statMode(t, dir, os.ModeDir|0o755)
	statMode(t, filepath.Join(dir, "manual.md"), 0o644)
	h.sendOwner("hello")
	h.waitEnds(h.mainID, 1)
	if target, err := os.Readlink(filepath.Join(h.mainHome, "manual")); err != nil || target != dir {
		t.Fatalf("manual link %q %v", target, err)
	}
}

func statMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode() != want {
		t.Fatalf("%s mode %v, want %v", path, info.Mode(), want)
	}
}

var quoteMarker = regexp.MustCompile(`\n[ \t]*>`)

// normalize collapses whitespace and drops the markdown quote markers the
// manual wraps quoted text in, so wrapping does not matter.
func normalize(s string) string {
	s = quoteMarker.ReplaceAllString(s, "\n")
	return strings.Join(strings.Fields(s), " ")
}

// The manual quotes every string the harness shows an agent. Each literal
// part of each string (the text between its %-verbs) must appear in it, so
// a change to a string fails here until the manual says the same.
func TestManualQuotesTheHarness(t *testing.T) {
	manual := normalize(docs.Manual)
	var sh struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	tool := proto.ShTool()
	if err := json.Unmarshal(tool.InputSchema, &sh); err != nil {
		t.Fatal(err)
	}
	strs := map[string]string{
		"SystemPrompt": SystemPrompt, "OwnerHeader": OwnerHeader, "MsgOwnerPrefix": MsgOwnerPrefix,
		"sh description": tool.Description, "command": sh.Properties["command"].Description, "wait": sh.Properties["wait"].Description,
		"MsgInterruptedFmt": MsgInterruptedFmt, "MsgResetFmt": MsgResetFmt, "MsgTimeoutFmt": MsgTimeoutFmt,
		"MsgOutputClipped": MsgOutputClipped, "MsgStraysFmt": MsgStraysFmt, "MsgInboxClipFmt": MsgInboxClipFmt,
		"MsgOverflowGuardFmt": MsgOverflowGuardFmt, "MsgProviderFmt": MsgProviderFmt, "MsgConfigPrefix": MsgConfigPrefix,
		"MsgSubagentFmt": MsgSubagentFmt, "MsgLimitFmt": MsgLimitFmt, "MsgLimitParentFmt": MsgLimitParentFmt,
		"MsgConfigTooLarge": MsgConfigTooLarge, "MsgUndeliveredFmt": MsgUndeliveredFmt, "MsgImageShownFmt": MsgImageShownFmt,
		"MsgImageTooLargeFmt": MsgImageTooLargeFmt, "MsgImageTooWideFmt": MsgImageTooWideFmt,
		"MsgImageUndecodableFmt": MsgImageUndecodableFmt, "MsgImageTypeFmt": MsgImageTypeFmt,
		"MsgImageNoModelFmt": MsgImageNoModelFmt, "MsgImageJournalFmt": MsgImageJournalFmt,
		"MsgImageBudgetFmt": MsgImageBudgetFmt, "MsgBinaryFmt": MsgBinaryFmt,
		"MsgUpgradeFmt": MsgUpgradeFmt, "MsgUpgradeSinceFmt": MsgUpgradeSinceFmt,
	}
	verb := regexp.MustCompile(`%[-+# 0-9.]*[a-zA-Z%]`)
	for name, s := range strs {
		for _, part := range verb.Split(s, -1) {
			if part = normalize(part); part != "" && !strings.Contains(manual, part) {
				t.Errorf("%s: the manual does not quote %q", name, part)
			}
		}
	}
}
