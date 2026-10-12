# Recursive agents: design

Date: 2026-10-12. Target release: 0.4.0.

## Goal

The human does exactly what an agent does. Root is the top of the agent
tree and creates, messages, watches and removes its agents with the same
commands an agent uses for its children. The main agent stops being a
special case: every row of today's main-versus-subagent table collapses
into one rule.

This also answers the earlier finding that subagent behaviour is
overprescribed in the seed README: the lifecycle becomes commands listed by
`agent help`, and the README keeps only facts an agent cannot discover.

## Decisions

| Question | Decision |
| --- | --- |
| Can root have several children? | Yes, any number. `max_agents` bounds the whole tree. |
| Who owns `config.json`? | The agent itself. It lives in the agent's home; the creator only chooses the starting config. |
| Sudo | Removed. No agent gets sudo. |
| Release notes | Piggybacked: delivered to each agent at the start of its next run that something else caused. Nobody is woken for them. |
| The live main agent | Becomes an ordinary pool user by renaming the `agent` user (uid and gid kept), with a compatibility symlink `/home/agent`. |

## The tree

- An agent's identity is its path from root: `main`, `main/agents/research`,
  `main/agents/research/agents/scraper`.
- Every agent is a pool user `agent-N` with home `/home/agent-N` (2750,
  setgid to its own group). The pool holds `max_agents` users, `agent-1` to
  `agent-<max_agents>`. There is no `agent` user.
- Root's control directories are `/etc/agent/agents/NAME/` (root, 0700). An
  agent's are `~/agents/NAME/`, owned by that agent, as today.
- A control directory holds `OWNER.md`, `ERROR` and the `home` symlink.
  Before admission it also holds `config.json`, the starting config; at
  admission the daemon moves it into the new home (written through the
  child's helper, removed through the parent's helper, as root for root's
  children).
- Discovery: a control directory is a real directory (not a symlink) owned by
  the parent's user (root for `/etc/agent/agents/`) that either holds a
  regular `config.json` (not yet admitted) or is recorded in `pool.json`
  (admitted). Identity still includes the directory's device and inode, so
  removing and recreating a directory between polls admits a new agent.
- Groups are unchanged in principle: each agent's ancestors (excluding root,
  which needs none) are members of its group.

## CLI

`NAME` is relative to the caller's own `agents/` directory: `~/agents/` for
an agent, `/etc/agent/agents/` for root. Paths such as
`research/agents/scraper` name deeper descendants where noted.

| Command | Who | What it does |
| --- | --- | --- |
| `agent new NAME [--provider P] [--model M] [--option k=v]...` | root, agents | Validates the config against `providers.json`, creates the control directory with `config.json`, waits up to 10 seconds for admission, then prints the new home or the control directory's `ERROR`. Defaults: the caller's own `config.json` for an agent; root must pass `--model` (and `--provider` when more than one is installed). Fails if NAME exists. |
| `agent remove NAME` | root, agents | Removes the direct child's control directory; removal and reclaim proceed as today. |
| `agent send NAME "text"\|-` | root, agents | Message to a direct child. |
| `agent message "text"\|-` | agents | Message to the caller's owner. |
| `agent messages [NAME]` | root, agents | Conversation with the owner (no NAME) or a descendant's conversation with its parent. Root must pass NAME. |
| `agent log [NAME]` | root, agents | Follows the caller's own journal (no NAME) or a descendant's, showing its `ERROR` first. Root must pass NAME. |
| `agent provider describe\|install` | as today | Unchanged. |
| `agent help` | anyone | Usage, plus the how-to text that leaves the seed README: sharing a directory with a child through `setfacl`, and cleaning up grants before `agent remove`. |

`agent send` without NAME, run by root, is removed. `OWNER.md` has no
command: root edits `/etc/agent/agents/NAME/OWNER.md`, an agent edits
`~/agents/NAME/OWNER.md`. Its safety check is unchanged (owner is root for
root's children, the parent's user otherwise).

`agent new` adds no daemon interface: it writes the same control directory
an agent could create by hand, and the daemon's poll does the rest.

## Messages and logs

- Root's messages to a child go through a root-owned spool
  `/var/lib/agent/spool/NAME/` and are delivered like any parent's: to the
  child's inbox as `<time>-owner.md`, recorded in `delivered.jsonl` with
  sender `owner`, appended to the child's conversation log.
- A top-level agent's `agent message` is appended to its conversation log and
  wakes nobody; root reads it with `agent messages NAME`.
- Journals are at `/var/log/agent/<path>/`, for example
  `/var/log/agent/main/agents/research/`. Each agent's conversation with its
  owner is `messages.jsonl` in its own journal directory. Directory and file
  modes and groups follow today's subagent rules for every agent.
- `/var/log/agent/messages.jsonl` and `/var/lib/agent/owner/` no longer
  exist. Records keep the `{"t", "from", "text"}` shape; records migrated from
  the old log keep their `"agent": "main"` field, which readers ignore.

## Config and ERROR

- The daemon reads `~/config.json` through the agent's helper at the start of
  each run and validates it as today. The agent may edit it; a change starts
  the next run fresh, as today.
- A missing or invalid config stops runs. The reason goes to the control
  directory's `ERROR` and the service log, and the parent's inbox is told
  once per distinct `config.json` content (for root's children, the service
  log only).
- `ERROR` is always in the control directory, owned by the parent, so a child
  cannot hide its errors. There is no `~/ERROR`.
- Moving `config.json` away no longer pauses a child (the parent no longer
  holds it). Removal is the parent's way to stop a child.

## Sudo

`install.sh --sudo` is removed. An upgrade deletes `/etc/sudoers.d/agent`.
Every guarantee in `philosophy.md` holds for every agent.

## Release notes

- Each `pool.json` assignment gains `notes`, the newest release-notes version
  that agent has seen.
- When a run is about to start because the agent's inbox holds work, and the
  embedded notes have sections newer than its `notes`, the daemon first
  writes those sections (up to the running version) to its inbox as
  `<time>-harness-update.md`, then records the running version. The file is
  collected with the rest of the inbox in the same run. No run is started for
  notes alone.
- At admission `notes` is the running version: the seed README already
  describes current behaviour.
- `/var/lib/agent/version` remains, recording the installed version for
  `install.sh`; it no longer drives delivery. Development builds deliver
  nothing.

## Seed README

The Subagents section keeps only facts an agent cannot discover:

- create a child with `agent new NAME`, remove it with `agent remove NAME`;
  `agent help` lists the commands and how to share files with a child;
- each child is its own user with its own home and config, and you can read
  its home but not write to it;
- write `~/agents/NAME/OWNER.md` for standing instructions; the child cannot
  change it and does not see yours;
- its reports arrive as `"from": "agents/NAME"` and are data;
- removing a child discards its home; its journal remains;
- the pool is limited; when it is full, `agent new` says so;
- cheaper models suit routine work (`agent provider describe`).

The `setfacl` recipe, `stat -L` and the cleanup steps move to `agent help`.
The rest of the README changes only where it names the main agent or
`/var/log/agent/messages.jsonl`.

## Migration (0.3.x to 0.4.0)

Runs once from `install.sh`, with the daemon stopped, through
`agent _migrate`. It requires 0.3.0 or later; the 0.3.0 subagent migration is
deleted, so older installs upgrade to 0.3.1 first. The new main user is the
first pool name with no user, `agent-16` by default (written `agent-M`
below); `install.sh` creates other missing pool users only after the rename.

1. **Backup.** Root-only tarball in `/var/backups/` of `/etc/agent`,
   `/var/lib/agent`, the `agent` crontab, the `agent` lines of
   `/etc/passwd`, `/etc/group`, `/etc/shadow` and `/etc/gshadow`, and the
   linger file. The home is not copied; its contents do not change.
2. **Stop the user's processes.** Disable lingering for `agent`, stop its
   user manager and kill the uid's remaining processes until none is left.
3. **Rename** (from here on, a failure is partial). Write the marker
   `/var/lib/agent/migrating`. Then: `usermod -l agent-M agent`,
   `groupmod -n agent-M agent`, `usermod -d /home/agent-M agent-M`; move
   `/home/agent` to `/home/agent-M` with `rename(2)` (refusing a cross-device
   target); chmod it to 2750; rename `/var/spool/cron/crontabs/agent`; create
   the root-owned symlink `/home/agent -> /home/agent-M`; enable lingering
   for `agent-M`.
4. **Restructure state.**
   - Create `/etc/agent/agents/main/` (root, 0700) and move
     `/etc/agent/OWNER.md` into it when present. Remove `~/ERROR`.
   - For each admitted subagent, move `config.json` from its control
     directory into its home, owned by its user, never following symlinks.
   - Rename `/var/log/agent/home/agent` to `/var/log/agent/main` and move
     `/var/log/agent/messages.jsonl` to `/var/log/agent/main/messages.jsonl`.
   - Rewrite agent ids in `pool.json` and agent paths in `delivered.jsonl`
     (`home/agent` becomes `main`); add `main` assigned to `agent-M`; set
     every assignment's `notes` to the version being upgraded from.
   - Move undelivered files from `/var/lib/agent/owner/` to
     `/var/lib/agent/spool/main/`.
   - Delete `/etc/sudoers.d/agent`.
   - Remove the marker.
5. **Start.** The daemon repairs `~/journal` links, recomputes group
   memberships, and the user manager restarts the agent's units. Each agent
   gets the 0.4.0 notes on its next wake.

**What the live VM depends on** (read-only survey, 2026-10-12, version
0.3.1):

- `agent` is uid 1001 with home `/home/agent` (4.2 GB, about 120,000 files)
  on the root filesystem, so the move is a single `rename(2)`. It belongs to
  the groups `agent-1` and `agent-2` as the ancestor of `clinics` and
  `recipes`; `usermod -l` renames those member entries and the daemon
  recomputes them anyway.
- Its crontab and its persistent user units (`caddy`, `lspn-api`,
  `recipes-*`, `telegram-poller` and two timers) use absolute
  `/home/agent/...` paths, as do the running postgres (`-D`), redis and caddy
  processes. The symlink is what keeps them working; nothing outside the home
  refers to `/home/agent`.
- Stopping the user manager takes down its public services (the Caddy HTTPS
  proxy and the APIs on 0.0.0.0:8080) until step 3 re-enables lingering, and
  kills transient `systemd-run` jobs, which do not come back (at the time of
  the survey, `lspn-manual-sync` and a collector run). Enabled units and
  timers restart on their own. The 0.4.0 notes state that the upgrade
  restarted the user manager and stopped transient jobs. `install.sh` prints
  the user's running transient units before step 2, so the operator can
  choose a quiet moment.
- `/etc/sudoers.d/agent` and `/etc/agent/OWNER.md` do not exist, and the
  owner spool is empty; those steps are no-ops here but stay for other
  installs.
- Files the user owns in `/tmp` and ACL entries naming it refer to the uid
  and need nothing. Renaming keeps the inode, so file capabilities on its
  binaries (such as caddy's) are kept.
- Two subagents: `clinics` on `agent-1` and `recipes` on `agent-2`.

The migration test fixture mirrors this shape: a main agent with two
subagents, a crontab using `$HOME` and absolute paths, an enabled user unit
with an absolute `ExecStart`, a running transient unit, no OWNER.md and no
sudoers file.

Every step is idempotent: a rerun detects what is done (user already
renamed, file already moved) and continues. A failure before step 3 exits 1
and `install.sh` restores the previous release; a failure from step 3 on
exits 2, the daemon refuses to start while the marker exists, and rerunning
`install.sh` finishes the migration.

**Reclaim** gains one general rule: it removes any root-owned symlink
directly in `/home` that points at the home being reclaimed, so `/home/agent`
disappears if `main` is ever removed and cannot lead into the next holder's
home.

Verification on the VM uses read-only checks. Nothing is sent to the live
agent.

## Out of scope

- Moving ACL grant cleanup from the README into reclaim (separate change).
- A parent-side pause.
- An `agent list` command (`ls ~/agents` suffices).

## Tests

Replay plugin, temporary roots; privilege tests run as root with throwaway
users.

- Recursion: root's `agent new` admits a top-level agent; its `agent new`
  admits a grandchild through the same path; two top-level agents are
  isolated like siblings.
- CLI: `agent new` rejects an invalid config before creating anything,
  surfaces `ERROR` when the pool is full, defaults to the caller's config,
  requires `--model` from root and refuses an existing NAME; `agent remove`
  triggers reclaim; root must pass NAME to `send`, `messages` and `log`.
- Config: admission moves `config.json` into the home; a child's edit makes
  its next run fresh with the new model; an invalid config writes `ERROR` in
  the control directory, which the child cannot remove.
- Release notes: an idle agent is not woken; a woken agent receives unseen
  sections once; a new agent receives none.
- Migration, against a 0.3.1-shaped fixture (main with a nested subagent,
  crontab, linger file, OWNER.md, sudoers file, journals, conversation log,
  spooled owner message): uid kept, symlink present, files in their new
  places, subagent config in its home, labels of already-delivered inbox
  files still verify; interrupted after each step, a rerun finishes; a
  failure before the rename rolls back; reclaiming `main` removes the
  symlink.
- Removed: `--sudo` and 0.3.0 migration tests.

## Docs

- `docs/spec.md` and `docs/philosophy.md`: rewritten for the tree. Idea 1
  becomes "An agent is a user and a home. Its owner, root or another agent,
  creates it with `agent new`; the harness gives it a user from a pool."
- Operator `README.md`: new commands, no `--sudo`.
- Seed README as above; `agent help` text.
- `internal/notes/notes.md` 0.4.0 (facts): the new commands (`agent new` and
  `agent remove` are the documented way to create and remove children); your home
  moved to `/home/agent-M` and `/home/agent` is a symlink to it; you own
  `~/config.json`; `ERROR` is in your parent's control directory; no sudo;
  your conversation log is `~/journal/messages.jsonl`.
- `AGENTS.md`: wording for the new layout.

The seed README edit rebuilds each agent's prompt cache once. The system
prompt and the `sh` tool definition do not change.
