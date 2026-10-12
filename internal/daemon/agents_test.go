package daemon

import (
	"errors"
	"io/fs"
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

// makeChild creates a control directory under main's agents/, as main
// would, once main is admitted; config.json is what makes the daemon admit it.
func makeChild(t *testing.T, h *harness, name string, config bool) string {
	t.Helper()
	ctrl := filepath.Join(h.mainHome, "agents", name)
	if config {
		return writeConfig(t, ctrl)
	}
	if err := os.MkdirAll(ctrl, 0o755); err != nil {
		t.Fatal(err)
	}
	return ctrl
}

// childID is the id of main's child name.
func childID(name string) string { return "main/agents/" + name }

func TestConfigValidationFailure(t *testing.T) {
	h := newHarness(t, done())
	config := filepath.Join(h.mainCtrl, "config.json")
	os.WriteFile(config, []byte(`{"provider":"replay","model":"nope"}`), 0o644)
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	errorFile := filepath.Join(h.mainCtrl, "ERROR")
	waitFor(t, "config ERROR", func() bool { return fileContains(errorFile, `config.json: unknown model "nope"`) })
	time.Sleep(100 * time.Millisecond)
	if n := len(h.requests()); n != 0 {
		t.Fatalf("%d requests with invalid config", n)
	}
	if names := inboxNames(h.mainHome); len(names) != 1 || names[0] != "go.md" {
		t.Fatalf("inbox touched: %v", names)
	}
	if strings.Count(h.logs.String(), `unknown model "nope"`) != 1 {
		t.Fatalf("config error not logged exactly once:\n%s", h.logs)
	}
	os.WriteFile(config, []byte(testConfig), 0o644)
	h.waitEnds(h.mainID, 1)
	if _, err := os.Stat(errorFile); !os.IsNotExist(err) {
		t.Fatal("config ERROR not cleared")
	}
}

func TestChildWithoutConfigWaits(t *testing.T) {
	h := newHarness(t)
	h.start()
	ctrl := makeChild(t, h, "c", false)
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Lstat(filepath.Join(ctrl, "home")); !os.IsNotExist(err) {
		t.Fatal("a directory without config.json was admitted")
	}
	if sys := h.cfg.System.(*fakeSystem); sys.called("home agent-2") {
		t.Fatalf("a user was prepared: %v", sys)
	}
	if n := countNotes(h.mainHome, "-subagent-error.md"); n != 0 {
		t.Fatalf("parent notified %d times", n)
	}
	writeConfig(t, ctrl)
	h.drop(h.childHome(ctrl), "task.md", "do it")
	h.waitEnds(childID("c"), 1)
}

func TestParentChildReport(t *testing.T) {
	create := "mkdir -p agents/c && printf %s " + shellQuote(testConfig) + " > agents/c/config.json && " + sendDownText("c", "child-task: say hi")
	h := newHarness(t,
		matchStep(parentMark, create, 100), doneFor(parentMark),
		matchStep(childMark, sendMessageText("report: hi"), 100), doneFor(childMark),
		doneFor(parentMark), doneFor(parentMark),
	)
	h.start()
	h.markParent()
	h.drop(h.mainHome, "go.md", "make a child")
	waitFor(t, "parent receiving the report", func() bool {
		return h.sawRecord(`"file":"inbox/processed/`, `-c.md","from":"agents/c","content":"report: hi"}`)
	})
	if !h.sawRecord(`-owner.md","from":"owner","content":"child-task: say hi"}`) {
		t.Fatal("child task not delivered from owner")
	}
	if records := h.waitEnds(childID("c"), 1); lastEnd(records) != "done" {
		t.Fatalf("child end %v", records)
	}
}

func TestRemovalStopsAgent(t *testing.T) {
	h := newHarness(t, matchStep("inbox/processed/task.md", "sleep 30", 100))
	h.start()
	ctrl := makeChild(t, h, "c", true)
	h.drop(h.childHome(ctrl), "task.md", "work")
	waitFor(t, "child command", func() bool { return len(h.requests()) >= 1 })
	time.Sleep(100 * time.Millisecond)
	os.RemoveAll(ctrl)
	start := time.Now()
	if end := lastEnd(h.waitEnds(childID("c"), 1)); end != "removed" {
		t.Fatalf("end %s", end)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("removal took %s", time.Since(start))
	}
}

func TestMalformedSettingsKeepsAgentsRunning(t *testing.T) {
	h := newHarness(t, step("sleep 0.5", 100), done())
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	waitFor(t, "first request", func() bool { return len(h.requests()) >= 1 })
	os.WriteFile(h.cfg.SettingsFile(), []byte("{"), 0o644)
	if end := lastEnd(h.waitEnds(h.mainID, 1)); end != "done" {
		t.Fatalf("run ended %q after settings.json broke", end)
	}
	if fileContains(filepath.Join(h.mainCtrl, "ERROR"), "Agent limit") {
		t.Fatal("broken settings treated as max_agents=0")
	}
	time.Sleep(100 * time.Millisecond)
	if n := strings.Count(h.logs.String(), "settings"); n != 1 {
		t.Fatalf("settings error logged %d times", n)
	}
}

func TestAdmissionSeedsPoolHome(t *testing.T) {
	h := newHarness(t, matchStep("inbox/processed/task.md", "pwd; cat README.md | head -1", 100), `{"match":"inbox/processed/task.md","text":"done"}`)
	h.start()
	ctrl := makeChild(t, h, "c", true)
	home := h.childHome(ctrl)
	if home != filepath.Join(h.cfg.Homes, "agent-2") {
		t.Fatal(home)
	}
	for _, p := range []string{"README.md", "TASK.md", "inbox/processed", "agents"} {
		if _, err := os.Stat(filepath.Join(home, p)); err != nil {
			t.Fatal(p, err)
		}
	}
	h.drop(home, "task.md", "work")
	h.waitEnds(childID("c"), 1) // journals are keyed by the path from root
	if tool := h.requests()[1].Messages[2].Content; !strings.Contains(tool, home) {
		t.Fatal(tool)
	}
	sys := h.cfg.System.(*fakeSystem)
	if !sys.called("members agent-2 agent-1") || !sys.called("linger agent-2") {
		t.Fatal(sys)
	}
	if info, err := os.Stat(h.cfg.JournalDir(childID("c"))); err != nil || info.Mode().Perm() != 0o751 {
		t.Fatalf("journal dir %v %v", info, err)
	}
}

func TestConfigErrorGoesToControlDir(t *testing.T) {
	h := newHarness(t, done())
	h.start()
	ctrl := makeChild(t, h, "c", false)
	os.WriteFile(filepath.Join(ctrl, "config.json"), []byte(`{"provider":"replay","model":"nope"}`), 0o644)
	home := h.childHome(ctrl)
	h.drop(home, "task.md", "work")
	waitFor(t, "child ERROR", func() bool { return fileContains(filepath.Join(ctrl, "ERROR"), `config.json: unknown model "nope"`) })
	waitFor(t, "parent note", func() bool { return countNotes(h.mainHome, "-subagent-error.md") == 1 })
	time.Sleep(200 * time.Millisecond)
	if n := countNotes(h.mainHome, "-subagent-error.md"); n != 1 {
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
	h.start()
	a := makeChild(t, h, "a", true)
	h.childHome(a)
	b := makeChild(t, h, "b", true)
	errorFile := filepath.Join(b, "ERROR")
	waitFor(t, "limit ERROR", func() bool { return fileContains(errorFile, "Agent limit reached (max_agents=2)") })
	waitFor(t, "parent note", func() bool { return countNotes(h.mainHome, "-subagent-error.md") == 1 })
	time.Sleep(200 * time.Millisecond)
	if n := countNotes(h.mainHome, "-subagent-error.md"); n != 1 {
		t.Fatalf("parent notified %d times", n)
	}
	if _, err := os.Lstat(filepath.Join(b, "home")); !os.IsNotExist(err) {
		t.Fatal("b admitted despite the limit")
	}
	os.WriteFile(h.cfg.SettingsFile(), []byte(`{"max_agents":3}`), 0o644)
	if home := h.childHome(b); home != filepath.Join(h.cfg.Homes, "agent-3") {
		t.Fatal(home)
	}
	waitFor(t, "limit ERROR cleared", func() bool {
		_, err := os.Stat(errorFile)
		return os.IsNotExist(err)
	})
}

func TestAdmissionClearsLeftoverHome(t *testing.T) {
	h := newHarness(t)
	leftover := filepath.Join(h.cfg.Homes, "agent-2")
	os.MkdirAll(leftover, 0o755)
	os.WriteFile(filepath.Join(leftover, "stale.txt"), []byte("old"), 0o644)
	os.WriteFile(filepath.Join(leftover, "README.md"), []byte("old readme"), 0o644)
	h.start()
	ctrl := makeChild(t, h, "c", true)
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
	h.start()
	c := makeChild(t, h, "c", true)
	cHome := h.childHome(c)
	g := writeConfig(t, filepath.Join(cHome, "agents", "g"))
	if home := h.childHome(g); home != filepath.Join(h.cfg.Homes, "agent-3") {
		t.Fatal(home)
	}
	sys := h.cfg.System.(*fakeSystem)
	if !sys.called("members agent-3 agent-2,agent-1") {
		t.Fatal(sys)
	}
	// The grandchild's id is its path from root, so its journals sit inside
	// its parent's journal directory whichever user holds the parent.
	id := childID("c/agents/g")
	if info, err := os.Stat(h.cfg.JournalDir(id)); err != nil || !info.IsDir() {
		t.Fatalf("grandchild journal dir: %v", err)
	}
	if _, err := os.Stat(h.cfg.JournalDir(g)); !os.IsNotExist(err) {
		t.Fatalf("journal keyed by the pool home path: %v", err)
	}
	data, _ := os.ReadFile(h.cfg.PoolFile())
	if !strings.Contains(string(data), `{"user":"agent-3","agent":"`+id+`","state":"active"}`) {
		t.Fatalf("pool.json %s", data)
	}
	h.drop(h.childHome(g), "task.md", "work")
	if records := h.waitEnds(id, 1); lastEnd(records) != "done" {
		t.Fatalf("grandchild run %v", records)
	}
}

func TestRestartKeepsChildHome(t *testing.T) {
	h := newHarness(t)
	h.start()
	ctrl := makeChild(t, h, "c", true)
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
	if sys := h.cfg.System.(*fakeSystem); sys.called("home agent-3") {
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
	if n := strings.Count(h.logs.String(), "pool users not found: agent-3; agents are limited to 3"); n != 1 {
		t.Fatalf("logged %d times:\n%s", n, h.logs)
	}
}

// Defence in depth against a lost pool.json: a freshly assigned home whose
// journal link already points at this agent's journals is this agent's own
// home, so it is not cleared. Another agent's journal link does not count.
func TestLostPoolKeepsOwnHome(t *testing.T) {
	h := newHarness(t, done(), done())
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	h.waitEnds(h.mainID, 1)
	ctrl := makeChild(t, h, "c", true)
	home := h.childHome(ctrl)
	h.drop(home, "task.md", "work")
	h.waitEnds(childID("c"), 1)
	work := filepath.Join(home, "work.txt")
	os.WriteFile(work, []byte("mine"), 0o644)
	other := filepath.Join(h.cfg.Homes, "agent-3")
	os.MkdirAll(other, 0o755)
	os.WriteFile(filepath.Join(other, "stale.txt"), []byte("old"), 0o644)
	os.Symlink(h.cfg.JournalDir(childID("x")), filepath.Join(other, "journal"))
	h.stop()
	os.Remove(h.cfg.PoolFile())
	makeChild(t, h, "d", true)
	h.start()
	waitFor(t, "all admitted", func() bool {
		logs := h.logs.String()
		return strings.Count(logs, "main: admitted as agent-1") == 2 && strings.Count(logs, "main/agents/c: admitted as agent-2") == 2 && strings.Contains(logs, "main/agents/d: admitted as agent-3")
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

// TestRootChildAdmitted: main is root's child, admitted from root's control
// directory like any subagent.
func TestRootChildAdmitted(t *testing.T) {
	h := newHarness(t, done())
	h.start()
	if home := h.childHome(h.mainCtrl); home != h.cfg.PoolHome("agent-1") || home != h.mainHome {
		t.Fatalf("main's home %s", home)
	}
	for _, p := range []string{"README.md", "TASK.md", "inbox/processed", "agents"} {
		if _, err := os.Stat(filepath.Join(h.mainHome, p)); err != nil {
			t.Fatal(p, err)
		}
	}
	if !strings.Contains(poolJSON(h), `{"user":"agent-1","agent":"main","state":"active"}`) {
		t.Fatalf("pool.json %s", poolJSON(h))
	}
	if sys := h.cfg.System.(*fakeSystem); !sys.called("members agent-1 ") {
		t.Fatalf("root listed among main's group members: %v", sys)
	}
	h.drop(h.mainHome, "go.md", "go")
	h.waitEnds("main", 1)
	journal := filepath.Join(h.cfg.Log, "main")
	if target, _ := os.Readlink(filepath.Join(h.mainHome, "journal")); target != journal {
		t.Fatalf("journal link %q, want %s", target, journal)
	}
}

// TestTwoTopLevelAgentsIsolated: root's children are siblings, each with its
// own user and journals, and no ancestor in its group.
func TestTwoTopLevelAgentsIsolated(t *testing.T) {
	h := newHarness(t, done(), done())
	research := topLevel(t, h, "research")
	h.start()
	mainHome, researchHome := h.childHome(h.mainCtrl), h.childHome(research)
	if mainHome != h.cfg.PoolHome("agent-1") || researchHome != h.cfg.PoolHome("agent-2") {
		t.Fatalf("homes %s and %s", mainHome, researchHome)
	}
	sys := h.cfg.System.(*fakeSystem)
	if !sys.called("members agent-1 ") || !sys.called("members agent-2 ") {
		t.Fatalf("group members: %v", sys)
	}
	h.drop(mainHome, "go.md", "go")
	h.drop(researchHome, "go.md", "go")
	h.waitEnds("main", 1)
	h.waitEnds("research", 1)
	for _, id := range []string{"main", "research"} {
		if info, err := os.Stat(filepath.Join(h.cfg.Log, id)); err != nil || !info.IsDir() {
			t.Fatalf("journal of %s: %v", id, err)
		}
	}
}

// TestOwnerMessageFromRoot: root's spool for main reaches main's inbox
// labelled owner and is logged in main's conversation.
func TestOwnerMessageFromRoot(t *testing.T) {
	h := newHarness(t, done())
	h.start()
	spooled := h.sendOwner("hi")
	if from := inboxRecordFrom(h.waitEnds("main", 1), "hi"); from != "owner" {
		t.Fatalf("labelled %q", from)
	}
	log := readRecords(h.cfg.ConversationFile("main"))
	if len(log) != 1 || log[0]["from"] != "owner" || log[0]["text"] != "hi" {
		t.Fatalf("conversation %v", log)
	}
	if _, err := os.Stat(spooled); !os.IsNotExist(err) {
		t.Fatalf("spool file kept: %v", err)
	}
}

// TestTopLevelMessageLoggedOnly: a top-level agent's message to its owner is
// logged in its conversation and delivered to no inbox: root has none.
func TestTopLevelMessageLoggedOnly(t *testing.T) {
	h := newHarness(t, step(sendMessageText("tests pass"), 100), done())
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	waitFor(t, "message logged", func() bool {
		log := readRecords(h.cfg.ConversationFile("main"))
		return len(log) == 1 && log[0]["from"] == "agent" && log[0]["text"] == "tests pass"
	})
	waitFor(t, "spool emptied", func() bool {
		entries, _ := os.ReadDir(filepath.Join(h.mainHome, ".messages"))
		return len(entries) == 0
	})
	if log := readRecords(h.cfg.ConversationFile("main")); log[0]["agent"] != nil {
		t.Fatalf("record names the agent: %v", log[0])
	}
	time.Sleep(100 * time.Millisecond)
	filepath.WalkDir(h.root, func(path string, e fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(path, "-main.md") {
			t.Errorf("delivered to an inbox: %s", path)
		}
		return nil
	})
	if _, err := os.Stat(filepath.Join(h.cfg.RootHome(), "inbox")); !os.IsNotExist(err) {
		t.Fatalf("root got an inbox: %v", err)
	}
}

// TestRestartKeepsAdmittedChild: after a restart, a control directory with a
// home link and a pool record is the same agent, whether or not it still
// holds config.json, and keeps its home.
func TestRestartKeepsAdmittedChild(t *testing.T) {
	h := newHarness(t)
	h.start()
	marker := filepath.Join(h.mainHome, "marker.txt")
	if err := os.WriteFile(marker, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.stop()
	if err := os.Remove(filepath.Join(h.mainCtrl, "config.json")); err != nil {
		t.Fatal(err)
	}
	h.start()
	waitFor(t, "main admitted again", func() bool { return strings.Count(h.logs.String(), "main: admitted as agent-1") == 2 })
	if data, err := os.ReadFile(marker); err != nil || string(data) != "mine" {
		t.Fatalf("marker after restart: %q %v", data, err)
	}
	if sys := h.cfg.System.(*fakeSystem); sys.called("home agent-2") {
		t.Fatalf("a second user was prepared: %v", sys)
	}
}

// After pool.json is lost every agent looks unheld on start, so its journals
// are handed to its nearest held ancestor's group (root's, for main) before
// admission asks whether a home is the agent's own. The group the journal
// directory had must still count, or main's whole home, its children's
// control directories included, is cleared. The harness's helpers run
// without credentials, so the groups here come from LookupUser alone.
func TestLostPoolKeepsJournalEvidence(t *testing.T) {
	other := -1
	groups, _ := os.Getgroups()
	for _, g := range groups {
		if g != os.Getgid() {
			other = g
			break
		}
	}
	if other < 0 {
		t.Skip("needs a second group")
	}
	h := newHarness(t)
	self := uint32(os.Getuid())
	h.cfg.RootUser = "root-test"
	h.cfg.LookupUser = func(name string) (*syscall.Credential, error) {
		switch name {
		case "agent-1":
			return &syscall.Credential{Uid: self, Gid: uint32(other)}, nil
		case "root-test":
			return &syscall.Credential{Uid: self, Gid: uint32(os.Getgid())}, nil
		}
		return nil, nil
	}
	dir := h.cfg.JournalDir(h.mainID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(dir, -1, other); err != nil {
		t.Skip(err)
	}
	os.MkdirAll(h.mainHome, 0o755)
	os.Symlink(dir, filepath.Join(h.mainHome, "journal"))
	d := &daemon{cfg: h.cfg}
	var err error
	if d.pool, err = loadPool(h.cfg.PoolFile(), h.cfg.PoolUsers); err != nil {
		t.Fatal(err)
	}
	main := detachedMain(t, h, d)
	d.secureUnheldJournals(d.root.id)
	if journalOwnedBy(dir, other) {
		t.Fatal("unheld journals not handed to root's group")
	}
	if !main.ownsHome() {
		t.Fatal("main's own home not recognised after a lost pool.json")
	}
}
