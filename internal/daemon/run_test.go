package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBasicRun(t *testing.T) {
	h := newHarness(t, step("echo hello", 100), done())
	h.drop(h.cfg.Home, "hi.md", "hello there")
	h.start()
	records := h.waitEnds(h.cfg.Home, 1)
	if lastEnd(records) != "done" {
		t.Fatalf("end %v", records)
	}
	reqs := h.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests %d", len(reqs))
	}
	sys := reqs[0].System
	if len(sys) != 3 || sys[0].Text != SystemPrompt || sys[1].Text != "readme" || sys[2].Text != "task" {
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
	if _, err := os.Stat(filepath.Join(h.cfg.Home, "inbox", "processed", "hi.md")); err != nil {
		t.Fatal(err)
	}
	if target, _ := os.Readlink(filepath.Join(h.cfg.Home, "journal")); target != h.cfg.JournalDir(h.cfg.Home) {
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
	h.drop(h.cfg.Home, "go.md", "go")
	h.start()
	h.waitEnds(h.cfg.Home, 1)
	tool := h.requests()[1].Messages[2].Content
	if !strings.Contains(tool, "bytes clipped") || !strings.Contains(tool, "[full output: ") || len(tool) > 17000 {
		t.Fatalf("tool content len %d: %.200q", len(tool), tool)
	}
	saved := tool[strings.Index(tool, "[full output: ")+len("[full output: "):]
	saved = saved[:strings.Index(saved, "]")]
	info, err := os.Stat(saved)
	if err != nil || info.Size() != 40000 || !strings.HasPrefix(saved, filepath.Join(h.cfg.Home, ".outputs")) {
		t.Fatalf("saved %s: %v %v", saved, info, err)
	}
}

func TestCommandTimeout(t *testing.T) {
	h := newHarness(t, step("sleep 30", 100), done())
	h.cfg.CommandTimeout = 300 * time.Millisecond
	h.drop(h.cfg.Home, "go.md", "go")
	h.start()
	if end := lastEnd(h.waitEnds(h.cfg.Home, 1)); end != "done" {
		t.Fatalf("end %s", end)
	}
	tool := h.requests()[1].Messages[2].Content
	if !strings.HasPrefix(tool, "Command timed out after 300ms") {
		t.Fatalf("tool %q", tool)
	}
}

func TestMidRunInboxDelivery(t *testing.T) {
	h := newHarness(t, step("sleep 0.5", 100), done())
	h.drop(h.cfg.Home, "first.md", "first")
	h.start()
	waitFor(t, "first request", func() bool { return len(h.requests()) >= 1 })
	h.drop(h.cfg.Home, "late.md", "late news")
	h.waitEnds(h.cfg.Home, 1)
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
	h.drop(h.cfg.Home, "go.md", "go")
	h.start()
	if end := lastEnd(h.waitEnds(h.cfg.Home, 1)); end != "done" {
		t.Fatalf("end %s", end)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(h.requests()); n != 1 {
		t.Fatalf("requests %d", n)
	}
}

func TestDotFilesIgnored(t *testing.T) {
	h := newHarness(t, done())
	partial := filepath.Join(h.cfg.Home, "inbox", ".partial")
	os.WriteFile(partial, []byte("half"), 0o644)
	os.MkdirAll(filepath.Join(h.cfg.Home, "inbox", "somedir"), 0o755)
	h.start()
	time.Sleep(200 * time.Millisecond)
	if n := len(h.requests()); n != 0 {
		t.Fatalf("woke on a dot-file: %d requests", n)
	}
	os.Rename(partial, filepath.Join(h.cfg.Home, "inbox", "x.md"))
	h.waitEnds(h.cfg.Home, 1)
}

func TestJournalSymlinkRepaired(t *testing.T) {
	h := newHarness(t, done())
	os.WriteFile(filepath.Join(h.cfg.Home, "journal"), []byte("not a link"), 0o644)
	h.drop(h.cfg.Home, "go.md", "go")
	h.start()
	h.waitEnds(h.cfg.Home, 1)
	if target, err := os.Readlink(filepath.Join(h.cfg.Home, "journal")); err != nil || target != h.cfg.JournalDir(h.cfg.Home) {
		t.Fatalf("journal link %q %v", target, err)
	}
}
