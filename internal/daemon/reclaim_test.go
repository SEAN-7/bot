package daemon

import (
	"os"
	"path/filepath"
	"strings"
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

func TestRemovalReclaimsUser(t *testing.T) {
	h := newHarness(t)
	h.setScript(matchStep("inbox/processed/task.md", "touch "+filepath.Join(h.cfg.TmpDirs[0], "mine")+"; sleep 30", 100))
	ctrl := makeChild(t, h, "c", true)
	h.start()
	home := h.childHome(ctrl)
	h.drop(home, "task.md", "work")
	waitFor(t, "command running", func() bool { return len(h.requests()) >= 1 })
	waitFor(t, "tmp file", func() bool { _, err := os.Stat(filepath.Join(h.cfg.TmpDirs[0], "mine")); return err == nil })
	os.RemoveAll(ctrl)
	if end := lastEnd(h.waitEnds(ctrl, 1)); end != "removed" {
		t.Fatal(end)
	}
	sys := h.cfg.System.(*fakeSystem)
	waitFor(t, "reclaim", func() bool { return sys.called("members agent-1 ") }) // cleared
	waitFor(t, "pool entry freed", func() bool { return !strings.Contains(poolJSON(h), "agent-1") })
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatal(entries)
	}
	if _, err := os.Stat(filepath.Join(h.cfg.TmpDirs[0], "mine")); !os.IsNotExist(err) {
		t.Fatal("tmp file kept")
	}
	want := []string{"unlinger agent-1", "stop agent-1", "crontab agent-1", "members agent-1 "}
	if !sys.inOrder(want...) {
		t.Fatal(sys)
	}
	if runs := h.runs(ctrl); len(runs) != 1 {
		t.Fatalf("journals not kept: %v", runs)
	}
}

func TestRemovalTakesDescendants(t *testing.T) {
	h := newHarness(t,
		matchStep("inbox/processed/task.md", "sleep 30", 100),
		matchStep("inbox/processed/task.md", "sleep 30", 100),
	)
	c := makeChild(t, h, "c", true)
	h.start()
	cHome := h.childHome(c)
	g := filepath.Join(cHome, "agents", "g")
	os.MkdirAll(g, 0o755)
	os.WriteFile(filepath.Join(g, "config.json"), []byte(`{"provider":"replay","model":"replay-1"}`), 0o644)
	gHome := h.childHome(g)
	gID := filepath.Join(h.cfg.Home, "agents", "c", "agents", "g")
	h.drop(cHome, "task.md", "work")
	h.drop(gHome, "task.md", "work")
	waitFor(t, "both commands", func() bool { return len(h.requests()) >= 2 })
	time.Sleep(100 * time.Millisecond)
	os.RemoveAll(c)
	if end := lastEnd(h.waitEnds(c, 1)); end != "removed" {
		t.Fatal("c", end)
	}
	if end := lastEnd(h.waitEnds(gID, 1)); end != "removed" {
		t.Fatal("g", end)
	}
	sys := h.cfg.System.(*fakeSystem)
	waitFor(t, "both reclaimed", func() bool { return sys.called("members agent-1 ") && sys.called("members agent-2 ") })
	if !sys.inOrder("unlinger agent-2", "members agent-2 ", "unlinger agent-1", "members agent-1 ") {
		t.Fatal(sys)
	}
	waitFor(t, "pool empty", func() bool { return poolJSON(h) == "[]" })
}

func TestReclaimingFinishedOnStart(t *testing.T) {
	h := newHarness(t)
	old := filepath.Join(h.cfg.Home, "agents", "old")
	writePool(t, h, `[{"user":"agent-1","agent":"`+old+`","state":"reclaiming"}]`)
	home := h.cfg.PoolHome("agent-1")
	os.MkdirAll(home, 0o755)
	os.WriteFile(filepath.Join(home, "secret.txt"), []byte("old"), 0o644)
	ctrl := makeChild(t, h, "n", true)
	h.start()
	if got := h.childHome(ctrl); got != home {
		t.Fatalf("new child got %s", got)
	}
	if _, err := os.Stat(filepath.Join(home, "secret.txt")); !os.IsNotExist(err) {
		t.Fatal("old home kept")
	}
	if p := poolJSON(h); strings.Contains(p, old) || !strings.Contains(p, ctrl) {
		t.Fatal(p)
	}
	sys := h.cfg.System.(*fakeSystem)
	if !sys.inOrder("unlinger agent-1", "members agent-1 ", "home agent-1") {
		t.Fatal(sys)
	}
}

func TestRemovedWhileStoppedIsReclaimed(t *testing.T) {
	h := newHarness(t)
	gone := filepath.Join(h.cfg.Home, "agents", "gone")
	writePool(t, h, `[{"user":"agent-2","agent":"`+gone+`","state":"active"}]`)
	home := h.cfg.PoolHome("agent-2")
	os.MkdirAll(home, 0o755)
	os.WriteFile(filepath.Join(home, "work.txt"), []byte("old"), 0o644)
	h.start()
	sys := h.cfg.System.(*fakeSystem)
	waitFor(t, "reclaim", func() bool { return sys.called("members agent-2 ") })
	waitFor(t, "entry gone", func() bool { return poolJSON(h) == "[]" })
	if !sys.inOrder("unlinger agent-2", "stop agent-2", "crontab agent-2", "members agent-2 ") {
		t.Fatal(sys)
	}
	if _, err := os.Stat(filepath.Join(home, "work.txt")); !os.IsNotExist(err) {
		t.Fatal("home kept")
	}
}

func TestRenameControlDirIsRemoveAndAdmit(t *testing.T) {
	h := newHarness(t, matchStep("inbox/processed/task.md", "sleep 30", 100))
	c := makeChild(t, h, "c", true)
	h.start()
	cHome := h.childHome(c)
	os.WriteFile(filepath.Join(cHome, "notes.txt"), []byte("mine"), 0o644)
	h.drop(cHome, "task.md", "work")
	waitFor(t, "command running", func() bool { return len(h.requests()) >= 1 })
	d := filepath.Join(h.cfg.Home, "agents", "d")
	if err := os.Rename(c, d); err != nil {
		t.Fatal(err)
	}
	if end := lastEnd(h.waitEnds(c, 1)); end != "removed" {
		t.Fatal(end)
	}
	var dHome string
	waitFor(t, "d admitted", func() bool {
		dHome, _ = os.Readlink(filepath.Join(d, "home"))
		return dHome != "" && dHome != cHome
	})
	if _, err := os.Stat(filepath.Join(dHome, "README.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dHome, "notes.txt")); !os.IsNotExist(err) {
		t.Fatal("d inherited c's files")
	}
	sys := h.cfg.System.(*fakeSystem)
	waitFor(t, "c reclaimed", func() bool { return sys.called("members agent-1 ") })
	if _, err := os.Stat(filepath.Join(cHome, "notes.txt")); !os.IsNotExist(err) {
		t.Fatal("c's home kept")
	}
}

func TestWaitingChildAdmittedWhenUserFrees(t *testing.T) {
	h := newHarness(t)
	os.WriteFile(h.cfg.SettingsFile(), []byte(`{"max_agents":2}`), 0o644)
	a := makeChild(t, h, "a", true)
	b := makeChild(t, h, "b", true)
	h.start()
	aHome := h.childHome(a)
	errorFile := filepath.Join(b, "ERROR")
	waitFor(t, "limit ERROR", func() bool { return fileContains(errorFile, "Agent limit reached (max_agents=2)") })
	waitFor(t, "parent note", func() bool { return countNotes(h.cfg.Home, "-subagent-error.md") == 1 })
	os.RemoveAll(a)
	if home := h.childHome(b); home != aHome {
		t.Fatalf("b got %s, want the freed %s", home, aHome)
	}
	waitFor(t, "limit ERROR cleared", func() bool {
		_, err := os.Stat(errorFile)
		return os.IsNotExist(err)
	})
	if n := countNotes(h.cfg.Home, "-subagent-error.md"); n != 1 {
		t.Fatalf("parent notified %d times", n)
	}
}

func TestMovedConfigKeepsAgent(t *testing.T) {
	h := newHarness(t, done())
	ctrl := makeChild(t, h, "c", true)
	h.start()
	home := h.childHome(ctrl)
	work := filepath.Join(home, "work.txt")
	os.WriteFile(work, []byte("mine"), 0o644)
	config, moved := filepath.Join(ctrl, "config.json"), filepath.Join(ctrl, "x")
	if err := os.Rename(config, moved); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * h.cfg.Poll)
	sys := h.cfg.System.(*fakeSystem)
	if sys.called("unlinger agent-1") || sys.called("stop agent-1") {
		t.Fatalf("reclaimed with its control directory still there: %v", sys)
	}
	if data, err := os.ReadFile(work); err != nil || string(data) != "mine" {
		t.Fatalf("home touched: %q %v", data, err)
	}
	if err := os.Rename(moved, config); err != nil {
		t.Fatal(err)
	}
	h.drop(home, "task.md", "work")
	if end := lastEnd(h.waitEnds(ctrl, 1)); end != "done" {
		t.Fatal(end)
	}
	if got, _ := os.Readlink(filepath.Join(ctrl, "home")); got != home {
		t.Fatalf("moved from %s to %s", home, got)
	}
	if sys.called("home agent-2") || sys.called("unlinger agent-1") {
		t.Fatal(sys)
	}
	if n := strings.Count(h.logs.String(), "c: admitted as agent-1"); n != 1 {
		t.Fatalf("admitted %d times: the agent was removed and readmitted", n)
	}
}

func TestAgentsDirReplacedByFileRemovesChild(t *testing.T) {
	h := newHarness(t)
	ctrl := makeChild(t, h, "c", true)
	h.start()
	h.childHome(ctrl)
	agents := filepath.Join(h.cfg.Home, "agents")
	os.Rename(agents, agents+".old")
	os.WriteFile(agents, []byte("not a directory"), 0o644)
	sys := h.cfg.System.(*fakeSystem)
	waitFor(t, "reclaim", func() bool { return sys.called("members agent-1 ") })
}
