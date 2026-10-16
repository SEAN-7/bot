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
	w, err := Create(dir, time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC), -1)
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
}

func TestCreateModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "home", "agent")
	w, err := Create(dir, time.Now(), -1)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if info, _ := os.Stat(w.Path()); info.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", info.Mode())
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o751 {
		t.Fatalf("dir mode %v", info.Mode())
	}
}

func TestIsRun(t *testing.T) {
	for name, want := range map[string]bool{
		"20261011T120000.000000000Z.jsonl": true,
		"messages.jsonl":                   false,
		"x.jsonl":                          false,
		"20261011T120000.000000000Z.txt":   false,
	} {
		if got := IsRun(name); got != want {
			t.Errorf("IsRun(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestLatestIgnoresConversationLog(t *testing.T) {
	dir := t.TempDir()
	w, _ := Create(dir, time.Unix(100, 0), -1)
	w.End("done")
	if err := os.WriteFile(filepath.Join(dir, "messages.jsonl"), []byte("{\"role\":\"user\"}\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	path, clean, err := Latest(dir)
	if path != w.Path() || !clean || err != nil {
		t.Fatalf("got %q %v %v", path, clean, err)
	}
}

func TestSecureFixesTree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agent")
	sub := filepath.Join(dir, "child")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	os.Chmod(dir, 0o755)
	run := filepath.Join(dir, "20261011T120000.000000000Z.jsonl")
	if err := os.WriteFile(run, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chmod(run, 0o644)
	// The link points outside the walked tree, so Secure must not follow it.
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chmod(target, 0o644)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := Secure(dir, os.Getgid()); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{dir: 0o751, sub: 0o751, run: 0o640} {
		if info, _ := os.Stat(path); info.Mode().Perm() != want {
			t.Errorf("%s mode %v, want %v", path, info.Mode().Perm(), want)
		}
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink changed: %v %v", info, err)
	}
	if info, _ := os.Stat(target); info.Mode().Perm() != 0o644 {
		t.Fatalf("symlink target mode %v", info.Mode())
	}
}

func TestCreateWithGroup(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agent")
	w, err := Create(dir, time.Now(), os.Getgid())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if info, _ := os.Stat(w.Path()); info.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", info.Mode())
	}
}

func TestSecureCreatesMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	if err := Secure(dir, os.Getgid()); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() || info.Mode().Perm() != 0o751 {
		t.Fatalf("dir: %v %v", info, err)
	}
}

func TestCreateAvoidsCollision(t *testing.T) {
	dir, now := t.TempDir(), time.Now()
	a, err := Create(dir, now, -1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Create(dir, now, -1)
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
	old, _ := Create(dir, time.Unix(100, 0), -1)
	old.End("done")
	w, _ := Create(dir, time.Unix(200, 0), -1)
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

// childRun makes dir/agents/x holding a run file with loose modes, as a
// subagent's journal directory nested in its parent's.
func childRun(t *testing.T, dir string) (string, string) {
	t.Helper()
	child := filepath.Join(dir, "agents", "x")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	os.Chmod(child, 0o755)
	run := filepath.Join(child, "20261011T120000.000000000Z.jsonl")
	if err := os.WriteFile(run, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chmod(run, 0o644)
	return child, run
}

func TestSecureSkipsChildJournals(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agent")
	child, run := childRun(t, dir)
	os.Chmod(filepath.Join(dir, "agents"), 0o755)
	if err := Secure(dir, os.Getgid()); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{filepath.Join(dir, "agents"): 0o751, child: 0o755, run: 0o644} {
		if info, _ := os.Stat(path); info.Mode().Perm() != want {
			t.Errorf("%s mode %v, want %v", path, info.Mode().Perm(), want)
		}
	}
}

func TestSecureTreeFixesChildJournals(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agent")
	child, run := childRun(t, dir)
	if err := SecureTree(dir, os.Getgid()); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{child: 0o751, run: 0o640} {
		if info, _ := os.Stat(path); info.Mode().Perm() != want {
			t.Errorf("%s mode %v, want %v", path, info.Mode().Perm(), want)
		}
	}
}

func TestAttach(t *testing.T) {
	dir := t.TempDir()
	w, err := Create(dir, time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC), -1)
	if err != nil {
		t.Fatal(err)
	}
	stem := strings.TrimSuffix(filepath.Base(w.Path()), ".jsonl")
	p1, err := w.Attach("jpg", []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	p2, err := w.Attach("png", []byte("b"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(p1) != stem+"-1.jpg" || filepath.Base(p2) != stem+"-2.png" {
		t.Fatalf("names %s %s", p1, p2)
	}
	for p, want := range map[string]string{p1: "a", p2: "b"} {
		data, err := os.ReadFile(p)
		if err != nil || string(data) != want {
			t.Fatalf("%s: %q %v", p, data, err)
		}
		if info, _ := os.Stat(p); info.Mode().Perm() != 0o640 {
			t.Fatalf("%s mode %v", p, info.Mode())
		}
	}
	if IsRun(filepath.Base(p1)) {
		t.Fatalf("%s looks like a run", p1)
	}
	if err := w.End("done"); err != nil {
		t.Fatal(err)
	}
	if path, ended, err := Latest(dir); err != nil || path != w.Path() || !ended {
		t.Fatalf("latest %s %v %v", path, ended, err)
	}
}

func TestLastVersion(t *testing.T) {
	dir := t.TempDir()
	if v, found, err := LastVersion(dir); err != nil || found || v != "" {
		t.Fatalf("no runs: %q %v %v", v, found, err)
	}
	old, _ := Create(dir, time.Unix(100, 0), -1)
	old.Record("start", map[string]any{"agent": "main"})
	old.End("done")
	if v, found, err := LastVersion(dir); err != nil || !found || v != "" {
		t.Fatalf("run without a version: %q %v %v", v, found, err)
	}
	w, _ := Create(dir, time.Unix(200, 0), -1)
	w.Record("start", map[string]any{"agent": "main", "version": "0.6.0"})
	w.End("done")
	os.WriteFile(filepath.Join(dir, "messages.jsonl"), []byte(`{"type":"start","version":"9.9.9"}`+"\n"), 0o640)
	if v, found, err := LastVersion(dir); err != nil || !found || v != "0.6.0" {
		t.Fatalf("newest run: %q %v %v", v, found, err)
	}
}
