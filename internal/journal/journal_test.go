package journal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func lines(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func TestRecordsAndEnd(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "home", "agent")
	w, err := Create(dir, time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(w.Path()) != "20261011T120000.000000000Z.jsonl" {
		t.Fatalf("name %s", w.Path())
	}
	if err := w.Record("assistant", map[string]any{"text": "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := w.End("done"); err != nil {
		t.Fatal(err)
	}
	got := lines(t, w.Path())
	if len(got) != 2 || got[0]["type"] != "assistant" || got[0]["text"] != "hi" || got[0]["t"] == nil {
		t.Fatalf("got %v", got)
	}
	if got[1]["type"] != "end" || got[1]["reason"] != "done" {
		t.Fatalf("got %v", got[1])
	}
	if info, _ := os.Stat(w.Path()); info.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v", info.Mode())
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o755 {
		t.Fatalf("dir mode %v", info.Mode())
	}
}

func TestCreateAvoidsCollision(t *testing.T) {
	dir, now := t.TempDir(), time.Now()
	a, err := Create(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Create(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	if a.Path() == b.Path() {
		t.Fatal("same path")
	}
}

func TestLatest(t *testing.T) {
	dir := t.TempDir()
	if path, clean, err := Latest(dir); path != "" || clean || err != nil {
		t.Fatalf("empty: %q %v %v", path, clean, err)
	}
	if path, _, err := Latest(filepath.Join(dir, "missing")); path != "" || err != nil {
		t.Fatalf("missing dir: %q %v", path, err)
	}
	old, _ := Create(dir, time.Unix(100, 0))
	old.End("done")
	w, _ := Create(dir, time.Unix(200, 0))
	w.Record("start", nil)
	path, clean, err := Latest(dir)
	if path != w.Path() || clean || err != nil {
		t.Fatalf("unclean: %q %v %v", path, clean, err)
	}
	w.Close()
	if err := MarkInterrupted(path); err != nil {
		t.Fatal(err)
	}
	if _, clean, _ := Latest(dir); !clean {
		t.Fatal("still unclean after MarkInterrupted")
	}
	if got := lines(t, path); got[len(got)-1]["reason"] != "interrupted" {
		t.Fatalf("got %v", got)
	}
}
