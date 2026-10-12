package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPoolAssignLowestFreeAndPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pool.json")
	users := []string{"agent-1", "agent-2"}
	p, err := loadPool(path, users)
	if err != nil {
		t.Fatal(err)
	}
	u1, _ := p.assign("/h/agents/a", 2, nil)
	u2, _ := p.assign("/h/agents/b", 2, nil)
	u3, _ := p.assign("/h/agents/c", 2, nil)
	if u1 != "agent-1" || u2 != "agent-2" || u3 != "" {
		t.Fatal(u1, u2, u3)
	}
	if again, _ := p.assign("/h/agents/a", 2, nil); again != "agent-1" {
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
	if u, _ := p.assign("/h/agents/a", 1, nil); u != "agent-1" {
		t.Fatal(u)
	}
	if u, err := p.assign("/h/agents/b", 1, nil); u != "" || err != nil {
		t.Fatal(u, err)
	}
}

func TestPoolFreeReuses(t *testing.T) {
	p, _ := loadPool(filepath.Join(t.TempDir(), "pool.json"), []string{"agent-1", "agent-2"})
	p.assign("/h/agents/a", 2, nil)
	p.assign("/h/agents/b", 2, nil)
	if err := p.free("agent-1"); err != nil {
		t.Fatal(err)
	}
	if p.userOf("/h/agents/a") != "" {
		t.Fatal("freed user still assigned")
	}
	if u, _ := p.assign("/h/agents/c", 2, nil); u != "agent-1" {
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
	if u, err := p.assign("/h/agents/a", 2, nil); u != "agent-1" || err != nil {
		t.Fatal(u, err)
	}
	if err := os.RemoveAll(dir); err != nil { // saves now fail
		t.Fatal(err)
	}
	if u, err := p.assign("/h/agents/b", 2, nil); u != "" || err == nil {
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
	if u, err := p.assign("/h/agents/b", 2, nil); u != "agent-2" || err != nil {
		t.Fatal(u, err)
	}
}

func TestPoolReclaimingUserIsNeverHandedOut(t *testing.T) {
	p, _ := loadPool(filepath.Join(t.TempDir(), "pool.json"), []string{"agent-1", "agent-2"})
	p.assign("/h/agents/a", 2, nil)
	p.mark("agent-1", poolReclaiming)
	if u, err := p.assign("/h/agents/a", 2, nil); u != "" || err != nil {
		t.Fatalf("same agent: %q %v", u, err)
	}
	if p.userOf("/h/agents/a") != "agent-1" {
		t.Fatal("userOf must still name the reclaiming user")
	}
	// A new agent skips the reclaiming user.
	if u, _ := p.assign("/h/agents/b", 2, nil); u != "agent-2" {
		t.Fatal(u)
	}
	// The reclaiming entry still counts toward the limit.
	q, _ := loadPool(filepath.Join(t.TempDir(), "pool.json"), []string{"agent-1", "agent-2"})
	q.assign("/h/agents/a", 1, nil)
	q.mark("agent-1", poolReclaiming)
	if u, _ := q.assign("/h/agents/b", 1, nil); u != "" {
		t.Fatal(u)
	}
}

func TestPoolAssignSkipsUnusable(t *testing.T) {
	p, _ := loadPool(filepath.Join(t.TempDir(), "pool.json"), []string{"agent-1", "agent-2", "agent-3"})
	usable := func(u string) bool { return u != "agent-1" }
	if u, err := p.assign("a", 3, usable); u != "agent-2" || err != nil {
		t.Fatalf("got %q %v", u, err)
	}
	if u, _ := p.assign("a", 3, func(string) bool { return false }); u != "agent-2" {
		t.Fatalf("held user not returned: %q", u)
	}
	if u, _ := p.assign("b", 3, func(u string) bool { return u == "agent-2" }); u != "" {
		t.Fatalf("taken or unusable user handed out: %q", u)
	}
	if p.isFree("agent-2") || !p.isFree("agent-1") {
		t.Fatal(p.sorted())
	}
}
