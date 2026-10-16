package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Each agent gets a user of its own, created at admission with its id in
// the comment field, before its home is prepared.
func TestAdmissionCreatesUser(t *testing.T) {
	h := newHarness(t)
	h.start()
	sys := h.cfg.System.(*fakeSystem)
	if !sys.inOrder("useradd agent-1 main", "home agent-1", "members agent-1 ", "linger agent-1") {
		t.Fatal(sys)
	}
	users, _ := sys.Users("agent-")
	if len(users) != 1 || users[0] != (User{"agent-1", "main"}) {
		t.Fatalf("users %v", users)
	}
}

// Removal stops what the user runs, then deletes the user, home and all.
func TestRemovalDeletesUser(t *testing.T) {
	h := newHarness(t)
	h.start()
	ctrl := makeChild(t, h, "c", true)
	home := h.childHome(ctrl)
	os.RemoveAll(ctrl)
	sys := h.cfg.System.(*fakeSystem)
	waitFor(t, "userdel", func() bool { return sys.called("userdel agent-2") })
	if !sys.inOrder("unlinger agent-2", "crontab agent-2", "stop agent-2", "userdel agent-2") {
		t.Fatal(sys)
	}
	waitFor(t, "reclaimed", func() bool { return strings.Contains(h.logs.String(), "agent-2: reclaimed") })
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("home kept after userdel: %v", err)
	}
	if users, _ := sys.Users("agent-"); len(users) != 1 {
		t.Fatalf("users %v", users)
	}
}

// A control directory recreated while its old user is still being removed
// never shares that user: the old one is marked removed, so the new agent
// gets a fresh user under the next free name, and the old one's deletion
// goes on being retried.
func TestRecreatedControlDirGetsFreshUser(t *testing.T) {
	h := newHarness(t)
	h.start()
	ctrl := makeChild(t, h, "c", true)
	oldHome := h.childHome(ctrl)
	os.WriteFile(filepath.Join(oldHome, "notes.txt"), []byte("old"), 0o644)
	sys := h.cfg.System.(*fakeSystem)
	sys.setFail("userdel agent-2", errors.New("busy"))
	removeControlDir(t, ctrl)
	waitFor(t, "removal attempted", func() bool { return sys.called("userdel agent-2") })
	makeChild(t, h, "c", true)
	if home := h.childHome(ctrl); home == oldHome {
		t.Fatal("recreated c got the user being removed")
	} else if _, err := os.Stat(filepath.Join(home, "notes.txt")); !os.IsNotExist(err) {
		t.Fatal("recreated c inherited the old agent's files")
	}
	if !sys.called("useradd agent-3 "+childID("c")) || !strings.Contains(h.logs.String(), "reclaim: userdel: busy") {
		t.Fatalf("%v\n%s", sys, h.logs)
	}
	time.Sleep(300 * time.Millisecond)
	if users, _ := sys.Users("agent-"); len(users) != 3 || users[1] != (User{"agent-2", removedMark + childID("c")}) {
		t.Fatalf("users %v", users)
	}
	sys.setFail("userdel agent-2", nil)
	waitFor(t, "old user deleted", func() bool { users, _ := sys.Users("agent-"); return len(users) == 2 })
	if strings.Count(h.logs.String(), "c: admitted as") != 2 {
		t.Fatalf("admissions:\n%s", h.logs)
	}
}

// max_agents counts the users that exist; a removed agent frees its slot.
func TestMaxAgentsCountsUsers(t *testing.T) {
	h := newHarness(t)
	os.WriteFile(h.cfg.SettingsFile(), []byte(`{"max_agents":1}`), 0o644)
	other := topLevel(t, h, "other")
	h.start()
	errorFile := filepath.Join(other, "ERROR")
	waitFor(t, "limit ERROR", func() bool { return fileContains(errorFile, "Agent limit reached (max_agents=1)") })
	sys := h.cfg.System.(*fakeSystem)
	if sys.called("useradd agent-2 other") {
		t.Fatal("a user was created over the limit")
	}
	os.RemoveAll(h.mainCtrl)
	waitFor(t, "main's user deleted", func() bool { return sys.called("userdel agent-1") })
	if home := h.childHome(other); home != h.cfg.Home("agent-1") {
		t.Fatalf("other got %s", home)
	}
	if !sys.called("useradd agent-1 other") {
		t.Fatal(sys)
	}
}

// A user whose agent's control directory is gone, left by a daemon that
// stopped before removing it, is removed at the next start.
func TestOrphanedUserRemovedOnStart(t *testing.T) {
	h := newHarness(t)
	sys := h.cfg.System.(*fakeSystem)
	home := h.cfg.Home("agent-3")
	os.MkdirAll(home, 0o755)
	os.WriteFile(filepath.Join(home, "secret.txt"), []byte("old"), 0o644)
	sys.seed("agent-3", "gone", home)
	h.start()
	waitFor(t, "userdel", func() bool { return sys.called("userdel agent-3") })
	if !sys.inOrder("unlinger agent-3", "crontab agent-3", "stop agent-3", "userdel agent-3") {
		t.Fatal(sys)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("orphan's home kept")
	}
}

// A subagent's ~/owner links to its owner's inbox, where it writes its
// messages; a top-level agent has none, since root has no inbox.
func TestOwnerLinkForChild(t *testing.T) {
	h := newHarness(t, doneFor(childMark))
	h.start()
	ctrl := makeChild(t, h, "c", true)
	home := h.childHome(ctrl)
	h.drop(home, "go.md", "go")
	h.waitEnds(childID("c"), 1)
	if target, err := os.Readlink(filepath.Join(home, "owner")); err != nil || target != filepath.Join(h.mainHome, "inbox") {
		t.Fatalf("owner link %q %v", target, err)
	}
	if _, err := os.Lstat(filepath.Join(h.mainHome, "owner")); !os.IsNotExist(err) {
		t.Fatalf("main, root's child, has an owner link: %v", err)
	}
}

// Admission fails, and is retried, while the child cannot be given write
// access to its owner's inbox: a child whose messages can never arrive
// must not run.
func TestGrantFailureHoldsAdmission(t *testing.T) {
	h := newHarness(t)
	h.start()
	sys := h.cfg.System.(*fakeSystem)
	grant := "grant agent-1 " + itoa(os.Getuid())
	sys.setFail(grant, errors.New("setfacl: command not found"))
	ctrl := makeChild(t, h, "c", true)
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Lstat(filepath.Join(ctrl, "home")); !os.IsNotExist(err) {
		t.Fatal("admitted without write access to its owner's inbox")
	}
	if n := strings.Count(h.logs.String(), "not started: share inbox: setfacl"); n != 1 {
		t.Fatalf("logged %d times:\n%s", n, h.logs)
	}
	sys.setFail(grant, nil)
	h.childHome(ctrl)
	if !sys.inOrder("useradd agent-2 "+childID("c"), grant, "linger agent-2") {
		t.Fatal(sys)
	}
}
