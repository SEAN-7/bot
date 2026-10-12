package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const replayConfig = `{"provider":"replay","model":"replay-1"}`

// legacyAgent creates a subagent directory as earlier releases kept it: its
// whole state, inbox/ included, beside its config.json.
func legacyAgent(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "inbox", "processed"), 0o755); err != nil {
		t.Fatal(err)
	}
	files["config.json"] = replayConfig
	for name, text := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func poolUserOf(t *testing.T, h *harness, id string) string {
	t.Helper()
	p, err := loadPool(h.cfg.PoolFile(), h.cfg.PoolUsers)
	if err != nil {
		t.Fatal(err)
	}
	return p.userOf(id)
}

func TestMigrateMovesLegacySubagent(t *testing.T) {
	h := newHarness(t)
	legacy := filepath.Join(h.cfg.Home, "agents", "recipes")
	legacyAgent(t, legacy, map[string]string{"TASK.md": "keep me", "README.md": "my readme", "inbox/processed/old.md": "old", "notes/n.md": "note"})
	if err := os.Symlink(h.cfg.JournalDir(legacy), filepath.Join(legacy, "journal")); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(h.cfg.Home, "agents", "recipes-wt") // no inbox: not a subagent
	os.MkdirAll(wt, 0o755)
	os.WriteFile(filepath.Join(wt, "file"), []byte("x"), 0o644)
	if err := Migrate(h.cfg); err != nil {
		t.Fatal(err)
	}
	home := h.cfg.PoolHome("agent-1")
	if got := readFile(t, filepath.Join(home, "TASK.md")); got != "keep me" {
		t.Fatalf("TASK.md %q", got)
	}
	if got := readFile(t, filepath.Join(home, "notes", "n.md")); got != "note" {
		t.Fatalf("notes %q", got)
	}
	if got := readFile(t, filepath.Join(home, "inbox", "processed", "old.md")); got != "old" {
		t.Fatalf("inbox %q", got)
	}
	if target, err := os.Readlink(filepath.Join(home, "journal")); err != nil || target != h.cfg.JournalDir(legacy) {
		t.Fatalf("journal link %q %v", target, err)
	}
	if names := dirNames(t, legacy); len(names) != 1 || names[0] != "config.json" {
		t.Fatalf("control directory holds %v", names)
	}
	if names := dirNames(t, wt); len(names) != 1 || names[0] != "file" {
		t.Fatalf("recipes-wt holds %v", names)
	}
	if user := poolUserOf(t, h, legacy); user != "agent-1" {
		t.Fatalf("pool: %s", poolJSON(h))
	}
	if strings.Contains(poolJSON(h), "recipes-wt") {
		t.Fatalf("recipes-wt assigned: %s", poolJSON(h))
	}
	if !strings.Contains(h.logs.String(), "migrated agents/recipes to agent-1") {
		t.Fatalf("log:\n%s", h.logs)
	}
	before := poolJSON(h)
	if err := Migrate(h.cfg); err != nil {
		t.Fatal("second run:", err)
	}
	if poolJSON(h) != before || len(dirNames(t, legacy)) != 1 {
		t.Fatalf("second run changed something: %s %v", poolJSON(h), dirNames(t, legacy))
	}
	// The daemon admits it with the user it was given and keeps its files:
	// its README.md is its own, not reseeded.
	h.start()
	if got := h.childHome(legacy); got != home {
		t.Fatalf("home %q, want %q", got, home)
	}
	if got := readFile(t, filepath.Join(home, "README.md")); got != "my readme" {
		t.Fatalf("README.md %q", got)
	}
	if got := readFile(t, filepath.Join(home, "TASK.md")); got != "keep me" {
		t.Fatalf("TASK.md after admission %q", got)
	}
}

// TestMigrateNestedUsesLogicalID: a subagent's own legacy subagent is
// migrated after it, from the place its parent's move put it, and keeps its
// id, the path it had: its journals stay where they are.
func TestMigrateNestedUsesLogicalID(t *testing.T) {
	h := newHarness(t)
	recipes := filepath.Join(h.cfg.Home, "agents", "recipes")
	scraper := filepath.Join(recipes, "agents", "scraper")
	legacyAgent(t, recipes, map[string]string{"TASK.md": "recipes task"})
	legacyAgent(t, scraper, map[string]string{"TASK.md": "scraper task", "notes/n.md": "scraper note"})
	journal := filepath.Join(h.cfg.JournalDir(scraper), "messages.jsonl")
	os.MkdirAll(filepath.Dir(journal), 0o755)
	os.WriteFile(journal, []byte("{}\n"), 0o644)
	if err := Migrate(h.cfg); err != nil {
		t.Fatal(err)
	}
	recipesHome, scraperHome := h.cfg.PoolHome("agent-1"), h.cfg.PoolHome("agent-2")
	if poolUserOf(t, h, recipes) != "agent-1" || poolUserOf(t, h, scraper) != "agent-2" {
		t.Fatalf("pool: %s", poolJSON(h))
	}
	if got := readFile(t, filepath.Join(recipesHome, "TASK.md")); got != "recipes task" {
		t.Fatalf("recipes TASK.md %q", got)
	}
	if got := readFile(t, filepath.Join(scraperHome, "notes", "n.md")); got != "scraper note" {
		t.Fatalf("scraper note %q", got)
	}
	if _, err := os.Stat(filepath.Join(scraperHome, "inbox", "processed")); err != nil {
		t.Fatal(err)
	}
	ctrl := filepath.Join(recipesHome, "agents", "scraper")
	if names := dirNames(t, ctrl); len(names) != 1 || names[0] != "config.json" {
		t.Fatalf("scraper's control directory holds %v", names)
	}
	if got := readFile(t, journal); got != "{}\n" {
		t.Fatalf("journal %q", got)
	}
	if !strings.Contains(h.logs.String(), "migrated agents/recipes/agents/scraper to agent-2") {
		t.Fatalf("log:\n%s", h.logs)
	}
	h.start()
	if got := h.childHome(ctrl); got != scraperHome {
		t.Fatalf("scraper home %q, want %q", got, scraperHome)
	}
	if got := readFile(t, filepath.Join(scraperHome, "TASK.md")); got != "scraper task" {
		t.Fatalf("scraper TASK.md after admission %q", got)
	}
}

func TestMigrateRefusesSymlinkedAgents(t *testing.T) {
	h := newHarness(t)
	elsewhere := filepath.Join(h.root, "elsewhere")
	legacyAgent(t, filepath.Join(elsewhere, "recipes"), map[string]string{"TASK.md": "keep me"})
	if err := os.Symlink(elsewhere, filepath.Join(h.cfg.Home, "agents")); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(h.cfg); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("got %v", err)
	}
	if got := readFile(t, filepath.Join(elsewhere, "recipes", "TASK.md")); got != "keep me" {
		t.Fatalf("TASK.md %q", got)
	}
	if _, err := os.Stat(h.cfg.PoolFile()); !os.IsNotExist(err) {
		t.Fatalf("pool written: %s", poolJSON(h))
	}
}

func TestMigrateRefusesNonEmptyPoolHome(t *testing.T) {
	h := newHarness(t)
	legacy := filepath.Join(h.cfg.Home, "agents", "recipes")
	legacyAgent(t, legacy, map[string]string{"TASK.md": "keep me"})
	home := h.cfg.PoolHome("agent-1")
	os.MkdirAll(home, 0o755)
	os.WriteFile(filepath.Join(home, "stranger"), []byte("x"), 0o644)
	if err := Migrate(h.cfg); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("got %v", err)
	}
	if names := dirNames(t, legacy); strings.Join(names, " ") != "TASK.md config.json inbox" {
		t.Fatalf("legacy directory holds %v", names)
	}
	// Not left assigned: a rerun would otherwise take the stranger's files
	// for an interrupted migration's.
	if user := poolUserOf(t, h, legacy); user != "" {
		t.Fatalf("pool: %s", poolJSON(h))
	}
	if err := Migrate(h.cfg); err == nil {
		t.Fatal("rerun moved into a non-empty home")
	}
}

// TestMigrateResumesInterruptedMove: a run that stopped part way, leaving
// its assignment and some files in the pool home, is finished by the next.
func TestMigrateResumesInterruptedMove(t *testing.T) {
	h := newHarness(t)
	legacy := filepath.Join(h.cfg.Home, "agents", "recipes")
	legacyAgent(t, legacy, map[string]string{"README.md": "my readme"})
	home := h.cfg.PoolHome("agent-1")
	os.MkdirAll(home, 0o755)
	os.WriteFile(filepath.Join(home, "TASK.md"), []byte("moved already"), 0o644)
	writePool(t, h, `[{"user":"agent-1","agent":"`+legacy+`","state":"active"}]`)
	if err := Migrate(h.cfg); err != nil {
		t.Fatal(err)
	}
	if names := dirNames(t, home); strings.Join(names, " ") != "README.md TASK.md inbox" {
		t.Fatalf("home holds %v", names)
	}
	if names := dirNames(t, legacy); len(names) != 1 {
		t.Fatalf("legacy directory holds %v", names)
	}
}

func TestMigrateRefusesNameInBoth(t *testing.T) {
	h := newHarness(t)
	legacy := filepath.Join(h.cfg.Home, "agents", "recipes")
	legacyAgent(t, legacy, map[string]string{"TASK.md": "legacy"})
	home := h.cfg.PoolHome("agent-1")
	os.MkdirAll(home, 0o755)
	os.WriteFile(filepath.Join(home, "TASK.md"), []byte("moved"), 0o644)
	writePool(t, h, `[{"user":"agent-1","agent":"`+legacy+`","state":"active"}]`)
	if err := Migrate(h.cfg); err == nil || !strings.Contains(err.Error(), "TASK.md") {
		t.Fatalf("got %v", err)
	}
	if readFile(t, filepath.Join(legacy, "TASK.md")) != "legacy" || readFile(t, filepath.Join(home, "TASK.md")) != "moved" {
		t.Fatal("a file was overwritten")
	}
	if _, err := os.Stat(filepath.Join(legacy, "inbox")); err != nil {
		t.Fatal("inbox moved before the rest:", err)
	}
}

func TestMigrateNeedsAUserEach(t *testing.T) {
	h := newHarness(t)
	for _, name := range []string{"a", "b", "c", "d"} {
		legacyAgent(t, filepath.Join(h.cfg.Home, "agents", name), map[string]string{"TASK.md": name})
	}
	err := Migrate(h.cfg)
	if err == nil || !strings.Contains(err.Error(), "4 subagents") {
		t.Fatalf("got %v", err)
	}
	for _, name := range []string{"a", "b", "c", "d"} {
		if got := readFile(t, filepath.Join(h.cfg.Home, "agents", name, "TASK.md")); got != name {
			t.Fatalf("%s moved", name)
		}
	}
	if _, err := os.Stat(h.cfg.PoolFile()); !os.IsNotExist(err) {
		t.Fatalf("pool written: %s", poolJSON(h))
	}
}

// TestMigrateLeavesAdmittedControlDirectory: a control directory the daemon
// already admitted (it has the home link) is not a legacy subagent, even
// with an inbox/ in it.
func TestMigrateLeavesAdmittedControlDirectory(t *testing.T) {
	h := newHarness(t)
	ctrl := makeChild(t, h, "new", true)
	os.MkdirAll(filepath.Join(ctrl, "inbox"), 0o755)
	os.Symlink(h.cfg.PoolHome("agent-3"), filepath.Join(ctrl, "home"))
	if err := Migrate(h.cfg); err != nil {
		t.Fatal(err)
	}
	if names := dirNames(t, ctrl); strings.Join(names, " ") != "config.json home inbox" {
		t.Fatalf("control directory holds %v", names)
	}
	if _, err := os.Stat(h.cfg.PoolFile()); !os.IsNotExist(err) {
		t.Fatalf("pool written: %s", poolJSON(h))
	}
}

// TestMigrateResumesNested: a run interrupted while moving a nested
// subagent's files left its parent's inbox/ in place, so the next run finds
// both, the nested one inside the parent's pool home, and finishes.
func TestMigrateResumesNested(t *testing.T) {
	h := newHarness(t)
	recipes := filepath.Join(h.cfg.Home, "agents", "recipes")
	scraper := recipes + "/agents/scraper"
	recipesHome, scraperHome := h.cfg.PoolHome("agent-1"), h.cfg.PoolHome("agent-2")
	legacyAgent(t, recipes, map[string]string{})
	os.WriteFile(filepath.Join(recipes, "inbox", "waiting.md"), []byte("for recipes"), 0o644)
	os.MkdirAll(recipesHome, 0o755)
	os.WriteFile(filepath.Join(recipesHome, "TASK.md"), []byte("recipes task"), 0o644)
	legacyAgent(t, filepath.Join(recipesHome, "agents", "scraper"), map[string]string{"notes/n.md": "scraper note"})
	os.MkdirAll(scraperHome, 0o755)
	os.WriteFile(filepath.Join(scraperHome, "TASK.md"), []byte("scraper task"), 0o644)
	writePool(t, h, `[{"user":"agent-1","agent":"`+recipes+`","state":"active"},{"user":"agent-2","agent":"`+scraper+`","state":"active"}]`)
	if err := Migrate(h.cfg); err != nil {
		t.Fatal(err)
	}
	if names := dirNames(t, recipes); len(names) != 1 || names[0] != "config.json" {
		t.Fatalf("recipes' control directory holds %v", names)
	}
	if names := dirNames(t, filepath.Join(recipesHome, "agents", "scraper")); len(names) != 1 || names[0] != "config.json" {
		t.Fatalf("scraper's control directory holds %v", names)
	}
	if got := readFile(t, filepath.Join(recipesHome, "inbox", "waiting.md")); got != "for recipes" {
		t.Fatalf("recipes inbox %q", got)
	}
	if names := dirNames(t, scraperHome); strings.Join(names, " ") != "TASK.md inbox notes" {
		t.Fatalf("scraper home holds %v", names)
	}
	if !strings.Contains(poolJSON(h), "agent-2") || strings.Contains(poolJSON(h), "agent-3") {
		t.Fatalf("pool: %s", poolJSON(h))
	}
}
