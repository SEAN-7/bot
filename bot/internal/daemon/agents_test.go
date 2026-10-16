package daemon

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
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
		return h.sawRecord(`"file":"inbox/processed/`, `"content":"report: hi"}`)
	})
	if !h.sawRecord(`"content":"child-task: say hi"}`) {
		t.Fatal("child task not delivered")
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
// the home link is written.
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
		return strings.Contains(logs, childID("x")+": not started: home link") ||
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

// settings.json is read when the daemon starts; a later change waits for a
// restart.
func TestSettingsReadOnceAtStart(t *testing.T) {
	h := newHarness(t)
	os.WriteFile(h.cfg.SettingsFile(), []byte(`{"max_agents":1}`), 0o644)
	other := topLevel(t, h, "other")
	h.start()
	errorFile := filepath.Join(other, "ERROR")
	waitFor(t, "limit ERROR", func() bool { return fileContains(errorFile, "Agent limit reached (max_agents=1)") })
	os.WriteFile(h.cfg.SettingsFile(), []byte(`{"max_agents":4}`), 0o644)
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Lstat(filepath.Join(other, "home")); !os.IsNotExist(err) {
		t.Fatal("other admitted after settings.json changed")
	}
	if strings.Contains(h.logs.String(), "settings.json:") {
		t.Fatalf("settings logged after start:\n%s", h.logs)
	}
}

func TestAdmissionSeedsHome(t *testing.T) {
	h := newHarness(t, matchStep("inbox/processed/task.md", "pwd; cat README.md | head -1", 100), `{"match":"inbox/processed/task.md","text":"done"}`)
	h.start()
	ctrl := makeChild(t, h, "c", true)
	home := h.childHome(ctrl)
	if home != filepath.Join(h.cfg.Homes, "agent-2") {
		t.Fatal(home)
	}
	for _, p := range []string{"README.md", "inbox/processed", "agents"} {
		if _, err := os.Stat(filepath.Join(home, p)); err != nil {
			t.Fatal(p, err)
		}
	}
	// The harness's directories are its ancestors' to read (the home's
	// group), not its children's, should it let them through its home. The
	// inbox, outside the home, is also its ancestors' to write into, and
	// sticky; ~/inbox links to it.
	inbox := h.cfg.Inbox("agent-2")
	if info, err := os.Lstat(inbox); err != nil || !info.IsDir() || info.Mode().Perm() != 0o770 || info.Mode()&fs.ModeSticky == 0 {
		t.Fatalf("inbox: %v %v", info, err)
	}
	if target, err := os.Readlink(filepath.Join(home, "inbox")); err != nil || target != inbox {
		t.Fatalf("~/inbox %q %v", target, err)
	}
	for _, p := range []string{filepath.Join(inbox, "processed"), filepath.Join(home, "agents")} {
		if info, err := os.Lstat(p); err != nil || !info.IsDir() || info.Mode().Perm() != 0o750 {
			t.Fatalf("%s: %v %v", p, info, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(home, ".messages")); !os.IsNotExist(err) {
		t.Fatalf("a spool was created: %v", err)
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

// A harness directory that is not a real directory the agent owns (a link,
// a file) keeps the agent from being admitted: logged once, no link
// followed, nothing replaced, and admitted once the agent's owner puts a
// directory there.
func TestHarnessDirNotADirectoryHoldsAdmission(t *testing.T) {
	h := newHarness(t)
	h.start()
	h.stop()
	processed := filepath.Join(h.cfg.Inbox("agent-1"), "processed")
	outside := t.TempDir()
	os.Chmod(outside, 0o755)
	os.RemoveAll(processed)
	os.Symlink(outside, processed)
	h.run()
	time.Sleep(300 * time.Millisecond)
	if n := strings.Count(h.logs.String(), "main: not started: seed home: "+processed+" is not a directory this user owns"); n != 1 {
		t.Fatalf("logged %d times:\n%s", n, h.logs)
	}
	if strings.Count(h.logs.String(), "main: admitted as agent-1") != 1 {
		t.Fatal("admitted with a link in place of a harness directory")
	}
	if info, _ := os.Lstat(processed); info.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("inbox/processed link replaced: %v", info.Mode())
	}
	if info, _ := os.Stat(outside); info.Mode().Perm() != 0o755 {
		t.Errorf("followed a link: %v", info.Mode())
	}
	os.Remove(processed)
	os.Mkdir(processed, 0o750)
	waitFor(t, "main admitted again", func() bool { return strings.Count(h.logs.String(), "main: admitted as agent-1") == 2 })
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
	// The parent knows its child as c, not by the child's path from root.
	if notes := noteTexts(h.mainHome, "-subagent-error.md"); !strings.HasPrefix(notes[0], "Subagent c did not start: config.json: ") {
		t.Fatalf("parent note %q", notes[0])
	}
	if _, err := os.Stat(filepath.Join(home, "ERROR")); !os.IsNotExist(err) {
		t.Fatal("ERROR written into the subagent's home")
	}
	if names := inboxNames(home); len(names) != 1 {
		t.Fatalf("child inbox touched: %v", names)
	}
}

func TestMaxAgentsHoldsSubagentBack(t *testing.T) {
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
	os.RemoveAll(a)
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
	// The grandchild may write into its parent's inbox, the parent into main's.
	if !sys.inOrder("grant agent-1 "+itoa(os.Getuid()), "grant agent-2 "+itoa(os.Getuid())) {
		t.Fatal(sys)
	}
	// The grandchild's id is its path from root, so its journals sit inside
	// its parent's journal directory whichever user holds the parent.
	id := childID("c/agents/g")
	if info, err := os.Stat(h.cfg.JournalDir(id)); err != nil || !info.IsDir() {
		t.Fatalf("grandchild journal dir: %v", err)
	}
	if _, err := os.Stat(h.cfg.JournalDir(g)); !os.IsNotExist(err) {
		t.Fatalf("journal keyed by the home path: %v", err)
	}
	if users, _ := h.cfg.System.(*fakeSystem).Users("agent-"); len(users) != 3 || users[2] != (User{"agent-3", id}) {
		t.Fatalf("users %v", users)
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

// discoverer is an agent with its own empty home, its helper running as us.
func discoverer(t *testing.T) *agent {
	t.Helper()
	h := newHarness(t)
	a := detachedMain(t, h, &daemon{cfg: h.cfg})
	a.home = t.TempDir()
	return a
}

func emptyControlDir(t *testing.T, home, name string) string {
	t.Helper()
	dir := filepath.Join(home, "agents", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestControlDirsFindsRealDirs(t *testing.T) {
	// Every real directory the user owns is a control directory, whatever it
	// holds: the daemon decides what it is.
	a := discoverer(t)
	b, c := writeConfig(t, filepath.Join(a.home, "agents", "b")), writeConfig(t, filepath.Join(a.home, "agents", "a"))
	empty := emptyControlDir(t, a.home, "empty")
	linked := emptyControlDir(t, a.home, "linked")
	os.Symlink(t.TempDir(), filepath.Join(linked, "home"))
	// A home that is a real directory or a regular file is no link, but
	// the control directory is still found.
	homeDir := emptyControlDir(t, a.home, "homedir")
	os.Mkdir(filepath.Join(homeDir, "home"), 0o755)
	homeFile := emptyControlDir(t, a.home, "homefile")
	os.WriteFile(filepath.Join(homeFile, "home"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(a.home, "agents", "file"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(c, "home", "agents", "deep"), 0o755) // one level only
	got, err := controlDirs(a)
	if want := []string{c, b, empty, homeDir, homeFile, linked}; err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v %v, want %v", got, err, want)
	}
}

func TestControlDirsSkipsSymlinks(t *testing.T) {
	a, real := discoverer(t), t.TempDir()
	writeConfig(t, filepath.Join(real, "agents", "x"))
	// agents/ itself a symlink: nothing is found through it.
	os.Symlink(filepath.Join(real, "agents"), filepath.Join(a.home, "agents"))
	if got, err := controlDirs(a); err != nil || len(got) != 0 {
		t.Fatalf("symlinked agents: got %v %v", got, err)
	}
	os.Remove(filepath.Join(a.home, "agents"))
	// agents/x a symlink to a real control directory, agents/z a genuine
	// one.
	os.Mkdir(filepath.Join(a.home, "agents"), 0o755)
	os.Symlink(filepath.Join(real, "agents", "x"), filepath.Join(a.home, "agents", "x"))
	z := writeConfig(t, filepath.Join(a.home, "agents", "z"))
	if got, err := controlDirs(a); err != nil || !reflect.DeepEqual(got, []string{z}) {
		t.Fatalf("got %v %v", got, err)
	}
}

func TestControlDirsWithoutAgentsDir(t *testing.T) {
	a := discoverer(t)
	if got, err := controlDirs(a); err != nil || len(got) != 0 {
		t.Fatalf("no agents/: got %v %v", got, err)
	}
	a.home = filepath.Join(a.home, "missing")
	if got, err := controlDirs(a); err != nil || len(got) != 0 {
		t.Fatalf("no home: got %v %v", got, err)
	}
	os.WriteFile(filepath.Join(filepath.Dir(a.home), "missing"), []byte("x"), 0o644)
	if got, err := controlDirs(a); err != nil || len(got) != 0 {
		t.Fatalf("home a file: got %v %v", got, err)
	}
}

// A directory in agents/ that another user owns is not a control directory
// of this agent's user, nor is anything in an agents/ another user owns.
// Only root can make one, so the test needs root.
func TestControlDirsSkipsOtherUsersDirs(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to give a directory to another user")
	}
	a := discoverer(t)
	mine, theirs := emptyControlDir(t, a.home, "mine"), emptyControlDir(t, a.home, "theirs")
	if err := os.Lchown(theirs, 1, 1); err != nil {
		t.Fatal(err)
	}
	if got, err := controlDirs(a); err != nil || !reflect.DeepEqual(got, []string{mine}) {
		t.Fatalf("got %v %v", got, err)
	}
	if err := os.Lchown(filepath.Join(a.home, "agents"), 1, 1); err != nil {
		t.Fatal(err)
	}
	if got, err := controlDirs(a); err != nil || len(got) != 0 {
		t.Fatalf("got %v %v", got, err)
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
		"/home/agent/agents/.x":           false,
		"/home/agent/agents/a\x1bb":       false,
		"/home/agent/agents/a\x7fb":       false,
		"/home/agent/agents/a b":          true,
	} {
		if got := validControlDir(home, dir); got != want {
			t.Errorf("validControlDir(%q) = %v, want %v", dir, got, want)
		}
	}
}

// TestRootChildAdmitted: main is root's child, admitted from root's control
// directory like any subagent.
func TestRootChildAdmitted(t *testing.T) {
	h := newHarness(t, done())
	h.start()
	if home := h.childHome(h.mainCtrl); home != h.cfg.Home("agent-1") || home != h.mainHome {
		t.Fatalf("main's home %s", home)
	}
	for _, p := range []string{"README.md", "inbox/processed", "agents"} {
		if _, err := os.Stat(filepath.Join(h.mainHome, p)); err != nil {
			t.Fatal(p, err)
		}
	}
	if !onlyMain(h) {
		t.Fatal(h.cfg.System)
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
	if mainHome != h.cfg.Home("agent-1") || researchHome != h.cfg.Home("agent-2") {
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
	if _, err := os.Stat(spooled); !os.IsNotExist(err) {
		t.Fatalf("inbox file kept: %v", err)
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
// run uses it.
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
	want := fmt.Sprintf(MsgSubagentFmt, "x", `config.json: unknown model "nope"`)
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

// An oversized ~/config.json is reported as too large, not as the JSON
// error of the part that was read.
func TestOversizedOwnConfigReported(t *testing.T) {
	h := newHarness(t)
	h.start()
	writeAtomic(t, filepath.Join(h.mainHome, "config.json"), oversizedConfig())
	h.drop(h.mainHome, "go.md", "go")
	errorFile := filepath.Join(h.mainCtrl, "ERROR")
	waitFor(t, "ERROR", func() bool { return fileContains(errorFile, MsgConfigPrefix) })
	if got := fileText(t, errorFile); got != MsgConfigPrefix+"larger than 1 MiB\n" {
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
// config.json in its control directory, which is left where it is: the next
// run uses the home's.
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
	waitFor(t, "c admitted again", func() bool { return strings.Count(h.logs.String(), "c: admitted as agent-2") == 2 })
	h.drop(home, "task.md", "work")
	h.waitEnds(childID("c"), 1)
	reqs := h.requests()
	if len(reqs) != 1 || reqs[0].Options["style"] != "b" {
		t.Fatalf("requests %+v", reqs)
	}
	if got := fileText(t, filepath.Join(home, "config.json")); got != own {
		t.Fatalf("~/config.json %q", got)
	}
	if got := fileText(t, filepath.Join(ctrl, "config.json")); got != stray {
		t.Fatalf("control directory config.json %q", got)
	}
}
