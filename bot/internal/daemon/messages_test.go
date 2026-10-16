package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// messageRecords is main's conversation with root.
func messageRecords(h *harness) []map[string]any {
	return readRecords(h.cfg.ConversationFile(h.mainID))
}

func hasMessage(h *harness, from, text string) bool {
	for _, m := range messageRecords(h) {
		if m["from"] == from && m["text"] == text {
			return true
		}
	}
	return false
}

const sendMessage = "mkdir -p ~/.messages && printf 'tests pass' > ~/.messages/.m && mv ~/.messages/.m ~/.messages/20261011T090000Z.md"

func TestChildMessageReachesParent(t *testing.T) {
	h := newHarness(t,
		matchStep(childMark, sendMessageText("report: hi"), 100), doneFor(childMark),
		doneFor(parentMark), doneFor(parentMark))
	h.start()
	h.markParent()
	ctrl := makeChild(t, h, "research", true)
	h.childHome(ctrl)
	h.spoolDown(h.mainHome, "research", "child-task")
	waitFor(t, "parent gets labelled report", func() bool { return h.sawRecord(`"from":"agents/research"`, "report: hi") })
	if !h.sawRecord(`"from":"owner"`, "child-task") {
		t.Fatal("child task not labelled owner")
	}
	log := readRecords(h.cfg.ConversationFile(childID("research")))
	if len(log) != 2 || log[0]["from"] != "owner" || log[0]["text"] != "child-task" || log[1]["from"] != "agent" || log[1]["text"] != "report: hi" {
		t.Fatalf("conversation log %v", log)
	}
	if log[0]["t"] == nil || log[1]["agent"] != nil {
		t.Fatalf("conversation records %v", log)
	}
	if hasMessage(h, "agent", "report: hi") {
		t.Fatal("subagent reached main's conversation")
	}
	if !h.sawRecord(`-research.md"`, `"from":"agents/research"`) {
		t.Fatal("report file not named <UTC>-research.md")
	}
	if entries, _ := os.ReadDir(filepath.Join(h.mainHome, ".messages", "agents", "research")); len(entries) != 0 {
		t.Fatalf("down spool not emptied: %v", entries)
	}
}

func TestDownMessageWaitsForAdmission(t *testing.T) {
	h := newHarness(t, doneFor(childMark), doneFor(parentMark), doneFor(parentMark), doneFor(parentMark))
	os.WriteFile(h.cfg.SettingsFile(), []byte(`{"max_agents":2}`), 0o644)
	h.start()
	h.markParent()
	first := makeChild(t, h, "first", true)
	h.childHome(first)
	ctrl := makeChild(t, h, "late", true)
	spooled := h.spoolDown(h.mainHome, "late", "when you start")
	waitFor(t, "limit ERROR", func() bool { return fileContains(filepath.Join(ctrl, "ERROR"), "Agent limit") })
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(spooled); err != nil {
		t.Fatalf("message for a child not yet admitted was not kept: %v", err)
	}
	if n := countNotes(h.mainHome, "-undelivered.md"); n != 0 {
		t.Fatalf("%d undelivered notes for a waiting child", n)
	}
	os.RemoveAll(first)
	h.childHome(ctrl)
	waitFor(t, "delivery after admission", func() bool { return h.sawRecord(`-owner.md"`, `"from":"owner"`, "when you start") })
	if _, err := os.Stat(spooled); !os.IsNotExist(err) {
		t.Fatalf("spool file kept after delivery: %v", err)
	}
}

func TestDownMessageToMissingChildDropped(t *testing.T) {
	h := newHarness(t, doneFor(parentMark), doneFor(parentMark))
	h.start()
	h.markParent()
	spooled := h.spoolDown(h.mainHome, "ghost", "anyone there?")
	waitFor(t, "spool file dropped", func() bool {
		_, err := os.Stat(spooled)
		return os.IsNotExist(err)
	})
	want := fmt.Sprintf(MsgUndeliveredFmt, "ghost")
	waitFor(t, "undelivered note", func() bool { return h.sawRecord(`-undelivered.md"`, `"from":"data"`, want) })
	time.Sleep(100 * time.Millisecond)
	if n := countNotes(h.mainHome, "-undelivered.md"); n != 1 {
		t.Fatalf("%d undelivered notes", n)
	}
}

// The messages and notes the harness writes into an inbox are 0640: the
// home's group, the agent's ancestors, reads them, and nobody else.
func TestInboxFilesClosedToOthers(t *testing.T) {
	h := newHarness(t, done(), done())
	h.start()
	h.sendOwner("hi")
	h.waitEnds(h.mainID, 1)
	h.spoolDown(h.mainHome, "ghost", "anyone there?")
	h.waitEnds(h.mainID, 2)
	entries, _ := os.ReadDir(filepath.Join(h.mainHome, "inbox", "processed"))
	if len(entries) != 2 {
		t.Fatalf("processed %v", entries)
	}
	for _, e := range entries {
		if info, _ := e.Info(); info.Mode().Perm() != 0o640 {
			t.Errorf("%s: %v", e.Name(), info.Mode())
		}
	}
}

// A message for a child whose control directory can never be admitted (it
// holds no user and no config.json, only a home link naming a free user) is
// dropped with a note, like one for a child that does not exist.
func TestDownMessageToUnadmissibleChildDropped(t *testing.T) {
	h := newHarness(t, done())
	h.start()
	ctrl := makeChild(t, h, "x", false)
	os.Symlink(h.cfg.Home("agent-3"), filepath.Join(ctrl, "home"))
	spooled := h.spoolDown(h.mainHome, "x", "anyone there?")
	waitFor(t, "spool file dropped", func() bool { _, err := os.Stat(spooled); return os.IsNotExist(err) })
	want := fmt.Sprintf(MsgUndeliveredFmt, "x") + "\n"
	waitFor(t, "undelivered note", func() bool { return countNotes(h.mainHome, "-undelivered.md") == 1 })
	if notes := noteTexts(h.mainHome, "-undelivered.md"); len(notes) != 1 || notes[0] != want {
		t.Fatalf("notes %q", notes)
	}
}

// A failure while deciding whether a child that is not running may be
// admitted (here its config.json cannot be looked up, as its parent closed
// its control directory) is no answer: the message waits, the error is
// logged once and no note is sent. Once the lookup answers that there is no
// config.json, the message is dropped with the note.
func TestDownMessageWaitsOnAdmissionError(t *testing.T) {
	skipIfRoot(t)
	h := newHarness(t, done())
	h.start()
	ctrl := makeChild(t, h, "x", false)
	if err := os.Chmod(ctrl, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(ctrl, 0o755) })
	spooled := h.spoolDown(h.mainHome, "x", "anyone there?")
	waitFor(t, "error logged", func() bool { return strings.Contains(h.logs.String(), "permission denied") })
	time.Sleep(30 * h.cfg.Poll)
	if _, err := os.Stat(spooled); err != nil {
		t.Fatalf("message dropped on a transient error: %v", err)
	}
	if n := countNotes(h.mainHome, "-undelivered.md"); n != 0 {
		t.Fatalf("%d undelivered notes on a transient error", n)
	}
	if n := strings.Count(h.logs.String(), "x: not started: admission"); n != 1 {
		t.Fatalf("error logged %d times:\n%s", n, h.logs)
	}
	os.Chmod(ctrl, 0o755)
	waitFor(t, "spool file dropped", func() bool { _, err := os.Stat(spooled); return os.IsNotExist(err) })
	waitFor(t, "undelivered note", func() bool { return countNotes(h.mainHome, "-undelivered.md") == 1 })
}

// Root has no inbox: a message from root to no child is dropped and the
// service log says so.
func TestRootUndeliveredLoggedOnly(t *testing.T) {
	h := newHarness(t)
	h.start()
	spooled := h.spoolDown(h.cfg.RootHome(), "ghost", "anyone there?")
	waitFor(t, "spool file dropped", func() bool { _, err := os.Stat(spooled); return os.IsNotExist(err) })
	waitFor(t, "log", func() bool { return strings.Contains(h.logs.String(), fmt.Sprintf(MsgUndeliveredFmt, "ghost")) })
	time.Sleep(100 * time.Millisecond)
	noRootInbox(t, h)
}

func TestNestedDownSpoolIgnored(t *testing.T) {
	h := newHarness(t)
	h.start()
	c := makeChild(t, h, "c", true)
	d := makeChild(t, h, "d", true)
	cHome, dHome := h.childHome(c), h.childHome(d)
	// A spool for c's own child is c's to send, not main's.
	nested := h.spoolDown(filepath.Join(h.mainHome, ".messages", "agents", "c"), "x", "nested")
	// A symlinked spool directory is not a real spool.
	elsewhere := filepath.Join(h.root, "elsewhere")
	evil := h.queue(elsewhere, "linked")
	os.Remove(filepath.Join(h.mainHome, ".messages", "agents", "d")) // the spool admission made
	if err := os.Symlink(elsewhere, filepath.Join(h.mainHome, ".messages", "agents", "d")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	for _, home := range []string{h.mainHome, cHome, dHome} {
		if n := countNotes(home, ".md"); n != 0 {
			t.Fatalf("%s got %d inbox files", home, n)
		}
	}
	for _, path := range []string{nested, evil} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	if len(messageRecords(h)) != 0 || len(readRecords(h.cfg.ConversationFile(childID("c")))) != 0 || len(readRecords(h.cfg.ConversationFile(childID("d")))) != 0 {
		t.Fatal("an ignored spool reached a conversation log")
	}
	if n := strings.Count(h.logs.String(), "ignoring "+filepath.Join(h.mainHome, ".messages", "agents", "d")); n != 1 {
		t.Fatalf("symlinked spool logged %d times:\n%s", n, h.logs)
	}
}

func TestSpoolFileNotOwnedIgnored(t *testing.T) {
	h := newHarness(t, doneFor(parentMark))
	// The child's helper starts as us; then its user resolves to another
	// uid, so the files in its spool are no longer its own.
	var foreign atomic.Bool
	h.cfg.LookupUser = func(name string) (*syscall.Credential, error) {
		if name == "agent-2" && foreign.Load() {
			return &syscall.Credential{Uid: uint32(os.Getuid() + 1), Gid: uint32(os.Getgid())}, nil
		}
		return nil, nil
	}
	h.start()
	h.markParent()
	ctrl := makeChild(t, h, "c", true)
	home := h.childHome(ctrl)
	foreign.Store(true)
	planted := h.queue(filepath.Join(home, ".messages"), "planted")
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(planted); err != nil {
		t.Fatalf("foreign spool file not left in place: %v", err)
	}
	if n := countNotes(h.mainHome, "-c.md"); n != 0 {
		t.Fatal("a file the child does not own was delivered as its message")
	}
	if n := strings.Count(h.logs.String(), planted); n != 1 {
		t.Fatalf("foreign file logged %d times:\n%s", n, h.logs)
	}
}

func TestConversationLogMode(t *testing.T) {
	h := newHarness(t, done())
	h.start()
	log := h.cfg.ConversationFile(h.mainID)
	h.sendOwner("check the mode")
	waitFor(t, "owner message in log", func() bool { return hasMessage(h, "owner", "check the mode") })
	if info, err := os.Stat(log); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("log %v %v", info, err)
	}
}

func TestChildMessageTruncated(t *testing.T) {
	h := newHarness(t, doneFor(parentMark), doneFor(parentMark))
	h.start()
	h.markParent()
	ctrl := makeChild(t, h, "c", true)
	home := h.childHome(ctrl)
	h.queue(filepath.Join(home, ".messages"), strings.Repeat("x", 70<<10))
	var got []byte
	waitFor(t, "truncated report", func() bool {
		matches, _ := filepath.Glob(filepath.Join(h.mainHome, "inbox", "*-c.md"))
		more, _ := filepath.Glob(filepath.Join(h.mainHome, "inbox", "processed", "*-c.md"))
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
	h.start()
	h.sendOwner("please check the backups")
	waitFor(t, "owner message in log", func() bool { return hasMessage(h, "owner", "please check the backups") })
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
	h.start()
	h.markParent()
	ctrl = makeChild(t, h, "c", true)
	home := h.childHome(ctrl)
	h.stop()
	spool = filepath.Join(home, ".messages")
	spooled = h.queue(spool, text)
	if err := os.Chmod(spool, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(spool, 0o755) })
	h.start()
	waitFor(t, "delivery", func() bool { return countNotes(h.mainHome, "-c.md") == 1 })
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
	if n := countNotes(h.mainHome, "-c.md"); n != 1 {
		t.Fatalf("parent received %d deliveries of one message", n)
	}
	os.Chmod(spool, 0o755)
	waitFor(t, "spool file removed", func() bool {
		_, err := os.Stat(spooled)
		return os.IsNotExist(err)
	})
	time.Sleep(100 * time.Millisecond)
	if n := countNotes(h.mainHome, "-c.md"); n != 1 {
		t.Fatalf("parent received %d deliveries after the file was removed", n)
	}
}

func TestUnremovableChildMessageDeliveredOnce(t *testing.T) {
	skipIfRoot(t)
	h := newHarness(t, doneFor(parentMark), doneFor(parentMark), doneFor(parentMark))
	_, spool, spooled := unremovableChildMessage(t, h, "report once")
	time.Sleep(300 * time.Millisecond)
	if n := countNotes(h.mainHome, "-c.md"); n != 1 {
		t.Fatalf("parent received %d deliveries of one message", n)
	}
	if n := strings.Count(h.logs.String(), spooled+" delivered but not removed"); n != 1 {
		t.Fatalf("remove failure logged %d times:\n%s", n, h.logs)
	}
	if log := readRecords(h.cfg.ConversationFile(childID("c"))); len(log) != 1 {
		t.Fatalf("conversation log %v", log)
	}
	os.Chmod(spool, 0o755)
	waitFor(t, "spool file removed", func() bool {
		_, err := os.Stat(spooled)
		return os.IsNotExist(err)
	})
	time.Sleep(100 * time.Millisecond)
	if n := countNotes(h.mainHome, "-c.md"); n != 1 {
		t.Fatalf("parent received %d deliveries after the file was removed", n)
	}
}

func TestUnremovableMainMessageLoggedOnce(t *testing.T) {
	skipIfRoot(t)
	h := newHarness(t)
	h.start()
	h.stop()
	spool := filepath.Join(h.mainHome, ".messages")
	h.queue(spool, "log me once")
	if err := os.Chmod(spool, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(spool, 0o755) })
	h.start()
	waitFor(t, "agent message in log", func() bool { return hasMessage(h, "agent", "log me once") })
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

// deliveryRecords counts the delivery records naming a file ending in suffix.
func deliveryRecords(h *harness, suffix string, consumed bool) int {
	n := 0
	for _, r := range readRecords(h.cfg.DeliveredFile()) {
		if name, _ := r["name"].(string); strings.HasSuffix(name, suffix) && (r["consumed"] == true) == consumed {
			n++
		}
	}
	return n
}

// A delivery that keeps failing is retried under the same inbox name, with
// one delivery record and one log line, and delivered once when it can be.
func TestFailingDeliveryRetriedOnce(t *testing.T) {
	skipIfRoot(t)
	h := newHarness(t, doneFor(parentMark), doneFor(parentMark))
	h.start()
	h.markParent()
	ctrl := makeChild(t, h, "c", true)
	home := h.childHome(ctrl)
	inbox := filepath.Join(h.mainHome, "inbox")
	if err := os.Chmod(inbox, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(inbox, 0o755) })
	h.queue(filepath.Join(home, ".messages"), "report once")
	waitFor(t, "failed delivery", func() bool { return strings.Contains(h.logs.String(), "c: message ") })
	time.Sleep(30 * h.cfg.Poll)
	if n := deliveryRecords(h, "-c.md", false); n != 1 {
		t.Fatalf("%d delivery records while failing", n)
	}
	if n := strings.Count(h.logs.String(), "c: message "); n != 1 {
		t.Fatalf("failure logged %d times", n)
	}
	os.Chmod(inbox, 0o755)
	waitFor(t, "labelled delivery", func() bool { return h.sawRecord(`"from":"agents/c"`, "report once") })
	time.Sleep(10 * h.cfg.Poll)
	if n := countNotes(h.mainHome, "-c.md"); n != 1 {
		t.Fatalf("delivered %d times", n)
	}
	if n := deliveryRecords(h, "-c.md", false); n != 1 {
		t.Fatalf("%d delivery records", n)
	}
	if log := readRecords(h.cfg.ConversationFile(childID("c"))); len(log) != 1 {
		t.Fatalf("conversation log %v", log)
	}
}

// TestDownMessageWaitsForReadmission: an admitted child that is not running
// yet after a restart keeps its messages, though its control directory no
// longer holds a config.json: it holds a user, so it starts again.
func TestDownMessageWaitsForReadmission(t *testing.T) {
	h := newHarness(t, doneFor(childMark), doneFor(parentMark))
	h.start()
	h.markParent()
	ctrl := makeChild(t, h, "c", true)
	h.childHome(ctrl)
	h.stop()
	sys := h.cfg.System.(*fakeSystem)
	sys.setFail("linger agent-2", errors.New("linger failed"))
	h.start()
	spooled := h.spoolDown(h.mainHome, "c", "when you start")
	time.Sleep(20 * h.cfg.Poll)
	if _, err := os.Stat(spooled); err != nil {
		t.Fatalf("message for a child not yet running again was not kept: %v", err)
	}
	if n := countNotes(h.mainHome, "-undelivered.md"); n != 0 {
		t.Fatalf("%d undelivered notes for a waiting child", n)
	}
	sys.setFail("linger agent-2", nil)
	waitFor(t, "delivery after admission", func() bool { return h.sawRecord(`-owner.md"`, `"from":"owner"`, "when you start") })
}
