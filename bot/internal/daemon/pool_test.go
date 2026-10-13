package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPoolAssignLowestFreeAndPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pool.json")
	users := []string{"agent-1", "agent-2"}
	p, err := loadPool(path, users)
	if err != nil {
		t.Fatal(err)
	}
	u1, _ := p.assign("/h/agents/a", 2)
	u2, _ := p.assign("/h/agents/b", 2)
	u3, _ := p.assign("/h/agents/c", 2)
	if u1 != "agent-1" || u2 != "agent-2" || u3 != "" {
		t.Fatal(u1, u2, u3)
	}
	if again, _ := p.assign("/h/agents/a", 2); again != "agent-1" {
		t.Fatal(again)
	}
	if p.userOf("/h/agents/b") != "agent-2" || p.userOf("/h/agents/c") != "" {
		t.Fatal("userOf")
	}
	if err := p.mark("agent-1", "reclaiming"); err != nil {
		t.Fatal(err)
	}
	q, err := loadPool(path, users)
	if err != nil {
		t.Fatal(err)
	}
	if e := q.entries(); len(e) != 2 || e[0].State != "reclaiming" || e[1].State != "active" {
		t.Fatal(e)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatal(info.Mode())
	}
}

func TestPoolLimit(t *testing.T) {
	p, _ := loadPool(filepath.Join(t.TempDir(), "pool.json"), []string{"agent-1", "agent-2"})
	if u, _ := p.assign("/h/agents/a", 1); u != "agent-1" {
		t.Fatal(u)
	}
	if u, err := p.assign("/h/agents/b", 1); u != "" || err != nil {
		t.Fatal(u, err)
	}
}

func TestPoolFreeReuses(t *testing.T) {
	p, _ := loadPool(filepath.Join(t.TempDir(), "pool.json"), []string{"agent-1", "agent-2"})
	p.assign("/h/agents/a", 2)
	p.assign("/h/agents/b", 2)
	if err := p.free("agent-1"); err != nil {
		t.Fatal(err)
	}
	if p.userOf("/h/agents/a") != "" {
		t.Fatal("freed user still assigned")
	}
	if u, _ := p.assign("/h/agents/c", 2); u != "agent-1" {
		t.Fatal(u)
	}
}

func TestPoolMissingFileIsEmpty(t *testing.T) {
	p, err := loadPool(filepath.Join(t.TempDir(), "none.json"), []string{"agent-1"})
	if err != nil || len(p.entries()) != 0 {
		t.Fatal(p, err)
	}
}

func TestPoolSaveFailureLeavesMemoryUnchanged(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lib")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p, _ := loadPool(filepath.Join(dir, "pool.json"), []string{"agent-1", "agent-2"})
	if u, err := p.assign("/h/agents/a", 2); u != "agent-1" || err != nil {
		t.Fatal(u, err)
	}
	if err := os.RemoveAll(dir); err != nil { // saves now fail
		t.Fatal(err)
	}
	if u, err := p.assign("/h/agents/b", 2); u != "" || err == nil {
		t.Fatalf("assign: %q %v", u, err)
	}
	if p.userOf("/h/agents/b") != "" || len(p.entries()) != 1 {
		t.Fatal("failed assign left state behind", p.entries())
	}
	if err := p.mark("agent-1", poolReclaiming); err == nil {
		t.Fatal("mark should fail")
	}
	if err := p.free("agent-1"); err == nil {
		t.Fatal("free should fail")
	}
	if e := p.entries(); len(e) != 1 || e[0].State != poolActive {
		t.Fatal(e)
	}
	// Once saving works again the unsaved user is still unassigned.
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if u, err := p.assign("/h/agents/b", 2); u != "agent-2" || err != nil {
		t.Fatal(u, err)
	}
}

func TestPoolReclaimingUserIsNeverHandedOut(t *testing.T) {
	p, _ := loadPool(filepath.Join(t.TempDir(), "pool.json"), []string{"agent-1", "agent-2"})
	p.assign("/h/agents/a", 2)
	p.mark("agent-1", poolReclaiming)
	if u, err := p.assign("/h/agents/a", 2); u != "" || err != nil {
		t.Fatalf("same agent: %q %v", u, err)
	}
	if p.userOf("/h/agents/a") != "agent-1" {
		t.Fatal("userOf must still name the reclaiming user")
	}
	// A new agent skips the reclaiming user.
	if u, _ := p.assign("/h/agents/b", 2); u != "agent-2" {
		t.Fatal(u)
	}
	// The reclaiming entry still counts toward the limit.
	q, _ := loadPool(filepath.Join(t.TempDir(), "pool.json"), []string{"agent-1", "agent-2"})
	q.assign("/h/agents/a", 1)
	q.mark("agent-1", poolReclaiming)
	if u, _ := q.assign("/h/agents/b", 1); u != "" {
		t.Fatal(u)
	}
}

// A pool.json written by 0.5.1 or older records "notes" per entry; the field
// is ignored, and the next write drops it.
func TestPoolLoadsEntriesWithOldNotesField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pool.json")
	old := `[{"user":"agent-1","agent":"a","state":"active","notes":"0.4.0"},{"user":"agent-2","agent":"b","state":"reclaiming","notes":"0.5.1"}]`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	users := []string{"agent-1", "agent-2"}
	p, err := loadPool(path, users)
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := p.entryOf("a"); !ok || e.User != "agent-1" || e.State != poolActive {
		t.Fatal(e, ok)
	}
	if e, ok := p.entryOf("b"); !ok || e.User != "agent-2" || e.State != poolReclaiming {
		t.Fatal(e, ok)
	}
	if err := p.mark("agent-1", poolReclaiming); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "notes") {
		t.Fatalf("pool.json %s", data)
	}
}

// Without pool.json the daemon would take every agent for new and hand out
// its user, so it never starts on a journal root that holds an agent's
// journal directory, or a link that may lead to one. It refuses before
// changing anything.
func TestDaemonRefusesLostPool(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func(path string) error
	}{
		{"directory", func(path string) error { return os.Mkdir(path, 0o751) }},
		{"link", func(path string) error { return os.Symlink(t.TempDir(), path) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			if err := tc.make(h.cfg.JournalDir(h.mainID)); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := Run(ctx, h.cfg)
			if err == nil || !strings.Contains(err.Error(), h.cfg.PoolFile()+" is missing") || !strings.Contains(err.Error(), `"If pool.json is lost" in the harness README`) {
				t.Fatalf("Run: %v", err)
			}
			if _, err := os.Lstat(h.cfg.PoolFile()); !os.IsNotExist(err) {
				t.Fatalf("pool.json created: %v", err)
			}
			if sys := h.cfg.System.(*fakeSystem); sys.String() != "" {
				t.Fatalf("changed before refusing: %s", sys)
			}
		})
	}
}

// A fresh install has neither pool.json nor journals, and starts; a regular
// file in the journal root is not an agent's journals.
func TestDaemonStartsFresh(t *testing.T) {
	h := newHarness(t, done())
	if err := os.WriteFile(filepath.Join(h.cfg.Log, "notes.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h.start()
	if !strings.Contains(poolJSON(h), `"agent":"main"`) {
		t.Fatalf("pool.json %s", poolJSON(h))
	}
}
