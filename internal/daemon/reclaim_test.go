package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func writePool(t *testing.T, h *harness, json string) {
	t.Helper()
	if err := os.WriteFile(h.cfg.PoolFile(), []byte(json), 0o600); err != nil {
		t.Fatal(err)
	}
}

func poolJSON(h *harness) string {
	data, _ := os.ReadFile(h.cfg.PoolFile())
	return string(data)
}

// onlyMainInPool reports whether main is the only agent holding a user.
func onlyMainInPool(h *harness) bool {
	return poolJSON(h) == `[{"user":"agent-1","agent":"main","state":"active"}]`
}

func TestRemovalReclaimsUser(t *testing.T) {
	h := newHarness(t)
	h.setScript(matchStep("inbox/processed/task.md", "touch "+filepath.Join(h.cfg.TmpDirs[0], "mine")+"; sleep 30", 100))
	h.start()
	ctrl := makeChild(t, h, "c", true)
	home := h.childHome(ctrl)
	h.drop(home, "task.md", "work")
	waitFor(t, "command running", func() bool { return len(h.requests()) >= 1 })
	waitFor(t, "tmp file", func() bool { _, err := os.Stat(filepath.Join(h.cfg.TmpDirs[0], "mine")); return err == nil })
	os.RemoveAll(ctrl)
	if end := lastEnd(h.waitEnds(childID("c"), 1)); end != "removed" {
		t.Fatal(end)
	}
	sys := h.cfg.System.(*fakeSystem)
	waitFor(t, "reclaim", func() bool { return sys.called("members agent-2 ") }) // cleared
	waitFor(t, "pool entry freed", func() bool { return !strings.Contains(poolJSON(h), "agent-2") })
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatal(entries)
	}
	if _, err := os.Stat(filepath.Join(h.cfg.TmpDirs[0], "mine")); !os.IsNotExist(err) {
		t.Fatal("tmp file kept")
	}
	// The crontab goes before the kill loop, so no cron job starts after it.
	want := []string{"unlinger agent-2", "crontab agent-2", "stop agent-2", "members agent-2 "}
	if !sys.inOrder(want...) {
		t.Fatal(sys)
	}
	if runs := h.runs(childID("c")); len(runs) != 1 {
		t.Fatalf("journals not kept: %v", runs)
	}
}

func TestRemovalTakesDescendants(t *testing.T) {
	h := newHarness(t,
		matchStep("inbox/processed/task.md", "sleep 30", 100),
		matchStep("inbox/processed/task.md", "sleep 30", 100),
	)
	h.start()
	c := makeChild(t, h, "c", true)
	cHome := h.childHome(c)
	g := filepath.Join(cHome, "agents", "g")
	os.MkdirAll(g, 0o755)
	os.WriteFile(filepath.Join(g, "config.json"), []byte(`{"provider":"replay","model":"replay-1"}`), 0o644)
	gHome := h.childHome(g)
	gID := childID("c/agents/g")
	h.drop(cHome, "task.md", "work")
	h.drop(gHome, "task.md", "work")
	waitFor(t, "both commands", func() bool { return len(h.requests()) >= 2 })
	time.Sleep(100 * time.Millisecond)
	os.RemoveAll(c)
	if end := lastEnd(h.waitEnds(childID("c"), 1)); end != "removed" {
		t.Fatal("c", end)
	}
	if end := lastEnd(h.waitEnds(gID, 1)); end != "removed" {
		t.Fatal("g", end)
	}
	sys := h.cfg.System.(*fakeSystem)
	waitFor(t, "both reclaimed", func() bool { return sys.called("members agent-2 ") && sys.called("members agent-3 ") })
	if !sys.inOrder("unlinger agent-3", "members agent-3 ", "unlinger agent-2", "members agent-2 ") {
		t.Fatal(sys)
	}
	waitFor(t, "pool empty", func() bool { return onlyMainInPool(h) })
}

func TestReclaimingFinishedOnStart(t *testing.T) {
	h := newHarness(t)
	old := childID("old")
	writePool(t, h, `[{"user":"agent-2","agent":"`+old+`","state":"reclaiming"}]`)
	home := h.cfg.PoolHome("agent-2")
	os.MkdirAll(home, 0o755)
	os.WriteFile(filepath.Join(home, "secret.txt"), []byte("old"), 0o644)
	h.start()
	ctrl := makeChild(t, h, "n", true)
	if got := h.childHome(ctrl); got != home {
		t.Fatalf("new child got %s", got)
	}
	if _, err := os.Stat(filepath.Join(home, "secret.txt")); !os.IsNotExist(err) {
		t.Fatal("old home kept")
	}
	if p := poolJSON(h); strings.Contains(p, old) || !strings.Contains(p, childID("n")) {
		t.Fatal(p)
	}
	sys := h.cfg.System.(*fakeSystem)
	if !sys.inOrder("unlinger agent-2", "members agent-2 ", "home agent-2") {
		t.Fatal(sys)
	}
}

// Agents whose control directories went while the daemon was stopped, one
// of main's and one of root's, are reclaimed on start.
func TestRemovedWhileStoppedIsReclaimed(t *testing.T) {
	h := newHarness(t)
	writePool(t, h, `[{"user":"agent-3","agent":"`+childID("gone")+`","state":"active"},{"user":"agent-4","agent":"top","state":"active"}]`)
	var homes []string
	for _, user := range []string{"agent-3", "agent-4"} {
		home := h.cfg.PoolHome(user)
		os.MkdirAll(home, 0o755)
		os.WriteFile(filepath.Join(home, "work.txt"), []byte("old"), 0o644)
		homes = append(homes, home)
	}
	h.start()
	sys := h.cfg.System.(*fakeSystem)
	waitFor(t, "entries gone", func() bool { return onlyMainInPool(h) })
	for _, user := range []string{"agent-3", "agent-4"} {
		if !sys.inOrder("unlinger "+user, "crontab "+user, "stop "+user, "members "+user+" ") {
			t.Fatal(sys)
		}
	}
	for _, home := range homes {
		if _, err := os.Stat(filepath.Join(home, "work.txt")); !os.IsNotExist(err) {
			t.Fatalf("%s kept", home)
		}
	}
}

// Renaming a control directory removes its agent. The renamed directory
// holds the home link but no config.json, which moved into the home: it is
// a new agent once given one, and never gets the old agent's files.
func TestRenameControlDirIsRemoveAndAdmit(t *testing.T) {
	h := newHarness(t, matchStep("inbox/processed/task.md", "sleep 30", 100))
	h.start()
	c := makeChild(t, h, "c", true)
	cHome := h.childHome(c)
	os.WriteFile(filepath.Join(cHome, "notes.txt"), []byte("mine"), 0o644)
	h.drop(cHome, "task.md", "work")
	waitFor(t, "command running", func() bool { return len(h.requests()) >= 1 })
	d := filepath.Join(h.mainHome, "agents", "d")
	if err := os.Rename(c, d); err != nil {
		t.Fatal(err)
	}
	if end := lastEnd(h.waitEnds(childID("c"), 1)); end != "removed" {
		t.Fatal(end)
	}
	time.Sleep(10 * h.cfg.Poll)
	if strings.Contains(h.logs.String(), "d: admitted as") {
		t.Fatal("d admitted without a config.json")
	}
	writeConfig(t, d)
	// d may get c's freed user, but never c's files.
	waitFor(t, "d admitted", func() bool { return strings.Contains(h.logs.String(), "d: admitted as") })
	dHome, _ := os.Readlink(filepath.Join(d, "home"))
	if _, err := os.Stat(filepath.Join(dHome, "README.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dHome, "notes.txt")); !os.IsNotExist(err) {
		t.Fatal("d inherited c's files")
	}
	sys := h.cfg.System.(*fakeSystem)
	waitFor(t, "c reclaimed", func() bool { return sys.called("members agent-2 ") })
	if _, err := os.Stat(filepath.Join(cHome, "notes.txt")); !os.IsNotExist(err) {
		t.Fatal("c's home kept")
	}
}

// removeControlDir removes a child's control directory the way agent remove
// does: the daemon restores a missing home link in a known child's directory
// every poll, which races a plain RemoveAll, so it is renamed out of agents/
// first.
func removeControlDir(t *testing.T, ctrl string) {
	t.Helper()
	gone := filepath.Join(filepath.Dir(filepath.Dir(ctrl)), ".removed-"+filepath.Base(ctrl)+"-"+time.Now().UTC().Format("20060102T150405.000000000Z"))
	if err := os.Rename(ctrl, gone); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
}

func TestWaitingChildAdmittedWhenUserFrees(t *testing.T) {
	h := newHarness(t)
	os.WriteFile(h.cfg.SettingsFile(), []byte(`{"max_agents":2}`), 0o644)
	h.start()
	a := makeChild(t, h, "a", true)
	b := makeChild(t, h, "b", true)
	aHome := h.childHome(a)
	errorFile := filepath.Join(b, "ERROR")
	waitFor(t, "limit ERROR", func() bool { return fileContains(errorFile, "Agent limit reached (max_agents=2)") })
	waitFor(t, "parent note", func() bool { return countNotes(h.mainHome, "-subagent-error.md") == 1 })
	removeControlDir(t, a)
	if home := h.childHome(b); home != aHome {
		t.Fatalf("b got %s, want the freed %s", home, aHome)
	}
	waitFor(t, "limit ERROR cleared", func() bool {
		_, err := os.Stat(errorFile)
		return os.IsNotExist(err)
	})
	if n := countNotes(h.mainHome, "-subagent-error.md"); n != 1 {
		t.Fatalf("parent notified %d times", n)
	}
}

func TestAgentsDirReplacedByFileRemovesChild(t *testing.T) {
	h := newHarness(t)
	h.start()
	ctrl := makeChild(t, h, "c", true)
	h.childHome(ctrl)
	agents := filepath.Join(h.mainHome, "agents")
	os.Rename(agents, agents+".old")
	os.WriteFile(agents, []byte("not a directory"), 0o644)
	sys := h.cfg.System.(*fakeSystem)
	waitFor(t, "reclaim", func() bool { return sys.called("members agent-2 ") })
}

// failingLookup makes the lookups of main's user fail while failing is set,
// except for the first one, which admission needs to start main's helper.
func failingLookup(h *harness, failing *atomic.Bool) {
	var started atomic.Bool
	h.cfg.LookupUser = func(name string) (*syscall.Credential, error) {
		if name == "agent-1" && failing.Load() && started.Swap(true) {
			return nil, errors.New("lookup: too many open files")
		}
		return nil, nil
	}
}

// A child is kept even if its parent's user cannot be looked up to check
// the control directory.
func TestLookupFailureKeepsChild(t *testing.T) {
	h := newHarness(t)
	var failing atomic.Bool
	failingLookup(h, &failing)
	h.start()
	ctrl := makeChild(t, h, "c", true)
	home := h.childHome(ctrl)
	work := filepath.Join(home, "work.txt")
	os.WriteFile(work, []byte("mine"), 0o644)
	failing.Store(true)
	time.Sleep(20 * h.cfg.Poll)
	failing.Store(false)
	time.Sleep(5 * h.cfg.Poll)
	sys := h.cfg.System.(*fakeSystem)
	if sys.called("unlinger agent-2") || sys.called("stop agent-2") {
		t.Fatalf("reclaimed on a failed lookup: %v", sys)
	}
	if data, err := os.ReadFile(work); err != nil || string(data) != "mine" {
		t.Fatalf("home touched: %q %v", data, err)
	}
	if n := strings.Count(h.logs.String(), "c: admitted as agent-2"); n != 1 {
		t.Fatalf("admitted %d times", n)
	}
}

func TestLookupFailureOnStartKeepsStoppedChild(t *testing.T) {
	h := newHarness(t)
	var failing atomic.Bool
	failingLookup(h, &failing)
	h.start()
	ctrl := makeChild(t, h, "c", true)
	home := h.childHome(ctrl)
	work := filepath.Join(home, "work.txt")
	os.WriteFile(work, []byte("mine"), 0o644)
	h.stop()
	failing.Store(true) // main cannot start: c holds only its pool entry
	h.start()
	time.Sleep(20 * h.cfg.Poll)
	failing.Store(false)
	time.Sleep(5 * h.cfg.Poll)
	sys := h.cfg.System.(*fakeSystem)
	if sys.called("unlinger agent-2") || sys.called("stop agent-2") {
		t.Fatalf("reclaimed on a failed lookup: %v", sys)
	}
	if data, err := os.ReadFile(work); err != nil || string(data) != "mine" {
		t.Fatalf("home touched: %q %v", data, err)
	}
	if p := poolJSON(h); !strings.Contains(p, `"user":"agent-2","agent":"`+childID("c")+`","state":"active"`) {
		t.Fatal(p)
	}
	waitFor(t, "c admitted again", func() bool { return strings.Count(h.logs.String(), "c: admitted as agent-2") == 2 })
	if got := h.childHome(ctrl); got != home {
		t.Fatal(got)
	}
}

// A stuck reclaim of a grandchild keeps its parent's user from being freed
// (the grandchild's group still lists it), but not from being stopped: the
// parent's lingering, crontab, processes and files go at once.
func TestParentWaitsForDescendantReclaim(t *testing.T) {
	h := newHarness(t)
	h.start()
	c := makeChild(t, h, "c", true)
	cHome := h.childHome(c)
	g := filepath.Join(cHome, "agents", "g")
	os.MkdirAll(g, 0o755)
	os.WriteFile(filepath.Join(g, "config.json"), []byte(`{"provider":"replay","model":"replay-1"}`), 0o644)
	h.childHome(g)
	os.WriteFile(filepath.Join(cHome, "work.txt"), []byte("mine"), 0o644)
	sys := h.cfg.System.(*fakeSystem)
	sys.setFail("stop agent-3", errors.New("processes survived SIGKILL"))
	os.RemoveAll(c)
	waitFor(t, "both reclaiming", func() bool {
		p := poolJSON(h)
		return strings.Count(p, `"state":"reclaiming"`) == 2
	})
	waitFor(t, "parent stopped", func() bool { return sys.inOrder("unlinger agent-2", "crontab agent-2", "stop agent-2") })
	waitFor(t, "parent's home wiped", func() bool {
		_, err := os.Stat(filepath.Join(cHome, "work.txt"))
		return os.IsNotExist(err)
	})
	time.Sleep(20 * h.cfg.Poll)
	if sys.called("members agent-2 ") {
		t.Fatalf("parent's group emptied before its child was reclaimed: %v", sys)
	}
	if p := poolJSON(h); strings.Count(p, `"state":"reclaiming"`) != 2 {
		t.Fatal(p)
	}
	if n := strings.Count(h.logs.String(), "processes survived SIGKILL"); n != 1 {
		t.Fatalf("reclaim error logged %d times", n)
	}
	sys.setFail("stop agent-3", nil)
	waitFor(t, "pool empty", func() bool { return onlyMainInPool(h) })
	if !sys.inOrder("members agent-3 ", "members agent-2 ") {
		t.Fatal(sys)
	}
}

// A directory without write permission for its owner (a Go module cache is
// 0555) must not stop a home from being cleared and its user reclaimed.
func TestReclaimClearsReadOnlyDirs(t *testing.T) {
	skipIfRoot(t)
	h := newHarness(t)
	h.start()
	c := makeChild(t, h, "c", true)
	home := h.childHome(c)
	ro := filepath.Join(home, "go", "pkg", "mod")
	os.MkdirAll(ro, 0o755)
	os.WriteFile(filepath.Join(ro, "f.go"), []byte("package f"), 0o444)
	os.Chmod(ro, 0o555)
	t.Cleanup(func() { os.Chmod(ro, 0o755) })
	os.RemoveAll(c)
	waitFor(t, "pool empty", func() bool { return onlyMainInPool(h) })
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
		t.Fatalf("home not emptied: %v %v", entries, err)
	}
}

// A grandchild that cannot start again holds its user without running;
// removing its parent must reclaim it too rather than leave the parent
// waiting for it forever.
func TestRemovalTakesPausedDescendant(t *testing.T) {
	h := newHarness(t)
	h.start()
	c := makeChild(t, h, "c", true)
	cHome := h.childHome(c)
	g := filepath.Join(cHome, "agents", "g")
	os.MkdirAll(g, 0o755)
	os.WriteFile(filepath.Join(g, "config.json"), []byte(`{"provider":"replay","model":"replay-1"}`), 0o644)
	h.childHome(g)
	h.stop()
	sys := h.cfg.System.(*fakeSystem)
	sys.setFail("linger agent-3", errors.New("linger failed")) // g is not admitted on start
	h.start()
	waitFor(t, "c running", func() bool { return strings.Count(h.logs.String(), "c: admitted as agent-2") == 2 })
	os.RemoveAll(c)
	waitFor(t, "pool empty", func() bool { return onlyMainInPool(h) })
	if strings.Count(h.logs.String(), "g: admitted as") != 1 {
		t.Fatal("g ran again")
	}
	if !sys.inOrder("members agent-3 ", "unlinger agent-2", "members agent-2 ") {
		t.Fatal(sys)
	}
}

// A control directory removed and created again between two polls is a new
// subagent: the old one is removed and reclaimed, and the new one admitted
// with a cleared home. Here the old directory is emptied and the new one
// renamed over it, so no poll sees the path missing.
func TestReplacedControlDirIsRemoveAndAdmit(t *testing.T) {
	h := newHarness(t)
	h.start()
	c := makeChild(t, h, "c", true)
	home := h.childHome(c)
	os.WriteFile(filepath.Join(home, "notes.txt"), []byte("old agent's"), 0o644)
	next := filepath.Join(h.mainHome, "next") // outside agents/, same file system
	os.MkdirAll(next, 0o755)
	os.WriteFile(filepath.Join(next, "config.json"), []byte(`{"provider":"replay","model":"replay-1"}`), 0o644)
	empty := func() {
		items, _ := os.ReadDir(c)
		for _, item := range items {
			os.Remove(filepath.Join(c, item.Name()))
		}
	}
	empty()
	time.Sleep(5 * h.cfg.Poll)
	// rename(2) replaces an empty directory atomically; os.Rename refuses
	// to. The daemon may restore the home link in between: empty it again.
	for i := 0; ; i++ {
		err := syscall.Rename(next, c)
		if err == nil {
			break
		}
		if i == 100 || !errors.Is(err, syscall.ENOTEMPTY) && !errors.Is(err, syscall.EEXIST) {
			t.Fatal(err)
		}
		empty()
	}
	waitFor(t, "readmitted", func() bool { return strings.Count(h.logs.String(), "c: admitted as") == 2 })
	sys := h.cfg.System.(*fakeSystem)
	if !sys.called("members agent-2 ") {
		t.Fatalf("old agent not reclaimed: %v", sys)
	}
	newHome := h.childHome(c)
	if _, err := os.Stat(filepath.Join(newHome, "notes.txt")); !os.IsNotExist(err) {
		t.Fatal("new agent inherited the old one's files")
	}
	if _, err := os.Stat(filepath.Join(newHome, "README.md")); err != nil {
		t.Fatal(err)
	}
}

// The parent may delete its child's home link; the daemon puts it back.
func TestHomeLinkRepaired(t *testing.T) {
	h := newHarness(t)
	h.start()
	c := makeChild(t, h, "c", true)
	home := h.childHome(c)
	if err := os.Remove(filepath.Join(c, "home")); err != nil {
		t.Fatal(err)
	}
	if got := h.childHome(c); got != home {
		t.Fatalf("home link points at %s, want %s", got, home)
	}
	if n := strings.Count(h.logs.String(), "c: admitted as"); n != 1 {
		t.Fatalf("admitted %d times", n)
	}
}
