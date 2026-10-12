package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if info, err := os.Stat(h.cfg.MessagesFile()); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("log %v %v", info, err)
	}
}

func TestSubagentMessageLabelled(t *testing.T) {
	h := newHarness(t, matchStep("inbox/processed/task.md", sendMessage, 100), `{"match":"inbox/processed/task.md","text":"done"}`)
	ctrl := makeChild(t, h, "research", true)
	h.start()
	h.drop(h.childHome(ctrl), "task.md", "work")
	waitFor(t, "subagent message", func() bool { return hasMessage(h, "agent", "research", "tests pass") })
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
