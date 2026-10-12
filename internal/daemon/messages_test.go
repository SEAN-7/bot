package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func messageRecords(h *harness) []map[string]any {
	data, _ := os.ReadFile(h.cfg.MessagesFile())
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func hasMessage(h *harness, from, agent, text string) bool {
	for _, m := range messageRecords(h) {
		if m["from"] == from && m["text"] == text && (agent == "" || m["agent"] == agent) {
			return true
		}
	}
	return false
}

const sendMessage = "mkdir -p ~/.messages && printf 'tests pass' > ~/.messages/.m && mv ~/.messages/.m ~/.messages/20261011T090000Z.md"

func TestAgentMessageLogged(t *testing.T) {
	h := newHarness(t, step(sendMessage, 100), done())
	h.drop(h.cfg.Home, "go.md", "go")
	h.start()
	waitFor(t, "agent message in log", func() bool { return hasMessage(h, "agent", "main", "tests pass") })
	waitFor(t, "spool emptied", func() bool {
		entries, _ := os.ReadDir(filepath.Join(h.cfg.Home, ".messages"))
		return len(entries) == 0
	})
}

func TestChildMessageReachesParent(t *testing.T) {
	h := newHarness(t,
		matchStep(childMark, sendMessageText("report: hi"), 100), doneFor(childMark),
		doneFor(parentMark), doneFor(parentMark))
	h.markParent()
	ctrl := makeChild(t, h, "research", true)
	h.start()
	h.childHome(ctrl)
	h.spoolDown(h.cfg.Home, "research", "child-task")
	waitFor(t, "parent gets labelled report", func() bool { return h.sawRecord(`"from":"agents/research"`, "report: hi") })
	if !h.sawRecord(`"from":"owner"`, "child-task") {
		t.Fatal("child task not labelled owner")
	}
	log := readRecords(h.cfg.ConversationFile(ctrl))
	if len(log) != 2 || log[0]["from"] != "owner" || log[0]["text"] != "child-task" || log[1]["from"] != "agent" || log[1]["text"] != "report: hi" {
		t.Fatalf("conversation log %v", log)
	}
	if log[0]["t"] == nil || log[1]["agent"] != nil {
		t.Fatalf("conversation records %v", log)
	}
	if hasMessage(h, "agent", "research", "report: hi") {
		t.Fatal("subagent reached the human's log")
	}
	if !h.sawRecord(`-research.md"`, `"from":"agents/research"`) {
		t.Fatal("report file not named <UTC>-research.md")
	}
	if entries, _ := os.ReadDir(filepath.Join(h.cfg.Home, ".messages", "agents", "research")); len(entries) != 0 {
		t.Fatalf("down spool not emptied: %v", entries)
	}
}

func TestDownMessageWaitsForAdmission(t *testing.T) {
	h := newHarness(t, doneFor(childMark), doneFor(parentMark), doneFor(parentMark), doneFor(parentMark))
	h.markParent()
	os.WriteFile(h.cfg.SettingsFile(), []byte(`{"max_agents":1}`), 0o644)
	ctrl := makeChild(t, h, "late", true)
	spooled := h.spoolDown(h.cfg.Home, "late", "when you start")
	h.start()
	waitFor(t, "limit ERROR", func() bool { return fileContains(filepath.Join(ctrl, "ERROR"), "Agent limit") })
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(spooled); err != nil {
		t.Fatalf("message for a child not yet admitted was not kept: %v", err)
	}
	if n := countNotes(h.cfg.Home, "-undelivered.md"); n != 0 {
		t.Fatalf("%d undelivered notes for a waiting child", n)
	}
	os.WriteFile(h.cfg.SettingsFile(), []byte(`{"max_agents":2}`), 0o644)
	h.childHome(ctrl)
	waitFor(t, "delivery after admission", func() bool { return h.sawRecord(`-owner.md"`, `"from":"owner"`, "when you start") })
	if _, err := os.Stat(spooled); !os.IsNotExist(err) {
		t.Fatalf("spool file kept after delivery: %v", err)
	}
}

func TestDownMessageToMissingChildDropped(t *testing.T) {
	h := newHarness(t, doneFor(parentMark), doneFor(parentMark))
	h.markParent()
	spooled := h.spoolDown(h.cfg.Home, "ghost", "anyone there?")
	h.start()
	waitFor(t, "spool file dropped", func() bool {
		_, err := os.Stat(spooled)
		return os.IsNotExist(err)
	})
	want := fmt.Sprintf(MsgUndeliveredFmt, "ghost")
	waitFor(t, "undelivered note", func() bool { return h.sawRecord(`-undelivered.md"`, `"from":"data"`, want) })
	time.Sleep(100 * time.Millisecond)
	if n := countNotes(h.cfg.Home, "-undelivered.md"); n != 1 {
		t.Fatalf("%d undelivered notes", n)
	}
}

func TestNestedDownSpoolIgnored(t *testing.T) {
	h := newHarness(t)
	c := makeChild(t, h, "c", true)
	d := makeChild(t, h, "d", true)
	h.start()
	cHome, dHome := h.childHome(c), h.childHome(d)
	// A spool for c's own child is c's to send, not main's.
	nested := h.spoolDown(filepath.Join(h.cfg.Home, ".messages", "agents", "c"), "x", "nested")
	// A symlinked spool directory is not a real spool.
	elsewhere := filepath.Join(h.root, "elsewhere")
	evil := h.queue(elsewhere, "linked")
	if err := os.Symlink(elsewhere, filepath.Join(h.cfg.Home, ".messages", "agents", "d")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	for _, home := range []string{h.cfg.Home, cHome, dHome} {
		if n := countNotes(home, ".md"); n != 0 {
			t.Fatalf("%s got %d inbox files", home, n)
		}
	}
	for _, path := range []string{nested, evil} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	if len(messageRecords(h)) != 0 || len(readRecords(h.cfg.ConversationFile(c))) != 0 || len(readRecords(h.cfg.ConversationFile(d))) != 0 {
		t.Fatal("an ignored spool reached a conversation log")
	}
	if n := strings.Count(h.logs.String(), "ignoring "+filepath.Join(h.cfg.Home, ".messages", "agents", "d")); n != 1 {
		t.Fatalf("symlinked spool logged %d times:\n%s", n, h.logs)
	}
}

func TestSpoolFileNotOwnedIgnored(t *testing.T) {
	h := newHarness(t, doneFor(parentMark))
	h.markParent()
	// The child's helper starts as us; then its user resolves to another
	// uid, so the files in its spool are no longer its own.
	var foreign atomic.Bool
	h.cfg.LookupUser = func(name string) (*syscall.Credential, error) {
		if name == "agent-1" && foreign.Load() {
			return &syscall.Credential{Uid: uint32(os.Getuid() + 1), Gid: uint32(os.Getgid())}, nil
		}
		return nil, nil
	}
	ctrl := makeChild(t, h, "c", true)
	h.start()
	home := h.childHome(ctrl)
	foreign.Store(true)
	planted := h.queue(filepath.Join(home, ".messages"), "planted")
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(planted); err != nil {
		t.Fatalf("foreign spool file not left in place: %v", err)
	}
	if n := countNotes(h.cfg.Home, "-c.md"); n != 0 {
		t.Fatal("a file the child does not own was delivered as its message")
	}
	if n := strings.Count(h.logs.String(), planted); n != 1 {
		t.Fatalf("foreign file logged %d times:\n%s", n, h.logs)
	}
}

func TestMainLogMode(t *testing.T) {
	h := newHarness(t, done())
	// A log left by an earlier release is tightened on the next append.
	os.WriteFile(h.cfg.MessagesFile(), nil, 0o644)
	h.spool("check the mode")
	h.start()
	waitFor(t, "owner message in log", func() bool { return hasMessage(h, "owner", "", "check the mode") })
	if info, err := os.Stat(h.cfg.MessagesFile()); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("log %v %v", info, err)
	}
}

func TestChildMessageTruncated(t *testing.T) {
	h := newHarness(t, doneFor(parentMark), doneFor(parentMark))
	h.markParent()
	ctrl := makeChild(t, h, "c", true)
	h.start()
	home := h.childHome(ctrl)
	h.queue(filepath.Join(home, ".messages"), strings.Repeat("x", 70<<10))
	var got []byte
	waitFor(t, "truncated report", func() bool {
		matches, _ := filepath.Glob(filepath.Join(h.cfg.Home, "inbox", "*-c.md"))
		more, _ := filepath.Glob(filepath.Join(h.cfg.Home, "inbox", "processed", "*-c.md"))
		matches = append(matches, more...)
		if len(matches) != 1 {
			return false
		}
		got, _ = os.ReadFile(matches[0])
		return len(got) > 0
	})
	if want := strings.Repeat("x", 64<<10) + "\n[message truncated]"; string(got) != want {
		t.Fatalf("report is %d bytes, ends %q", len(got), got[max(0, len(got)-40):])
	}
}

func TestOwnerMessageLogged(t *testing.T) {
	h := newHarness(t, done())
	h.spool("please check the backups")
	h.start()
	waitFor(t, "owner message in log", func() bool { return hasMessage(h, "owner", "", "please check the backups") })
	for _, m := range messageRecords(h) {
		if m["from"] == "owner" && m["agent"] != nil {
			t.Fatalf("owner record carries an agent field: %v", m)
		}
	}
}

// skipIfRoot skips a test that relies on file modes, which root ignores.
func skipIfRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("file modes do not stop root")
	}
}

// unremovableChildMessage admits child c, then restarts the daemon with a
// message queued in c's read-only spool, and waits for its delivery.
func unremovableChildMessage(t *testing.T, h *harness, text string) (ctrl, spool, spooled string) {
	t.Helper()
	h.markParent()
	ctrl = makeChild(t, h, "c", true)
	h.start()
	home := h.childHome(ctrl)
	h.stop()
	spool = filepath.Join(home, ".messages")
	spooled = h.queue(spool, text)
	if err := os.Chmod(spool, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(spool, 0o755) })
	h.start()
	waitFor(t, "delivery", func() bool { return countNotes(h.cfg.Home, "-c.md") == 1 })
	return ctrl, spool, spooled
}

func TestUnreadableUnremovedMessageNotRedelivered(t *testing.T) {
	skipIfRoot(t)
	h := newHarness(t, doneFor(parentMark), doneFor(parentMark), doneFor(parentMark))
	_, spool, spooled := unremovableChildMessage(t, h, "report once")
	time.Sleep(100 * time.Millisecond)
	// The sender makes the delivered file unreadable for a few polls.
	if err := os.Chmod(spooled, 0); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	os.Chmod(spooled, 0o644)
	time.Sleep(200 * time.Millisecond)
	if n := countNotes(h.cfg.Home, "-c.md"); n != 1 {
		t.Fatalf("parent received %d deliveries of one message", n)
	}
	os.Chmod(spool, 0o755)
	waitFor(t, "spool file removed", func() bool {
		_, err := os.Stat(spooled)
		return os.IsNotExist(err)
	})
	time.Sleep(100 * time.Millisecond)
	if n := countNotes(h.cfg.Home, "-c.md"); n != 1 {
		t.Fatalf("parent received %d deliveries after the file was removed", n)
	}
}

func TestUnremovableChildMessageDeliveredOnce(t *testing.T) {
	skipIfRoot(t)
	h := newHarness(t, doneFor(parentMark), doneFor(parentMark), doneFor(parentMark))
	ctrl, spool, spooled := unremovableChildMessage(t, h, "report once")
	time.Sleep(300 * time.Millisecond)
	if n := countNotes(h.cfg.Home, "-c.md"); n != 1 {
		t.Fatalf("parent received %d deliveries of one message", n)
	}
	if n := strings.Count(h.logs.String(), spooled+" delivered but not removed"); n != 1 {
		t.Fatalf("remove failure logged %d times:\n%s", n, h.logs)
	}
	if log := readRecords(h.cfg.ConversationFile(ctrl)); len(log) != 1 {
		t.Fatalf("conversation log %v", log)
	}
	os.Chmod(spool, 0o755)
	waitFor(t, "spool file removed", func() bool {
		_, err := os.Stat(spooled)
		return os.IsNotExist(err)
	})
	time.Sleep(100 * time.Millisecond)
	if n := countNotes(h.cfg.Home, "-c.md"); n != 1 {
		t.Fatalf("parent received %d deliveries after the file was removed", n)
	}
}

func TestUnremovableMainMessageLoggedOnce(t *testing.T) {
	skipIfRoot(t)
	h := newHarness(t)
	spool := filepath.Join(h.cfg.Home, ".messages")
	h.queue(spool, "log me once")
	if err := os.Chmod(spool, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(spool, 0o755) })
	h.start()
	waitFor(t, "agent message in log", func() bool { return hasMessage(h, "agent", "main", "log me once") })
	time.Sleep(300 * time.Millisecond)
	n := 0
	for _, m := range messageRecords(h) {
		if m["text"] == "log me once" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("message logged %d times", n)
	}
}
