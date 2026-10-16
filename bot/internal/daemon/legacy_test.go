package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

// A pool.json from a release before 0.9.0 is migrated once: each active
// entry's user gets its agent id as its comment, reclaiming entries and
// pool users with no entry are deleted, and the file goes.
func TestLegacyPoolMigrated(t *testing.T) {
	h := newHarness(t)
	sys := h.cfg.System.(*fakeSystem)
	for _, name := range []string{"agent-1", "agent-2", "agent-3"} {
		home := h.cfg.Home(name)
		os.MkdirAll(home, 0o755)
		sys.seed(name, "", home)
	}
	pool := filepath.Join(h.cfg.Lib, "pool.json")
	os.WriteFile(pool, []byte(`[{"user":"agent-1","agent":"main","state":"active"},{"user":"agent-2","agent":"main/agents/r","state":"reclaiming"}]`), 0o600)
	if err := migratePool(h.cfg, sys); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"comment agent-1 main", "userdel agent-2", "userdel agent-3"} {
		if !sys.called(want) {
			t.Fatalf("missing %q: %v", want, sys)
		}
	}
	if sys.called("userdel agent-1") {
		t.Fatalf("live agent's user deleted: %v", sys)
	}
	users, _ := sys.Users("agent-")
	if len(users) != 1 || users[0] != (User{"agent-1", "main"}) {
		t.Fatalf("users %v", users)
	}
	if _, err := os.Stat(pool); !os.IsNotExist(err) {
		t.Fatalf("pool.json kept: %v", err)
	}
	if _, err := os.Stat(h.cfg.Home("agent-1")); err != nil {
		t.Fatalf("main's home removed: %v", err)
	}
	// No pool.json: nothing to do, nothing logged.
	if err := migratePool(h.cfg, sys); err != nil {
		t.Fatal(err)
	}
}
