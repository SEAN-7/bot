package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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

// noteTexts returns the contents of the notes in dir's inbox, collected or
// not, whose names end with suffix.
func noteTexts(dir, suffix string) []string {
	var texts []string
	for _, sub := range []string{"inbox", "inbox/processed"} {
		entries, _ := os.ReadDir(filepath.Join(dir, sub))
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), suffix) {
				data, _ := os.ReadFile(filepath.Join(dir, sub, e.Name()))
				texts = append(texts, string(data))
			}
		}
	}
	return texts
}

// noRootInbox fails if anything created an inbox for root, which has none.
func noRootInbox(t *testing.T, h *harness) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(h.cfg.RootHome(), "inbox")); !os.IsNotExist(err) {
		t.Fatalf("root got an inbox: %v", err)
	}
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
	noRootInbox(t, h) // main is root's child: the service log reports it
	os.WriteFile(filepath.Join(h.mainHome, "config.json"), []byte(testConfig), 0o644)
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

// removingSystem moves the control directory ctrl away, as agent remove
// does, when admission enables the child's linger: after the scan, before
// the control directory is closed and the home link written.
type removingSystem struct {
	*fakeSystem
	ctrl  string
	moved atomic.Bool
}

func (s *removingSystem) EnableLinger(user string) error {
	if user != "agent-1" && !s.moved.Swap(true) {
		if err := os.Rename(s.ctrl, s.ctrl+".removed"); err != nil {
			return err
		}
	}
	return s.fakeSystem.EnableLinger(user)
}

func TestRemoveDuringAdmissionNotUndone(t *testing.T) {
	h := newHarness(t, done())
	ctrl := filepath.Join(h.mainHome, "agents", "x")
	sys := &removingSystem{fakeSystem: &fakeSystem{}, ctrl: ctrl}
	h.cfg.System = sys
	h.start()
	waitFor(t, "main admitted", func() bool { return strings.Contains(h.logs.String(), "main: admitted") })
	makeChild(t, h, "x", true)
	waitFor(t, "admission ended", func() bool {
		logs := h.logs.String()
		return strings.Contains(logs, childID("x")+": not started: control directory") ||
			strings.Contains(logs, childID("x")+": admitted")
	})
	time.Sleep(10 * h.cfg.Poll)
	if _, err := os.Lstat(ctrl); !os.IsNotExist(err) {
		t.Fatalf("removed control directory is back (%v):\n%s", err, h.logs)
	}
	if strings.Contains(h.logs.String(), childID("x")+": admitted") {
		t.Fatalf("removed agent started:\n%s", h.logs)
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
	// The harness's directories are its ancestors' to read (the home's
	// group), not its children's, should it let them through its home; so
	// is its control directory, created 0755 here.
	for _, p := range []string{"inbox", "inbox/processed", "agents", ".messages", ".messages/agents", ".outputs"} {
		if info, err := os.Lstat(filepath.Join(home, p)); err != nil || !info.IsDir() || info.Mode().Perm() != 0o750 {
			t.Fatalf("%s: %v %v", p, info, err)
		}
	}
	if info, _ := os.Stat(ctrl); info.Mode().Perm() != 0o750 {
		t.Fatalf("control directory %v", info.Mode())
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

// TestRestartTightensHarnessDirs: the harness's directories that an earlier
// release left open to others are closed to them when the agent is admitted
// again, keeping their group's bits, and so are its control directories,
// through its parent; root's stay 0700. No link is followed, and what is not
// a directory is left alone.
func TestRestartTightensHarnessDirs(t *testing.T) {
	h := newHarness(t)
	if err := os.Chmod(h.mainCtrl, 0o700); err != nil {
		t.Fatal(err)
	}
	h.start()
	ctrl := makeChild(t, h, "c", true)
	home := h.childHome(ctrl)
	h.stop()
	open := map[string]os.FileMode{"inbox": 0o755, "inbox/processed": 0o755, "agents": 0o775, ".messages": 0o755, ".messages/agents": 0o705}
	for p, mode := range open {
		path := filepath.Join(h.mainHome, p)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		os.Chmod(path, mode)
	}
	os.Chmod(ctrl, 0o755)
	outside := filepath.Join(h.root, "outside")
	os.Mkdir(outside, 0o755)
	os.Chmod(outside, 0o755)
	for _, dir := range []string{h.mainHome, home} {
		os.Remove(filepath.Join(dir, ".outputs"))
	}
	os.Symlink(outside, filepath.Join(h.mainHome, ".outputs"))
	os.WriteFile(filepath.Join(home, ".outputs"), []byte("mine"), 0o644)
	h.start()
	waitFor(t, "c admitted again", func() bool { return strings.Count(h.logs.String(), "c: admitted as agent-2") == 2 })
	for p, want := range map[string]os.FileMode{"inbox": 0o750, "inbox/processed": 0o750, "agents": 0o770, ".messages": 0o750, ".messages/agents": 0o700} {
		if info, _ := os.Lstat(filepath.Join(h.mainHome, p)); info.Mode().Perm() != want {
			t.Errorf("%s: %v, want %v", p, info.Mode(), want)
		}
	}
	for path, want := range map[string]os.FileMode{ctrl: 0o750, h.mainCtrl: 0o700, outside: 0o755} {
		if info, _ := os.Stat(path); info.Mode().Perm() != want {
			t.Errorf("%s: %v, want %v", path, info.Mode(), want)
		}
	}
	if info, _ := os.Lstat(filepath.Join(h.mainHome, ".outputs")); info.Mode()&fs.ModeSymlink == 0 {
		t.Errorf(".outputs link replaced: %v", info.Mode())
	}
	if got := fileText(t, filepath.Join(home, ".outputs")); got != "mine" {
		t.Errorf("c's .outputs file %q", got)
	}
}

// A harness directory left alone because it is not a real directory the
// agent owns (a link, a file) is named in the service log once per daemon
// run, so a directory left open is visible, also when admission is retried.
func TestSkippedHarnessDirLoggedOnce(t *testing.T) {
	h := newHarness(t)
	sys := h.cfg.System.(*fakeSystem)
	h.start()
	h.stop()
	outputs, spools := filepath.Join(h.mainHome, ".outputs"), filepath.Join(h.mainHome, ".messages", "agents")
	os.RemoveAll(outputs)
	os.Symlink(t.TempDir(), outputs)
	os.RemoveAll(spools)
	os.WriteFile(spools, nil, 0o644)
	sys.setFail("linger agent-1", errors.New("linger failed"))
	h.run()
	waitFor(t, "admission retried", func() bool { return strings.Count(sys.String(), "linger agent-1") >= 4 })
	sys.setFail("linger agent-1", nil)
	waitFor(t, "main admitted again", func() bool { return strings.Count(h.logs.String(), "main: admitted as agent-1") == 2 })
	for _, p := range []string{outputs, spools} {
		if n := strings.Count(h.logs.String(), "main: not closed to others: "+p+" "); n != 1 {
			t.Errorf("%s logged %d times:\n%s", p, n, h.logs)
		}
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

// TestRestartKeepsChildHome: a child whose home link went while the daemon
// was stopped is admitted again with its own user and home, the link
// restored, and runs.
func TestRestartKeepsChildHome(t *testing.T) {
	h := newHarness(t, done())
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
	h.drop(home, "task.md", "work")
	if end := lastEnd(h.waitEnds(childID("c"), 1)); end != "done" {
		t.Fatal(end)
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
		for _, e := range entries {
			if !e.IsDir() { // agents/, the down spools, is seeded
				return false
			}
		}
		return true
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
// home link and a pool record, its config.json long moved into the home, is
// the same agent and keeps its home.
func TestRestartKeepsAdmittedChild(t *testing.T) {
	h := newHarness(t)
	h.start()
	marker := filepath.Join(h.mainHome, "marker.txt")
	if err := os.WriteFile(marker, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.stop()
	h.start()
	waitFor(t, "main admitted again", func() bool { return strings.Count(h.logs.String(), "main: admitted as agent-1") == 2 })
	if data, err := os.ReadFile(marker); err != nil || string(data) != "mine" {
		t.Fatalf("marker after restart: %q %v", data, err)
	}
	if sys := h.cfg.System.(*fakeSystem); sys.called("home agent-2") {
		t.Fatalf("a second user was prepared: %v", sys)
	}
}

// TestAdmissionMovesConfigIntoHome: admission moves the starting config from
// the control directory into the new home, byte for byte.
func TestAdmissionMovesConfigIntoHome(t *testing.T) {
	h := newHarness(t)
	h.start()
	ctrl := makeChild(t, h, "c", false)
	config := `{"provider":"replay","model":"replay-1","options":{"style":"a"}}`
	if err := os.WriteFile(filepath.Join(ctrl, "config.json"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	home := h.childHome(ctrl)
	if data, err := os.ReadFile(filepath.Join(home, "config.json")); err != nil || string(data) != config {
		t.Fatalf("~/config.json %q %v", data, err)
	}
	for _, c := range []string{ctrl, h.mainCtrl} {
		if _, err := os.Lstat(filepath.Join(c, "config.json")); !os.IsNotExist(err) {
			t.Fatalf("config.json left in %s: %v", c, err)
		}
	}
}

// TestChildChangesOwnConfig: an agent edits its own ~/config.json; the next
// run uses it and starts fresh.
func TestChildChangesOwnConfig(t *testing.T) {
	h := newHarness(t, done(), done())
	h.start()
	h.drop(h.mainHome, "one.md", "one")
	h.waitEnds(h.mainID, 1)
	config := `{"provider":"replay","model":"replay-1","options":{"style":"b"}}`
	if err := os.WriteFile(filepath.Join(h.mainHome, "config.json"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	h.drop(h.mainHome, "two.md", "two")
	h.waitEnds(h.mainID, 2)
	reqs := h.requests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests", len(reqs))
	}
	if style := reqs[1].Options["style"]; style != "b" {
		t.Fatalf("options %v", reqs[1].Options)
	}
	if n := len(reqs[1].Messages); n != 1 {
		t.Fatalf("second run continued the first: %d messages", n)
	}
}

// TestInvalidOwnConfigReportsInControlDir: an invalid ~/config.json is
// reported in the control directory, where the agent cannot remove it, and
// to the parent once; fixing the file clears it.
func TestInvalidOwnConfigReportsInControlDir(t *testing.T) {
	h := newHarness(t, doneFor(parentMark), doneFor(childMark))
	h.start()
	h.markParent()
	ctrl := makeChild(t, h, "x", true)
	home := h.childHome(ctrl)
	config := filepath.Join(home, "config.json")
	os.WriteFile(config, []byte(`{"provider":"replay","model":"nope"}`), 0o644)
	h.drop(home, "task.md", "work")
	errorFile := filepath.Join(ctrl, "ERROR")
	waitFor(t, "ERROR", func() bool {
		data, _ := os.ReadFile(errorFile)
		return strings.HasPrefix(string(data), "config.json: ")
	})
	waitFor(t, "parent note", func() bool { return countNotes(h.mainHome, "-subagent-error.md") == 1 })
	time.Sleep(200 * time.Millisecond)
	if n := countNotes(h.mainHome, "-subagent-error.md"); n != 1 {
		t.Fatalf("parent notified %d times", n)
	}
	want := fmt.Sprintf(MsgSubagentFmt, childID("x"), `config.json: unknown model "nope"`)
	if notes := noteTexts(h.mainHome, "-subagent-error.md"); len(notes) != 1 || !strings.HasPrefix(notes[0], want) {
		t.Fatalf("parent note %q, want %q", notes, want)
	}
	if _, err := os.Stat(filepath.Join(home, "ERROR")); !os.IsNotExist(err) {
		t.Fatal("ERROR written into the agent's home")
	}
	os.WriteFile(config, []byte(testConfig), 0o644)
	h.waitEnds(childID("x"), 1)
	if _, err := os.Stat(errorFile); !os.IsNotExist(err) {
		t.Fatal("config ERROR not cleared")
	}
}

// TestStrayConfigInAdmittedControlDirIgnored: a config.json that appears in
// an admitted agent's control directory changes nothing: the agent keeps its
// user, its home and its own config.
func TestStrayConfigInAdmittedControlDirIgnored(t *testing.T) {
	h := newHarness(t, doneFor(childMark))
	h.start()
	ctrl := makeChild(t, h, "c", true)
	home := h.childHome(ctrl)
	os.WriteFile(filepath.Join(home, "work.txt"), []byte("mine"), 0o644)
	os.WriteFile(filepath.Join(ctrl, "config.json"), []byte(`{"provider":"replay","model":"other"}`), 0o644)
	time.Sleep(20 * h.cfg.Poll)
	h.drop(home, "task.md", "work")
	if end := lastEnd(h.waitEnds(childID("c"), 1)); end != "done" {
		t.Fatal(end)
	}
	if data, _ := os.ReadFile(filepath.Join(home, "config.json")); string(data) != testConfig {
		t.Fatalf("~/config.json %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(home, "work.txt")); string(data) != "mine" {
		t.Fatalf("home touched: %q", data)
	}
	if got, _ := os.Readlink(filepath.Join(ctrl, "home")); got != home {
		t.Fatalf("moved from %s to %s", home, got)
	}
	if n := strings.Count(h.logs.String(), "c: admitted as agent-2"); n != 1 {
		t.Fatalf("admitted %d times", n)
	}
	if sys := h.cfg.System.(*fakeSystem); sys.called("home agent-3") || sys.called("unlinger agent-2") {
		t.Fatal(sys)
	}
	if _, err := os.Stat(filepath.Join(ctrl, "ERROR")); !os.IsNotExist(err) {
		t.Fatal("stray config reported")
	}
}

// TestRestartWithoutOwnConfigReports: an agent that deleted its own
// config.json is admitted again after a restart and reported like any
// config error: ERROR in its control directory and one note to its parent.
// Restoring the file lets it run.
func TestRestartWithoutOwnConfigReports(t *testing.T) {
	h := newHarness(t, doneFor(parentMark), doneFor(childMark))
	h.start()
	h.markParent()
	ctrl := makeChild(t, h, "c", true)
	home := h.childHome(ctrl)
	h.stop()
	config := filepath.Join(home, "config.json")
	os.Remove(config)
	h.start()
	h.drop(home, "task.md", "work")
	errorFile := filepath.Join(ctrl, "ERROR")
	waitFor(t, "ERROR", func() bool {
		data, _ := os.ReadFile(errorFile)
		return strings.HasPrefix(string(data), "config.json: ")
	})
	waitFor(t, "parent note", func() bool { return countNotes(h.mainHome, "-subagent-error.md") == 1 })
	time.Sleep(200 * time.Millisecond)
	if n := countNotes(h.mainHome, "-subagent-error.md"); n != 1 {
		t.Fatalf("parent notified %d times", n)
	}
	os.WriteFile(config, []byte(testConfig), 0o644)
	if end := lastEnd(h.waitEnds(childID("c"), 1)); end != "done" {
		t.Fatal(end)
	}
}

// TestRestartSetsAsideStrayConfig: a config.json the parent left in an
// admitted agent's control directory is renamed config.json.ignored, replacing
// an older one, when the agent is admitted again, and said so once in the
// service log; the agent keeps its own. Anything else of that name is left
// alone.
func TestRestartSetsAsideStrayConfig(t *testing.T) {
	h := newHarness(t)
	h.start()
	ctrl := makeChild(t, h, "c", true)
	home := h.childHome(ctrl)
	stray, ignored := filepath.Join(ctrl, "config.json"), filepath.Join(ctrl, "config.json.ignored")
	for _, text := range []string{`{"provider":"replay","model":"other"}`, `{"provider":"replay","model":"newer"}`} {
		h.stop()
		os.WriteFile(stray, []byte(text), 0o644)
		h.start()
		waitFor(t, "stray config set aside", func() bool { _, err := os.Lstat(stray); return os.IsNotExist(err) })
		if got := fileText(t, ignored); got != text {
			t.Fatalf("config.json.ignored %q", got)
		}
	}
	if n := strings.Count(h.logs.String(), "config.json.ignored"); n != 2 {
		t.Fatalf("logged %d times:\n%s", n, h.logs)
	}
	if data, _ := os.ReadFile(filepath.Join(home, "config.json")); string(data) != testConfig {
		t.Fatalf("~/config.json %q", data)
	}
	h.stop()
	os.MkdirAll(filepath.Join(stray, "keep"), 0o755)
	h.start()
	waitFor(t, "c admitted again", func() bool { return strings.Count(h.logs.String(), "c: admitted as agent-2") == 4 })
	if _, err := os.Stat(filepath.Join(stray, "keep")); err != nil {
		t.Fatalf("directory named config.json removed: %v", err)
	}
}

// A root child held back by the limit is reported in its ERROR and the
// service log; root has no inbox.
func TestRootChildHoldBackLoggedOnly(t *testing.T) {
	h := newHarness(t)
	os.WriteFile(h.cfg.SettingsFile(), []byte(`{"max_agents":1}`), 0o644)
	h.start()
	y := topLevel(t, h, "y")
	waitFor(t, "limit ERROR", func() bool { return fileContains(filepath.Join(y, "ERROR"), "Agent limit reached (max_agents=1)") })
	waitFor(t, "log", func() bool { return strings.Contains(h.logs.String(), "y: not started") })
	time.Sleep(100 * time.Millisecond)
	noRootInbox(t, h)
}

// A starting config.json over the size limit is reported in the control
// directory's ERROR and once to the parent, and takes no user; a smaller one
// is admitted and clears the ERROR.
func TestOversizedStartingConfigReported(t *testing.T) {
	h := newHarness(t)
	h.start()
	ctrl := makeChild(t, h, "big", false)
	writeAtomic(t, filepath.Join(ctrl, "config.json"), oversizedConfig())
	text := MsgConfigPrefix + MsgConfigTooLarge
	errorFile := filepath.Join(ctrl, "ERROR")
	waitFor(t, "ERROR", func() bool { return fileContains(errorFile, text) })
	waitFor(t, "parent note", func() bool { return countNotes(h.mainHome, "-subagent-error.md") == 1 })
	time.Sleep(200 * time.Millisecond)
	want := fmt.Sprintf(MsgSubagentFmt, childID("big"), text)
	if notes := noteTexts(h.mainHome, "-subagent-error.md"); len(notes) != 1 || notes[0] != want+"\n" {
		t.Fatalf("parent notes %q, want %q", notes, want)
	}
	if got := fileText(t, errorFile); got != text+"\n" {
		t.Fatalf("ERROR %q", got)
	}
	if sys := h.cfg.System.(*fakeSystem); sys.called("home agent-2") {
		t.Fatalf("a user was prepared: %v", sys)
	}
	writeAtomic(t, filepath.Join(ctrl, "config.json"), testConfig)
	h.childHome(ctrl)
	waitFor(t, "ERROR cleared", func() bool { _, err := os.Lstat(errorFile); return os.IsNotExist(err) })
}

// For root's child, an oversized starting config.json goes to its ERROR and
// the service log only.
func TestOversizedRootChildConfigLoggedOnly(t *testing.T) {
	h := newHarness(t)
	h.start()
	ctrl := filepath.Join(h.cfg.RootHome(), "agents", "big")
	os.Mkdir(ctrl, 0o700)
	writeAtomic(t, filepath.Join(ctrl, "config.json"), oversizedConfig())
	text := MsgConfigPrefix + MsgConfigTooLarge
	waitFor(t, "ERROR", func() bool { return fileContains(filepath.Join(ctrl, "ERROR"), text) })
	waitFor(t, "log", func() bool { return strings.Contains(h.logs.String(), "big: not started: "+text) })
	time.Sleep(100 * time.Millisecond)
	noRootInbox(t, h)
}

// An oversized ~/config.json is reported as too large, not as the JSON
// error of the part that was read.
func TestOversizedOwnConfigReported(t *testing.T) {
	h := newHarness(t)
	h.start()
	writeAtomic(t, filepath.Join(h.mainHome, "config.json"), oversizedConfig())
	h.drop(h.mainHome, "go.md", "go")
	errorFile := filepath.Join(h.mainCtrl, "ERROR")
	waitFor(t, "ERROR", func() bool { return fileContains(errorFile, MsgConfigPrefix) })
	if got := fileText(t, errorFile); got != MsgConfigPrefix+MsgConfigTooLarge+"\n" {
		t.Fatalf("ERROR %q", got)
	}
	noRootInbox(t, h)
}

// oversizedConfig is a valid config.json just over the 1 MiB limit.
func oversizedConfig() string {
	head := `{"provider":"replay","model":"replay-1","pad":"`
	return head + strings.Repeat("a", configLimit-len(head)) + `"}`
}

// writeAtomic writes text to path with a dot-name rename, as agent new does.
func writeAtomic(t *testing.T, path, text string) {
	t.Helper()
	temp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err := os.WriteFile(temp, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temp, path); err != nil {
		t.Fatal(err)
	}
}

// After a restart, an agent's own ~/config.json wins over a stray
// config.json in its control directory, which is set aside: the next run
// uses the home's.
func TestRestartKeepsOwnConfigOverStray(t *testing.T) {
	h := newHarness(t, doneFor(childMark))
	h.start()
	ctrl := makeChild(t, h, "c", true)
	home := h.childHome(ctrl)
	own := `{"provider":"replay","model":"replay-1","options":{"style":"b"}}`
	stray := `{"provider":"replay","model":"replay-1","options":{"style":"a"}}`
	os.WriteFile(filepath.Join(home, "config.json"), []byte(own), 0o644)
	h.stop()
	os.WriteFile(filepath.Join(ctrl, "config.json"), []byte(stray), 0o644)
	h.start()
	waitFor(t, "stray config set aside", func() bool { _, err := os.Lstat(filepath.Join(ctrl, "config.json")); return os.IsNotExist(err) })
	if got := fileText(t, filepath.Join(ctrl, "config.json.ignored")); got != stray {
		t.Fatalf("config.json.ignored %q", got)
	}
	h.drop(home, "task.md", "work")
	h.waitEnds(childID("c"), 1)
	reqs := h.requests()
	if len(reqs) != 1 || reqs[0].Options["style"] != "b" {
		t.Fatalf("requests %+v", reqs)
	}
	if got := fileText(t, filepath.Join(home, "config.json")); got != own {
		t.Fatalf("~/config.json %q", got)
	}
}
