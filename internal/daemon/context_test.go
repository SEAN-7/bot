package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWarningAt80(t *testing.T) {
	h := newHarness(t, step("true", 800), step("true", 850), done())
	h.drop(h.cfg.Home, "go.md", "go")
	h.start()
	h.waitEnds(h.cfg.Home, 1)
	reqs := h.requests()
	msgs := reqs[1].Messages
	if last := msgs[len(msgs)-1]; last.Role != "user" || !strings.Contains(last.Content, MsgWarn80) {
		t.Fatalf("no warning after 80%%: %+v", msgs)
	}
	count := 0
	for _, m := range reqs[2].Messages {
		count += strings.Count(m.Content, MsgWarn80)
	}
	if count != 1 {
		t.Fatalf("warning appeared %d times", count)
	}
}

func TestResetAt95(t *testing.T) {
	h := newHarness(t, step("true", 100), step("touch ran", 960), done())
	h.drop(h.cfg.Home, "go.md", "go")
	h.start()
	first := h.waitEnds(h.cfg.Home, 1)
	if end := lastEnd(first); end != "context" {
		t.Fatalf("end %s", end)
	}
	second := h.waitEnds(h.cfg.Home, 2)
	runs := h.runs(h.cfg.Home)
	want := "Context reset. Continue from TASK.md. Previous journal: " + runs[0]
	found := false
	for _, r := range second {
		if r["type"] == "inbox" && strings.Contains(r["content"].(string), want) && strings.HasSuffix(r["file"].(string), "-reset.md") {
			found = true
		}
	}
	if !found {
		t.Fatalf("second run did not receive %q: %v", want, second)
	}
	if _, err := os.Stat(filepath.Join(h.cfg.Home, "ran")); err == nil {
		t.Fatal("command issued at 95% was run")
	}
}

func TestFirstCallGuard(t *testing.T) {
	h := newHarness(t, step("true", 990))
	h.drop(h.cfg.Home, "go.md", "go")
	h.start()
	if end := lastEnd(h.waitEnds(h.cfg.Home, 1)); end != "context-guard" {
		t.Fatalf("end %s", end)
	}
	errorFile := filepath.Join(h.cfg.Home, "ERROR")
	if !fileContains(errorFile, "over 95%") {
		t.Fatal("ERROR not written")
	}
	time.Sleep(300 * time.Millisecond)
	if n := len(h.requests()); n != 1 {
		t.Fatalf("guard looped: %d requests", n)
	}
	for _, name := range inboxNames(h.cfg.Home) {
		t.Fatalf("unexpected inbox file %s", name)
	}
	h.drop(h.cfg.Home, "smaller.md", "go") // exhausted script: replies without a command
	h.waitEnds(h.cfg.Home, 2)
	if _, err := os.Stat(errorFile); !os.IsNotExist(err) {
		t.Fatal("guard ERROR not cleared after a normal run")
	}
}

func TestProviderRetry(t *testing.T) {
	h := newHarness(t, `{"error":"rate limited"}`, `{"error":"rate limited"}`, `{"error":"rate limited"}`, step("echo ok", 10), done())
	h.drop(h.cfg.Home, "go.md", "go")
	h.start()
	if end := lastEnd(h.waitEnds(h.cfg.Home, 1)); end != "done" {
		t.Fatalf("end %s", end)
	}
	if !strings.Contains(h.logs.String(), "Provider failing since") || !strings.Contains(h.logs.String(), "rate limited") {
		t.Fatalf("logs:\n%s", h.logs)
	}
	if _, err := os.Stat(filepath.Join(h.cfg.Home, "ERROR")); !os.IsNotExist(err) {
		t.Fatal("provider ERROR not cleared")
	}
	reqs := h.requests()
	if tool := reqs[len(reqs)-1].Messages[2].Content; tool != "exit status 0\nok\n" {
		t.Fatalf("tool %q", tool)
	}
	processed, _ := os.ReadDir(filepath.Join(h.cfg.Home, "inbox", "processed"))
	if len(processed) != 1 {
		t.Fatalf("provider errors produced inbox files: %v", processed)
	}
}

func TestProviderErrorFileWritten(t *testing.T) {
	h := newHarness(t, `{"error":"bad key"}`, `{"error":"bad key"}`, `{"error":"bad key"}`, `{"error":"bad key"}`, `{"error":"bad key"}`)
	h.cfg.RetryBase, h.cfg.RetryMax = 20*time.Millisecond, 200*time.Millisecond
	h.drop(h.cfg.Home, "go.md", "go")
	h.start()
	waitFor(t, "ERROR file", func() bool { return fileContains(filepath.Join(h.cfg.Home, "ERROR"), "Provider failing since") })
	if !fileContains(filepath.Join(h.cfg.Home, "ERROR"), "bad key") {
		t.Fatal("ERROR lacks provider message")
	}
}

func TestNoResetWhenStopping(t *testing.T) {
	h := newHarness(t, step("true", 100), `{"text":"finished","usage":{"input_tokens":970}}`)
	h.drop(h.cfg.Home, "go.md", "go")
	h.start()
	if end := lastEnd(h.waitEnds(h.cfg.Home, 1)); end != "done" {
		t.Fatalf("end %s", end)
	}
	time.Sleep(200 * time.Millisecond)
	if names := inboxNames(h.cfg.Home); len(names) != 0 || len(h.runs(h.cfg.Home)) != 1 {
		t.Fatalf("reset note written for a run that chose to stop: %v", names)
	}
}
