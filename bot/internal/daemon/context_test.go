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
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	h.waitEnds(h.mainID, 1)
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
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	first := h.waitEnds(h.mainID, 1)
	if end := lastEnd(first); end != "context" {
		t.Fatalf("end %s", end)
	}
	second := h.waitEnds(h.mainID, 2)
	runs := h.runs(h.mainID)
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
	if _, err := os.Stat(filepath.Join(h.mainHome, "ran")); err == nil {
		t.Fatal("command issued at 95% was run")
	}
}

func TestFirstCallGuard(t *testing.T) {
	h := newHarness(t, step("true", 990))
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	if end := lastEnd(h.waitEnds(h.mainID, 1)); end != "context-guard" {
		t.Fatalf("end %s", end)
	}
	errorFile := filepath.Join(h.mainCtrl, "ERROR")
	if !fileContains(errorFile, "over 95%") {
		t.Fatal("ERROR not written")
	}
	time.Sleep(300 * time.Millisecond)
	if n := len(h.requests()); n != 1 {
		t.Fatalf("guard looped: %d requests", n)
	}
	for _, name := range inboxNames(h.mainHome) {
		t.Fatalf("unexpected inbox file %s", name)
	}
	h.drop(h.mainHome, "smaller.md", "go") // exhausted script: replies without a command
	h.waitEnds(h.mainID, 2)
	if _, err := os.Stat(errorFile); !os.IsNotExist(err) {
		t.Fatal("guard ERROR not cleared after a normal run")
	}
}

func TestProviderRetry(t *testing.T) {
	h := newHarness(t, `{"error":"rate limited"}`, `{"error":"rate limited"}`, `{"error":"rate limited"}`, step("echo ok", 10), done())
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	if end := lastEnd(h.waitEnds(h.mainID, 1)); end != "done" {
		t.Fatalf("end %s", end)
	}
	if !strings.Contains(h.logs.String(), "Provider failing since") || !strings.Contains(h.logs.String(), "rate limited") {
		t.Fatalf("logs:\n%s", h.logs)
	}
	if _, err := os.Stat(filepath.Join(h.mainCtrl, "ERROR")); !os.IsNotExist(err) {
		t.Fatal("provider ERROR not cleared")
	}
	reqs := h.requests()
	if tool := reqs[len(reqs)-1].Messages[2].Content; tool != "exit status 0\nok\n" {
		t.Fatalf("tool %q", tool)
	}
	processed, _ := os.ReadDir(filepath.Join(h.mainHome, "inbox", "processed"))
	if len(processed) != 1 {
		t.Fatalf("provider errors produced inbox files: %v", processed)
	}
}

func TestProviderErrorFileWritten(t *testing.T) {
	h := newHarness(t, `{"error":"bad key"}`, `{"error":"bad key"}`, `{"error":"bad key"}`, `{"error":"bad key"}`, `{"error":"bad key"}`)
	h.cfg.RetryBase, h.cfg.RetryMax = 20*time.Millisecond, 200*time.Millisecond
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	waitFor(t, "ERROR file", func() bool { return fileContains(filepath.Join(h.mainCtrl, "ERROR"), "Provider failing since") })
	if !fileContains(filepath.Join(h.mainCtrl, "ERROR"), "bad key") {
		t.Fatal("ERROR lacks provider message")
	}
}

func TestNoResetWhenStopping(t *testing.T) {
	h := newHarness(t, step("true", 100), `{"text":"finished","usage":{"input_tokens":970}}`)
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	if end := lastEnd(h.waitEnds(h.mainID, 1)); end != "done" {
		t.Fatalf("end %s", end)
	}
	time.Sleep(200 * time.Millisecond)
	if names := inboxNames(h.mainHome); len(names) != 0 || len(h.runs(h.mainID)) != 1 {
		t.Fatalf("reset note written for a run that chose to stop: %v", names)
	}
}

const overflow = `{"error":"prompt is too long","error_kind":"context_overflow"}`

func TestOverflowMidRunResets(t *testing.T) {
	h := newHarness(t, step("true", 100), overflow, done())
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	if end := lastEnd(h.waitEnds(h.mainID, 1)); end != "context" {
		t.Fatalf("end %s", end)
	}
	found := false
	for _, r := range h.waitEnds(h.mainID, 2) {
		if r["type"] == "inbox" && strings.Contains(r["content"].(string), "Context reset. Continue from TASK.md.") {
			found = true
		}
	}
	if !found {
		t.Fatal("no reset note in the next run")
	}
}

func TestOverflowFirstCallGuards(t *testing.T) {
	h := newHarness(t, overflow, done())
	h.start()
	h.drop(h.mainHome, "huge.md", "too much")
	if end := lastEnd(h.waitEnds(h.mainID, 1)); end != "context-guard" {
		t.Fatalf("end %s", end)
	}
	if !fileContains(filepath.Join(h.mainCtrl, "ERROR"), "over the model's limit") {
		t.Fatal("no ERROR for an oversized first call")
	}
	time.Sleep(200 * time.Millisecond)
	if n := len(h.requests()); n != 1 {
		t.Fatalf("overflow retried: %d requests", n)
	}
}

func TestStaleProviderErrorCleared(t *testing.T) {
	h := newHarness(t, done())
	errorFile := filepath.Join(h.mainCtrl, "ERROR")
	os.WriteFile(errorFile, []byte("Provider failing since 2026-10-11T00:00:00Z: 429\n"), 0o644)
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	h.waitEnds(h.mainID, 1)
	if _, err := os.Stat(errorFile); !os.IsNotExist(err) {
		t.Fatal("provider ERROR from before a restart survived a successful call")
	}
}

func textStep(text string, tokens int) string {
	return `{"text":"` + text + `","usage":{"input_tokens":` + itoa(tokens) + `}}`
}

func itoa(n int) string {
	s := ""
	for {
		s = string(rune('0'+n%10)) + s
		if n /= 10; n == 0 {
			return s
		}
	}
}
