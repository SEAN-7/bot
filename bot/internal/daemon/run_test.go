package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SEAN-7/bot/internal/plugin"
	"github.com/SEAN-7/bot/internal/seed"
)

func TestBasicRun(t *testing.T) {
	h := newHarness(t, step("echo hello", 100), done())
	h.start()
	h.drop(h.mainHome, "hi.md", "hello there")
	records := h.waitEnds(h.mainID, 1)
	if lastEnd(records) != "done" {
		t.Fatalf("end %v", records)
	}
	reqs := h.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests %d", len(reqs))
	}
	sys := reqs[0].System
	if len(sys) != 2 || sys[0].Text != SystemPrompt || sys[1].Text != seed.README {
		t.Fatalf("system %+v", sys)
	}
	if len(reqs[0].Tools) != 1 || reqs[0].Tools[0].Name != "sh" || reqs[0].Model != "replay-1" {
		t.Fatalf("request %+v", reqs[0])
	}
	want := `{"file":"inbox/processed/hi.md","from":"data","content":"hello there"}`
	if reqs[0].Messages[0].Role != "user" || !strings.Contains(reqs[0].Messages[0].Content, want) {
		t.Fatalf("first message %q", reqs[0].Messages[0].Content)
	}
	msgs := reqs[1].Messages
	if msgs[1].Role != "assistant" || *msgs[1].Command != "echo hello" || msgs[1].CallID != "call-1" {
		t.Fatalf("assistant %+v", msgs[1])
	}
	if msgs[2].Role != "tool" || msgs[2].CallID != "call-1" || !strings.HasPrefix(msgs[2].Content, "exit status 0\nhello") {
		t.Fatalf("tool %+v", msgs[2])
	}
	if _, err := os.Stat(filepath.Join(h.mainHome, "inbox", "processed", "hi.md")); err != nil {
		t.Fatal(err)
	}
	if target, _ := os.Readlink(filepath.Join(h.mainHome, "journal")); target != h.cfg.JournalDir(h.mainID) {
		t.Fatalf("journal link %q", target)
	}
	types := map[string]bool{}
	for _, r := range records {
		types[r["type"].(string)] = true
	}
	for _, typ := range []string{"start", "inbox", "assistant", "result", "end"} {
		if !types[typ] {
			t.Errorf("journal lacks %s record", typ)
		}
	}
}

func TestOutputClipping(t *testing.T) {
	h := newHarness(t, step("head -c 40000 /dev/zero | tr '\\0' a", 100), done())
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	records := h.waitEnds(h.mainID, 1)
	tool := h.requests()[1].Messages[2].Content
	if !strings.Contains(tool, "bytes clipped") || !strings.HasSuffix(tool, MsgOutputClipped) || len(tool) > 17000 {
		t.Fatalf("tool content len %d: %.200q", len(tool), tool)
	}
	if _, err := os.Stat(filepath.Join(h.mainHome, ".outputs")); !os.IsNotExist(err) {
		t.Fatalf("clipped output saved: %v", err)
	}
	for _, r := range records {
		if r["type"] == "result" && r["output"] != tool {
			t.Fatalf("journal output differs from what the model saw: %.100q", r["output"])
		}
	}
}

func TestCommandTimeout(t *testing.T) {
	h := newHarness(t, step("sleep 30", 100), done())
	h.cfg.CommandWait = 300 * time.Millisecond
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	if end := lastEnd(h.waitEnds(h.mainID, 1)); end != "done" {
		t.Fatalf("end %s", end)
	}
	tool := h.requests()[1].Messages[2].Content
	if !strings.HasPrefix(tool, "Command timed out after 300ms; its process group was killed. Give it a longer wait (up to 3s)") {
		t.Fatalf("tool %q", tool)
	}
}

// The model's wait replaces the default, up to the maximum, and is passed
// back to the plugin with the rest of the call.
func TestCommandWait(t *testing.T) {
	h := newHarness(t, waitStep("sleep 0.5; echo slept", 1, 100), waitStep("sleep 30", 600, 100), done())
	h.cfg.CommandWait, h.cfg.MaxCommandWait = 100*time.Millisecond, 2*time.Second
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	if end := lastEnd(h.waitEnds(h.mainID, 1)); end != "done" {
		t.Fatalf("end %s", end)
	}
	reqs := h.requests()
	if got := reqs[1].Messages[2].Content; got != "exit status 0\nslept\n" {
		t.Fatalf("waited command: %q", got)
	}
	if got := string(reqs[1].Messages[1].Wait); got != "1" {
		t.Fatalf("wait sent back to the plugin: %q", got)
	}
	if got := reqs[2].Messages[4].Content; !strings.HasPrefix(got, "Command timed out after 2s") {
		t.Fatalf("capped command: %q", got)
	}
}

func TestCommandWaitArgument(t *testing.T) {
	def, limit := 120*time.Second, 600*time.Second
	for raw, want := range map[string]time.Duration{
		``:        def,
		`30`:      30 * time.Second,
		`30.9`:    30 * time.Second,
		`0`:       time.Second,
		`-5`:      time.Second,
		`600`:     limit,
		`99999`:   limit,
		`1e308`:   limit,
		`"45"`:    def,
		`null`:    def,
		`true`:    def,
		`[60]`:    def,
		`{"s":1}`: def,
	} {
		if got := commandWait(json.RawMessage(raw), def, limit); got != want {
			t.Errorf("wait %s: got %v, want %v", raw, got, want)
		}
	}
}

func TestMidRunInboxDelivery(t *testing.T) {
	h := newHarness(t, step("sleep 0.5", 100), done())
	h.start()
	h.drop(h.mainHome, "first.md", "first")
	waitFor(t, "first request", func() bool { return len(h.requests()) >= 1 })
	h.drop(h.mainHome, "late.md", "late news")
	h.waitEnds(h.mainID, 1)
	msgs := h.requests()[1].Messages
	last := msgs[len(msgs)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "late.md") || !strings.Contains(last.Content, "late news") {
		t.Fatalf("last message %+v", last)
	}
	if msgs[len(msgs)-2].Role != "tool" {
		t.Fatalf("new inbox not after tool result: %+v", msgs)
	}
}

func TestWhitespaceCommandEndsRun(t *testing.T) {
	h := newHarness(t, step("   ", 100), step("echo never", 100))
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	if end := lastEnd(h.waitEnds(h.mainID, 1)); end != "done" {
		t.Fatalf("end %s", end)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(h.requests()); n != 1 {
		t.Fatalf("requests %d", n)
	}
}

// A dot-file in the inbox does not wake the agent, but the next run, woken
// by something else, collects it; a directory is never collected.
func TestDotFilesWaitForARun(t *testing.T) {
	h := newHarness(t, done())
	h.start()
	os.WriteFile(filepath.Join(h.mainHome, "inbox", ".quiet"), []byte("later"), 0o644)
	os.MkdirAll(filepath.Join(h.mainHome, "inbox", "somedir"), 0o755)
	time.Sleep(200 * time.Millisecond)
	if n := len(h.requests()); n != 0 {
		t.Fatalf("woke on a dot-file: %d requests", n)
	}
	h.drop(h.mainHome, "x.md", "wake")
	var files []string
	for _, r := range h.waitEnds(h.mainID, 1) {
		if r["type"] == "inbox" {
			files = append(files, r["file"].(string))
			if strings.HasSuffix(r["file"].(string), ".quiet") && (r["from"] != "data" || r["content"] != "later") {
				t.Fatalf("dot-file record %v", r)
			}
		}
	}
	if strings.Join(files, " ") != "inbox/processed/.quiet inbox/processed/x.md" {
		t.Fatalf("collected %v", files)
	}
}

// The harness stages what it writes into an inbox in inbox/.tmp, so nothing
// half-written is ever in the inbox itself; admission clears leftovers.
func TestInboxWritesStaged(t *testing.T) {
	h := newHarness(t, done())
	stage := filepath.Join(h.mainHome, "inbox", ".tmp")
	os.MkdirAll(stage, 0o750)
	os.WriteFile(filepath.Join(stage, ".x.md.123.tmp"), []byte("half"), 0o640)
	h.start()
	waitFor(t, "leftover cleared", func() bool { entries, _ := os.ReadDir(stage); return len(entries) == 0 })
	h.sendOwner("hello")
	records := h.waitEnds(h.mainID, 1)
	for _, r := range records {
		if r["type"] == "inbox" && (r["from"] != "owner" || strings.Contains(r["file"].(string), ".tmp")) {
			t.Fatalf("record %v", r)
		}
	}
	statMode(t, stage, os.ModeDir|0o750)
	// An agent that removes the staging directory gets it back.
	os.RemoveAll(stage)
	h.sendOwner("again")
	waitFor(t, "second message", func() bool { return countNotes(h.mainHome, "-owner.md") == 2 })
}

func TestJournalSymlinkRepaired(t *testing.T) {
	h := newHarness(t, done())
	h.start()
	os.WriteFile(filepath.Join(h.mainHome, "journal"), []byte("not a link"), 0o644)
	h.drop(h.mainHome, "go.md", "go")
	h.waitEnds(h.mainID, 1)
	if target, err := os.Readlink(filepath.Join(h.mainHome, "journal")); err != nil || target != h.cfg.JournalDir(h.mainID) {
		t.Fatalf("journal link %q %v", target, err)
	}
}

// countRecorder counts the journal records it is given.
type countRecorder struct{ n *int }

func (r countRecorder) Record(string, map[string]any) error { *r.n++; return nil }
func (countRecorder) Attach(string, []byte) (string, error) {
	return "", errors.New("no attachments")
}

// Collecting the inbox makes inbox/processed again if the agent removed it,
// follows a link the agent put in its place to a directory, and fails
// before recording anything when it leads nowhere.
func TestInboxProcessedRecreatedOrFollowed(t *testing.T) {
	h := newHarness(t)
	main := detachedMain(t, h, &daemon{cfg: h.cfg, delivered: &delivered{pending: map[string]string{}}})
	inbox := filepath.Join(h.mainHome, "inbox")
	processed := filepath.Join(inbox, "processed")
	var recorded int
	j := countRecorder{&recorded}
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(inbox, "a.md"), []byte("a"), 0o644)
	if records, _, err := main.collectInbox(j, nil, 0); err != nil || len(records) != 1 {
		t.Fatalf("removed: %v %v", records, err)
	}
	if info, err := os.Lstat(processed); err != nil || !info.IsDir() || info.Mode().Perm() != 0o750 {
		t.Fatalf("processed not made again: %v %v", info, err)
	}
	elsewhere := t.TempDir()
	os.RemoveAll(processed)
	os.Symlink(elsewhere, processed)
	os.WriteFile(filepath.Join(inbox, "b.md"), []byte("b"), 0o644)
	if records, _, err := main.collectInbox(j, nil, 0); err != nil || len(records) != 1 {
		t.Fatalf("link: %v %v", records, err)
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "b.md")); err != nil {
		t.Fatalf("link not followed: %v", err)
	}
	os.Remove(processed)
	os.WriteFile(processed, nil, 0o644)
	os.WriteFile(filepath.Join(inbox, "c.md"), []byte("c"), 0o644)
	if _, _, err := main.collectInbox(j, nil, 0); err == nil || recorded != 2 {
		t.Fatalf("a file: %d records, %v", recorded, err)
	}
}

func TestVanishedInboxDoesNotCallModel(t *testing.T) {
	h := newHarness(t, step("echo never", 100))
	d := &daemon{cfg: h.cfg, delivered: &delivered{pending: map[string]string{}},
		runner: &plugin.Runner{PluginDir: h.cfg.Plugins(), CredDir: h.cfg.Credentials(), Timeout: 10 * time.Second}}
	main := detachedMain(t, h, d)
	if err := os.MkdirAll(filepath.Join(h.mainHome, "inbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The loop saw a file, but it vanished before the run collected it.
	main.run(context.Background())
	if n := len(h.requests()); n != 0 {
		t.Fatalf("model called %d times with nothing to say", n)
	}
}
