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
