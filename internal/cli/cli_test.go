package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/SEAN-7/bot/internal/journal"
	"github.com/SEAN-7/bot/internal/layout"
	"github.com/SEAN-7/bot/internal/proto"
)

func tempLayout(t *testing.T) layout.Layout {
	root := t.TempDir()
	return layout.Layout{Home: filepath.Join(root, "home"), Etc: filepath.Join(root, "etc"), Lib: filepath.Join(root, "lib"), Log: filepath.Join(root, "log")}
}

func TestSendRequiresRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	if err := Send(tempLayout(t), "hi", time.Now()); err == nil || !strings.Contains(err.Error(), "sudo") {
		t.Fatalf("got %v", err)
	}
}

func TestSendWritesSpool(t *testing.T) {
	l := tempLayout(t)
	if err := send(l, "hello agent", time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(l.OwnerSpool())
	if len(entries) != 1 || strings.HasPrefix(entries[0].Name(), ".") || !strings.HasSuffix(entries[0].Name(), ".md") {
		t.Fatalf("spool %v", entries)
	}
	if data, _ := os.ReadFile(filepath.Join(l.OwnerSpool(), entries[0].Name())); string(data) != "hello agent\n" {
		t.Fatalf("content %q", data)
	}
}

func TestFormat(t *testing.T) {
	stamp := "2026-10-11T09:30:15Z"
	clock := time.Date(2026, 10, 11, 9, 30, 15, 0, time.UTC).Local().Format("15:04:05")
	cases := map[string]string{
		`{"t":"` + stamp + `","type":"assistant","text":"checking","command":"ls -la"}`:                          clock + " assistant: checking\n  $ ls -la",
		`{"t":"` + stamp + `","type":"result","exit_code":0,"output":"exit status 0\na\nb"}`:                     clock + " result (exit 0):\n  exit status 0\n  a\n  b",
		`{"t":"` + stamp + `","type":"inbox","file":"inbox/processed/x-owner.md","from":"owner","content":"hi"}`: clock + " inbox inbox/processed/x-owner.md [owner]\n  hi",
		`{"t":"` + stamp + `","type":"note","text":"inbox: oops"}`:                                               clock + " note: inbox: oops",
		`{"t":"` + stamp + `","type":"end","reason":"done"}`:                                                     clock + " end (done)",
	}
	for line, want := range cases {
		if got := Format([]byte(line)); got != want {
			t.Errorf("Format(%s)\n got %q\nwant %q", line, got, want)
		}
	}
	long := `{"t":"` + stamp + `","type":"result","exit_code":1,"output":"` + strings.Repeat("x\\n", 30) + `"}`
	if got := Format([]byte(long)); strings.Count(got, "\n") > 22 || !strings.Contains(got, "more lines") {
		t.Fatalf("long result not truncated: %q", got)
	}
}

func TestLogFollowsNewRuns(t *testing.T) {
	l := tempLayout(t)
	os.MkdirAll(l.Home, 0o755)
	os.WriteFile(filepath.Join(l.Home, "ERROR"), []byte("Provider failing since now: 429\n"), 0o644)
	dir := l.JournalDir(l.Home)
	first, _ := journal.Create(dir, time.Now().Add(-time.Minute), -1)
	first.Record("assistant", map[string]any{"text": "first run"})
	first.End("done")
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	var out bytes.Buffer
	go func() {
		time.Sleep(300 * time.Millisecond)
		second, _ := journal.Create(dir, time.Now(), -1)
		second.Record("assistant", map[string]any{"text": "second run"})
		second.End("done")
	}()
	if err := Log(ctx, l.JournalDir(l.Home), filepath.Join(l.Home, "ERROR"), &out); err != nil && err != context.DeadlineExceeded {
		t.Fatal(err)
	}
	got := out.String()
	i, j, k := strings.Index(got, "Provider failing"), strings.Index(got, "first run"), strings.Index(got, "second run")
	if i < 0 || j < 0 || k < 0 || !(i < j && j < k) {
		t.Fatalf("output:\n%s", got)
	}
}

func TestDescribeReadsCacheAsNonRoot(t *testing.T) {
	l := tempLayout(t)
	os.MkdirAll(l.Lib, 0o755)
	min, max := 1, 10
	cache := map[string]any{"providers": map[string]proto.DescribeResponse{"replay": {Models: []proto.Model{{ID: "replay-1", ContextTokens: 1000, Options: map[string]proto.Option{
		"style":  {Values: []any{"a", "b"}},
		"budget": {Type: "integer", Min: &min, Max: &max},
	}}}}}}
	data, _ := json.Marshal(cache)
	os.WriteFile(l.ProvidersCache(), data, 0o644)
	var out bytes.Buffer
	if err := Describe(context.Background(), l, nil, false, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"replay\n", "replay-1 (1000 tokens)", "style: a, b", "budget: integer 1-10"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
}

func TestLogDoesNotFollowErrorSymlink(t *testing.T) {
	l := tempLayout(t)
	os.MkdirAll(l.Home, 0o755)
	secret := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(secret, []byte("TOPSECRET"), 0o600)
	os.Symlink(secret, filepath.Join(l.Home, "ERROR"))
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var out bytes.Buffer
	Log(ctx, l.JournalDir(l.Home), filepath.Join(l.Home, "ERROR"), &out)
	if strings.Contains(out.String(), "TOPSECRET") {
		t.Fatal("agent log followed a symlinked ERROR")
	}
}

func TestLogDoesNotHangOnErrorFIFO(t *testing.T) {
	l := tempLayout(t)
	os.MkdirAll(l.Home, 0o755)
	if err := syscall.Mkfifo(filepath.Join(l.Home, "ERROR"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { Log(ctx, l.JournalDir(l.Home), filepath.Join(l.Home, "ERROR"), io.Discard); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("agent log hung on a FIFO ERROR")
	}
}

func TestFormatStripsControlCharacters(t *testing.T) {
	got := Format([]byte(`{"t":"2026-10-11T09:30:15Z","type":"assistant","text":"hi\u001b[2J\u0007there","command":"echo \u001b]0;pwned\u0007"}`))
	if strings.ContainsAny(got, "\x1b\x07") || !strings.Contains(got, "hi") || !strings.Contains(got, "there") {
		t.Fatalf("got %q", got)
	}
}
