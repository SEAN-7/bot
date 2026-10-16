package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

// A subagent's message is a file it writes into its owner's inbox through
// ~/owner; the owner's message is a file written into the subagent's inbox.
// The labels come from the files' owners, which one uid cannot show; the
// privilege tests check them.
func TestChildMessageReachesParent(t *testing.T) {
	h := newHarness(t,
		matchStep(childMark, sendMessageText("report: hi"), 100), doneFor(childMark),
		doneFor(parentMark), doneFor(parentMark))
	h.start()
	h.markParent()
	ctrl := makeChild(t, h, "research", true)
	home := h.childHome(ctrl)
	h.sendTo(home, "child-task")
	waitFor(t, "parent gets the report", func() bool { return h.sawRecord("report: hi") })
	if !h.sawRecord("child-task") {
		t.Fatal("child task not delivered")
	}
	waitFor(t, "child's inbox collected", func() bool { return len(inboxNames(home)) == 0 })
}

// Inbox files are collected into processed/ with their modes kept closed
// to others, whoever wrote them.
func TestInboxFilesClosedToOthers(t *testing.T) {
	h := newHarness(t, done(), done())
	h.start()
	h.sendOwner("hi")
	h.waitEnds(h.mainID, 1)
	h.sendOwner("again")
	h.waitEnds(h.mainID, 2)
	entries, _ := os.ReadDir(filepath.Join(h.mainHome, "inbox", "processed"))
	if len(entries) != 2 {
		t.Fatalf("processed %v", entries)
	}
	for _, e := range entries {
		if info, _ := e.Info(); info.Mode().Perm() != 0o644 {
			t.Errorf("%s: %v", e.Name(), info.Mode())
		}
	}
}

// skipIfRoot skips a test whose point is a permission denied, which root
// never sees.
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permission checks do not apply to root")
	}
}
