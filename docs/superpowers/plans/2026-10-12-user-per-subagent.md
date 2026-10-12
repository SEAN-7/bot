# User per Subagent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run each subagent as its own Unix user from a reusable pool, created from a parent-owned control directory, with owner-relative messaging, OWNER.md, and a migration for existing installs.

**Architecture:** The daemon's single file helper and `a.dir` paths become a tree of agents, each with an id (main home or control directory), a home, its own `fsio` helper and a pool user. Root-only side effects (group members, lingering, killing a uid, crontabs, preparing a home) go behind a `System` interface, so the unprivileged test suite runs every agent as the current user with a recording fake, while root tests in CI exercise the real implementation with real users.

**Tech Stack:** Go standard library only; POSIX shell for `install.sh`; system tools `gpasswd`, `loginctl`, `systemctl`, `pkill`, `pgrep`, `crontab`.

**Spec:** `docs/spec.md` (sections: Three ideas, Privilege model, The daemon, The loop, Messages, Journal, OWNER.md, CLI, Seed files, Install, Tests, Migration → To a user per subagent). Philosophy: `docs/philosophy.md`.

## Global Constraints

- Go standard library only. `go vet ./...` and `go test ./...` pass before every commit.
- Work on `main`; commit and push after each task. Commit messages end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- `SystemPrompt`, the `sh` tool definition and plugin request building do not change.
- The daemon never opens a path inside an agent home or control directory as root; homes go through that agent's helper, control directories through the parent's.
- Pool users are `agent-1` … `agent-<max_agents - 1>`; a pool home is `/home/<user>`, owned `<user>:<user>`, mode `02750`. Assignments live in `/var/lib/agent/pool.json` (root, 0600).
- Every agent user's group is its user-private group (same name). A subagent's group members are its ancestors' users.
- Journal directories are root-owned, group = the agent's group, mode `0751` (traversable, so a child reaches its own journal dir nested inside its parent's); files `0640`. The main conversation log `/var/log/agent/messages.jsonl` is `root:agent 0640`; a subagent's is `messages.jsonl` in its journal directory.
- Spools: up `~/.messages/<UTC>.md`; down `~/.messages/agents/<NAME>/<UTC>.md`. Delivered inbox names: `<UTC>-owner.md` (from an owner) and `<UTC>-<NAME>.md` (from child NAME). Labels: `owner`, `agents/<NAME>`, `data`. Message limit 64 KiB.
- Commands run with umask 022. Never send synthetic messages to the live agent; tests use temporary roots.
- Agent-facing changes go in `internal/notes/notes.md` under `## 0.3.0` and are mirrored in the seed README.

## Review Focus

1. A control directory renamed while its subagent runs: the old subagent is removed (home wiped, user reclaimed) and a fresh one admitted under the new name — Task 5 `TestRenameControlDirIsRemoveAndAdmit`.
2. A free pool user whose home is not empty (leftovers from a manual edit or failed cleanup): admission must not hand the newcomer someone else's files — Task 4 `TestAdmissionClearsLeftoverHome`.
3. `agent send` naming a grandchild (`research/agents/x`) or a directory under `.messages/agents/` that is not a direct child: refused by the CLI and ignored by the daemon — Task 6 `TestNestedDownSpoolIgnored`, Task 8 `TestSendChildRejectsNestedName`.
4. Two control directories appear with one free user: one is admitted, the other waits with one ERROR and one parent note, and is admitted when the user frees — Task 5 `TestWaitingChildAdmittedWhenUserFrees`.
5. A symlinked `agents/` folder, a symlinked control directory, or a symlinked `config.json`: none is admitted — Task 1 `TestScanSkipsSymlinks`.

---

### Task 1: Helper: owners, one-level scan, Clear, PurgeOwned

**Files:**
- Modify: `internal/fsio/server.go`, `internal/fsio/client.go`
- Test: `internal/fsio/fsio_test.go`

**Interfaces:**
- Produces:
  - `Entry` gains `UID int \`json:"uid"\`` and `Mode uint32 \`json:"mode"\`` (`uint32(info.Mode().Perm())`), filled by `list` and `stat`.
  - `(*Client).Scan(home string) ([]string, error)`: control directories `<home>/agents/<name>`, sorted, where `agents` and `<name>` are real directories (lstat) owned by the helper's uid and `<name>/config.json` is a regular file (lstat). One level only; no recursion.
  - `(*Client).Clear(dir string) error`: removes every entry inside `dir` with `os.RemoveAll`, keeps `dir`; missing `dir` is not an error.
  - `(*Client).PurgeOwned(dirs []string) error`: for each dir, removes (`os.RemoveAll`) each top-level entry whose lstat uid equals `os.Getuid()`; missing dirs and entries that vanish are skipped.

- [ ] **Step 1: Write the failing tests**

```go
func TestScanFindsControlDirs(t *testing.T) {
	c, home := start(t), t.TempDir()
	for _, n := range []string{"b", "a"} {
		os.MkdirAll(filepath.Join(home, "agents", n), 0o755)
		os.WriteFile(filepath.Join(home, "agents", n, "config.json"), []byte("{}"), 0o644)
	}
	os.MkdirAll(filepath.Join(home, "agents", "noconfig"), 0o755)
	os.MkdirAll(filepath.Join(home, "agents", "a", "home", "agents", "deep"), 0o755) // not scanned: one level
	got, err := c.Scan(home)
	want := []string{filepath.Join(home, "agents", "a"), filepath.Join(home, "agents", "b")}
	if err != nil || !reflect.DeepEqual(got, want) { t.Fatalf("got %v %v", got, err) }
}

func TestScanSkipsSymlinks(t *testing.T) // agents/ as symlink → none; agents/x symlink to a real control dir → skipped;
                                        // agents/y with config.json symlink → skipped
func TestEntryHasOwnerAndMode(t *testing.T) // Stat of a 0640 file: UID == os.Getuid(), Mode == 0o640
func TestClearKeepsDir(t *testing.T)       // dir with files, subdir, dotfile → empty, dir still exists; missing dir → nil
func TestPurgeOwnedRemovesOwnEntries(t *testing.T) // two temp dirs with files and a subtree → all removed; nonexistent dir in list → nil
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/fsio -run 'Scan|EntryHas|Clear|PurgeOwned' -v`
Expected: FAIL (compile errors: `Clear`, `PurgeOwned`, `UID` undefined; old scan recurses).

- [ ] **Step 3: Implement** the ops `scan` (replace the breadth-first version), `clear`, `purgeOwned` (request field `Paths []string \`json:"paths,omitempty"\``), and fill `UID`/`Mode` in `entry()` from `info.Sys().(*syscall.Stat_t)`. Delete the old recursive `scan`.

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/fsio -v`
Expected: PASS. (`internal/daemon` will not compile until Task 4; do not run the whole suite yet — commit with `go vet ./internal/fsio`.)

- [ ] **Step 5: Commit**

```bash
git add internal/fsio
git commit -m "Scan control directories one level deep and report file owners"
```

---

### Task 2: Journal permissions and run-file filter

**Files:**
- Modify: `internal/journal/journal.go`, `internal/cli/log.go` (`newest`), `docs/spec.md` (Journal: directories `0751`, not `0750`; say why: a child's journal directory is nested inside its parent's)
- Test: `internal/journal/journal_test.go`

**Interfaces:**
- Produces:
  - `journal.Create(dir string, now time.Time, gid int) (*Writer, error)`: as today, but directories `0751`, files `0640`; when `gid >= 0` the new file gets `f.Chown(-1, gid)`, and `gid < 0` leaves the group.
  - `journal.IsRun(name string) bool`: true only for `<stamp>.jsonl` names (parse the stem with layout `20060102T150405.000000000Z`).
  - `journal.Secure(dir string, gid int) error`: `MkdirAll(dir, 0o751)`; then walk `dir` without following symlinks: directories `0751`, regular files `0640`, and `Lchown(path, -1, gid)` when `gid >= 0`; symlinks and other types untouched.
  - `journal.Latest` considers only `IsRun` names.

- [ ] **Step 1: Write the failing tests**

```go
func TestCreateModes(t *testing.T)   // Create(dir, now, -1): dir perm 0751, file perm 0640
func TestIsRun(t *testing.T)         // "20261011T120000.000000000Z.jsonl" true; "messages.jsonl", "x.jsonl", "20261011T120000.000000000Z.txt" false
func TestLatestIgnoresConversationLog(t *testing.T) // dir with one ended run + "messages.jsonl": Latest returns the run, clean=true
func TestSecureFixesTree(t *testing.T) // pre-existing 0755 dir with a 0644 run file and a subdir: after Secure(dir, os.Getgid()) → 0751 / 0640 / 0751; a symlink inside is left a symlink
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/journal -v`
Expected: FAIL (`Create` arity, `IsRun`/`Secure` undefined).

- [ ] **Step 3: Implement** in `journal.go`; update `newest` in `internal/cli/log.go` to filter with `journal.IsRun`. Update the spec line in the Journal section.

- [ ] **Step 4: Run**

Run: `go test ./internal/journal ./internal/cli -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/journal internal/cli/log.go docs/spec.md
git commit -m "Restrict journal permissions and tell run files from conversation logs"
```

---

### Task 3: Layout, pool and System

**Files:**
- Modify: `internal/layout/layout.go`, `internal/layout/layout_test.go`
- Create: `internal/daemon/pool.go`, `internal/daemon/system.go`
- Test: `internal/daemon/pool_test.go`, `internal/daemon/system_test.go`

**Interfaces:**
- Produces (layout):
  - `Layout.Homes string` — parent of pool homes; `Default()` sets `"/home"`.
  - `(Layout) PoolFile() string` → `Lib/pool.json`; `(Layout) OwnerFile() string` → `Etc/OWNER.md`; `(Layout) PoolHome(user string) string` → `Homes/user`.
  - `(Layout) ConversationFile(agentID string) string` → `MessagesFile()` when `agentID == l.Home`, else `JournalDir(agentID)/messages.jsonl`.
  - `layout.PoolUsers(maxAgents int) []string` → `agent-1` … `agent-<maxAgents-1>` (empty for `maxAgents <= 1`).
- Produces (daemon, unexported):
  - `type poolEntry struct { User string \`json:"user"\`; Agent string \`json:"agent"\`; State string \`json:"state"\` }` with states `"active"`, `"reclaiming"`; a free user has no entry. File format: a JSON array of entries.
  - `loadPool(path string, users []string) (*pool, error)` (missing file = empty pool).
  - `(*pool) userOf(agentID string) string`; `(*pool) assign(agentID string, limit int) (string, error)` — returns the existing user if assigned; otherwise the first user in `users` order with no entry, provided the number of entries is below `limit`; `""` when none; persists before returning. `(*pool) mark(user, state string) error`; `(*pool) free(user string) error`; `(*pool) entries() []poolEntry` sorted by user. Writes are atomic (temp file + rename, 0600).
  - `type System interface { PrepareHome(user, home string) error; SetMembers(group string, members []string) error; EnableLinger(user string) error; DisableLinger(user string) error; StopProcesses(user string) error; RemoveCrontab(user string) error }`
  - `type execSystem struct { run func(name string, args ...string) ([]byte, error); lookup func(string) (*syscall.Credential, error) }`, `newExecSystem() System`. Commands: `gpasswd -M <comma-joined> <group>`; `loginctl enable-linger|disable-linger <user>`; `StopProcesses`: `systemctl stop user@<uid>.service` (error ignored), then repeat `pkill -KILL -u <user>` until `pgrep -u <user>` exits 1, at most 50 tries 100 ms apart, else error; `RemoveCrontab`: `crontab -r -u <user>`, treating output containing `no crontab` as success. `PrepareHome` runs in-process as root: `Mkdir(home, 0o750)` if missing, `Lchown(home, uid, gid)`, `os.Chmod(home, 0o750|fs.ModeSetgid)` after `Lstat` confirms a directory (the parent of `home` is root-owned, so the entry cannot be swapped).
  - `Config` gains `System System`, `PoolUsers []string` (nil: `layout.PoolUsers(settings.MaxAgents)` filtered to users that exist), `LookupUser func(string) (*syscall.Credential, error)` (default `osuser.Lookup`), `TmpDirs []string` (default `/tmp`, `/var/tmp`, `/dev/shm`), `OwnerUID int` (default 0). `DefaultConfig` fills all of them.

- [ ] **Step 1: Write the failing tests**

```go
func TestPoolAssignLowestFreeAndPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pool.json")
	p, _ := loadPool(path, []string{"agent-1", "agent-2"})
	u1, _ := p.assign("/h/agents/a", 2)
	u2, _ := p.assign("/h/agents/b", 2)
	u3, _ := p.assign("/h/agents/c", 2)
	if u1 != "agent-1" || u2 != "agent-2" || u3 != "" { t.Fatal(u1, u2, u3) }
	if again, _ := p.assign("/h/agents/a", 2); again != "agent-1" { t.Fatal(again) }
	p.mark("agent-1", "reclaiming")
	q, _ := loadPool(path, []string{"agent-1", "agent-2"})
	if e := q.entries(); len(e) != 2 || e[0].State != "reclaiming" { t.Fatal(e) }
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 { t.Fatal(info.Mode()) }
}
func TestPoolLimit(t *testing.T)         // limit 1 with 2 users: second assign returns ""
func TestPoolFreeReuses(t *testing.T)    // free("agent-1") then assign new id → "agent-1"
func TestExecSystemCommands(t *testing.T) // fake run records argv: SetMembers("agent-3", []string{"agent","agent-1"}) → ["gpasswd","-M","agent,agent-1","agent-3"];
                                          // SetMembers(g, nil) → ["gpasswd","-M","",g]; RemoveCrontab with output "no crontab for agent-3" and exit error → nil
func TestPoolUsers(t *testing.T)          // layout.PoolUsers(16) has 15 names, first "agent-1", last "agent-15"; PoolUsers(1) empty
func TestConversationFile(t *testing.T)   // main id → MessagesFile; child id → JournalDir(id)/messages.jsonl
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/layout -v` and `go vet ./internal/daemon`
Expected: FAIL / undefined symbols.

- [ ] **Step 3: Implement** `layout.go` additions, `pool.go`, `system.go`, and the `Config` fields with defaults.

- [ ] **Step 4: Run**

Run: `go test ./internal/layout -v` (daemon tests run from Task 4 on).
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/layout internal/daemon/pool.go internal/daemon/system.go internal/daemon/pool_test.go internal/daemon/system_test.go internal/daemon/daemon.go
git commit -m "Add the user pool and the root-only system operations"
```

---

### Task 4: Agent tree and admission

**Files:**
- Modify: `internal/daemon/daemon.go`, `agent.go`, `run.go`, `inbox.go`, `conversation.go`, `owner.go`, `releasenotes.go`, `messages.go` (only to compile against the new agent type; behaviour changes in Task 6)
- Test: `internal/daemon/harness_test.go`, `agents_test.go`, and every existing daemon test that calls `makeChild` or uses child directories
- Create: `internal/daemon/fake_system_test.go`

**Interfaces:**
- Consumes: Task 1 `Scan`, `Clear`, `Entry.UID`; Task 2 `journal.Create(dir, now, gid)`, `journal.Secure`; Task 3 `pool`, `System`, `Config` fields, `Layout.PoolHome`.
- Produces:
  - `agent` fields: `id` (main home or control dir), `name` (`"main"` or path under main's `agents/`, e.g. `research/agents/scraper`), `home`, `ctrl` (`""` for main), `user` (`cfg.AgentUser` for main, pool user for children), `parent *agent`, `fs *fsio.Client` (started with this user's credential), `done chan struct{}` (closed when `loop` returns), `failedConfig`, `failedOwner string`, `conv *conversation`.
  - `(*agent) cred() (*syscall.Credential, error)` — `cfg.LookupUser(a.user)` on every call, so new group memberships apply to the next command. `(*agent) uid() int` (cred uid, or `os.Getuid()` for nil). `(*agent) gid() int` (cred gid, or `-1` for nil).
  - `(*agent) ctl() (*fsio.Client, string)` — `(a.parent.fs, a.ctrl)` for a subagent, `(a.fs, a.home)` for main. `config.json` and `ERROR` are always read/written through `ctl()`.
  - `(*daemon) writeNote(to *agent, kind, text string) error` — writes `to.home/inbox/<noteName(kind)>` through `to.fs`.
  - `(*agent) pending() ([]string, error)`; `collectInbox`, `label`, `takeConversation`, `toolContent`, `setError`, `clearError` use `a.home`/`a.fs`/`ctl()`.
  - `validControlDir(home, dir string) bool` replaces `validAgentDir`: `dir == filepath.Join(home, "agents", base)` with a clean, non-empty `base` other than `.`/`..` and no `/`.
  - `(*daemon) admit(parent *agent, ctrl string) *agent` — nil when no user is free (limited handling as today, but `ERROR` goes to `ctrl/ERROR` via `parent.fs` and the note to `parent`).
- Behaviour (spec: The daemon → Discovery, Admission, max_agents):
  - `Run`: `syscall.Umask(0o022)`; load the pool (`cfg.PoolFile()`, users from `cfg.PoolUsers` or settings); create the main agent (`id = home = cfg.Home`); `journal.Secure(JournalDir(main.id), main.gid())`.
  - `reconcile`: breadth-first from main; for each agent `a`, `a.fs.Scan(a.home)`; skip paths failing `validControlDir`; existing agents are kept, new ones go through `admit`. Limit passed to `pool.assign` is `min(len(users), settings.MaxAgents-1)`. Removal is Task 5 — in this task, a vanished agent is only cancelled (`errRemoved`) as today.
  - `admit` steps, in order: `pool.assign`; `System.PrepareHome(user, home)`; for a newly assigned user, `Clear(home)` through the new helper; `System.SetMembers(user, ancestors' users nearest-first)`; start the helper; seed through it: `Mkdir(home/inbox/processed)`, `Mkdir(home/agents)`, write `README.md` (`seed.README`) and `TASK.md` (`seed.TASK`) only if `Stat` says they do not exist; `System.EnableLinger(user)`; `journal.Secure(JournalDir(ctrl), gid)`; `parent.fs.Symlink(home, ctrl/home)`; start the loop.
  - Shell commands: `Dir`/`Home` = `a.home`, `Env(a.home, a.user-or-current-name, a.uid())`, `User: a.cred()` (error → the command result says so).
- Test harness: `newHarness` sets `Layout.Homes = root/home`, `PoolUsers = ["agent-1","agent-2","agent-3"]`, `LookupUser = func(string) (*syscall.Credential, error) { return nil, nil }`, `System = &fakeSystem{}` (records each call as one string: `"home <user>"`, `"members <group> <comma-joined members>"`, `"linger <user>"`, `"unlinger <user>"`, `"stop <user>"`, `"crontab <user>"`; `PrepareHome` also does `os.MkdirAll(home, 0o755)`; method `called(s string) bool`), `TmpDirs = []string{root/tmp}`, `OwnerUID = os.Getuid()`. `makeChild(t, h, name, config bool) string` creates only the control dir (+ `config.json`). New helpers: `h.childHome(ctrl string) string` waits for `ctrl/home` and returns its target; `h.mainUser() string` is the main agent's user name (`cfg.AgentUser`, or the current user's name when empty, as `d.agentName` is today). Existing tests drop child messages into `h.childHome(ctrl)` instead of `ctrl`; `TestParentChildReport` is rewritten in Task 6.

- [ ] **Step 1: Write the failing tests** (in `agents_test.go`)

```go
func TestAdmissionSeedsPoolHome(t *testing.T) {
	h := newHarness(t, matchStep("inbox/processed/task.md", "pwd; cat README.md | head -1", 100), `{"match":"inbox/processed/task.md","text":"done"}`)
	ctrl := makeChild(t, h, "c", true)
	h.start()
	home := h.childHome(ctrl)
	if home != filepath.Join(h.cfg.Homes, "agent-1") { t.Fatal(home) }
	for _, p := range []string{"README.md", "TASK.md", "inbox/processed", "agents"} {
		if _, err := os.Stat(filepath.Join(home, p)); err != nil { t.Fatal(p, err) }
	}
	h.drop(home, "task.md", "work")
	h.waitEnds(ctrl, 1) // journals are keyed by the control directory
	if tool := h.requests()[1].Messages[2].Content; !strings.Contains(tool, home) { t.Fatal(tool) }
	sys := h.cfg.System.(*fakeSystem)
	if !sys.called("members agent-1 "+h.mainUser()) || !sys.called("linger agent-1") { t.Fatal(sys.calls) }
}
func TestConfigErrorGoesToControlDir(t *testing.T) // child with model "nope": ctrl/ERROR has `config.json: unknown model "nope"`, parent gets one -subagent-error.md note, home/ERROR absent
func TestMaxAgentsUsesPool(t *testing.T)          // settings max_agents=2 → limit 1: first child admitted, second gets ctrl/ERROR "Agent limit reached" and one parent note; raising max_agents to 3 admits it and clears ERROR
func TestAdmissionClearsLeftoverHome(t *testing.T) // pre-create root/home/agent-1/stale.txt, then admit: stale.txt gone, README.md present
func TestGrandchildAdmitted(t *testing.T)          // ctrl "c" admitted; create childHome(c)/agents/g/config.json → g admitted as agent-2; fakeSystem has "members agent-2 agent-1,"+h.mainUser(); journal dir JournalDir(childHome(c)/agents/g) exists
func TestValidControlDir(t *testing.T)            // table: home/agents/x true; home/agents/x/y, home/agents/., home/agents/.., home/x false
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/daemon -run 'Admission|ControlDir|MaxAgentsUsesPool|Grandchild' -v`
Expected: FAIL (compile errors first).

- [ ] **Step 3: Implement** the agent tree, admission and path changes listed above. Update existing tests to the new child layout so the whole package compiles; delete `TestValidAgentDir`, `TestMaxAgents` and `TestLimitedAgentResumes` in favour of the new tests.

- [ ] **Step 4: Run the whole suite**

Run: `go vet ./... && go test ./...`
Expected: PASS except tests explicitly left for Task 5/6 (`TestRemovalStopsAgent` should still pass: removal still cancels; `TestParentChildReport` and `TestSubagentMessageLabelled` may be marked `t.Skip("Task 6")` — remove the skips in Task 6).

- [ ] **Step 5: Commit**

```bash
git add internal/daemon
git commit -m "Give each subagent a pool user, its own helper and a home"
```

---

### Task 5: Removal and reclaim

**Files:**
- Create: `internal/daemon/reclaim.go`
- Modify: `internal/daemon/daemon.go` (`reconcile`, `Run` startup)
- Test: `internal/daemon/reclaim_test.go`

**Interfaces:**
- Consumes: Task 3 `pool.mark/free/entries`, `System`; Task 1 `Clear`, `PurgeOwned`; Task 2 `journal.Secure`.
- Produces:
  - `(*daemon) remove(gone []*agent)` — sorts deepest first (depth = length of the parent chain), and for each: `cancel(errRemoved)`, wait on `done`, delete from `d.agents`, `d.reclaim(a.user, a.id)`.
  - `(*daemon) reclaim(user, agentID string) error` — in order: `pool.mark(user, "reclaiming")`; `DisableLinger`; `StopProcesses`; `RemoveCrontab`; start a helper as `user`, `Clear(PoolHome(user))`, `PurgeOwned(cfg.TmpDirs)`, close it; `SetMembers(user, nil)`; `journal.Secure(JournalDir(agentID), gid of the parent's user)` where the parent's user is `cfg.AgentUser` when `Dir(Dir(agentID)) == cfg.Home`, else `Base(Dir(Dir(agentID)))`; `pool.free(user)`. On any error: log, leave the entry `reclaiming`, return the error (retried next poll and on startup).
  - Startup: before the first `reconcile`, reclaim every `reclaiming` entry. After each `reconcile`, reclaim every `active` entry whose agent is not in `d.agents` (removed while the daemon was stopped). A user with an unfinished reclaim is never assigned.

- [ ] **Step 1: Write the failing tests**

```go
func TestRemovalReclaimsUser(t *testing.T) {
	h := newHarness(t)
	h.setScript(matchStep("inbox/processed/task.md", "touch "+filepath.Join(h.cfg.TmpDirs[0], "mine")+"; sleep 30", 100))
	ctrl := makeChild(t, h, "c", true)
	h.start()
	home := h.childHome(ctrl)
	h.drop(home, "task.md", "work")
	waitFor(t, "command running", func() bool { return len(h.requests()) >= 1 })
	os.RemoveAll(ctrl)
	if end := lastEnd(h.waitEnds(ctrl, 1)); end != "removed" { t.Fatal(end) }
	sys := h.cfg.System.(*fakeSystem)
	waitFor(t, "reclaim", func() bool { return sys.called("members agent-1 ") }) // cleared
	if entries, _ := os.ReadDir(home); len(entries) != 0 { t.Fatal(entries) }
	if _, err := os.Stat(filepath.Join(h.cfg.TmpDirs[0], "mine")); !os.IsNotExist(err) { t.Fatal("tmp file kept") }
	want := []string{"unlinger agent-1", "stop agent-1", "crontab agent-1", "members agent-1 "}
	if !sys.inOrder(want...) { t.Fatal(sys.calls) }
}
func TestRemovalTakesDescendants(t *testing.T)            // child c with grandchild g, both running `sleep 30`; remove ctrl c → both journals end "removed"; g's reclaim (agent-2) happens before c's (agent-1)
func TestReclaimingFinishedOnStart(t *testing.T)           // write pool.json [{"user":"agent-1","agent":"<ctrl>","state":"reclaiming"}] with a file in its home; start: home emptied, pool entry gone, a new child gets agent-1
func TestRemovedWhileStoppedIsReclaimed(t *testing.T)      // pool.json active entry for a ctrl that does not exist: after start, entry gone and fakeSystem shows its reclaim
func TestRenameControlDirIsRemoveAndAdmit(t *testing.T)    // admitted c (agent-1, home has notes.txt); rename ctrl c → d: c ends "removed"; d admitted with a fresh home (no notes.txt)
func TestWaitingChildAdmittedWhenUserFrees(t *testing.T)   // limit 1: a admitted, b waits (one ERROR, one note); remove a → b admitted, ctrl b/ERROR cleared
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/daemon -run 'Removal|Reclaim|Rename|WaitingChild' -v`
Expected: FAIL.

- [ ] **Step 3: Implement** `reclaim.go` and the reconcile/startup hooks. Extend `fakeSystem` with `inOrder(calls ...string) bool`.

- [ ] **Step 4: Run**

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon
git commit -m "Reclaim a subagent's user when its control directory goes"
```

---

### Task 6: Messages between owners and children

**Files:**
- Modify: `internal/daemon/messages.go`, `owner.go` (delivery records, owner spool), `inbox.go` (`label`)
- Test: `internal/daemon/messages_test.go`, `owner_test.go`, `agents_test.go` (`TestParentChildReport`)

**Interfaces:**
- Consumes: Task 4 agent tree, `writeNote`; Task 1 `Entry.UID`; Task 3 `ConversationFile`.
- Produces:
  - `deliveryRecord` gains `From string \`json:"from,omitempty"\``; loading a record without `From` means `"owner"`. `(*delivered) add(agent, name, sum, from string) error`; `(*delivered) take(agent, name, sum string) string` (`""` when not delivered). `label` returns `take(...)` or `"data"`.
  - `(*daemon) deliver(to *agent, name string, data []byte, from string) error` — `delivered.add(to.id, name, sha256, from)`, then `to.fs.Write(to.home/inbox/name, data)`.
  - `(*daemon) appendConversation(path string, gid int, record map[string]any) error` — adds `"t"`, appends one line, file mode `0640`, `Chown(-1, gid)` when `gid >= 0`; replaces `appendMessage`.
  - `(*daemon) collectMessages()` — for every admitted agent `a` (snapshot under `d.mu`):
    - up spool `a.home/.messages`: regular, non-dot files with `UID == a.uid()`, name order, read ≤ 64 KiB (append `"\n[message truncated]"` when larger). Main: `appendConversation(MessagesFile, main.gid(), {"from":"agent","agent":"main","text"})`. Subagent: `deliver(a.parent, noteName(base(a.ctrl), time.Now()), data, "agents/"+base(a.ctrl))` then `appendConversation(ConversationFile(a.id), a.gid(), {"from":"agent","text"})`. Then remove the spool file.
    - down spool `a.home/.messages/agents/<NAME>/`: only directories owned by `a.uid()` whose `NAME` passes `validControlDir(a.home, a.home/agents/NAME)`. Target admitted → `deliver(target, noteName("owner", time.Now()), data, "owner")`, `appendConversation(ConversationFile(target.id), target.gid(), {"from":"owner","text"})`, remove. Target not admitted but `a.home/agents/NAME/config.json` exists → leave queued. Otherwise → remove the file and `writeNote(a, "undelivered", fmt.Sprintf(MsgUndeliveredFmt, NAME))` with `MsgUndeliveredFmt = "Message to %s dropped: there is no subagent by that name."`.
    - files not owned by the agent: logged once per path, left in place.
  - `deliverOwner` uses `deliver(main, noteName("owner", time.Now()), data, "owner")` and `appendConversation(MessagesFile, main.gid(), {"from":"owner","text"})`.

- [ ] **Step 1: Write the failing tests**

```go
func TestChildMessageReachesParent(t *testing.T) {
	h := newHarness(t, matchStep("inbox/processed/", sendMessageText("report: hi"), 100), `{"match":"inbox/processed/","text":"done"}`, done(), done())
	ctrl := makeChild(t, h, "research", true)
	h.start()
	h.spoolDown(h.cfg.Home, "research", "child-task") // writes main home/.messages/agents/research/<UTC>.md
	waitFor(t, "parent gets labelled report", func() bool { return h.sawRecord(`"from":"agents/research"`, "report: hi") })
	if !h.sawRecord(`"from":"owner"`, "child-task") { t.Fatal("child task not labelled owner") }
	log := readRecords(h.cfg.ConversationFile(ctrl))
	// expect {"from":"owner","text":"child-task"} then {"from":"agent","text":"report: hi"}
	if len(log) != 2 || log[0]["from"] != "owner" || log[1]["from"] != "agent" { t.Fatal(log) }
	if hasMessage(h, "agent", "research", "report: hi") { t.Fatal("subagent reached the human's log") }
	if !h.sawRecord(`-research.md"`, `"from":"agents/research"`) { t.Fatal("report file not named <UTC>-research.md") }
}
func TestDownMessageWaitsForAdmission(t *testing.T)   // spool to "late" before ctrl exists with config → stays queued; create ctrl/config.json → delivered, from owner
func TestDownMessageToMissingChildDropped(t *testing.T) // spool to "ghost" with no ctrl → spool file gone, main inbox gets a -undelivered.md note with MsgUndeliveredFmt text
func TestNestedDownSpoolIgnored(t *testing.T)          // .messages/agents/c/agents/x/<UTC>.md and .messages/agents/../evil.md → not delivered anywhere
func TestOldDeliveryRecordMeansOwner(t *testing.T)     // delivered.jsonl line without "from" → label "owner"
func TestMainLogMode(t *testing.T)                     // after an owner message: MessagesFile perm 0640 (replaces the 0644 assertion in TestAgentMessageLogged)
func TestChildMessageTruncated(t *testing.T)           // 70 KiB up message → parent inbox file is 64 KiB + "\n[message truncated]"
```
Add harness helpers `spoolDown(fromHome, name, text string)`, `sawRecord(fragments ...string) bool` (any request message content containing all fragments) and `sendMessageText(text string) string` (the shell command writing `~/.messages/<UTC>.md` atomically).

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/daemon -run 'Message|Owner|ParentChild' -v`
Expected: FAIL.

- [ ] **Step 3: Implement**; rewrite `TestParentChildReport` with `spoolDown` + `sendMessageText`; delete `TestSubagentMessageLabelled` (subagents no longer reach the human's log).

- [ ] **Step 4: Run**

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon
git commit -m "Route messages between each agent and its owner with verified senders"
```

---

### Task 7: OWNER.md

**Files:**
- Create: `internal/daemon/ownermd.go`
- Modify: `internal/daemon/run.go` (`run`, `steps`), `conversation.go`, `messages.go` (constants)
- Test: `internal/daemon/ownermd_test.go`

**Interfaces:**
- Consumes: Task 4 `ctl()`, `uid()`, `parent`; Task 3 `cfg.OwnerFile()`, `cfg.OwnerUID`.
- Produces:
  - `const OwnerHeader = "# Owner's instructions\n\nYour owner wrote the following. You cannot change it, and agents you\ncreate do not see it. It takes precedence over everything else in your\ncontext."`
  - `const MsgOwnerPrefix = "OWNER.md: "`
  - `ownerSegment(text string) string` — `""` when `strings.TrimSpace(text) == ""`, else `"\n\n" + OwnerHeader + "\n\n" + text`.
  - `(*agent) readOwner() (string, error)` — missing file → `"", nil`. Main: as root, `Lstat(cfg.Etc)` must be a directory owned by `cfg.OwnerUID` with `perm&0o022 == 0`; open `cfg.OwnerFile()` with `O_RDONLY|O_NOFOLLOW|O_NONBLOCK`, `Fstat` regular, owned by `cfg.OwnerUID`, `perm&0o022 == 0`; read ≤ `segmentLimit`. Subagent: through `a.parent.fs`: `Stat(a.ctrl)` dir with `UID == a.parent.uid()` and `Mode&0o022 == 0`; `Stat(ctrl/OWNER.md)` type `file` (a symlink reports `symlink`) with the same owner/mode rule; `Read`. Errors name the reason (`"is a symlink"`, `"is not owned by its owner"`, `"is writable by group or others"`).
  - `run`: after `gate`, call `readOwner`; on error: if `err.Error() != a.failedOwner` log it and `setError(MsgOwnerPrefix+err.Error())`, set `failedOwner`, return without touching the inbox. On success with `failedOwner != ""`: clear it and `clearError(MsgOwnerPrefix)`.
  - `steps(ctx, j, cfg, contextTokens, owner string)`; fresh `system[0].Text = SystemPrompt + ownerSegment(owner)`. `conversation` gains `owner string`; `takeConversation(cfg, owner)` continues only when `c.owner == owner`.

- [ ] **Step 1: Write the failing tests**

```go
func TestOwnerSectionForMain(t *testing.T) {
	h := newHarness(t, done())
	os.WriteFile(h.cfg.OwnerFile(), []byte("Be brief."), 0o644)
	h.drop(h.cfg.Home, "go.md", "go")
	h.start()
	h.waitEnds(h.cfg.Home, 1)
	want := SystemPrompt + "\n\n" + OwnerHeader + "\n\nBe brief."
	if got := h.requests()[0].System[0].Text; got != want { t.Fatalf("%q", got) }
}
func TestNoOwnerFileKeepsPrompt(t *testing.T)       // missing and whitespace-only file: System[0].Text == SystemPrompt exactly
func TestOwnerSectionForChild(t *testing.T)         // ctrl/OWNER.md "Child rules." → child's System[0] ends with "Child rules."; main's System[0] == SystemPrompt + main's section (not child's)
func TestParentOwnerNotInherited(t *testing.T)      // main OWNER.md "MAIN-ONLY" → no child request contains "MAIN-ONLY"
func TestUnsafeOwnerStopsRun(t *testing.T)          // three subtests: symlink, chmod 0664, (child) ctrl dir chmod 0777 → ERROR starts "OWNER.md: ", inbox untouched, 0 requests, logged once; fixing the file → run happens, ERROR cleared
func TestOwnerEditStartsFresh(t *testing.T)         // run ends; edit OWNER.md; new message within ContinueWindow → request's first message is the new inbox only (fresh), System[0] has the new text
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/daemon -run Owner -v`
Expected: FAIL.

- [ ] **Step 3: Implement** `ownermd.go` and the `run`/`steps`/`conversation` changes.

- [ ] **Step 4: Run**

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon
git commit -m "Load each agent's OWNER.md after the system prompt"
```

---

### Task 8: CLI: send to a child, conversations and logs per caller

**Files:**
- Modify: `internal/cli/send.go`, `internal/cli/message.go`, `internal/cli/log.go`, `cmd/agent/main.go` (usage and dispatch)
- Test: `internal/cli/message_test.go`, `internal/cli/cli_test.go`

**Interfaces:**
- Consumes: Task 3 `Layout.ConversationFile`; Task 2 `journal.IsRun`.
- Produces:
  - `cli.SendChild(home, name, text string, now time.Time) error` — `name` must be one clean component (no `/`, not `.`/`..`, non-empty); `home/agents/name/config.json` must exist (`"no subagent %q (expected ~/agents/%s/config.json)"`); empty and > `MessageLimit` rejected as in `Message`; writes `home/.messages/agents/name/<UTC>.md` via dot-name + rename.
  - `cli.Target(l layout.Layout, root bool, home, name string) (journalDir, errorFile, conversation string, err error)`:
    - `name == ""`: root or `home == l.Home` → main: `l.JournalDir(l.Home)`, `l.Home/ERROR`, `l.MessagesFile()`. Otherwise (a subagent asking about itself): `journalDir = EvalSymlinks(home/journal)`, `errorFile = ""` (its ERROR is in its parent's control dir), `conversation = journalDir/messages.jsonl`.
    - `name != ""`: base is `l.Home` for root, else `home`. Split `name` on `"/agents/"`; walk: `ctrl = dir/agents/<part>`; between parts `dir = EvalSymlinks(ctrl/home)`. The final `ctrl` gives `l.JournalDir(ctrl)`, `ctrl/ERROR`, `l.ConversationFile(ctrl)`.
  - `cli.Log(ctx, journalDir, errorFile string, w io.Writer) error` (empty `errorFile` skips the ERROR line); `cli.Messages(ctx, path string, w io.Writer) error`.
  - `main.go`: `agent send "text"` → `cli.Send` (root only, as today). `agent send NAME "text"`: as non-root → `cli.SendChild(os.Getenv("HOME"), ...)`, prints `Queued for NAME.`; as root → error `the human sends only to the main agent: agent send "text"`. Non-root `agent send "text"` → error `agent send needs root; agents use agent send NAME "text"`. `messages` and `log` take an optional `NAME` and go through `Target`. Usage text lists `agent send NAME "text"` as `(agents) send a message to your subagent NAME`.

- [ ] **Step 1: Write the failing tests**

```go
func TestSendChildWritesDownSpool(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "agents", "research"), 0o755)
	os.WriteFile(filepath.Join(home, "agents", "research", "config.json"), []byte("{}"), 0o644)
	if err := SendChild(home, "research", "go", time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)); err != nil { t.Fatal(err) }
	data, err := os.ReadFile(filepath.Join(home, ".messages", "agents", "research", "20261012T090000.000000000Z.md"))
	if err != nil || string(data) != "go" { t.Fatal(string(data), err) }
}
func TestSendChildRejectsNestedName(t *testing.T) // "research/agents/x", "..", "", "a/b" → errors; missing config.json → error naming the expected path
func TestTargetMainAndChild(t *testing.T)          // temp layout: root, name "" → main paths; root, name "research" → JournalDir(home/agents/research), its ERROR, ConversationFile;
                                                   // name "research/agents/x" with home/agents/research/home → poolhome symlink → ctrl poolhome/agents/x
func TestTargetSubagentSelf(t *testing.T)          // home ≠ l.Home with home/journal → jdir symlink: journalDir == jdir, errorFile == "", conversation == jdir/messages.jsonl
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/cli -v`
Expected: FAIL.

- [ ] **Step 3: Implement**; remove `AgentDir`.

- [ ] **Step 4: Run**

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli cmd/agent/main.go
git commit -m "Let agents message their subagents and read their journals and conversations"
```

---

### Task 9: Root privilege tests and CI users

**Files:**
- Modify: `internal/daemon/privilege_test.go`, `.github/workflows/ci.yml`
- Test: `internal/daemon/privilege_test.go`

**Interfaces:**
- Consumes: everything above; Task 3 `newExecSystem()`.
- Produces:
  - `rootHarness` additionally: pool users from `AGENT_TEST_POOL` (default `agent-test-1,agent-test-2,agent-test-3`); `LookupUser = osuser.Lookup`; `System = newExecSystem()`; `Layout.Homes = root/home` with each pool home created root-owned via the real `PrepareHome`; `OwnerUID = 0`; `TmpDirs = []string{root/tmp}` (mode 1777).
  - CI step creates `agent-test-1..3` with `sudo useradd --system --no-create-home --user-group agent-test-N` and runs `sudo AGENT_ROOT_TESTS=1 "$(which go)" test ./internal/daemon -run Privilege -v`; add `pkill`/`pgrep` (procps) presence check.

- [ ] **Step 1: Write the tests**

```go
func TestPrivilegeSubagentIsolation(t *testing.T) // main's test user creates ctrl a and b (owned by main user); a runs:
	// "touch $MAIN/x; echo rc1=$?; echo hi > <ctrl a>/OWNER.md; echo rc2=$?; cat <home b>/TASK.md; echo rc3=$?"
	// expect rc1, rc2, rc3 all non-zero; main runs "cat agents/a/home/TASK.md; echo rc=$?; cat <JournalDir(a)>/*.jsonl >/dev/null; echo jrc=$?" → rc=0, jrc=0;
	// b runs "cat <JournalDir(a)>/*.jsonl; echo jrc=$?" → non-zero
func TestPrivilegeReclaimKillsEverything(t *testing.T) // child runs "setsid sleep 300 </dev/null >/dev/null 2>&1 & echo '* * * * * true' | crontab -"; remove ctrl;
	// after reclaim: `pgrep -u agent-test-1` exits 1, `crontab -l -u agent-test-1` fails, home empty, `getent group agent-test-1` has no members
func TestPrivilegeReusedUserCannotReadOldJournals(t *testing.T) // child a (agent-test-1) runs once, removed; child b gets agent-test-1; b runs "cat <JournalDir(a)>/*.jsonl; echo rc=$?" → non-zero
func TestPrivilegeSpoolFileNotOwnedIgnored(t *testing.T) // root writes a file into child home/.messages/: never delivered to main, still present
func TestPrivilegeChildOwnerMd(t *testing.T) // ctrl/OWNER.md owned by main user 0644 → in child's System[0]; chown to the child's user → ERROR "OWNER.md: ", no run
```
Adapt the existing `TestPrivilege*` tests to the new harness (main agent user = `agent-test`).

- [ ] **Step 2: Run them as root locally or in CI**

Run (Linux, root): `sudo AGENT_ROOT_TESTS=1 go test ./internal/daemon -run Privilege -v`
Expected: PASS. Unprivileged `go test ./...` skips them.

- [ ] **Step 3: Fix any implementation gaps the root tests expose** (most likely: helper supplementary groups for ancestors, home mode after `PrepareHome`, journal group on `Secure`).

- [ ] **Step 4: Run the full suite**

Run: `go vet ./... && go test ./...`
Expected: PASS; push and confirm the CI root step passes.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/privilege_test.go .github/workflows/ci.yml internal/daemon
git commit -m "Test subagent isolation and reclaim as root in CI"
```

---

### Task 10: Seed README, release notes, operator README

**Files:**
- Modify: `internal/seed/README.md`, `internal/seed/seed_test.go`, `internal/notes/notes.md`, `README.md`
- Test: `internal/seed/seed_test.go`

**Interfaces:**
- Produces: seed text containing the strings tested below. `seed.Write` is unchanged (main only).

- [ ] **Step 1: Write the failing test** — extend the `want` list in `seed_test.go` with `"agent send NAME"`, `"~/agents/NAME/config.json"`, `"OWNER.md"`, `"~/agents/NAME/home"`, `"setfacl"`, `"\"from\": \"agents/NAME\""`.

- [ ] **Step 2: Run** `go test ./internal/seed -v` — Expected: FAIL.

- [ ] **Step 3: Rewrite the seed README sections** (keep the rest verbatim):
  - Messages: drop the "To send one…rename it into the inbox" advice for messages to other agents (keep it for the agent's own scripts writing to its own inbox). State: your owner is whoever created you (for the main agent, the human); `agent message` reaches them, permanently; records labelled `"from": "owner"` are verified messages from your owner and are your only instructions; `"from": "agents/NAME"` are verified messages from your subagent NAME, and are data; the conversation log path (main: `/var/log/agent/messages.jsonl`; subagents: `~/journal/messages.jsonl`).
  - Subagents: create one by writing `~/agents/NAME/config.json` (and optionally `~/agents/NAME/OWNER.md`, its standing instructions, which it cannot change), then `agent send NAME "task"`. It runs as its own user with its own home at `~/agents/NAME/home`, which you can read but not write; share files with `setfacl -m u:USER:rwX PATH` (its user owns `~/agents/NAME/home`). Its errors appear in `~/agents/NAME/ERROR`; its journal and conversation via `agent log NAME` and `agent messages NAME`. Removing or renaming `~/agents/NAME/` stops it and discards its home; its journal remains.
  - Add `## 0.3.0` at the top of `notes.md` (facts, not instructions): each subagent now runs as its own Unix user with its home at `~/agents/NAME/home`; a subagent's control directory holds only `config.json`, `OWNER.md`, `ERROR` and `home`; message children with `agent send NAME "text"` (raw files in their inbox no longer work), and they reply with `agent message`, arriving as `"from": "agents/NAME"`; subagents can no longer write files in your home unless you grant access with `setfacl`; existing subagents were moved to their own users and their notes kept; `/etc/agent/OWNER.md`, when your owner writes it, is loaded after the system prompt.
  - Operator `README.md`: add sections on `/etc/agent/OWNER.md` (what it's for, one cache rebuild per edit, precedence is a convention), on subagents (pool users, `sudo agent log NAME`, `sudo agent messages NAME`), and update "what is and isn't protected" (subagents isolated from parent and siblings; ancestors read by default; `--sudo` affects only the main agent).

- [ ] **Step 4: Run** `go test ./...` — Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/seed internal/notes/notes.md README.md
git commit -m "Describe subagent users and owner messaging to agents and operators"
```

---

### Task 11: Installer: pool users and migration

**Files:**
- Create: `internal/daemon/migrate.go`
- Modify: `cmd/agent/main.go` (hidden `_migrate` subcommand), `install.sh`, `internal/cli/install_test.go` (if it inspects `install.sh`)
- Test: `internal/daemon/migrate_test.go`

**Interfaces:**
- Consumes: Task 3 `pool`, `Layout.PoolHome`, `Config.LookupUser`; Task 2 `journal.Secure`.
- Produces:
  - `daemon.Migrate(cfg Config) error` (run as root with the daemon stopped). For each legacy subagent directory — a real directory `<home>/agents/<name>` containing a real directory `inbox/` — breadth-first starting from `cfg.Home`, parents before children:
    1. refuse (return an error, change nothing further) if any path component under `cfg.Home` is a symlink;
    2. `user := pool.assign(ctrl, len(users))`; error if none;
    3. the pool home must be empty (error otherwise);
    4. `os.Rename` every entry of the legacy dir except `config.json` into the pool home;
    5. walk the pool home with `filepath.WalkDir` (no symlink following) and `Lchown` entries owned by the main agent's uid to the pool user's uid/gid;
    6. if `ctrl` differs from the old id (nested legacy dirs, whose old id was `<old parent dir>/agents/<name>`), `os.Rename` the old journal directory to `JournalDir(ctrl)`;
    7. queue `<pool home>/agents/*` for the same treatment.
    A legacy dir with no `inbox/` is left alone (e.g. the VM's `recipes-wt`). Running `Migrate` twice is a no-op.
  - `agent _migrate`: root only; `daemon.Migrate(daemon.DefaultConfig(self))`; prints one line per migrated subagent (`migrated agents/recipes to agent-1`).
  - `install.sh`: add `gpasswd pkill pgrep` to the tool check; after the settings file exists, read `max_agents` (`sed -n 's/.*"max_agents": *\([0-9]*\).*/\1/p'`, default 16) and for `i` in `1..max_agents-1`: `id agent-$i >/dev/null 2>&1 || useradd --create-home --home-dir /home/agent-$i --user-group --shell /bin/bash agent-$i`, then `chown agent-$i:agent-$i /home/agent-$i && chmod 2750 /home/agent-$i`; `chmod 750 /home/agent`. On upgrade, after stopping the daemon: if any `/home/agent/agents/*/inbox` exists, `tar -C /home/agent -czf /var/backups/agent-subagents-$(date -u +%Y%m%dT%H%M%SZ).tgz agents && chmod 600` that file, then `/usr/local/bin/agent _migrate`; a failure aborts the install (existing rollback path).

- [ ] **Step 1: Write the failing tests**

```go
func TestMigrateMovesLegacySubagent(t *testing.T) {
	h := newHarness(t)
	legacy := filepath.Join(h.cfg.Home, "agents", "recipes")
	os.MkdirAll(filepath.Join(legacy, "inbox", "processed"), 0o755)
	os.WriteFile(filepath.Join(legacy, "config.json"), []byte(`{"provider":"replay","model":"replay-1"}`), 0o644)
	os.WriteFile(filepath.Join(legacy, "TASK.md"), []byte("keep me"), 0o644)
	os.WriteFile(filepath.Join(legacy, "README.md"), []byte("my readme"), 0o644)
	os.MkdirAll(filepath.Join(h.cfg.Home, "agents", "recipes-wt"), 0o755) // no inbox: untouched
	if err := Migrate(h.cfg); err != nil { t.Fatal(err) }
	home := h.cfg.PoolHome("agent-1")
	if data, _ := os.ReadFile(filepath.Join(home, "TASK.md")); string(data) != "keep me" { t.Fatal(string(data)) }
	if entries, _ := os.ReadDir(legacy); len(entries) != 1 || entries[0].Name() != "config.json" { t.Fatal(entries) }
	p, _ := loadPool(h.cfg.PoolFile(), h.cfg.PoolUsers)
	if p.userOf(legacy) != "agent-1" { t.Fatal(p.entries()) }
	if err := Migrate(h.cfg); err != nil { t.Fatal("second run:", err) }
	// then start the daemon: README.md in home is the legacy one (not reseeded), ctrl/home symlink appears
}
func TestMigrateNestedMovesJournals(t *testing.T) // legacy recipes/agents/scraper with inbox and a journal at JournalDir(home/agents/recipes/agents/scraper):
	// after Migrate: scraper's files in agent-2's home, journal dir at JournalDir(poolhome(agent-1)/agents/scraper)
func TestMigrateRefusesSymlinkedAgents(t *testing.T) // main home/agents is a symlink → error, nothing moved
func TestMigrateRefusesNonEmptyPoolHome(t *testing.T) // agent-1 home has a file → error, legacy dir intact
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/daemon -run Migrate -v`
Expected: FAIL.

- [ ] **Step 3: Implement** `migrate.go`, `_migrate` and the `install.sh` changes. Check `install.sh` with `sh -n install.sh`.

- [ ] **Step 4: Run**

Run: `go vet ./... && go test ./... && sh -n install.sh && sh scripts/release_test.sh`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/migrate.go internal/daemon/migrate_test.go cmd/agent/main.go install.sh internal/cli
git commit -m "Create pool users at install and migrate existing subagents"
```

---

## After the plan

Releasing 0.3.0 and deploying to the VM follow `AGENTS.md` (release script, tag, `install.sh` on host `bot`, read-only health checks). The live tree has two subagents, `clinics` and `recipes`; check their migration with read-only commands (`sudo cat /var/lib/agent/pool.json`, `ls -la /home/agent/agents/*/`, `systemctl is-active agent`, `journalctl -u agent`). Do not message the live agent; the human decides whether to tell main anything beyond the release notes.
