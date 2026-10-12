package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/SEAN-7/bot/internal/layout"
)

// legacyInbox is a message the 0.3.1 daemon delivered to the main agent and
// the agent has not collected yet.
const (
	legacyInbox     = "20261011T000000.000000000Z-owner.md"
	legacyInboxText = "hello from the owner"
)

// legacyInstall lays out a 0.3.1 installation in a temporary root, shaped
// like the live VM: the main agent's user agent with its home, two
// subagents (clinics on agent-1, with its own subagent scraper on agent-3,
// and recipes on agent-2), journals under Log/home/agent, the conversation
// log at the log root, a spooled owner message, a sudoers file and no
// OWNER.md. Pool users agent-1 to agent-15 exist; agent-16 does not.
func legacyInstall(t *testing.T) (Config, *fakeSystem) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return legacyInstallIn(t, layout.Layout{Homes: filepath.Join(root, "home"), Etc: filepath.Join(root, "etc", "agent"), Lib: filepath.Join(root, "lib"), Log: filepath.Join(root, "log")})
}

// legacyInstallIn lays out the 0.3.1 installation in l, whose directories
// share one parent.
func legacyInstallIn(t *testing.T, l layout.Layout) (Config, *fakeSystem) {
	t.Helper()
	self, _ := os.Executable()
	cfg := DefaultConfig(self)
	cfg.Layout = l
	cfg.SudoersDir = filepath.Join(filepath.Dir(l.Homes), "sudoers.d")
	sys := &fakeSystem{users: map[string]bool{"agent": true}}
	for i := 1; i <= 15; i++ {
		sys.users[fmt.Sprintf("agent-%d", i)] = true
	}
	cfg.System, cfg.LookupUser = sys, sys.lookup
	cfg.Logger = log.New(testWriter{t}, "", 0)

	main := filepath.Join(cfg.Homes, "agent")
	oldLog := filepath.Join(cfg.Log, "home", "agent")
	pool, _ := json.Marshal([]poolEntry{
		{User: "agent-1", Agent: main + "/agents/clinics", State: poolActive},
		{User: "agent-2", Agent: main + "/agents/recipes", State: poolActive},
		{User: "agent-3", Agent: main + "/agents/clinics/agents/scraper", State: poolActive},
	})
	sum := sha256.Sum256([]byte(legacyInboxText))
	var delivered []string
	for _, r := range []deliveryRecord{
		{Agent: main, Name: legacyInbox, SHA256: hex.EncodeToString(sum[:])},
		{Agent: main + "/agents/clinics", Name: "n.md", SHA256: "abc", From: "owner"},
		{Agent: main + "/agents/recipes", Name: "o.md", SHA256: "def", Consumed: true},
	} {
		line, _ := json.Marshal(r)
		delivered = append(delivered, string(line))
	}
	writeTree(t, map[string]string{
		cfg.SettingsFile():                                                           `{"max_agents":16}`,
		filepath.Join(main, "config.json"):                                           testConfig,
		filepath.Join(main, "ERROR"):                                                 "provider failed\n",
		filepath.Join(main, "notes.txt"):                                             "main's own file",
		filepath.Join(main, "inbox", legacyInbox):                                    legacyInboxText,
		filepath.Join(main, "agents", "clinics", "config.json"):                      testConfig,
		filepath.Join(main, "agents", "clinics", "OWNER.md"):                         "clinics owner",
		filepath.Join(main, "agents", "recipes", "config.json"):                      testConfig,
		filepath.Join(main, "agents", "recipes", "ERROR"):                            "recipes error\n",
		filepath.Join(cfg.PoolHome("agent-1"), "data.txt"):                           "clinics data",
		filepath.Join(cfg.PoolHome("agent-1"), "agents", "scraper", "config.json"):   testConfig,
		filepath.Join(cfg.PoolHome("agent-2"), "data.txt"):                           "recipes data",
		filepath.Join(cfg.PoolHome("agent-3"), "data.txt"):                           "scraper data",
		filepath.Join(oldLog, "20261011T000000Z.jsonl"):                              "{}\n",
		filepath.Join(oldLog, "agents", "clinics", "run.jsonl"):                      "{}\n",
		filepath.Join(oldLog, "agents", "recipes", "run.jsonl"):                      "{}\n",
		filepath.Join(oldLog, "agents", "clinics", "agents", "scraper", "run.jsonl"): "{}\n",
		filepath.Join(cfg.Log, "messages.jsonl"):                                     `{"agent":"main","text":"hi"}` + "\n",
		cfg.PoolFile():                                                               string(pool),
		cfg.DeliveredFile():                                                          strings.Join(delivered, "\n") + "\n",
		filepath.Join(cfg.Lib, "owner", "20261012T000000.000000000Z.md"):             "queued for main",
		cfg.VersionFile():                      "0.3.1\n",
		filepath.Join(cfg.SudoersDir, "agent"): "agent ALL=(ALL) NOPASSWD: ALL\n",
	}, map[string]string{
		filepath.Join(main, "journal"):                                      oldLog,
		filepath.Join(main, "agents", "clinics", "home"):                    cfg.PoolHome("agent-1"),
		filepath.Join(main, "agents", "recipes", "home"):                    cfg.PoolHome("agent-2"),
		filepath.Join(cfg.PoolHome("agent-1"), "agents", "scraper", "home"): cfg.PoolHome("agent-3"),
		filepath.Join(cfg.PoolHome("agent-1"), "journal"):                   filepath.Join(oldLog, "agents", "clinics"),
		filepath.Join(cfg.PoolHome("agent-2"), "journal"):                   filepath.Join(oldLog, "agents", "recipes"),
		filepath.Join(cfg.PoolHome("agent-3"), "journal"):                   filepath.Join(oldLog, "agents", "clinics", "agents", "scraper"),
	})
	for dir, mode := range map[string]fs.FileMode{main: 0o750, filepath.Join(cfg.Lib, "owner"): 0o700} {
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
	}
	return cfg, sys
}

// writeTree creates files and symbolic links, with their directories.
func writeTree(t *testing.T, files, links map[string]string) {
	t.Helper()
	for path, text := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for path, target := range links {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
	}
}

// snapshot describes everything under the installation's root, and which
// users exist, with the root's path replaced so two installations compare.
func snapshot(t *testing.T, cfg Config, sys *fakeSystem) string {
	t.Helper()
	root := filepath.Dir(cfg.Homes)
	var lines []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		line := strings.TrimPrefix(path, root) + " " + info.Mode().String()
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			line += " -> " + target
		case d.Type().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			line += " " + string(data)
		}
		lines = append(lines, strings.ReplaceAll(line, root, "$ROOT"))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sys.mu.Lock()
	for u := range sys.users {
		lines = append(lines, "user "+u)
	}
	sys.mu.Unlock()
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func readLink(t *testing.T, path string) string {
	t.Helper()
	target, err := os.Readlink(path)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func fileText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestMigrateRestructuresState(t *testing.T) {
	for _, ownerMD := range []bool{false, true} {
		t.Run(fmt.Sprintf("OWNER.md=%v", ownerMD), func(t *testing.T) {
			cfg, sys := legacyInstall(t)
			if ownerMD {
				writeTree(t, map[string]string{filepath.Join(cfg.Etc, "OWNER.md"): "the owner"}, nil)
			}
			if err := Migrate(cfg); err != nil {
				t.Fatal(err)
			}
			old, home := filepath.Join(cfg.Homes, "agent"), cfg.PoolHome("agent-16")

			// The user, its home and the link at its old path.
			if sys.users["agent"] || !sys.users["agent-16"] {
				t.Fatalf("users: %v", sys.users)
			}
			if !sys.inOrder("unlinger agent", "stop agent", "rename agent agent-16", "rename crontab agent agent-16", "linger agent-16") {
				t.Fatalf("system calls: %s", sys)
			}
			if got := readLink(t, old); got != home {
				t.Fatalf("%s -> %s, want %s", old, got, home)
			}
			info, err := os.Lstat(home)
			if err != nil || !info.IsDir() || info.Mode().Perm() != 0o750 || info.Mode()&fs.ModeSetgid == 0 {
				t.Fatalf("home %v %v", info.Mode(), err)
			}
			if got := fileText(t, filepath.Join(home, "notes.txt")); got != "main's own file" {
				t.Fatal(got)
			}
			if exists(filepath.Join(home, "ERROR")) {
				t.Fatal("~/ERROR kept")
			}
			// Each agent's config.json stays where it is: main's in its home,
			// the subagents' in their control directories until admission
			// moves them.
			for _, path := range []string{filepath.Join(home, "config.json"), filepath.Join(home, "agents", "clinics", "config.json"), filepath.Join(home, "agents", "recipes", "config.json")} {
				if !exists(path) {
					t.Fatalf("%s missing", path)
				}
			}

			// Root's control directory for main.
			ctrl := filepath.Join(cfg.RootHome(), "agents", "main")
			if info, err := os.Lstat(ctrl); err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
				t.Fatalf("%s: %v %v", ctrl, info, err)
			}
			if ownerMD {
				if got := fileText(t, filepath.Join(ctrl, "OWNER.md")); got != "the owner" {
					t.Fatal(got)
				}
				if exists(filepath.Join(cfg.Etc, "OWNER.md")) {
					t.Fatal("OWNER.md left in /etc/agent")
				}
			}

			// pool.json: ids from root, main on the new user, notes at the
			// version upgraded from.
			p, err := loadPool(cfg.PoolFile(), nil)
			if err != nil {
				t.Fatal(err)
			}
			want := []poolEntry{
				{User: "agent-1", Agent: "main/agents/clinics", State: poolActive, Notes: "0.3.1"},
				{User: "agent-16", Agent: "main", State: poolActive, Notes: "0.3.1"},
				{User: "agent-2", Agent: "main/agents/recipes", State: poolActive, Notes: "0.3.1"},
				{User: "agent-3", Agent: "main/agents/clinics/agents/scraper", State: poolActive, Notes: "0.3.1"},
			}
			if got := p.entries(); fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("pool %v", got)
			}

			// Journals and the conversation log, and the links to them.
			for _, path := range []string{
				filepath.Join(cfg.JournalDir("main"), "20261011T000000Z.jsonl"),
				filepath.Join(cfg.JournalDir("main/agents/clinics"), "run.jsonl"),
				filepath.Join(cfg.JournalDir("main/agents/clinics/agents/scraper"), "run.jsonl"),
				cfg.ConversationFile("main"),
			} {
				if !exists(path) {
					t.Fatalf("%s missing", path)
				}
			}
			for _, path := range []string{filepath.Join(cfg.Log, "home"), filepath.Join(cfg.Log, "messages.jsonl")} {
				if exists(path) {
					t.Fatalf("%s left", path)
				}
			}
			for home, id := range map[string]string{home: "main", cfg.PoolHome("agent-1"): "main/agents/clinics", cfg.PoolHome("agent-2"): "main/agents/recipes", cfg.PoolHome("agent-3"): "main/agents/clinics/agents/scraper"} {
				if got := readLink(t, filepath.Join(home, "journal")); got != cfg.JournalDir(id) {
					t.Fatalf("%s/journal -> %s, want %s", home, got, cfg.JournalDir(id))
				}
			}

			// Delivery records under the new ids.
			var agents []string
			for _, r := range readRecords(cfg.DeliveredFile()) {
				agents = append(agents, r["agent"].(string))
			}
			if got := strings.Join(agents, " "); got != "main main/agents/clinics main/agents/recipes" {
				t.Fatal(got)
			}

			// Root's spool for main, the sudoers file, the migration's own
			// record.
			spool := filepath.Join(cfg.RootHome(), ".messages", "agents", "main")
			if got := fileText(t, filepath.Join(spool, "20261012T000000.000000000Z.md")); got != "queued for main" {
				t.Fatal(got)
			}
			if info, err := os.Stat(spool); err != nil || info.Mode().Perm() != 0o700 {
				t.Fatalf("spool %v %v", info, err)
			}
			for _, path := range []string{filepath.Join(cfg.Lib, "owner"), filepath.Join(cfg.SudoersDir, "agent"), cfg.MigratingUserFile()} {
				if exists(path) {
					t.Fatalf("%s left", path)
				}
			}

			// Reclaiming main's user removes the link to its home.
			d := &daemon{cfg: cfg}
			if err := d.removeHomeLinks("agent-16"); err != nil || exists(old) {
				t.Fatalf("link after reclaim: %v %v", exists(old), err)
			}
		})
	}
}

// An inbox file the 0.3.1 daemon delivered is still labelled as the owner's
// once the agent collects it under its new id.
func TestMigrateKeepsDeliveredLabels(t *testing.T) {
	cfg, _ := legacyInstall(t)
	if err := Migrate(cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(cfg.PoolHome("agent-16"), "inbox", legacyInbox))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	d, err := loadDelivered(cfg.DeliveredFile())
	if err != nil {
		t.Fatal(err)
	}
	if from := d.take("main", legacyInbox, hex.EncodeToString(sum[:])); from != "owner" {
		t.Fatalf("labelled %q", from)
	}
	if from := d.take("main/agents/clinics", "n.md", "abc"); from != "owner" {
		t.Fatalf("subagent's labelled %q", from)
	}
}

var errInterrupted = errors.New("interrupted")

// A migration interrupted before any of its changes finishes on the next
// run, with the same target user and the same result as one run.
func TestMigrateResumesAfterEachStep(t *testing.T) {
	cfg, sys := legacyInstall(t)
	var changes []string
	migrateHook = func(change string) error { changes = append(changes, change); return nil }
	err := Migrate(cfg)
	migrateHook = nil
	if err != nil {
		t.Fatal(err)
	}
	want := snapshot(t, cfg, sys)
	renamed := -1
	for i, c := range changes {
		if c == "rename user" {
			renamed = i
		}
	}
	if renamed < 0 {
		t.Fatalf("no rename among %q", changes)
	}
	for i, change := range changes {
		t.Run(fmt.Sprintf("%02d %s", i, change), func(t *testing.T) {
			cfg, sys := legacyInstall(t)
			n := 0
			migrateHook = func(string) error {
				if n++; n == i+1 {
					return errInterrupted
				}
				return nil
			}
			err := Migrate(cfg)
			migrateHook = nil
			var partial *PartialError
			if !errors.Is(err, errInterrupted) || errors.As(err, &partial) != (i >= renamed) {
				t.Fatalf("interrupted run: %v (partial from change %d)", err, renamed)
			}
			if i > 0 {
				// A pool user free now must not change the target the
				// interrupted run recorded.
				sys.setUser("agent-15", false)
				defer sys.setUser("agent-15", true)
			}
			if err := Migrate(cfg); err != nil {
				t.Fatalf("rerun: %v", err)
			}
			sys.setUser("agent-15", true)
			if got := snapshot(t, cfg, sys); got != want {
				t.Fatalf("after rerun:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

// A failure before the user is renamed changes nothing a previous release
// could trip over, and is not partial: install.sh restores that release.
func TestMigrateFailureBeforeRenameIsNotPartial(t *testing.T) {
	cfg, sys := legacyInstall(t)
	sys.setFail("stop agent", errors.New("processes survived"))
	err := Migrate(cfg)
	var partial *PartialError
	if err == nil || errors.As(err, &partial) {
		t.Fatalf("got %v", err)
	}
	if sys.called("rename agent agent-16") || !sys.users["agent"] {
		t.Fatalf("renamed: %s", sys)
	}
	if info, err := os.Lstat(filepath.Join(cfg.Homes, "agent")); err != nil || !info.IsDir() {
		t.Fatalf("home %v %v", info, err)
	}
	if exists(cfg.PoolHome("agent-16")) || exists(filepath.Join(cfg.RootHome(), "agents")) {
		t.Fatal("state changed")
	}
	sys.setFail("stop agent", nil)
	if err := Migrate(cfg); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(cfg.PoolHome("agent-16"), "notes.txt")) {
		t.Fatal("not migrated on the second run")
	}
}

// With no legacy user and no migration under way there is nothing to do: a
// new installation, or one migrated already.
func TestMigrateNothingToDo(t *testing.T) {
	cfg, sys := legacyInstall(t)
	if err := Migrate(cfg); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, cfg, sys)
	calls := sys.String()
	if err := Migrate(cfg); err != nil {
		t.Fatal(err)
	}
	if snapshot(t, cfg, sys) != before || sys.String() != calls {
		t.Fatalf("a second run changed something: %s", sys)
	}

	fresh := Config{Layout: layout.Layout{Homes: t.TempDir(), Etc: t.TempDir(), Lib: t.TempDir(), Log: t.TempDir()}, LegacyUser: "agent", System: &fakeSystem{}}
	fresh.LookupUser = (&fakeSystem{}).lookup
	if err := Migrate(fresh); err != nil {
		t.Fatal(err)
	}
}

// pool.json with a 0.3 id but no legacy user to migrate: the daemon would
// take the agent for removed and wipe its home, so nothing goes ahead.
func TestMigrateRefusesOldPoolWithoutLegacyUser(t *testing.T) {
	cfg, sys := legacyInstall(t)
	sys.setUser("agent", false)
	err := Migrate(cfg)
	if err == nil || !strings.Contains(err.Error(), "pool.json") {
		t.Fatalf("got %v", err)
	}
}

// Anything that would stop the migration part way is found before the first
// change.
func TestMigrateRefusesConflicts(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T, cfg Config){
		"target home exists": func(t *testing.T, cfg Config) {
			os.MkdirAll(cfg.PoolHome("agent-16"), 0o755)
		},
		"unknown pool id": func(t *testing.T, cfg Config) {
			writeTree(t, map[string]string{cfg.PoolFile(): `[{"user":"agent-1","agent":"/srv/other/agents/x","state":"active"}]`}, nil)
		},
		"main's journals exist": func(t *testing.T, cfg Config) {
			os.MkdirAll(cfg.JournalDir("main"), 0o755)
		},
		"spooled name taken": func(t *testing.T, cfg Config) {
			writeTree(t, map[string]string{filepath.Join(cfg.RootHome(), ".messages", "agents", "main", "20261012T000000.000000000Z.md"): "other"}, nil)
		},
		"OWNER.md in both places": func(t *testing.T, cfg Config) {
			writeTree(t, map[string]string{filepath.Join(cfg.Etc, "OWNER.md"): "a", filepath.Join(cfg.RootHome(), "agents", "main", "OWNER.md"): "b"}, nil)
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, sys := legacyInstall(t)
			setup(t, cfg)
			before := snapshot(t, cfg, sys)
			err := Migrate(cfg)
			var partial *PartialError
			if err == nil || errors.As(err, &partial) {
				t.Fatalf("got %v", err)
			}
			if snapshot(t, cfg, sys) != before || sys.String() != "" {
				t.Fatalf("changed before refusing: %s", sys)
			}
		})
	}
}

// The daemon starts on a migrated installation and keeps every agent's
// home: main on the renamed user, each subagent on its own user, with its
// config.json adopted into its home.
func TestMigratedInstallationStarts(t *testing.T) {
	h := newHarness(t, done(), done(), done())
	os.RemoveAll(filepath.Join(h.cfg.RootHome(), "agents")) // the migration creates main's
	cfg, _ := legacyInstallIn(t, h.cfg.Layout)
	if err := Migrate(cfg); err != nil {
		t.Fatal(err)
	}
	h.cfg.PoolUsers = layout.PoolUsers(16)
	h.mainHome = h.cfg.PoolHome("agent-16")
	h.run()
	waitFor(t, "subagents' configs adopted", func() bool {
		return exists(filepath.Join(h.cfg.PoolHome("agent-1"), "config.json")) && exists(filepath.Join(h.cfg.PoolHome("agent-2"), "config.json")) && exists(filepath.Join(h.cfg.PoolHome("agent-3"), "config.json"))
	})
	for path, text := range map[string]string{
		filepath.Join(h.cfg.PoolHome("agent-16"), "notes.txt"): "main's own file",
		filepath.Join(h.cfg.PoolHome("agent-1"), "data.txt"):   "clinics data",
		filepath.Join(h.cfg.PoolHome("agent-2"), "data.txt"):   "recipes data",
		filepath.Join(h.cfg.PoolHome("agent-3"), "data.txt"):   "scraper data",
	} {
		if got := fileText(t, path); got != text {
			t.Fatalf("%s: %q", path, got)
		}
	}
	for _, ctrl := range []string{filepath.Join(h.cfg.PoolHome("agent-16"), "agents", "clinics"), filepath.Join(h.cfg.PoolHome("agent-1"), "agents", "scraper")} {
		if exists(filepath.Join(ctrl, "config.json")) {
			t.Fatalf("%s: starting config.json not adopted", ctrl)
		}
	}
	if !strings.Contains(h.logs.String(), "main: admitted as agent-16") {
		t.Fatalf("main not admitted on agent-16:\n%s", h.logs)
	}
}

// Before the rename, the recorded target binds nothing: if a user of that
// name appears after a failed run, the next run chooses again.
func TestMigrateChoosesAgainBeforeRename(t *testing.T) {
	cfg, sys := legacyInstall(t)
	sys.setFail("stop agent", errors.New("processes survived"))
	if err := Migrate(cfg); err == nil {
		t.Fatal("no error")
	}
	sys.setFail("stop agent", nil)
	sys.setUser("agent-16", true)
	sys.setUser("agent-15", false)
	if err := Migrate(cfg); err != nil {
		t.Fatal(err)
	}
	if got := readLink(t, filepath.Join(cfg.Homes, "agent")); got != cfg.PoolHome("agent-15") {
		t.Fatalf("migrated to %s", got)
	}
}
