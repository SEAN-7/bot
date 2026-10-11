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
	os.MkdirAll(l.Log, 0o755)
	os.WriteFile(l.MessagesFile(), []byte(
		`{"t":"2026-10-11T09:00:00Z","from":"owner","text":"set up telegram"}`+"\n"+
			`{"t":"2026-10-11T09:05:00Z","from":"agent","agent":"main","text":"done\u001b[2J, token saved"}`+"\n"), 0o644)
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	var out bytes.Buffer
	go func() {
		time.Sleep(200 * time.Millisecond)
		f, _ := os.OpenFile(l.MessagesFile(), os.O_APPEND|os.O_WRONLY, 0)
		f.WriteString(`{"t":"2026-10-11T09:06:00Z","from":"agent","agent":"research","text":"report ready"}` + "\n")
		f.Close()
	}()
	Messages(ctx, l, &out)
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
