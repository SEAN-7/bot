package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func countNotes(dir, suffix string) int {
	n := 0
	for _, sub := range []string{"inbox", "inbox/processed"} {
		entries, _ := os.ReadDir(filepath.Join(dir, sub))
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), suffix) {
				n++
			}
		}
	}
	return n
}

func makeChild(t *testing.T, h *harness, name string, config bool) string {
	t.Helper()
	dir := filepath.Join(h.cfg.Home, "agents", name)
	if err := os.MkdirAll(filepath.Join(dir, "inbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if config {
		os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"provider":"replay","model":"replay-1"}`), 0o644)
	}
	return dir
}

func TestConfigValidationFailure(t *testing.T) {
	h := newHarness(t, done())
	config := filepath.Join(h.cfg.Home, "config.json")
	os.WriteFile(config, []byte(`{"provider":"replay","model":"nope"}`), 0o644)
	h.drop(h.cfg.Home, "go.md", "go")
	h.start()
	errorFile := filepath.Join(h.cfg.Home, "ERROR")
	waitFor(t, "config ERROR", func() bool { return fileContains(errorFile, `config.json: unknown model "nope"`) })
	time.Sleep(100 * time.Millisecond)
	if n := len(h.requests()); n != 0 {
		t.Fatalf("%d requests with invalid config", n)
	}
	if names := inboxNames(h.cfg.Home); len(names) != 1 || names[0] != "go.md" {
		t.Fatalf("inbox touched: %v", names)
	}
	if strings.Count(h.logs.String(), `unknown model "nope"`) != 1 {
		t.Fatalf("config error not logged exactly once:\n%s", h.logs)
	}
	os.WriteFile(config, []byte(`{"provider":"replay","model":"replay-1"}`), 0o644)
	h.waitEnds(h.cfg.Home, 1)
	if _, err := os.Stat(errorFile); !os.IsNotExist(err) {
		t.Fatal("config ERROR not cleared")
	}
}

func TestChildWithoutConfigWaits(t *testing.T) {
	h := newHarness(t)
	child := makeChild(t, h, "c", false)
	h.drop(child, "task.md", "do it")
	h.start()
	waitFor(t, "child ERROR", func() bool { return fileContains(filepath.Join(child, "ERROR"), "config.json") })
	waitFor(t, "parent note", func() bool { return countNotes(h.cfg.Home, "-subagent-error.md") == 1 })
	time.Sleep(300 * time.Millisecond)
	if n := countNotes(h.cfg.Home, "-subagent-error.md"); n != 1 {
		t.Fatalf("parent notified %d times", n)
	}
	if names := inboxNames(child); len(names) != 1 {
		t.Fatalf("child inbox touched: %v", names)
	}
	os.WriteFile(filepath.Join(child, "config.json"), []byte(`{"provider":"replay","model":"replay-1"}`), 0o644)
	h.waitEnds(child, 1)
}

func TestParentChildReport(t *testing.T) {
	create := "mkdir -p agents/c/inbox && cp config.json agents/c/ && printf 'child-task: say hi' > agents/c/inbox/.t && mv agents/c/inbox/.t agents/c/inbox/task.md"
	report := "printf 'report: hi' > ../../inbox/.r && mv ../../inbox/.r ../../inbox/report.md"
	h := newHarness(t,
		matchStep("inbox/processed/task.md", report, 100),
		matchStep("parent-readme", create, 100),
		`{"match":"inbox/processed/task.md","text":"done"}`,
		`{"match":"parent-readme","text":"done"}`,
		`{"match":"parent-readme","text":"done"}`,
		`{"match":"parent-readme","text":"done"}`,
	)
	os.WriteFile(filepath.Join(h.cfg.Home, "README.md"), []byte("parent-readme"), 0o644)
	h.drop(h.cfg.Home, "go.md", "make a child")
	h.start()
	want := `{\"file\":\"inbox/processed/report.md\",\"from\":\"data\",\"content\":\"report: hi\"}`
	waitFor(t, "parent receiving the report", func() bool {
		for _, req := range h.requests() {
			for _, m := range req.Messages {
				if strings.Contains(m.Content, strings.ReplaceAll(want, `\"`, `"`)) {
					return true
				}
			}
		}
		return false
	})
	if records := h.waitEnds(filepath.Join(h.cfg.Home, "agents", "c"), 1); lastEnd(records) != "done" {
		t.Fatalf("child end %v", records)
	}
}

func TestRemovalStopsAgent(t *testing.T) {
	h := newHarness(t, matchStep("inbox/processed/task.md", "sleep 30", 100))
	child := makeChild(t, h, "c", true)
	h.drop(child, "task.md", "work")
	h.start()
	waitFor(t, "child command", func() bool { return len(h.requests()) >= 1 })
	time.Sleep(100 * time.Millisecond)
	os.RemoveAll(child)
	start := time.Now()
	if end := lastEnd(h.waitEnds(child, 1)); end != "removed" {
		t.Fatalf("end %s", end)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("removal took %s", time.Since(start))
	}
}

func TestMaxAgents(t *testing.T) {
	h := newHarness(t)
	os.WriteFile(h.cfg.SettingsFile(), []byte(`{"max_agents":1}`), 0o644)
	child := makeChild(t, h, "c", true)
	h.drop(child, "task.md", "work")
	h.start()
	waitFor(t, "limit ERROR", func() bool { return fileContains(filepath.Join(child, "ERROR"), "Agent limit reached (max_agents=1)") })
	waitFor(t, "parent note", func() bool { return countNotes(h.cfg.Home, "-subagent-error.md") == 1 })
	time.Sleep(200 * time.Millisecond)
	if n := countNotes(h.cfg.Home, "-subagent-error.md"); n != 1 {
		t.Fatalf("parent notified %d times", n)
	}
	for _, req := range h.requests() {
		if strings.Contains(req.Messages[0].Content, "task.md") {
			t.Fatal("child ran despite the limit")
		}
	}
	os.WriteFile(h.cfg.SettingsFile(), []byte(`{"max_agents":2}`), 0o644)
	h.waitEnds(child, 1)
	if _, err := os.Stat(filepath.Join(child, "ERROR")); !os.IsNotExist(err) {
		t.Fatal("limit ERROR not cleared")
	}
}
