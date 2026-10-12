# Recursive Agents Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make root the top of the agent tree so the human and every agent create, message, watch and remove agents with the same commands, and migrate the live main agent to an ordinary pool user (release 0.4.0).

**Architecture:** The daemon gains a root node (home `/etc/agent`, a helper running as the daemon's own user, no loop); the main agent becomes root's child `main`, admitted from `/etc/agent/agents/main/` like any subagent. Agent ids become paths from root (`main/agents/x`), so journals and conversations live at `/var/log/agent/<id>/`. Each agent owns `~/config.json`; `ERROR` and `OWNER.md` stay in the parent's control directory. A new `agent _migrate` renames the `agent` user to a pool name and restructures state.

**Tech Stack:** Go standard library only; POSIX sh (`install.sh`); systemd, shadow-utils (`usermod`, `groupmod`).

**Spec:** `docs/superpowers/specs/2026-10-12-recursive-agents-design.md`

## Global Constraints

- Go standard library only. `go vet ./...` and `go test ./...` pass after every task; privilege tests run with `sudo AGENT_ROOT_TESTS=1 go test ./internal/daemon -run Privilege`.
- The system prompt (`SystemPrompt`), the `sh` tool definition and how plugins build requests do not change.
- Pool: `max_agents` users `agent-1` … `agent-<max_agents>` (default `max_agents` 16). No `agent` user after migration.
- Root's control directories: `/etc/agent/agents/NAME/` (root, 0700). Root's spool: `/etc/agent/.messages/agents/NAME/`.
- Journals: `/var/log/agent/<id>/`; conversation: `/var/log/agent/<id>/messages.jsonl` for every agent.
- `agent new` waits up to 10 seconds for admission.
- Release notes in `internal/notes/notes.md` are facts, not instructions.
- Work on `main`; commit after each task and push (`git push`).
- Never send messages to the live agent; VM checks are read-only.

## Review Focus

- **Daemon restart after admission:** an admitted child's control directory has no `config.json` any more; after a restart the child must be found again (pool record + `home` link) and keep its home. Test in Task 2.
- **`agent send` to an admitted child:** its control directory no longer holds `config.json`; `SendChild` must accept a control directory that is a directory, not require `config.json`. Test in Task 5.
- **A `config.json` written into an admitted child's control directory:** it must not re-admit, overwrite the child's `~/config.json` or wipe its home. Test in Task 3.
- **An agent asking about itself:** `agent messages` / `agent log` with no NAME, run by a subagent, must resolve its relative id from `~/journal`. Test in Task 5.
- **Migration interrupted after the user rename:** a rerun must find `agent-M` from the marker, not pick a new free name. Test in Task 7.

---

### Task 1: Scan finds admitted control directories

**Files:**
- Modify: `internal/fsio/server.go:204-227` (`scan`)
- Test: `internal/fsio/fsio_test.go`

**Interfaces:**
- Produces: `Client.Scan(home string) ([]string, error)` now also returns owned real directories under `<home>/agents/` that hold a `home` symlink (no `config.json` needed). Signature unchanged.

- [ ] **Step 1: Write the failing test**

```go
func TestScanFindsAdmittedControlDirs(t *testing.T) {
	// agents/a: config.json; agents/b: home symlink only; agents/c: neither.
	// Expect [a, b].
}
```

- [ ] **Step 2: Run** `go test ./internal/fsio -run TestScan` — expect FAIL (b missing).
- [ ] **Step 3: Implement**: in `scan`, accept a directory when `config.json` is a regular file or `home` is a symlink (`os.Lstat`, `ModeSymlink`).
- [ ] **Step 4: Run** `go test ./internal/fsio` — expect PASS.
- [ ] **Step 5: Commit** `git commit -m "Scan finds admitted control directories"`

---

### Task 2: Root is the top of the tree

The largest task: `main` stops being special. Config stays in the control directory in this task (Task 3 moves it), so existing child behaviour carries over to `main` unchanged.

**Files:**
- Modify: `internal/layout/layout.go`, `internal/layout/layout_test.go`
- Modify: `internal/daemon/daemon.go`, `agent.go`, `admit.go`, `conversation.go`, `messages.go`(constants only if needed), `owner.go`, `ownermd.go`, `reclaim.go`, `releasenotes.go` (stop calling it from `Run`; Task 4 replaces it)
- Modify tests: `internal/daemon/harness_test.go` and every daemon test that uses `cfg.Home`, `OwnerSpool`, `MessagesFile`, `OwnerFile`, `OwnerUID`, `AgentUser` or absolute agent ids
- Modify: `cmd/agent/main.go` only as needed to compile (`AgentUser`, `OwnerSpool` users)

**Interfaces:**
- Produces (layout):
  - Remove `Layout.Home`, `OwnerSpool()`, `OwnerFile()`, `MessagesFile()`.
  - `func (l Layout) RootHome() string` → `l.Etc`.
  - `func (l Layout) ConversationFile(id string) string` → `filepath.Join(l.JournalDir(id), "messages.jsonl")` for every id.
  - `JournalDir(id)` unchanged in code; ids are now relative (`main`, `main/agents/x`).
  - `func PoolUsers(maxAgents int) []string` → `agent-1` … `agent-<maxAgents>`.
- Produces (daemon):
  - `Config`: remove `AgentUser`, `OwnerUID`; add `RootUser string` (default `"root"`; tests `""`, which `LookupUser` resolves to the current user).
  - `daemon.root *agent`: `id ""`, `home cfg.RootHome()`, `user cfg.RootUser`, `fs` a helper started with root's credential, no loop, never in `d.agents`, never removed. `daemon.main` is deleted.
  - `func (d *daemon) childID(parent *agent, ctrl string) string`: `filepath.Base(ctrl)` when `parent == d.root`, else `parent.id + "/agents/" + filepath.Base(ctrl)`. `agent.name` equals `agent.id`.
  - `func parentID(id string) string`: `""` for a top-level id (no `/agents/`).
  - `ancestorGid(id)` for a top-level id returns root's gid (`d.root.gid()`; 0 in production), so a removed top-level agent's journals become readable by root only.
  - Pool limit: `d.pool.assign(id, min(len(d.pool.users), d.settings.MaxAgents))`.
  - `admit`: ancestors for `SetMembers` stop before `d.root`.
  - `readOwner`: always `readChildOwner(a.parent.fs, a.parent.uid(), filepath.Join(a.ctrl, "OWNER.md"))`; delete `readMainOwner`, `checkMainOwner`.
  - `collectUp`: when `a.parent == d.root`, append `{"from":"agent","text":…}` to `ConversationFile(a.id)` and deliver nothing.
  - `collectMessages`: also collects `d.root`'s down spool (`RootHome()/.messages/agents/NAME`) with root's uid; root has no up spool.
  - `deliverOwner`, `ownerSpoolAgent`, `secureOldLogs`' messages-file branch: deleted. `secureUnheldJournals` starts from the top-level journal directories.
  - Parent notifications (`writeNote(a.parent, …)` in `gate`, `holdBack`, undelivered notes): skipped when the parent is `d.root` (service log only).
- Produces (test harness): `harness.mainHome string` (= `PoolHome("agent-1")`), `harness.mainID = "main"`; `newHarness` writes `Etc/agents/main/config.json` and seeds nothing in a home itself; `cfg.PoolUsers` = `agent-1` … `agent-4`; `h.sendOwner(text)` spools into `RootHome()/.messages/agents/main/`.

- [ ] **Step 1: Write the failing tests** in `internal/daemon/agents_test.go` and `internal/layout/layout_test.go`:

```go
func TestPoolUsersCoverMaxAgents(t *testing.T) // PoolUsers(3) == [agent-1 agent-2 agent-3]
func TestRootChildAdmitted(t *testing.T) {
	// Etc/agents/main/config.json → admitted as agent-1, home PoolHome("agent-1"),
	// journal under Log/main, Etc/agents/main/home links to it.
}
func TestTwoTopLevelAgentsIsolated(t *testing.T) {
	// main and research under Etc/agents: different users, journals Log/main
	// and Log/research; SetMembers for each lists no ancestors.
}
func TestOwnerMessageFromRoot(t *testing.T) {
	// h.sendOwner("hi") reaches main's inbox labelled "owner" and
	// Log/main/messages.jsonl gets {"from":"owner","text":"hi"}.
}
func TestTopLevelMessageLoggedOnly(t *testing.T) {
	// main runs `agent message`-style spool; Log/main/messages.jsonl gets
	// {"from":"agent"}; no inbox file appears anywhere.
}
func TestRestartKeepsAdmittedChild(t *testing.T) {
	// Admit main, stop daemon, remove Etc/agents/main/config.json (as Task 3 will),
	// start again: main is admitted again as agent-1 and its home keeps a marker file.
}
```

- [ ] **Step 2: Run** `go test ./internal/layout ./internal/daemon` — expect build failures, then FAIL.
- [ ] **Step 3: Implement the layout changes** listed above.
- [ ] **Step 4: Implement the daemon root node and id scheme** listed above; `reconcile` walks breadth-first from `d.root` (root's scan runs through `d.root.fs`).
- [ ] **Step 5: Port the existing daemon tests** to the harness fields above. Delete tests that only covered removed behaviour: owner spool (`TestOwner*` that use `OwnerSpool`), `/etc/agent/OWNER.md` for main, `secureOldLogs` messages-file tightening, `AgentUser`. Keep every subagent test, re-expressed with `main` as a child.
- [ ] **Step 6: Run** `go vet ./... && go test ./...` — expect PASS.
- [ ] **Step 7: Run privilege tests as root** (Linux): `sudo AGENT_ROOT_TESTS=1 go test ./internal/daemon -run Privilege -v`; update `privilege_test.go` so `agent-test` is no longer the main user: main is admitted to `agent-test-1` from a root control directory. Expect PASS.
- [ ] **Step 8: Commit** `git commit -m "Make root the top of the agent tree"`

---

### Task 3: Each agent owns its config

**Files:**
- Modify: `internal/daemon/admit.go`, `agent.go` (replace `ctl()`), `run.go` (`gate`, `setError`, `ensureError`, `clearError`), `daemon.go` (`reconcile` admission rule)
- Test: `internal/daemon/agents_test.go`

**Interfaces:**
- Consumes: `Client.Scan` from Task 1; root node from Task 2.
- Produces:
  - `func (a *agent) errorFile() (*fsio.Client, string)` → `a.parent.fs, filepath.Join(a.ctrl, "ERROR")` for every agent (replaces `ctl()`).
  - `gate` reads `filepath.Join(a.home, "config.json")` through `a.fs`.
  - `func (c *agent) adoptConfig() error` (called in `admit` after `seed`): if `home/config.json` is missing, copy `ctrl/config.json` (read via `parent.fs`, 1 MiB limit) to `home/config.json` via `c.fs.Write`; then remove `ctrl/config.json` via `parent.fs`. Missing ctrl config with a present home config is success.
  - `reconcile`: a scanned control directory that is not a known agent is admitted only if it holds a regular `config.json` or `d.pool.userOf(id) != ""`.

- [ ] **Step 1: Write the failing tests**

```go
func TestAdmissionMovesConfigIntoHome(t *testing.T) {
	// After admission: home/config.json == original bytes; ctrl/config.json gone.
}
func TestChildChangesOwnConfig(t *testing.T) {
	// main writes {"provider":"replay","model":"replay-1","options":{"style":"b"}}
	// to ~/config.json; the next run's request carries options.style == "b"
	// and does not continue the previous conversation.
}
func TestInvalidOwnConfigReportsInControlDir(t *testing.T) {
	// A child of main writes an unknown model to its ~/config.json:
	// main/agents/x ERROR starts with "config.json: "; main's inbox gets one
	// subagent-error note; fixing the file clears ERROR.
}
func TestStrayConfigInAdmittedControlDirIgnored(t *testing.T) {
	// After admission, parent writes ctrl/config.json with another model:
	// the child keeps its user and home, its ~/config.json is unchanged.
}
```

- [ ] **Step 2: Run** `go test ./internal/daemon -run 'Config|Admission'` — expect FAIL.
- [ ] **Step 3: Implement** the interfaces above. Remove the "moving config.json away pauses the child" behaviour and its test (`TestChildWithoutConfigWaits` keeps only the not-yet-admitted case).
- [ ] **Step 4: Run** `go vet ./... && go test ./...` — expect PASS.
- [ ] **Step 5: Commit** `git commit -m "Each agent owns its config.json"`

---

### Task 4: Release notes ride along with the next wake

**Files:**
- Modify: `internal/daemon/pool.go`, `releasenotes.go`, `run.go`, `admit.go`, `daemon.go` (drop `deliverReleaseNotes` call)
- Test: `internal/daemon/notes_test.go` (rewrite), `pool_test.go`

**Interfaces:**
- Produces:
  - `poolEntry.Notes string` with tag `json:"notes,omitempty"`.
  - `func (p *pool) setNotes(user, version string) error` (persisted like other changes).
  - `func notesSince(all string, seen, current [3]int) []string` — the `## X.Y.Z` sections with `seen < v <= current`, each prefixed `"## "`, in file order.
  - `func (d *daemon) catchUp(a *agent)` — called in `run` after the owner check and before the journal is created: if `cfg.Version` parses, the agent's entry has a parsable `Notes` older than it, and `notesSince` is non-empty, write one note of kind `harness-update` with the text `fmt.Sprintf("The harness was upgraded to v%s. What changed for you:\n\n%s\n", version, strings.Join(sections, "\n\n"))`, then `setNotes(user, cfg.Version)`. Empty `Notes` delivers nothing. A write failure is logged and retried on the next run.
  - `admit`, for a fresh assignment: `setNotes(user, cfg.Version)` when the version parses.
  - `deliverReleaseNotes` and its `VersionFile` writes are deleted (`VersionFile()` stays in layout for `install.sh`).

- [ ] **Step 1: Write the failing tests**

```go
func TestNotesWaitForWake(t *testing.T)      // entry notes "0.3.1", version "0.4.0": no run starts, inbox empty
func TestNotesDeliveredWithNextWake(t *testing.T) // then an owner message: first request has a harness-update record containing "## 0.4.0" and not "## 0.3.1"; entry notes == "0.4.0"
func TestNotesDeliveredOnce(t *testing.T)    // a second wake has no harness-update record
func TestNewAgentGetsNoNotes(t *testing.T)   // fresh admission records notes == cfg.Version; first run has no harness-update
func TestDevBuildDeliversNoNotes(t *testing.T) // cfg.Version "dev": nothing delivered, notes unchanged
```

- [ ] **Step 2: Run** `go test ./internal/daemon -run Notes` — expect FAIL.
- [ ] **Step 3: Implement** the interfaces above.
- [ ] **Step 4: Run** `go vet ./... && go test ./...` — expect PASS.
- [ ] **Step 5: Commit** `git commit -m "Deliver release notes with each agent's next wake"`

---

### Task 5: The CLI is the same for root and agents

**Files:**
- Create: `internal/cli/new.go`, `internal/cli/new_test.go`, `internal/cli/help.go`
- Modify: `internal/cli/message.go` (`SendChild`, `Target`, `idFromJournal`), `internal/cli/describe.go` (export the cache loader), `internal/cli/message_test.go`, `internal/cli/cli_test.go`, `cmd/agent/main.go`
- Delete: `cli.Send` (root's owner spool)

**Interfaces:**
- Consumes: `layout.RootHome()`, `ConversationFile(id)` from Task 2.
- Produces:
  - `func Base(l layout.Layout, root bool, home string) (string, error)` → `l.RootHome()` for root, else `home` (error if empty).
  - `func SendChild(base, name, text string, now time.Time) error` — requires `base/agents/NAME` to be a directory (not `config.json`); error text `no subagent %q (expected %s/agents/%s/)`.
  - `func Target(l layout.Layout, root bool, home, name string) (journalDir, errorFile, conversation string, err error)` — root with `name == ""` returns the error `root has no owner; name an agent, for example: agent messages main`. Ids are relative: `idFromJournal` returns `rel` without a leading slash.
  - `type NewOptions struct { Provider, Model string; Options []string /* "k=v" */; Wait, Poll time.Duration }`
  - `func New(l layout.Layout, root bool, home, name string, o NewOptions, w io.Writer) error` — base from `Base`; `cleanName(name)`; fails if `base/agents/NAME` exists; starting config = caller's `home/config.json` for an agent (flags override provider/model and add options), required `--model` for root (`root must choose a model: agent new NAME --model M`); provider defaults to the only installed one in `providers.json`; an option whose describe entry has `type: "integer"` is parsed with `strconv.Atoi`, others stay strings; validates with `plugin.Validate` against the cached describe; creates the directory (0700 for root, 0755 otherwise) and writes `config.json` (0644) with a dot-name rename; polls every `o.Poll` up to `o.Wait` for `base/agents/NAME/home`: prints `Created NAME: home <target>`; if `ERROR` appears first returns `NAME was not started: <ERROR text>`; on timeout prints `Created NAME; the daemon has not admitted it yet (agent log NAME shows why).` and returns nil.
  - `func Remove(l layout.Layout, root bool, home, name string) error` — `cleanName`; `base/agents/NAME` must be a real directory; `os.RemoveAll`.
  - `func LoadProviders(l layout.Layout) (map[string]proto.DescribeResponse, error)` — reads `providers.json` (existing cache type).
  - `const Usage` and `const SharingHelp` in `help.go`. `SharingHelp` carries the seed README's current `setfacl` recipe, `stat -L -c %U ~/agents/NAME/home`, and the cleanup steps before `agent remove`, rewritten as reference text.
- `main.go`: `new` (flags `--provider`, `--model`, repeated `--option`, via `flag` with `reorder`), `remove`, `send NAME text|-` for everyone (root uses `RootHome()`; root without NAME gets `name an agent: sudo agent send NAME "text"`), `messages`/`log` via `Target`, `help` prints `Usage` then `SharingHelp`. `New` uses `Wait: 10*time.Second, Poll: 200*time.Millisecond`.

- [ ] **Step 1: Write the failing tests** (`internal/cli`):

```go
func TestNewWritesValidatedConfig(t *testing.T)       // agent caller: ctrl dir 0755, config.json = caller's config with --model override
func TestNewRootRequiresModel(t *testing.T)           // root, no --model: error, nothing created
func TestNewRejectsInvalidConfig(t *testing.T)        // unknown model: error, base/agents/NAME absent
func TestNewRejectsExistingName(t *testing.T)
func TestNewReportsError(t *testing.T)                // a fake "daemon" goroutine writes ERROR: returns "x was not started: Agent limit reached..."
func TestNewReportsHome(t *testing.T)                 // goroutine creates home symlink: output "Created x: home /…"
func TestNewParsesIntegerOption(t *testing.T)         // --option budget=5 → options.budget == 5 (number)
func TestRemoveDeletesControlDir(t *testing.T)        // home symlink target untouched
func TestSendChildAfterAdmission(t *testing.T)        // ctrl dir with home link, no config.json: queued
func TestTargetRootNeedsName(t *testing.T)
func TestTargetSelfFromJournal(t *testing.T)          // home/journal → Log/main/agents/x: conversation Log/main/agents/x/messages.jsonl, errorFile ""
```

- [ ] **Step 2: Run** `go test ./internal/cli` — expect FAIL.
- [ ] **Step 3: Implement** the interfaces above and the `main.go` dispatch.
- [ ] **Step 4: Run** `go vet ./... && go test ./...` — expect PASS.
- [ ] **Step 5: Commit** `git commit -m "Give root and agents the same lifecycle commands"`

---

### Task 6: Reclaim removes links to the home

**Files:**
- Modify: `internal/daemon/reclaim.go`
- Test: `internal/daemon/reclaim_test.go`

**Interfaces:**
- Produces: `func (d *daemon) removeHomeLinks(user string) error` — lists `cfg.Homes` (root-owned, opened directly), removes every entry that is a symlink owned by the daemon's uid whose `os.Readlink` equals `cfg.PoolHome(user)`; a reclaim step `"home links"` right after `"wipe files"`.

- [ ] **Step 1: Write the failing test**

```go
func TestReclaimRemovesHomeLinks(t *testing.T) {
	// Homes/agent -> PoolHome(main's user), Homes/other -> PoolHome("agent-4"),
	// Homes/agent-1 real dir. Remove main: Homes/agent gone, Homes/other kept.
}
```

- [ ] **Step 2: Run** `go test ./internal/daemon -run ReclaimRemovesHomeLinks` — expect FAIL.
- [ ] **Step 3: Implement** `removeHomeLinks` and the step.
- [ ] **Step 4: Run** `go vet ./... && go test ./...` — expect PASS.
- [ ] **Step 5: Commit** `git commit -m "Reclaim removes links to a reclaimed home"`

---

### Task 7: Migrate a 0.3.x installation

Replaces the 0.3.0 migration (delete it and its tests).

**Files:**
- Rewrite: `internal/daemon/migrate.go`, `internal/daemon/migrate_test.go`
- Modify: `internal/daemon/system.go` (+ `fake_system_test.go`), `internal/daemon/privilege_test.go`, `.github/workflows/ci.yml` (create `agent-test-main`)
- Modify: `cmd/agent/main.go` (`_migrate` comment)

**Interfaces:**
- Consumes: layout and pool from Tasks 2 and 4.
- Produces:
  - `Config.LegacyUser string` (default `"agent"`).
  - `System` gains `RenameUser(old, new, home string) error` (exec: `usermod -l new old`, `groupmod -n new old`, `usermod -d home new`) and `RenameCrontab(old, new string) error` (exec: rename `/var/spool/cron/crontabs/old` to `new` if present).
  - `func Migrate(cfg Config) error` — no-op when `cfg.LegacyUser` does not resolve and `MigratingFile()` is absent. Otherwise, in this order (each step skips what is already done):
    1. Target user: the name stored in `MigratingFile()`; else the first `layout.PoolUsers(max_agents)` name that does not resolve, written to `MigratingFile()` before anything changes.
    2. `DisableLinger(legacy)`, `StopProcesses(legacy)` (failure here returns a plain error: nothing renamed).
    3. `RenameUser`, `os.Rename(Homes/legacy, PoolHome(target))` (refuse `EXDEV`), chmod 2750, `RenameCrontab`, root-owned symlink `Homes/legacy -> PoolHome(target)`, `EnableLinger(target)`. From here errors are `*PartialError`.
    4. `Etc/agents/main/` (0700); move `Etc/OWNER.md` into it if present; remove `PoolHome(target)/ERROR`.
    5. For each `pool.json` entry: rewrite `Agent` from `<Homes>/<legacy>/agents/x…` to `main/agents/x…`; move its control directory's `config.json` into its home (owner = its user, mode 0644, `O_NOFOLLOW` at every component) unless the home already has one; set `Notes` to the version read from `VersionFile()`.
    6. Add `{User: target, Agent: "main", State: active, Notes: <old version>}`.
    7. `os.Rename(Log/home/<legacy>, Log/main)`; `os.Rename(Log/messages.jsonl, Log/main/messages.jsonl)`; repoint the `journal` symlink in main's home and in each pool entry's home to `JournalDir(<new id>)`, through a helper running as that home's user (`fsio.Client.Symlink`), since the daemon never writes inside a home as root.
    8. Rewrite `delivered.jsonl` agent fields with the same id mapping (main's old id `<Homes>/<legacy>` → `main`).
    9. Move files from `Lib/owner/` to `RootHome()/.messages/agents/main/`; remove `Lib/owner/`.
    10. Remove `/etc/sudoers.d/<legacy>` (path from `Config.SudoersDir`, default `/etc/sudoers.d`).
    11. Remove `MigratingFile()`.

- [ ] **Step 1: Write the failing tests** (`migrate_test.go`, fake `System`, fixture mirroring the VM: legacy home with `agents/clinics` and `agents/recipes` control dirs holding `config.json` + `home` links, pool entries for agent-1/agent-2, `Log/home/agent/...` journals, `Log/messages.jsonl`, one spooled owner message, a sudoers file, no OWNER.md, `version` = `0.3.1`):

```go
func TestMigrateRestructuresState(t *testing.T)   // all steps' results, pool ids, notes "0.3.1", symlink, delivered ids
func TestMigrateKeepsDeliveredLabels(t *testing.T) // an inbox file delivered before migration still labels "owner" after
func TestMigrateResumesAfterEachStep(t *testing.T) // fake System fails at step k for k in 2..10, rerun: same end state, same target user
func TestMigrateFailureBeforeRenameIsNotPartial(t *testing.T) // StopProcesses fails: not *PartialError, home untouched
func TestMigrateNothingToDo(t *testing.T)
```

  Privilege test (root, real users): `TestPrivilegeMigrateRenamesUser` — `agent-test-main` with a home and a crontab becomes `agent-test-4` with the same uid, home moved, symlink in place.

- [ ] **Step 2: Run** `go test ./internal/daemon -run Migrate` — expect FAIL.
- [ ] **Step 3: Implement** `Migrate`, the `System` methods and the fakes; delete the 0.3.0 code paths.
- [ ] **Step 4: Run** `go vet ./... && go test ./...`, then the privilege tests as root — expect PASS.
- [ ] **Step 5: Commit** `git commit -m "Migrate the main agent to a pool user"`

---

### Task 8: install.sh

**Files:**
- Modify: `install.sh`, `scripts/release_test.sh` (only if it checks removed strings)

**Interfaces:**
- Consumes: `agent _migrate` (Task 7), `agent new` (Task 5).

- [ ] **Step 1: Remove `--sudo`** (option parsing, `GRANT_SUDO`, the sudoers block, header comment); an unknown option still dies.
- [ ] **Step 2: Replace the legacy-subagent migration block** with: if `id agent` resolves or `$MIGRATING` exists → list running transient units (`/run/user/$(id -u agent)/systemd/transient/*.service` that `systemctl --user -M agent@ is-active` reports active), print them and `ask "These jobs will be stopped and not restarted. Continue? [y/N] "` (no terminal: continue with a warning); root-only backup tarball in `/var/backups/agent-0.4-$(date -u +%Y%m%dT%H%M%SZ).tgz` of `/etc/agent /var/lib/agent`, the crontab file, `getent passwd/group/shadow/gshadow agent` output and `/var/lib/systemd/linger/agent` (first run only, recorded in the marker as today); run `agent _migrate` with the existing exit-status handling (1: roll back; 2: stay stopped).
- [ ] **Step 3: Create pool users after migration**, loop `1..max_agents` inclusive. Remove `useradd … agent`, `chmod 750 /home/agent`, `loginctl enable-linger agent`, the heartbeat-removal block, and `install -d … /var/lib/agent/owner`; add `install -d -m 700 -o root -g root /etc/agent/agents`.
- [ ] **Step 4: Fresh install**: after the provider/model prompts, start the service, then `agent new main --provider "$provider" --model "$model"`; write `/var/lib/agent/version`. Upgrade detection uses `/etc/agent/agents/main` or the legacy user instead of `/home/agent/config.json`. Next-steps text: `sudo agent send main "Hello. …"`, `sudo agent log main`.
- [ ] **Step 5: Verify** `sh -n install.sh` and `sh scripts/release_test.sh` — expect success.
- [ ] **Step 6: Commit** `git commit -m "Install and upgrade for the recursive tree"`

---

### Task 9: Seed README, release notes and docs

**Files:**
- Modify: `internal/seed/README.md`, `internal/seed/seed.go` (drop `Write`'s `config.json` if unused after Task 8; keep `_seed` only if used), `internal/notes/notes.md`, `docs/spec.md`, `docs/philosophy.md`, `README.md`, `AGENTS.md`

- [ ] **Step 1: Seed README Subagents section** — replace with the spec's seven bullets (spec "Seed README"); update Messages: `agent send NAME` fails at once if `~/agents/NAME/` does not exist; the conversation log is `~/journal/messages.jsonl` for every agent; the main agent's message is read by the human with `agent messages`.
- [ ] **Step 2: `notes.md` `## 0.4.0`** — facts from the spec's Docs section, plus: the upgrade restarted your user manager and stopped transient `systemd-run` jobs that were running.
- [ ] **Step 3: `docs/spec.md` and `docs/philosophy.md`** — rewrite for the tree (layout table, privilege model, daemon discovery/admission, messages, journal, OWNER.md, configuration, CLI, seed files, install, tests, migration: replace "To a user per subagent" with "To a recursive tree"); philosophy idea 1 as in the spec.
- [ ] **Step 4: Operator `README.md`** — `sudo agent send main`, `sudo agent messages main`, `sudo agent log main`, `sudo agent new NAME --model M`, `/etc/agent/agents/NAME/OWNER.md`; remove `--sudo`; describe the 0.4.0 upgrade (rename, symlink, brief outage, transient jobs stopped).
- [ ] **Step 5: `AGENTS.md`** — adjust any paths that name `/home/agent` or the main agent's user.
- [ ] **Step 6: Run** `go vet ./... && go test ./...` (seed tests read the README) — expect PASS.
- [ ] **Step 7: Commit** `git commit -m "Document the recursive tree"` and `git push`.

---

### Task 10: Release and deploy

- [ ] **Step 1:** `scripts/release.sh 0.4.0`; commit `plugins/index.json` and `install.sh`; `git tag v0.4.0`; `git push && git push origin v0.4.0`; confirm the release workflow publishes.
- [ ] **Step 2:** Ask the user for a quiet moment. Read-only check first: `ssh bot 'ls /run/user/$(id -u agent)/systemd/transient/'` and report running transient jobs.
- [ ] **Step 3:** With the user's go-ahead, run the release's `install.sh` on `bot` (see memory `deploy-token-via-stdin`).
- [ ] **Step 4: Verify read-only:** `systemctl is-active agent`; `journalctl -u agent -n 50`; `getent passwd agent-16` has uid 1001; `readlink /home/agent`; `sudo cat /var/lib/agent/pool.json` shows `main`, `main/agents/clinics`, `main/agents/recipes`; `systemctl --user -M agent-16@ list-units --state=running` shows caddy, lspn-api, recipes-*, telegram-poller; `sudo agent log main` starts without error. Send nothing to the agent.
