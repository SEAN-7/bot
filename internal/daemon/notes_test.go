package daemon

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

const testNotes = "# Release notes\n\nintro\n\n## 0.4.0\n- new thing\n\n### detail\nmore\n\n## 0.3.1\n- old thing\n\n## 0.3.0\n- older thing\n"

// notesHarness runs version 0.4.0 with main's pool entry recording notes seen
// up to seen, as an upgrade leaves it.
func notesHarness(t *testing.T, seen string, steps ...string) *harness {
	t.Helper()
	h := newHarness(t, steps...)
	h.cfg.Version, h.cfg.ReleaseNotes = "0.4.0", testNotes
	if seen != "" {
		entry := `[{"user":"agent-1","agent":"main","state":"active","notes":"` + seen + `"}]`
		if err := os.WriteFile(h.cfg.PoolFile(), []byte(entry), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

// poolNotes is the notes version pool.json records for agent id.
func poolNotes(h *harness, id string) string {
	var list []poolEntry
	json.Unmarshal([]byte(poolJSON(h)), &list)
	for _, e := range list {
		if e.Agent == id {
			return e.Notes
		}
	}
	return ""
}

func TestNotesSince(t *testing.T) {
	got := notesSince(testNotes, [3]int{0, 3, 0}, [3]int{0, 4, 0})
	want := []string{"## 0.4.0\n- new thing\n\n### detail\nmore", "## 0.3.1\n- old thing"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
	if got := notesSince(testNotes, [3]int{0, 3, 0}, [3]int{0, 3, 5}); len(got) != 1 || !strings.HasPrefix(got[0], "## 0.3.1") {
		t.Fatalf("capped at the running version: %q", got)
	}
	if got := notesSince(testNotes, [3]int{0, 4, 0}, [3]int{0, 4, 0}); len(got) != 0 {
		t.Fatalf("nothing newer: %q", got)
	}
}

// An idle agent is not woken for notes.
func TestNotesWaitForWake(t *testing.T) {
	h := notesHarness(t, "0.3.1")
	h.start()
	time.Sleep(200 * time.Millisecond)
	if n := countNotes(h.mainHome, "-harness-update.md"); n != 0 {
		t.Fatalf("%d notes delivered to an idle agent", n)
	}
	if runs := h.runs(h.mainID); len(runs) != 0 {
		t.Fatalf("runs %v", runs)
	}
	if got := poolNotes(h, h.mainID); got != "0.3.1" {
		t.Fatalf("notes %q", got)
	}
}

func TestNotesDeliveredWithNextWake(t *testing.T) {
	h := notesHarness(t, "0.3.1", done())
	h.start()
	h.sendOwner("wake up")
	records := h.waitEnds(h.mainID, 1)
	var note string
	for _, r := range records {
		if f, _ := r["file"].(string); strings.HasSuffix(f, "-harness-update.md") {
			note, _ = r["content"].(string)
			if from := r["from"]; from != "data" {
				t.Fatalf("from %v", from)
			}
		}
	}
	if !strings.HasPrefix(note, "The harness was upgraded to v0.4.0. What changed for you:\n\n## 0.4.0\n") ||
		!strings.Contains(note, "new thing") || strings.Contains(note, "## 0.3.1") {
		t.Fatalf("note %q", note)
	}
	if !h.sawRecord("-harness-update.md", "## 0.4.0") {
		t.Fatal("the model did not see the note in its first request")
	}
	if got := poolNotes(h, h.mainID); got != "0.4.0" {
		t.Fatalf("notes %q", got)
	}
}

func TestNotesDeliveredOnce(t *testing.T) {
	h := notesHarness(t, "0.3.1", done(), done())
	h.start()
	h.sendOwner("first")
	h.waitEnds(h.mainID, 1)
	h.sendOwner("second")
	records := h.waitEnds(h.mainID, 2)
	for _, r := range records {
		if f, _ := r["file"].(string); strings.HasSuffix(f, "-harness-update.md") {
			t.Fatalf("second wake got a note: %v", r)
		}
	}
	if n := countNotes(h.mainHome, "-harness-update.md"); n != 1 {
		t.Fatalf("%d notes", n)
	}
}

func TestNewAgentGetsNoNotes(t *testing.T) {
	h := notesHarness(t, "", done())
	h.start()
	waitFor(t, "notes recorded", func() bool { return poolNotes(h, h.mainID) == "0.4.0" })
	h.sendOwner("hello")
	h.waitEnds(h.mainID, 1)
	if n := countNotes(h.mainHome, "-harness-update.md"); n != 0 {
		t.Fatalf("%d notes for a new agent", n)
	}
}

func TestDevBuildDeliversNoNotes(t *testing.T) {
	h := notesHarness(t, "0.3.1", done())
	h.cfg.Version = "dev"
	h.start()
	h.sendOwner("hello")
	h.waitEnds(h.mainID, 1)
	if n := countNotes(h.mainHome, "-harness-update.md"); n != 0 {
		t.Fatalf("%d notes from a dev build", n)
	}
	if got := poolNotes(h, h.mainID); got != "0.3.1" {
		t.Fatalf("notes %q", got)
	}
}

// An entry without notes (recorded before they existed) has nothing to
// catch up on: admission records the running version, delivering nothing.
func TestNoRecordedNotesDeliversNothing(t *testing.T) {
	h := notesHarness(t, "", done())
	os.WriteFile(h.cfg.PoolFile(), []byte(`[{"user":"agent-1","agent":"main","state":"active"}]`), 0o600)
	h.start()
	h.sendOwner("hello")
	h.waitEnds(h.mainID, 1)
	if n := countNotes(h.mainHome, "-harness-update.md"); n != 0 {
		t.Fatalf("%d notes", n)
	}
	if got := poolNotes(h, h.mainID); got != "0.4.0" {
		t.Fatalf("notes %q", got)
	}
}
