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

// The harness writes nothing to an agent for an upgrade: the start record
// and ~/manual/ carry the version, and the agent decides what to do with it.
func TestNoUpgradeNoteWritten(t *testing.T) {
	h := newHarness(t, done())
	h.cfg.Version = "0.8.0"
	w, err := journal.Create(h.cfg.JournalDir(h.mainID), time.Now().Add(-time.Hour), -1)
	if err != nil {
		t.Fatal(err)
	}
	w.Record("start", map[string]any{"agent": h.mainID, "version": "0.5.2"})
	w.End("done")
	h.start()
	h.sendOwner("hello")
	for _, r := range h.waitEnds(h.mainID, 2) {
		if f, _ := r["file"].(string); r["type"] == "inbox" && strings.HasSuffix(f, "-harness-upgrade.md") {
			t.Fatalf("upgrade note delivered: %v", r)
		}
	}
	if n := countNotes(h.mainHome, "-harness-upgrade.md"); n != 0 {
		t.Fatalf("%d upgrade notes in the inbox", n)
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
		"MsgInvalidRequestFmt": MsgInvalidRequestFmt, "MsgImageShownFmt": MsgImageShownFmt,
		"MsgImageJournalFmt": MsgImageJournalFmt, "MsgBinaryFmt": MsgBinaryFmt,
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
