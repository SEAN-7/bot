package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// writeAs writes text to path as the given user, staged in dir and renamed
// in, the way a sender does. It fails the test if the user cannot.
func writeAs(t *testing.T, user, dir, path, text string) {
	t.Helper()
	uid, gid := lookupIDs(t, user)
	cmd := exec.Command("sh", "-c", fmt.Sprintf("printf %%s %s > %s/.m && mv %s/.m %s", shellQuote(text), dir, dir, path))
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("write %s as %s: %v %s", path, user, err, out)
	}
}

// TestPrivilegeLabels: an inbox file's label is who wrote it. Root's file in
// main's inbox is the owner's; a subagent's file, written through the grant
// on its owner's inbox, is the subagent's; main's own file and a file by an
// unrelated user are data.
func TestPrivilegeLabels(t *testing.T) {
	h, mainUser := rootHarness(t, doneFor(childMark), doneFor(parentMark))
	h.start()
	h.markParent()
	ctrl := rootChild(t, h, "c")
	home := h.childHome(ctrl)
	cUser := ownerName(t, home)
	// The subagent needs a run for its ~/owner link; its message goes
	// through it as a command would.
	h.drop(home, "go.md", "go")
	h.waitEnds(childID("c"), 1)
	inbox := filepath.Join(h.mainHome, "inbox")
	writeAs(t, cUser, home, filepath.Join(home, "owner", "from-c.md"), "report")
	writeAs(t, mainUser, h.mainHome, filepath.Join(inbox, "from-main.md"), "note to self")
	other := "agent-test-provider" // an unrelated user: never an owner or a subagent
	ouid, ogid := lookupIDs(t, other)
	stage := filepath.Join(h.root, "stage")
	os.MkdirAll(stage, 0o777)
	os.Chmod(stage, 0o777)
	os.WriteFile(filepath.Join(stage, "from-other.md"), []byte("stranger"), 0o644)
	os.Chown(filepath.Join(stage, "from-other.md"), ouid, ogid)
	// Root moves it in: a file whose owner is nobody the agent knows.
	if err := os.Rename(filepath.Join(stage, "from-other.md"), filepath.Join(inbox, "from-other.md")); err != nil {
		t.Fatal(err)
	}
	h.sendOwner("hello")
	// Each file wakes main, so they may arrive over several runs.
	want := map[string]string{"hello": "owner", "report": "agents/c", "note to self": "data", "stranger": "data"}
	labels := func() map[string]string {
		got := map[string]string{}
		for _, run := range h.runs(h.mainID) {
			for content := range want {
				if from := inboxRecordFrom(readRecords(run), content); from != "" {
					got[content] = from
				}
			}
		}
		return got
	}
	waitFor(t, "every file collected", func() bool { return len(labels()) == len(want) })
	for content, from := range want {
		if got := labels()[content]; got != from {
			t.Errorf("%q labelled %q, want %q", content, got, from)
		}
	}
}

// TestPrivilegeOwnerLink: a subagent's ~/owner leads to its owner's inbox,
// where it can create files but neither list them nor read the owner's.
func TestPrivilegeOwnerLink(t *testing.T) {
	h, _ := rootHarness(t, doneFor(parentMark))
	h.setScript(matchStep("task-for-c", "ls ~/owner; echo ls=$?; cat "+h.mainHome+"/README.md; echo cat=$?; ls "+h.cfg.Inboxes()+"; echo lsall=$?; printf hi > ~/.m && mv ~/.m ~/owner/hi.md; echo mv=$?", 100))
	h.start()
	h.markParent()
	ctrl := rootChild(t, h, "c")
	home := h.childHome(ctrl)
	h.drop(home, "task.md", "task-for-c")
	h.waitEnds(childID("c"), 1)
	out := h.toolOutput("mv=")
	exitCodes(t, out, false, "ls", "cat", "lsall")
	exitCodes(t, out, true, "mv")
	if target, err := os.Readlink(filepath.Join(home, "owner")); err != nil || target != h.cfg.Inbox(h.cfg.UserPrefix+"1") {
		t.Fatalf("owner link %q %v", target, err)
	}
	if !strings.Contains(h.logs.String(), "main: admitted") {
		t.Fatal(h.logs)
	}
	// Main is woken by it and may have collected it already.
	inbox := h.cfg.Inbox(h.cfg.UserPrefix + "1")
	if _, err := os.Stat(filepath.Join(inbox, "hi.md")); err != nil {
		if _, err := os.Stat(filepath.Join(inbox, "processed", "hi.md")); err != nil {
			t.Fatalf("message not in main's inbox: %v", err)
		}
	}
}
