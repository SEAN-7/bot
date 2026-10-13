package daemon

import (
	"context"
	"encoding/json"
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
	if len(sys) != 3 || sys[0].Text != SystemPrompt || sys[1].Text != seed.README || sys[2].Text != seed.TASK {
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

func TestDotFilesIgnored(t *testing.T) {
	h := newHarness(t, done())
	h.start()
	partial := filepath.Join(h.mainHome, "inbox", ".partial")
	os.WriteFile(partial, []byte("half"), 0o644)
	os.MkdirAll(filepath.Join(h.mainHome, "inbox", "somedir"), 0o755)
	time.Sleep(200 * time.Millisecond)
	if n := len(h.requests()); n != 0 {
		t.Fatalf("woke on a dot-file: %d requests", n)
	}
	os.Rename(partial, filepath.Join(h.mainHome, "inbox", "x.md"))
	h.waitEnds(h.mainID, 1)
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
