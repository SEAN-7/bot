package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/SEAN-7/bot/internal/seed"
)

func countNotes(dir, suffix string) int {
	n := 0
	for _, sub := range []string{"inbox", "inbox/processed"} {
		entries, _ := os.ReadDir(filepath.Join(dir, sub))
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), suffix) {
				n++
			}
		}
	}
	return n
}

// makeChild creates a control directory under the main agent's agents/, as
// a parent would; config.json is what makes the daemon admit it.
func makeChild(t *testing.T, h *harness, name string, config bool) string {
	t.Helper()
	ctrl := filepath.Join(h.cfg.Home, "agents", name)
	if err := os.MkdirAll(ctrl, 0o755); err != nil {
		t.Fatal(err)
	}
	if config {
		os.WriteFile(filepath.Join(ctrl, "config.json"), []byte(`{"provider":"replay","model":"replay-1"}`), 0o644)
	}
	return ctrl
}

func TestConfigValidationFailure(t *testing.T) {
	h := newHarness(t, done())
	config := filepath.Join(h.cfg.Home, "config.json")
	os.WriteFile(config, []byte(`{"provider":"replay","model":"nope"}`), 0o644)
	h.drop(h.cfg.Home, "go.md", "go")
	h.start()
	errorFile := filepath.Join(h.cfg.Home, "ERROR")
	waitFor(t, "config ERROR", func() bool { return fileContains(errorFile, `config.json: unknown model "nope"`) })
	time.Sleep(100 * time.Millisecond)
	if n := len(h.requests()); n != 0 {
		t.Fatalf("%d requests with invalid config", n)
	}
	if names := inboxNames(h.cfg.Home); len(names) != 1 || names[0] != "go.md" {
		t.Fatalf("inbox touched: %v", names)
	}
	if strings.Count(h.logs.String(), `unknown model "nope"`) != 1 {
		t.Fatalf("config error not logged exactly once:\n%s", h.logs)
	}
	os.WriteFile(config, []byte(`{"provider":"replay","model":"replay-1"}`), 0o644)
	h.waitEnds(h.cfg.Home, 1)
	if _, err := os.Stat(errorFile); !os.IsNotExist(err) {
		t.Fatal("config ERROR not cleared")
	}
}

func TestChildWithoutConfigWaits(t *testing.T) {
	h := newHarness(t)
	ctrl := makeChild(t, h, "c", false)
	h.start()
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Lstat(filepath.Join(ctrl, "home")); !os.IsNotExist(err) {
		t.Fatal("a directory without config.json was admitted")
	}
	if sys := h.cfg.System.(*fakeSystem); sys.called("home agent-1") {
		t.Fatalf("a user was prepared: %v", sys)
	}
	if n := countNotes(h.cfg.Home, "-subagent-error.md"); n != 0 {
		t.Fatalf("parent notified %d times", n)
	}
	os.WriteFile(filepath.Join(ctrl, "config.json"), []byte(`{"provider":"replay","model":"replay-1"}`), 0o644)
	h.drop(h.childHome(ctrl), "task.md", "do it")
	h.waitEnds(ctrl, 1)
}

func TestParentChildReport(t *testing.T) {
	create := "mkdir -p agents/c && cp config.json agents/c/ && " + sendDownText("c", "child-task: say hi")
	h := newHarness(t,
		matchStep(parentMark, create, 100), doneFor(parentMark),
		matchStep(childMark, sendMessageText("report: hi"), 100), doneFor(childMark),
		doneFor(parentMark), doneFor(parentMark),
	)
	h.markParent()
	h.drop(h.cfg.Home, "go.md", "make a child")
	h.start()
	waitFor(t, "parent receiving the report", func() bool {
		return h.sawRecord(`"file":"inbox/processed/`, `-c.md","from":"agents/c","content":"report: hi"}`)
	})
	if !h.sawRecord(`-owner.md","from":"owner","content":"child-task: say hi"}`) {
		t.Fatal("child task not delivered from owner")
	}
	if records := h.waitEnds(filepath.Join(h.cfg.Home, "agents", "c"), 1); lastEnd(records) != "done" {
		t.Fatalf("child end %v", records)
	}
}

func TestRemovalStopsAgent(t *testing.T) {
	h := newHarness(t, matchStep("inbox/processed/task.md", "sleep 30", 100))
	ctrl := makeChild(t, h, "c", true)
	h.start()
	h.drop(h.childHome(ctrl), "task.md", "work")
	waitFor(t, "child command", func() bool { return len(h.requests()) >= 1 })
	time.Sleep(100 * time.Millisecond)
	os.RemoveAll(ctrl)
	start := time.Now()
	if end := lastEnd(h.waitEnds(ctrl, 1)); end != "removed" {
		t.Fatalf("end %s", end)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("removal took %s", time.Since(start))
	}
}

func TestMalformedSettingsKeepsAgentsRunning(t *testing.T) {
	h := newHarness(t, step("sleep 0.5", 100), done())
	h.drop(h.cfg.Home, "go.md", "go")
	h.start()
	waitFor(t, "first request", func() bool { return len(h.requests()) >= 1 })
	os.WriteFile(h.cfg.SettingsFile(), []byte("{"), 0o644)
	if end := lastEnd(h.waitEnds(h.cfg.Home, 1)); end != "done" {
		t.Fatalf("run ended %q after settings.json broke", end)
	}
	if fileContains(filepath.Join(h.cfg.Home, "ERROR"), "Agent limit") {
		t.Fatal("broken settings treated as max_agents=0")
	}
	time.Sleep(100 * time.Millisecond)
	if n := strings.Count(h.logs.String(), "settings"); n != 1 {
		t.Fatalf("settings error logged %d times", n)
	}
}

func TestAdmissionSeedsPoolHome(t *testing.T) {
	h := newHarness(t, matchStep("inbox/processed/task.md", "pwd; cat README.md | head -1", 100), `{"match":"inbox/processed/task.md","text":"done"}`)
	ctrl := makeChild(t, h, "c", true)
	h.start()
	home := h.childHome(ctrl)
	if home != filepath.Join(h.cfg.Homes, "agent-1") {
		t.Fatal(home)
	}
	for _, p := range []string{"README.md", "TASK.md", "inbox/processed", "agents"} {
		if _, err := os.Stat(filepath.Join(home, p)); err != nil {
			t.Fatal(p, err)
		}
	}
	h.drop(home, "task.md", "work")
	h.waitEnds(ctrl, 1) // journals are keyed by the control directory
	if tool := h.requests()[1].Messages[2].Content; !strings.Contains(tool, home) {
		t.Fatal(tool)
	}
	sys := h.cfg.System.(*fakeSystem)
	if !sys.called("members agent-1 "+h.mainUser()) || !sys.called("linger agent-1") {
		t.Fatal(sys)
	}
	if info, err := os.Stat(h.cfg.JournalDir(ctrl)); err != nil || info.Mode().Perm() != 0o751 {
		t.Fatalf("journal dir %v %v", info, err)
	}
}

func TestConfigErrorGoesToControlDir(t *testing.T) {
	h := newHarness(t, done())
	ctrl := makeChild(t, h, "c", false)
	os.WriteFile(filepath.Join(ctrl, "config.json"), []byte(`{"provider":"replay","model":"nope"}`), 0o644)
	h.start()
	home := h.childHome(ctrl)
	h.drop(home, "task.md", "work")
	waitFor(t, "child ERROR", func() bool { return fileContains(filepath.Join(ctrl, "ERROR"), `config.json: unknown model "nope"`) })
	waitFor(t, "parent note", func() bool { return countNotes(h.cfg.Home, "-subagent-error.md") == 1 })
	time.Sleep(200 * time.Millisecond)
	if n := countNotes(h.cfg.Home, "-subagent-error.md"); n != 1 {
		t.Fatalf("parent notified %d times", n)
	}
	if _, err := os.Stat(filepath.Join(home, "ERROR")); !os.IsNotExist(err) {
		t.Fatal("ERROR written into the subagent's home")
	}
	if names := inboxNames(home); len(names) != 1 {
		t.Fatalf("child inbox touched: %v", names)
	}
}

func TestMaxAgentsUsesPool(t *testing.T) {
	h := newHarness(t)
	os.WriteFile(h.cfg.SettingsFile(), []byte(`{"max_agents":2}`), 0o644)
	a := makeChild(t, h, "a", true)
	b := makeChild(t, h, "b", true)
	h.start()
	h.childHome(a)
	errorFile := filepath.Join(b, "ERROR")
	waitFor(t, "limit ERROR", func() bool { return fileContains(errorFile, "Agent limit reached (max_agents=2)") })
	waitFor(t, "parent note", func() bool { return countNotes(h.cfg.Home, "-subagent-error.md") == 1 })
	time.Sleep(200 * time.Millisecond)
	if n := countNotes(h.cfg.Home, "-subagent-error.md"); n != 1 {
		t.Fatalf("parent notified %d times", n)
	}
	if _, err := os.Lstat(filepath.Join(b, "home")); !os.IsNotExist(err) {
		t.Fatal("b admitted despite the limit")
	}
	os.WriteFile(h.cfg.SettingsFile(), []byte(`{"max_agents":3}`), 0o644)
	if home := h.childHome(b); home != filepath.Join(h.cfg.Homes, "agent-2") {
		t.Fatal(home)
	}
	waitFor(t, "limit ERROR cleared", func() bool {
		_, err := os.Stat(errorFile)
		return os.IsNotExist(err)
	})
}

func TestAdmissionClearsLeftoverHome(t *testing.T) {
	h := newHarness(t)
	leftover := filepath.Join(h.cfg.Homes, "agent-1")
	os.MkdirAll(leftover, 0o755)
	os.WriteFile(filepath.Join(leftover, "stale.txt"), []byte("old"), 0o644)
	os.WriteFile(filepath.Join(leftover, "README.md"), []byte("old readme"), 0o644)
	ctrl := makeChild(t, h, "c", true)
	h.start()
	home := h.childHome(ctrl)
	waitFor(t, "seeded README", func() bool {
		data, err := os.ReadFile(filepath.Join(home, "README.md"))
		return err == nil && string(data) == seed.README
	})
	if _, err := os.Stat(filepath.Join(home, "stale.txt")); !os.IsNotExist(err) {
		t.Fatal("leftover file kept")
	}
}

func TestGrandchildAdmitted(t *testing.T) {
	h := newHarness(t)
	c := makeChild(t, h, "c", true)
	h.start()
	cHome := h.childHome(c)
	g := filepath.Join(cHome, "agents", "g")
	os.MkdirAll(g, 0o755)
	os.WriteFile(filepath.Join(g, "config.json"), []byte(`{"provider":"replay","model":"replay-1"}`), 0o644)
	if home := h.childHome(g); home != filepath.Join(h.cfg.Homes, "agent-2") {
		t.Fatal(home)
	}
	sys := h.cfg.System.(*fakeSystem)
	if !sys.called("members agent-2 agent-1," + h.mainUser()) {
		t.Fatal(sys)
	}
	// The grandchild's id is its path as seen from main, so its journals sit
	// inside its parent's journal directory whichever user holds the parent.
	id := filepath.Join(h.cfg.Home, "agents", "c", "agents", "g")
	if info, err := os.Stat(h.cfg.JournalDir(id)); err != nil || !info.IsDir() {
		t.Fatalf("grandchild journal dir: %v", err)
	}
	if _, err := os.Stat(h.cfg.JournalDir(g)); !os.IsNotExist(err) {
		t.Fatalf("journal keyed by the pool home path: %v", err)
	}
	data, _ := os.ReadFile(h.cfg.PoolFile())
	if !strings.Contains(string(data), `{"user":"agent-2","agent":"`+id+`","state":"active"}`) {
		t.Fatalf("pool.json %s", data)
	}
	h.drop(h.childHome(g), "task.md", "work")
	if records := h.waitEnds(id, 1); lastEnd(records) != "done" {
		t.Fatalf("grandchild run %v", records)
	}
}

func TestRestartKeepsChildHome(t *testing.T) {
	h := newHarness(t)
	ctrl := makeChild(t, h, "c", true)
	h.start()
	home := h.childHome(ctrl)
	work := filepath.Join(home, "work.txt")
	os.WriteFile(work, []byte("mine"), 0o644)
	os.WriteFile(filepath.Join(home, "README.md"), []byte("my own readme"), 0o644)
	h.stop()
	os.Remove(filepath.Join(ctrl, "home"))
	h.start()
	if again := h.childHome(ctrl); again != home {
		t.Fatalf("restart moved the child from %s to %s", home, again)
	}
	if data, err := os.ReadFile(work); err != nil || string(data) != "mine" {
		t.Fatalf("work.txt after restart: %q %v", data, err)
	}
	if data, _ := os.ReadFile(filepath.Join(home, "README.md")); string(data) != "my own readme" {
		t.Fatalf("README.md after restart: %q", data)
	}
	if sys := h.cfg.System.(*fakeSystem); sys.called("home agent-2") {
		t.Fatalf("a second user was prepared: %v", sys)
	}
}

func TestValidControlDir(t *testing.T) {
	home := "/home/agent"
	for dir, want := range map[string]bool{
		"/home/agent/agents/x":            true,
		"/home/agent/agents/x/y":          false,
		"/home/agent/agents/x/agents/y":   false,
		"/home/agent/agents/.":            false,
		"/home/agent/agents/..":           false,
		"/home/agent/agents":              false,
		"/home/agent/agents/":             false,
		"/home/agent/agents/x/":           false,
		"/home/agent/agents/../../../etc": false,
		"/home/agent/x":                   false,
		"/home/agent":                     false,
		"/home/agentx/agents/x":           false,
		"":                                false,
	} {
		if got := validControlDir(home, dir); got != want {
			t.Errorf("validControlDir(%q) = %v, want %v", dir, got, want)
		}
	}
}

func TestMissingPoolUsersLogged(t *testing.T) {
	h := newHarness(t)
	h.cfg.LookupUser = func(name string) (*syscall.Credential, error) {
		if name == "agent-3" {
			return nil, errors.New("unknown user")
		}
		return nil, nil
	}
	h.start()
	time.Sleep(100 * time.Millisecond)
	h.stop()
	if n := strings.Count(h.logs.String(), "pool users not found: agent-3; subagents are limited to 2"); n != 1 {
		t.Fatalf("logged %d times:\n%s", n, h.logs)
	}
}

// Defence in depth against a lost pool.json: a freshly assigned home whose
// journal link already points at this agent's journals is this agent's own
// home, so it is not cleared. Another agent's journal link does not count.
func TestLostPoolKeepsOwnHome(t *testing.T) {
	h := newHarness(t, done())
	ctrl := makeChild(t, h, "c", true)
	h.start()
	home := h.childHome(ctrl)
	h.drop(home, "task.md", "work")
	h.waitEnds(ctrl, 1)
	work := filepath.Join(home, "work.txt")
	os.WriteFile(work, []byte("mine"), 0o644)
	other := filepath.Join(h.cfg.Homes, "agent-2")
	os.MkdirAll(other, 0o755)
	os.WriteFile(filepath.Join(other, "stale.txt"), []byte("old"), 0o644)
	os.Symlink(h.cfg.JournalDir(filepath.Join(h.cfg.Home, "agents", "x")), filepath.Join(other, "journal"))
	h.stop()
	os.Remove(h.cfg.PoolFile())
	makeChild(t, h, "d", true)
	h.start()
	waitFor(t, "both admitted", func() bool {
		return strings.Count(h.logs.String(), "c: admitted as agent-1") == 2 && strings.Contains(h.logs.String(), "d: admitted as agent-2")
	})
	if data, err := os.ReadFile(work); err != nil || string(data) != "mine" {
		t.Fatalf("own home cleared: %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(other, "stale.txt")); !os.IsNotExist(err) {
		t.Fatal("a home with another agent's journal link was not cleared")
	}
}

// The lost-pool keep rule trusts a journal link only if the journal
// directory belongs to the newly assigned user's group: the link is in a
// home the previous holder controlled, but only the daemon sets the group.
func TestJournalOwnedBy(t *testing.T) {
	dir := t.TempDir()
	if !journalOwnedBy(dir, os.Getgid()) {
		t.Fatal("own group not accepted")
	}
	if journalOwnedBy(dir, os.Getgid()+1) {
		t.Fatal("another group accepted")
	}
	if journalOwnedBy(filepath.Join(dir, "missing"), os.Getgid()) {
		t.Fatal("missing journal directory accepted")
	}
	link := filepath.Join(t.TempDir(), "link")
	os.Symlink(dir, link)
	if journalOwnedBy(link, os.Getgid()) {
		t.Fatal("symlinked journal directory accepted")
	}
	if !journalOwnedBy(filepath.Join(dir, "missing"), -1) {
		t.Fatal("without a credential (one user for every agent) the group cannot be checked")
	}
}
