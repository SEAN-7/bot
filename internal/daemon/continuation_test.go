package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

func TestFollowUpContinuesConversation(t *testing.T) {
	h := newHarness(t, step("echo first", 100), textStep("answered first", 120), step("echo second", 150), textStep("answered second", 170))
	h.drop(h.cfg.Home, "a.md", "first question")
	h.start()
	h.waitEnds(h.cfg.Home, 1)
	os.WriteFile(filepath.Join(h.cfg.Home, "README.md"), []byte("edited readme"), 0o644)
	h.drop(h.cfg.Home, "b.md", "follow-up question")
	second := h.waitEnds(h.cfg.Home, 2)
	reqs := h.requests()
	req := reqs[2] // first call of the second run
	if req.System[1].Text != "readme" {
		t.Fatalf("system sections changed mid-conversation: %q", req.System[1].Text)
	}
	msgs := req.Messages
	if len(msgs) != 5 || !strings.Contains(msgs[0].Content, "first question") || msgs[3].Text != "answered first" || !strings.Contains(msgs[4].Content, "follow-up question") {
		t.Fatalf("second run did not continue the conversation: %+v", msgs)
	}
	// call ids stay unique across the conversation: run 1 used call-1 and call-2
	if last := reqs[3].Messages[5]; last.CallID != "call-3" {
		t.Fatalf("call id %q, want call-3", last.CallID)
	}
	if second[0]["continues"] != h.runs(h.cfg.Home)[0] {
		t.Fatalf("start record does not name the run it continues: %v", second[0])
	}
}

func TestColdFollowUpStartsFresh(t *testing.T) {
	h := newHarness(t, textStep("one", 100), textStep("two", 100))
	h.cfg.ContinueWindow = 100 * time.Millisecond
	h.drop(h.cfg.Home, "a.md", "first")
	h.start()
	h.waitEnds(h.cfg.Home, 1)
	time.Sleep(300 * time.Millisecond)
	os.WriteFile(filepath.Join(h.cfg.Home, "README.md"), []byte("edited readme"), 0o644)
	h.drop(h.cfg.Home, "b.md", "second")
	h.waitEnds(h.cfg.Home, 2)
	req := h.requests()[1]
	if len(req.Messages) != 1 || req.System[1].Text != "edited readme" {
		t.Fatalf("cold follow-up did not start fresh: %d messages, readme %q", len(req.Messages), req.System[1].Text)
	}
}

func TestResetMarkerStartsFresh(t *testing.T) {
	h := newHarness(t, step("touch ~/.reset", 100), textStep("done", 100), textStep("fresh", 100))
	h.drop(h.cfg.Home, "a.md", "first")
	h.start()
	h.waitEnds(h.cfg.Home, 1)
	h.drop(h.cfg.Home, "b.md", "second")
	h.waitEnds(h.cfg.Home, 2)
	if n := len(h.requests()[2].Messages); n != 1 {
		t.Fatalf("~/.reset ignored: %d messages", n)
	}
	if _, err := os.Stat(filepath.Join(h.cfg.Home, ".reset")); !os.IsNotExist(err) {
		t.Fatal("~/.reset not removed after use")
	}
}
