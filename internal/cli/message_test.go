package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMessageWritesSpool(t *testing.T) {
	home := t.TempDir()
	if err := Message(home, "build finished", time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(home, ".messages"))
	if len(entries) != 1 || strings.HasPrefix(entries[0].Name(), ".") {
		t.Fatalf("spool %v", entries)
	}
	if data, _ := os.ReadFile(filepath.Join(home, ".messages", entries[0].Name())); string(data) != "build finished" {
		t.Fatalf("content %q", data)
	}
}

func TestMessageRejectsEmptyAndHuge(t *testing.T) {
	home := t.TempDir()
	if err := Message(home, "  ", time.Now()); err == nil {
		t.Fatal("empty message accepted")
	}
	if err := Message(home, strings.Repeat("x", 64<<10+1), time.Now()); err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("huge message: %v", err)
	}
	if err := Message("", "hi", time.Now()); err == nil {
		t.Fatal("missing HOME accepted")
	}
}

func TestMessagesShowsConversation(t *testing.T) {
	l := tempLayout(t)
	path := l.ConversationFile("main")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(
		`{"t":"2026-10-11T09:00:00Z","from":"owner","text":"set up telegram"}`+"\n"+
			`{"t":"2026-10-11T09:05:00Z","from":"agent","agent":"main","text":"done\u001b[2J, token saved"}`+"\n"), 0o644)
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	var out bytes.Buffer
	go func() {
		time.Sleep(200 * time.Millisecond)
		f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		f.WriteString(`{"t":"2026-10-11T09:06:00Z","from":"agent","agent":"research","text":"report ready"}` + "\n")
		f.Close()
	}()
	Messages(ctx, path, &out)
	got := out.String()
	for _, want := range []string{"owner: set up telegram", "main: done[2J, token saved", "research: report ready"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\x1b") {
		t.Fatal("control characters printed")
	}
}

func TestMessagesShowsSubagentConversation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages.jsonl")
	os.WriteFile(path, []byte(
		`{"t":"2026-10-11T09:00:00Z","from":"owner","text":"scrape it"}`+"\n"+
			`{"t":"2026-10-11T09:05:00Z","from":"agent","text":"scraped"}`+"\n"), 0o644)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var out bytes.Buffer
	Messages(ctx, path, &out)
	for _, want := range []string{" owner: scrape it", " agent: scraped"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
}

func agentWithConfig(t *testing.T, home, name string) {
	t.Helper()
	dir := filepath.Join(home, "agents", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSendChildWritesDownSpool(t *testing.T) {
	home := t.TempDir()
	agentWithConfig(t, home, "research")
	if err := SendChild(home, "research", "go", time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".messages", "agents", "research")
	data, err := os.ReadFile(filepath.Join(dir, "20261012T090000.000000000Z.md"))
	if err != nil || string(data) != "go" {
		t.Fatal(string(data), err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("spool %v", entries)
	}
}

func TestSendChildRejectsBadNames(t *testing.T) {
	home := t.TempDir()
	agentWithConfig(t, home, "research")
	agentWithConfig(t, filepath.Join(home, "agents", "research", "home"), "x")
	for _, name := range []string{"research/agents/x", "..", ".", "", "a/b", "/research", "research/"} {
		if err := SendChild(home, name, "go", time.Now()); err == nil {
			t.Errorf("name %q accepted", name)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".messages")); err == nil {
		t.Fatal("a rejected name left a spool")
	}
}

func TestSendChildNeedsConfig(t *testing.T) {
	home := t.TempDir()
	err := SendChild(home, "ghost", "go", time.Now())
	if err == nil || !strings.Contains(err.Error(), `no subagent "ghost" (expected ~/agents/ghost/config.json)`) {
		t.Fatalf("got %v", err)
	}
	if err := SendChild("", "ghost", "go", time.Now()); err == nil {
		t.Fatal("missing HOME accepted")
	}
}

func TestSendChildRejectsEmptyAndHuge(t *testing.T) {
	home := t.TempDir()
	agentWithConfig(t, home, "research")
	if err := SendChild(home, "research", " \n", time.Now()); err == nil {
		t.Fatal("empty message accepted")
	}
	if err := SendChild(home, "research", strings.Repeat("x", 64<<10+1), time.Now()); err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("huge message: %v", err)
	}
}

func mustLink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func resolved(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestTargetRootAndDescendants(t *testing.T) {
	l := tempLayout(t)
	mainCtrl := filepath.Join(l.RootHome(), "agents", "main")
	mainHome, pool := resolved(t), resolved(t)
	mustLink(t, mainHome, filepath.Join(mainCtrl, "home"))
	mustLink(t, l.JournalDir("main"), filepath.Join(mainHome, "journal"))
	os.MkdirAll(l.JournalDir("main"), 0o755)
	mustLink(t, pool, filepath.Join(mainHome, "agents", "research", "home"))
	child, grand := "main/agents/research", "main/agents/research/agents/x"

	// Root names its agents under RootHome; with no name it means main.
	for _, name := range []string{"", "main"} {
		j, e, c, err := Target(l, true, "/root", name)
		if err != nil || j != l.JournalDir("main") || e != filepath.Join(mainCtrl, "ERROR") || c != l.ConversationFile("main") {
			t.Fatal(name, j, e, c, err)
		}
	}
	j, e, c, err := Target(l, true, "/root", "main/agents/research")
	if err != nil || j != l.JournalDir(child) || e != filepath.Join(mainHome, "agents", "research", "ERROR") || c != l.ConversationFile(child) {
		t.Fatal(j, e, c, err)
	}
	if c != filepath.Join(l.Log, "main", "agents", "research", "messages.jsonl") {
		t.Fatal(c)
	}
	j, e, c, err = Target(l, true, "/root", "main/agents/research/agents/x")
	if err != nil || j != l.JournalDir(grand) || e != filepath.Join(pool, "agents", "x", "ERROR") || c != l.ConversationFile(grand) {
		t.Fatal(j, e, c, err)
	}
	// Main running as itself reaches the same places.
	j, e, c, err = Target(l, false, mainHome, "research/agents/x")
	if err != nil || j != l.JournalDir(grand) || e != filepath.Join(pool, "agents", "x", "ERROR") || c != l.ConversationFile(grand) {
		t.Fatal(j, e, c, err)
	}
}

func TestTargetSubagentSelf(t *testing.T) {
	l := tempLayout(t)
	home := resolved(t)
	jdir := l.JournalDir("main/agents/research")
	os.MkdirAll(jdir, 0o755)
	mustLink(t, jdir, filepath.Join(home, "journal"))
	want, _ := filepath.EvalSymlinks(jdir)
	j, e, c, err := Target(l, false, home, "")
	if err != nil || j != want || e != "" || c != filepath.Join(want, "messages.jsonl") {
		t.Fatal(j, e, c, err)
	}
	if !strings.HasSuffix(c, strings.TrimPrefix(l.ConversationFile("main/agents/research"), l.Log)) {
		t.Fatalf("conversation %s differs from the daemon's", c)
	}
}

func TestTargetSubagentChild(t *testing.T) {
	l := tempLayout(t)
	home := resolved(t)
	self := "main/agents/research"
	jdir := l.JournalDir(self)
	os.MkdirAll(jdir, 0o755)
	mustLink(t, jdir, filepath.Join(home, "journal"))
	pool2 := resolved(t)
	mustLink(t, pool2, filepath.Join(home, "agents", "x", "home"))
	j, e, c, err := Target(l, false, home, "x")
	grand := self + "/agents/x"
	if err != nil || j != l.JournalDir(grand) || e != filepath.Join(home, "agents", "x", "ERROR") || c != l.ConversationFile(grand) {
		t.Fatal(j, e, c, err)
	}
	j, e, c, err = Target(l, false, home, "x/agents/y")
	grand = self + "/agents/x/agents/y"
	if err != nil || j != l.JournalDir(grand) || e != filepath.Join(pool2, "agents", "y", "ERROR") || c != l.ConversationFile(grand) {
		t.Fatal(j, e, c, err)
	}
}

func TestTargetRejects(t *testing.T) {
	l := tempLayout(t)
	for _, name := range []string{"..", ".", "../x", "a/b", "a//b", "a/agents/", "/a", "a/agents/..", "a\x00b"} {
		if _, _, _, err := Target(l, true, "/root", name); err == nil {
			t.Errorf("name %q accepted", name)
		}
	}
	// A subagent's intermediate home link must exist to reach a grandchild.
	if _, _, _, err := Target(l, true, "/root", "nobody/agents/x"); err == nil {
		t.Error("grandchild of a missing child accepted")
	}
	// Not an agent: no journal link, or one outside the journal root.
	home := t.TempDir()
	if _, _, _, err := Target(l, false, home, ""); err == nil {
		t.Error("a home without a journal link accepted")
	}
	if _, _, _, err := Target(l, false, "", ""); err == nil {
		t.Error("empty HOME accepted")
	}
	mustLink(t, t.TempDir(), filepath.Join(home, "journal"))
	if _, _, _, err := Target(l, false, home, ""); err == nil {
		t.Error("a journal link outside the log root accepted")
	}
	if _, _, _, err := Target(l, false, home, "x"); err == nil {
		t.Error("a journal link outside the log root accepted for a child")
	}
}
