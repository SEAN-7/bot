package daemon

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/SEAN-7/bot/internal/journal"
)

func inboxRecordFrom(records []map[string]any, content string) string {
	for _, r := range records {
		if r["type"] == "inbox" && r["content"] == content {
			return r["from"].(string)
		}
	}
	return ""
}

func TestCrashRecovery(t *testing.T) {
	h := newHarness(t, done(), done())
	// main held its user when the daemon stopped mid-run.
	h.cfg.System.(*fakeSystem).seed("agent-1", "main", h.mainHome)
	w, err := journal.Create(h.cfg.JournalDir(h.mainID), time.Now().Add(-time.Hour), -1)
	if err != nil {
		t.Fatal(err)
	}
	w.Record("start", nil)
	w.Close()
	h.start()
	records := h.waitEnds(h.mainID, 2)
	found := false
	note := fmt.Sprintf(MsgInterruptedFmt, w.Path())
	for _, r := range records {
		if r["type"] == "inbox" && strings.HasSuffix(r["file"].(string), "-interrupted.md") && r["content"] == note+"\n" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no interrupted note: %v", records)
	}
	if _, clean, _ := journal.Latest(h.cfg.JournalDir(h.mainID)); !clean {
		t.Fatal("latest run not clean")
	}
	h.stop()
	h.start()
	time.Sleep(300 * time.Millisecond)
	if n := countNotes(h.mainHome, "-interrupted.md"); n != 1 {
		t.Fatalf("%d interrupted notes after a clean restart", n)
	}
}
