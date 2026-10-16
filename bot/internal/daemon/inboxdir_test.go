package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An agent's inbox lives outside its home, under a root-owned directory
// every user may traverse, so a subagent reaches it without entering the
// home; ~/inbox links to it.
func TestInboxOutsideHome(t *testing.T) {
	h := newHarness(t, done())
	h.start()
	inbox := h.cfg.Inbox("agent-1")
	if info, err := os.Lstat(inbox); err != nil || !info.IsDir() || info.Mode().Perm() != 0o770 || info.Mode()&os.ModeSticky == 0 {
		t.Fatalf("inbox %v %v", info, err)
	}
	if info, err := os.Lstat(h.cfg.Inboxes()); err != nil || !info.IsDir() || info.Mode().Perm() != 0o711 {
		t.Fatalf("inboxes %v %v", info, err)
	}
	if target, err := os.Readlink(filepath.Join(h.mainHome, "inbox")); err != nil || target != inbox {
		t.Fatalf("~/inbox %q %v", target, err)
	}
	if info, err := os.Lstat(filepath.Join(inbox, "processed")); err != nil || !info.IsDir() || info.Mode().Perm() != 0o750 {
		t.Fatalf("processed %v %v", info, err)
	}
	if !h.cfg.System.(*fakeSystem).inOrder("home agent-1", "inbox agent-1", "linger agent-1") {
		t.Fatal(h.cfg.System)
	}
	h.sendOwner("hi")
	if from := inboxRecordFrom(h.waitEnds(h.mainID, 1), "hi"); from != "owner" {
		t.Fatalf("labelled %q", from)
	}
}

// An inbox from a release before 0.9.0 is a real directory in the home:
// its pending files move to the new inbox, the directory is kept as
// inbox.0.8.0 with its processed/ history, and ~/inbox becomes the link.
func TestLegacyInboxMoved(t *testing.T) {
	h := newHarness(t, done())
	// An upgraded agent keeps its user, so its home is not cleared.
	h.cfg.System.(*fakeSystem).seed("agent-1", "main", h.mainHome)
	old := filepath.Join(h.mainHome, "inbox")
	os.MkdirAll(filepath.Join(old, "processed"), 0o750)
	os.WriteFile(filepath.Join(old, "processed", "earlier.md"), []byte("read"), 0o644)
	os.WriteFile(filepath.Join(old, "pending.md"), []byte("unread"), 0o644)
	h.start()
	records := h.waitEnds(h.mainID, 1)
	if from := inboxRecordFrom(records, "unread"); from == "" {
		t.Fatalf("pending file not collected: %v", records)
	}
	if target, err := os.Readlink(old); err != nil || target != h.cfg.Inbox("agent-1") {
		t.Fatalf("~/inbox %q %v", target, err)
	}
	if _, err := os.Stat(filepath.Join(h.mainHome, "inbox.0.8.0", "processed", "earlier.md")); err != nil {
		t.Fatalf("history not kept: %v", err)
	}
}

// Removal takes the inbox with the user.
func TestRemovalDeletesInbox(t *testing.T) {
	h := newHarness(t)
	h.start()
	ctrl := makeChild(t, h, "c", true)
	h.childHome(ctrl)
	inbox := h.cfg.Inbox("agent-2")
	if _, err := os.Stat(inbox); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(ctrl)
	waitFor(t, "inbox gone", func() bool { _, err := os.Lstat(inbox); return os.IsNotExist(err) })
}

// A user marked removed by an earlier daemon loses its grant on its parent's
// inbox when the next start deletes it, though the parent is not running
// yet: the parent is found by its user, and the uid may come back.
func TestRevokeOnStartupRemoval(t *testing.T) {
	h := newHarness(t)
	sys := h.cfg.System.(*fakeSystem)
	sys.seed("agent-1", "main", h.mainHome)
	os.MkdirAll(h.mainHome, 0o755)
	home := h.cfg.Home("agent-2")
	os.MkdirAll(home, 0o755)
	sys.seed("agent-2", removedMark+childID("c"), home)
	h.start()
	waitFor(t, "userdel", func() bool { return sys.called("userdel agent-2") })
	if !sys.inOrder("revoke agent-1 "+itoa(os.Getuid()), "userdel agent-2") {
		t.Fatal(sys)
	}
}

// A subagent name cannot hold a comma or a colon: the id rides in the
// passwd comment field, where either would cut it short.
func TestCleanNameRejectsPasswdSeparators(t *testing.T) {
	for _, bad := range []string{"a,b", "a:b", "research,phase2"} {
		if cleanName(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
	if !cleanName("research-phase2") {
		t.Error("plain name rejected")
	}
}

// A failed migration stops the daemon: nothing else is safe to run on a
// half-migrated passwd. Users the migration does not keep are marked
// removed, so the sweep deletes them through the full sequence.
func TestLegacyPoolMigrationFailsClosed(t *testing.T) {
	h := newHarness(t)
	sys := h.cfg.System.(*fakeSystem)
	for _, name := range []string{"agent-1", "agent-2", "agent-3"} {
		home := h.cfg.Home(name)
		os.MkdirAll(home, 0o755)
		sys.seed(name, "", home)
	}
	pool := filepath.Join(h.cfg.Lib, "pool.json")
	os.WriteFile(pool, []byte(`[{"user":"agent-1","agent":"main","state":"active"},{"user":"agent-2","agent":"main/agents/r","state":"reclaiming"}]`), 0o600)
	sys.setFail("comment agent-1 main", errors.New("usermod: busy"))
	if err := migratePool(h.cfg, sys); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("migration error %v", err)
	}
	if sys.called("userdel agent-2") || sys.called("userdel agent-3") {
		t.Fatalf("deleted during a failed migration: %v", sys)
	}
	if _, err := os.Stat(pool); err != nil {
		t.Fatal("pool.json removed after a failure")
	}
	sys.setFail("comment agent-1 main", nil)
	if err := migratePool(h.cfg, sys); err != nil {
		t.Fatal(err)
	}
	users, _ := sys.Users("agent-")
	want := []User{{"agent-1", "main"}, {"agent-2", removedMark + "main/agents/r"}, {"agent-3", removedMark + "agent-3"}}
	if len(users) != 3 || users[0] != want[0] || users[1] != want[1] || users[2] != want[2] {
		t.Fatalf("users %v, want %v", users, want)
	}
	if sys.called("userdel agent-2") || sys.called("userdel agent-3") {
		t.Fatalf("the migration deleted users itself: %v", sys)
	}
}
