package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The harness does not watch context size: a run near the model's limit
// keeps going, and its commands run, until the provider reports an overflow.
func TestNoHarnessContextLimit(t *testing.T) {
	h := newHarness(t, step("true", 800), step("touch ran", 990), done())
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	if end := lastEnd(h.waitEnds(h.mainID, 1)); end != "done" {
		t.Fatalf("end %s", end)
	}
	if _, err := os.Stat(filepath.Join(h.mainHome, "ran")); err != nil {
		t.Fatal("command issued near the context limit was not run")
	}
	for _, req := range h.requests() {
		for _, m := range req.Messages {
			if m.Role == "user" && strings.Contains(m.Content, "Context is") {
				t.Fatalf("harness added a context message: %q", m.Content)
			}
		}
	}
	if names := inboxNames(h.mainHome); len(names) != 0 || len(h.runs(h.mainID)) != 1 {
		t.Fatalf("reset without an overflow: %v", names)
	}
}

func TestFirstCallGuard(t *testing.T) {
	h := newHarness(t, overflow)
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	if end := lastEnd(h.waitEnds(h.mainID, 1)); end != "context-guard" {
		t.Fatalf("end %s", end)
	}
	errorFile := filepath.Join(h.mainCtrl, "ERROR")
	if !fileContains(errorFile, "over the model's limit") {
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
