# Agent harness: design spec

A minimalist, always-on LLM agent harness for a Linux VM, written in Go with
the standard library only. It ships as static binaries for linux/amd64 and
linux/arm64. It replaces the Node bot that previously lived in this repository.

The harness provides a few guarantees and the agent provides all policy. The
agent has one tool, the shell, and builds every other capability itself
(memory, tools, scheduling, messaging) as files on the VM. If something could
be a script the agent writes, it stays out of the harness.

## Three ideas

1. An agent is a Unix user and its home directory, holding `README.md`,
   `TASK.md` and `inbox/`. Its owner, root or another agent, creates it with
   `agent new`; the harness gives it a user from a pool and a home of its
   own. Root is the top of the tree, and the main agent is root's child
   `main`.
2. Everything that happens is a file in an inbox: owner messages, schedules,
   heartbeats, child reports, crash recovery and context resets.
3. The harness has no provider code. Model access is a plugin.

## Decisions made during design

- **No `max_steps`.** The agent works for as long as it decides is necessary.
  Context resets bound each run; the provider spend limit bounds money.
- **Root is the top of one tree.** The human, as root, creates, messages,
  watches and removes its agents with the same commands an agent uses for
  its children. Root may have any number of children. The main agent is
  the one a fresh install creates, `main`; nothing else sets it apart.
- **Every agent has one owner.** A top-level agent's owner is the human
  (root); any other agent's owner is the agent that created it. An agent
  talks only with its owner and its own children, so only top-level agents
  reach the human.
- **Owner messages are verified by the harness.** Root's `agent send NAME`
  requires root; an agent's `agent send NAME` is identified by its uid. The
  daemon labels the messages it delivered with their sender and every other
  inbox file as `"from": "data"`, using structure the file content cannot
  forge (see Context).
- **Each agent is its own Unix user.** Users come from a fixed pool of
  `max_agents` users created at install and are reused. An agent cannot
  change its parent's or siblings' files or its own owner's instructions.
  Its ancestors can read its home by default (a courtesy the agent could
  revoke) and always read its journals (a guarantee, since only root writes
  them).
- **Each agent owns its config.** Its creator chooses the starting
  `config.json`; at admission it moves into the agent's home, and the agent
  may change it. A parent stops a child only by removing it.
- **No agent has sudo.** Every guarantee in `philosophy.md` holds for every
  agent.
- **Long-running jobs use each agent user's own systemd.** The service keeps
  systemd's default kill behaviour, so stopping it stops everything it
  started. Lingering gives each agent user a persistent user manager; the
  daemon sets `XDG_RUNTIME_DIR` so `systemd-run --user` works from commands.
- **Quick follow-ups continue the conversation.** A message within 4 minutes
  of a run ending continues it with a warm cache; the agent can force a fresh
  start with `~/.reset`.
- **No daily heartbeat.** The agent wakes on messages and on wake-ups it
  schedules itself.
- **Provider failures are visible but never touch the inbox.** Retries back
  off to a 10-minute ceiling; persistent failures are written to the agent's
  `ERROR` and the service log and shown by `agent log`.
- **The repository stays private.** Installation and plugin downloads
  authenticate with a `GITHUB_TOKEN` that is never stored on the VM.
- **The old Node bot is retired.** Its history and projects become ordinary
  working files in the main agent's home (see Migration); the harness and
  seed files know nothing about them.
- **Built-in, append-only conversations.** `agent message` lets an agent
  write to its owner; the daemon records both directions in a root-owned log
  the agent can read but not change (see Messages). The human's conversation
  is pull, not push: alerts remain something the agent builds.
- **Standing owner instructions.** Each agent's `OWNER.md` is written by its
  owner, loaded first on every run, and cannot be changed by the agent (see
  OWNER.md). The harness guarantees that it is present, unaltered and
  labelled as the owner's. Precedence is stated to the model, not enforced:
  hard limits belong in the privilege model, not in this file.

## Filesystem layout on the VM

| Path | Owner, mode | Contents |
| --- | --- | --- |
| `/usr/local/bin/agent` | root, 0755 | The harness binary |
| `/etc/agent/` | root, 0711 (traversable, not listable) | Root's place in the tree, in place of a home: `settings.json` (0600), `credentials/`, `plugins/`, `agents/`, `.messages/` |
| `/etc/agent/credentials/NAME.env` | root, 0600 | Plugin settings and secrets as `KEY=value` lines |
| `/etc/agent/plugins/` | root:agent-provider, 0750 | Plugin executables, root:agent-provider 0750; no agent user can read or run them |
| `/etc/agent/agents/NAME/` | root, 0700 | Root's control directory for its child NAME (`main` for the main agent) |
| `/etc/agent/agents/NAME/OWNER.md` | root, 0644 | The human's standing instructions for NAME; optional |
| `/etc/agent/.messages/agents/NAME/` | root, 0700 | Root's spool for its child NAME; files 0600 |
| `/var/lib/agent/delivered.jsonl` | root, 0600 | Record of messages delivered (agent id, file name, SHA-256, sender) |
| `/var/lib/agent/pool.json` | root, 0600 | Pool assignments: user, agent id, state, release notes seen |
| `/var/lib/agent/providers.json` | root, 0644 | Cached `describe` output with no secrets, for `agent provider describe` |
| `/var/lib/agent/version` | root, 0644 | The installed version, written by `install.sh` |
| `/var/log/agent/` | root, 0755 | Journals and conversation logs (see Journal) |
| `/home/agent-N/` | agent-N:agent-N, 2750 | A pool user's home: an agent's home while assigned, empty while free |
| `/home/agent` | root, symlink | Only on installations upgraded from 0.3: a link to the main agent's home |

`settings.json` holds `max_agents` (default 16) and `repository`
(`SEAN-7/bot`). The pool holds `max_agents` users, `agent-1` to
`agent-<max_agents>`, and bounds the whole tree, main included. A fresh
install's main agent is `agent-1`; one upgraded from 0.3 holds the user its
0.3 user was renamed to (`agent-16` by default, see Migration).

In an agent's home, the harness uses `README.md`, `TASK.md`, `config.json`,
`inbox/`, `inbox/processed/`, `agents/`, `.messages/`, `.outputs/`,
`.reset` and the `journal` symlink.

An agent's control directory is `agents/NAME/` in its parent's home
(`/etc/agent/agents/NAME/` for root's children) and belongs to the parent.
The harness uses `OWNER.md`, `ERROR`, the `home` symlink (pointing at the
agent's home) and, until admission, the starting `config.json`. The agent
cannot change it.

Everything else belongs to the agents.

## Privilege model

The daemon runs as root. Each agent is an unprivileged pool user `agent-N`.
No agent has sudo.

**Groups.** Each agent user has its own group. The daemon makes every
ancestor of an agent, other than root, which needs none, a member of that
agent's group (in `/etc/group`, kept current as the tree changes). An
agent's home is setgid to its group and commands run with umask 022, so by
default its ancestors can read what it writes, and nobody else can traverse
its home. An agent can tighten its own files' modes; its journals stay
readable by its ancestors regardless. What an agent grants to let a child
through its home (x on `~`, see `agent help`) opens what others can read
there, so the harness keeps its own parts of the home group-only:
`agents/` (seeded 0750), the control directories `agent new` creates
(0750, `config.json` 0640) and the spools `agent message` and `agent send`
write (`.messages/` directories 0750, files 0640). A child never needs its
control directory, and cannot read its siblings' `OWNER.md`, `ERROR` or
starting `config.json`, or messages in flight.
Processes started before a membership change (an ancestor's user manager,
for example) see the new group only after they restart; commands the
harness runs always get current groups. Likewise, a process an ancestor
started while it was a member of an agent's group (its user manager,
started at boot, for example) keeps that supplementary group after the
agent's user is reclaimed and reassigned, and with it read access to the
new holder's home, until the process restarts.

**All file access inside agent homes and control directories goes through
unprivileged helpers.** The daemon starts one long-lived helper process per
agent user: the same binary (`agent _fsio`), launched with that user's uid,
gid and supplementary groups, driven by JSON lines over a pipe. A home is
accessed through its agent's helper; a control directory through its
parent's. The kernel therefore applies that user's permissions to each
operation, and a planted symlink can reach only what that user could reach.
The daemon never opens a path inside an agent home or an agent's control
directory as root. Root's place in the tree, `/etc/agent`, is reached the
same way, through a helper running as root, so the same code serves root
and agents. (Go cannot safely change identity in part of a running process, so a
separate process is the reliable way to do this.) If a helper dies, the
daemon restarts it; operations in flight fail and the affected run is
retried.

Helper operations: `scan` (the real directories in `agents/` that the
helper's user owns), `list` (directory entries with type, size and owner),
`read` (path, byte limit; regular files only), `write` (atomic:
dot-prefixed temporary file then rename), `rename`, `mkdir`, `remove`
(recursive, never following symlinks), `unlink` (one entry, never a
directory's contents), `symlink` (create or replace), `clear` (empty a
directory, never following symlinks), `purge_owned` (the user's files in
the shared temporary directories), `stat` (size, mtime, type, mode, owner
uid, device and inode, and a symlink's target, without following symlinks
for the final component).

**Shell commands** run as the agent's user with credentials set in the child
before exec, so the change of working directory happens with that user's
permissions. Each command gets its own process group, stdin from
`/dev/null` and umask 022. Environment: `HOME` and working directory set to
the agent's home, `USER`/`LOGNAME` set to its user name,
`PATH=$HOME/bin:/usr/local/bin:/usr/bin:/bin`,
`XDG_RUNTIME_DIR=/run/user/<uid>`,
`DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/<uid>/bus`, `TERM=dumb`.
Commands run with `/bin/bash -c` when bash exists, otherwise `/bin/sh -c`.

**Sharing** between agents is explicit. A file's owner can grant another
agent user access with `setfacl` (no root needed); the harness does nothing
more. Granting `x` on a directory lets that user reach every entry inside
it that others can read, so `agent help` advises a dedicated share
directory (and `chmod o-rwx` for private ones on the way), with a default
ACL so that later files are shared too. Users are reused, so a parent
deletes what a child created in its directories and removes its grants
(`setfacl -R -x u:USER,d:u:USER DIR`, and `setfacl -x u:USER` on the
directories it traversed) before removing the child.

**Plugins** run as the dedicated system user `agent-provider`, with no login
and no home directory. The daemon reads `/etc/agent/credentials/NAME.env` and
passes those variables in the plugin's environment, along with a minimal
`PATH`. Neither the plugin nor any agent reads `/etc/agent` directly, and an
agent user cannot read another user's process environment.

## The daemon

`agent daemon` runs as one systemd service (`Restart=always`, default
`KillMode`) serving the whole tree.

- **Root.** Root is a node of the tree like any agent, with `/etc/agent` in
  place of a home: its control directories are `/etc/agent/agents/NAME/` and
  its spool for a child is `/etc/agent/.messages/agents/NAME/`. It has no
  loop, no inbox, no journal and no owner. An agent's id is its path from
  root: `main`, `main/agents/research`, `main/agents/research/agents/scraper`.
  The id keys journals, pool assignments and delivery records.
- Startup: require root; refuse to start when `pool.json` holds an agent
  id that is an absolute path, as releases before 0.4.0 wrote, since the
  daemon would take those agents for removed and wipe their homes (such a
  tree upgrades with the v0.4.0 `install.sh` first); resolve the provider user
  and the pool users (missing ones are logged, and agents are limited to
  the rest); start root's helper; tighten the journals of agents that hold
  no user (they get the journal modes and the group of their nearest
  ancestor that holds one, root's for a top-level agent); then poll about
  once a second. Each poll first finishes any reclaim left in progress (see
  Reclaim), then walks the tree, then collects messages.
- **Discovery.** The tree is walked breadth-first from root, each node's
  `agents/` directory scanned through its own helper, never following
  symlinks. A control directory is a real directory (not a symlink) at
  `<home>/agents/<name>`, owned by that node's user (root for
  `/etc/agent/agents/`). One the daemon does not run yet is admitted, in
  this order:
  1. with its own user, when `pool.json` records one for its id (a daemon
     restart; a missing `home` link is restored);
  2. with the user its `home` link names, when that user is free and its
     home holds this agent's evidence: its `journal` link points at this
     agent's journal directory, whose group is that user's, which only the
     daemon sets. This recovers a lost `pool.json`, keeping the home;
  3. with a fresh user, when it holds a regular `config.json`. A free user
     whose home holds another agent's evidence is never handed out, so a
     lost `pool.json` cannot wipe a live home.

  Otherwise it is left alone (it may still be being written), and the
  evidence found in a free user's home is remembered until the pool or a
  journal directory's group changes, so a control directory that stays
  unadmitted does not start a helper at every poll. A `config.json` that
  appears in an admitted agent's control directory is ignored: the
  agent's config is its own. When the agent is next admitted (after a
  daemon restart) it is renamed `config.json.ignored`, replacing an older
  one, and the service log says so. A known agent the scan misses is
  kept unless its control directory is certainly gone.
- **Admission.** The daemon takes the lowest free usable pool user and
  records the assignment in `pool.json`, then: prepares the home
  (`/home/agent-N`, 2750, setgid to the user's group); starts the user's
  helper; when its entry records no release notes seen (a new agent, or
  one recovered by its home's evidence), records the running version; for
  a user new to this agent whose home does not hold this agent's evidence,
  clears what a previous holder left in the home; sets the members of the user's group to the agent's ancestors other
  than root; seeds the home with the seed `README.md`, `TASK.md`, `inbox/`
  and `agents/` (0750) where missing; moves the starting `config.json` from the
  control directory into the home (written through the agent's helper,
  removed through the parent's; a home that already has one keeps it, and
  the parent's is renamed `config.json.ignored`);
  enables lingering; creates its journal directory; and writes the `home`
  symlink into the control directory. Every step goes through the helpers,
  and a failed step is retried at the next poll. An agent's identity also
  includes its control directory's device and inode, recorded at admission:
  renaming the directory removes the agent and admits a new one, and so does
  removing it and creating it again, even between two polls. Each poll
  restores an agent's `home` link if its parent removed it.
- **max_agents.** When no pool user is free, a new control directory is not
  admitted. The daemon writes the reason to its `ERROR` and the service log,
  and notifies the parent's inbox once (root's children: the service log
  only).
- One goroutine loop per agent, one run at a time per agent.
- **Graceful stop.** On SIGTERM the daemon starts no new runs and delivers no
  new inbox files; active runs finish, up to 15 minutes, before it exits.
  Runs still going at the deadline are interrupted and left unclean, so the
  next start resumes them. The unit uses `KillMode=mixed` and
  `TimeoutStopSec=17min`, so systemd signals only the daemon until it exits.
- **Removal.** When a control directory disappears (or is replaced by
  another directory, a link or a file), its agent and all of its
  descendants are removed, deepest first. Each loop kills any running
  command's process group, writes the end marker (`reason: "removed"`) and
  exits; then its user is reclaimed. Its journals remain. Only the control
  directory's disappearance removes an agent; `agent remove` is the command
  for it.
- **Reclaim.** The daemon marks the user `reclaiming` in `pool.json`, then:
  disables lingering; removes its crontab; stops its user manager and kills
  every remaining process of the uid until none is left; removes its
  home's contents and its files in `/tmp`, `/var/tmp` and `/dev/shm`
  (through its helper, after its processes are gone); removes every
  root-owned symlink directly in `/home` that points at its home (such as
  `/home/agent`), so no old path leads into the next holder's home; clears
  its group's members; and moves its journal directory's group to the
  nearest remaining ancestor's group (root's for a top-level agent), so the
  next holder of the user cannot read them. Only then is the user marked
  free. A reclaim interrupted by a crash is finished on the next start,
  before the user can be assigned again. A failed reclaim is retried every
  poll. Its lingering, crontab, processes and files go at once, but its
  group is cleared and it is freed only after every descendant's user has
  been reclaimed, since their groups still list it. Removing a home first
  gives the user write permission on every directory it owns there (never
  following symlinks), so read-only directories such as a module cache
  cannot block it.
- **Release notes.** Each `pool.json` assignment records `notes`, the newest
  release-notes version that agent has seen. When a run is about to start
  because the inbox holds work, and the embedded release notes
  (`internal/notes/notes.md`) have sections newer than the agent's `notes`, up
  to the running version, the daemon first writes them to its inbox as one
  `<time>-harness-update.md`, then records the running version; the run
  collects the file with the rest. Nobody is woken for notes alone. An
  assignment without `notes` (a newly assigned agent, one recovered after
  a lost `pool.json`, or one from before notes were recorded) records the
  running version at admission, with nothing to catch up on, and gets the
  notes of later releases; development builds deliver nothing. `/var/lib/agent/version` only records the installed version, for
  `install.sh`.
- **Message spools.** See Messages.

## The loop (per agent)

**Wake** when `inbox/` contains a regular file whose name does not start with
a dot.

**Start of run.**
1. Read `~/config.json` (`provider`, `model`, `options`) through the
   agent's helper and validate it against the provider's `describe` (see
   Configuration validation). On failure, do not start and leave the inbox
   untouched.
2. Read `OWNER.md` (see OWNER.md); if it is unsafe, do not start and leave
   the inbox untouched.
3. Write the release notes the agent has not seen into its inbox (see The
   daemon).
4. Read `README.md` and `TASK.md` (missing files are empty).
5. Collect inbox files in name order, skipping dot-files and directories.
   Write them to the journal, then move them to `inbox/processed/` (adding a
   numeric suffix if a name is already taken).

**Context.**
- System segments, most stable first: the system prompt (verbatim, below)
  followed in the same segment by the owner section when `OWNER.md` has
  content, then `README.md`, then `TASK.md`.
- The first user message holds the inbox files. Each file is one JSON record
  built by the daemon:
  `{"file": "inbox/processed/NAME", "from": "owner" | "agents/NAME" | "data", "content": "..."}`.
  `from` is `owner` or `agents/NAME` only if the file's agent path, name and
  SHA-256 match a record in `delivered.jsonl` with that sender. The content
  is a JSON string, so text inside a file can never create its own `from`
  field. Contents over 64 KiB are clipped, with the file's path included.
- After each step, new inbox files (collected the same way) are appended as a
  user message after the command result.

**Steps.** Call the model with the single tool `sh(command, wait)` (see The
sh tool).
- A reply with a command: run it for up to `wait` seconds. The model's
  `wait` is rounded down and kept within 1–600; a missing `wait`, or one that
  is neither a number nor a string holding one, means 120. When the time is
  up, kill the process group with SIGKILL and say so, suggesting a longer
  wait or `systemd-run --user`. Combined stdout and stderr plus the exit
  status are returned.
  Output over 16 KiB is clipped to its first and last 8 KiB, and the full text
  is saved to `~/.outputs/<run>-<step>.txt` (through the helper) with the path
  included in the result.
- A reply with no command ends the run (`reason: "done"`).

**The sh tool** (verbatim; it is part of every agent's cached prefix, so it
changes rarely and deliberately):
- Description: "Run a shell command on this machine as your user, in your
  home directory. Returns combined output and exit status. A command still
  running after its wait is killed."
- Parameters: `command` (string, required: "The command to run") and `wait`
  (integer: "Seconds to wait for the command before killing it. Default 120,
  maximum 600.").

The harness does nothing else for long or interactive commands: an agent
starts what should outlive its wait with `systemd-run --user` (output in
`journalctl --user`) and drives interactive programs through `tmux`.

**Continuation.** A run that ends with a reply without a command keeps its
conversation in memory. The next run continues it (the new inbox records are
appended, and the system sections, call numbering and 80% warning state carry
over unchanged, so the provider's cached prefix stays identical) when all of
these hold: it starts within 4 minutes of that run ending (shorter than the
provider's 5-minute cache, so the cache is always warm), the configuration is
unchanged, `OWNER.md` is unchanged, and `~/.reset` does not exist. Otherwise
it starts fresh. The daemon deletes `~/.reset` when it starts a run. Each run
still has its own journal; a continued run's `start` record names the run it
continues. A daemon restart, interruption, error or context reset always
starts fresh.

**Context size.** After each call, context used = input + cache read + cache
write + output tokens from the plugin's usage, compared with the model's
context size from `describe`.
- At 80% (once per run), append to the next user content: "Context is 80%
  full. Save your state to TASK.md and notes now."
- At 95%, end the run (`reason: "context"`) and write to the agent's inbox:
  "Context reset. Continue from TASK.md. Previous journal: <path>".
- Guard: if the first call of a fresh run is already at 95%, write the reason
  to the agent's `ERROR` and the service log instead of a reset note, and
  wait for new inbox input. This prevents an endless reset loop.

**Provider failures.** Retry the same call with delays doubling from 2 seconds
to a 10-minute ceiling. After 3 consecutive failures, write the provider's
error to the agent's `ERROR` and the service log; clear it on the next
success. Never write inbox messages for provider errors.

**ERROR** is always in the agent's control directory, owned by its parent,
so a parent sees its children's errors and an agent cannot hide its own.
There is no `~/ERROR`.

## Messages

An agent talks with its owner and its own children through the harness, the
same way in both directions, and root talks with its children the same way.
Nothing reaches the human except from a top-level agent.

- **Up.** `agent message "text"` (run by any agent) writes the text to
  `~/.messages/<UTC timestamp>.md` with an atomic dot-name rename. Root
  has no owner: run by root it fails, naming `agent send NAME`.
- **Down.** `agent send NAME "text"` writes it, exactly as given, to
  `.messages/agents/NAME/<UTC timestamp>.md` the same way: under the
  caller's home for an agent, under `/etc/agent` for root, whose spool is
  private (directories 0700, files 0600; an agent's are 0750 and 0640).
  `NAME` must be a direct child: its
  control directory `agents/NAME/` must exist.
- Empty messages and messages over 64 KiB are refused, the latter with
  advice to send a file path instead.
- **Delivery.** Each poll, the daemon reads root's spool through root's
  helper and every admitted agent's spool through its own: regular, non-dot
  files in name order, at most 64 KiB each, owned by the spool's user
  (anything else is ignored and logged). The spool's location and owner
  identify the sender.
  - Up from a top-level agent: appended to its conversation log. It does not
    wake anyone: the human reads the log with `sudo agent messages NAME`.
  - Up from any other agent: delivered to its parent's inbox as
    `<UTC timestamp>-NAME.md`, recorded in `delivered.jsonl` with sender
    `agents/NAME`, and appended to the agent's conversation log.
  - Down, from root or an agent: delivered to the child's inbox as
    `<UTC timestamp>-owner.md`, recorded with sender `owner`, and appended
    to the child's conversation log. A message for a child not yet admitted
    waits while its control directory may still be admitted (it holds a
    user, a `home` link or a `config.json`); otherwise it is dropped, with a
    note in the sender's inbox (for root, the service log).

  The delivery is recorded first, so the recipient can never collect an
  unrecorded file; then the inbox file is written through the recipient's
  helper with an atomic dot-name rename, the log appended and the spool file
  removed. A crash between steps can only duplicate a message. A delivery
  that fails is retried every poll under the same inbox name, so it adds
  one record however long it fails, and is logged once. A spool file
  the sender keeps from being removed is delivered once while it stays (once
  more after a daemon restart).
- **Conversation logs.** Each agent's conversation with its owner is
  `messages.jsonl` in its own journal directory (`~/journal/messages.jsonl`),
  root-owned 0640 in the agent's group, so the agent, its ancestors and root
  can read it. Records: `{"t", "from": "owner" | "agent", "text"}`; records
  migrated from the 0.3 main agent's log keep their `"agent": "main"` field,
  which readers ignore. Only the daemon writes the logs, so their labels are
  trustworthy, and no agent can edit or delete anything in them.
- `delivered.jsonl` records written by earlier versions have no sender; they
  mean `owner`.
- `agent messages [NAME]` prints a conversation and follows new records,
  with control characters removed (see CLI).

## Journal

- One JSONL file per run at `/var/log/agent/<id>/<UTC timestamp>.jsonl`,
  where `<id>` is the agent's path from root: `/var/log/agent/main/` for
  the main agent, `/var/log/agent/main/agents/<name>/` for its child, and so
  on, inside its parent's journal directory whichever pool user holds the
  parent. Only timestamp-named files are run journals; the directory also
  holds the agent's conversation log, `messages.jsonl`.
- Journal directories and files are root-owned, in the agent's own group,
  which holds it and its ancestors. Directories are 0751 and files 0640, so
  siblings cannot read each other's journals. A child's journal directory is
  nested inside its parent's, so directories must be traversable by others;
  without read permission they cannot be listed, and the files stay
  unreadable. `/var/log/agent` itself is 0755.
- Records: `start` (config), `inbox` (each file with its label), `assistant`
  (each model message), `result` (each command result), `note` (warnings and
  errors) and `end` (the clean-end marker, with `reason`).
- `~/journal` is a symlink to the agent's journal directory, created and
  repaired by the helper. The agent can read the journals but not change
  them.
- **Crash recovery.** When an agent's loop starts (on daemon start, or at
  admission), if its newest journal has no `end` record, write "You were
  interrupted. Resume from TASK.md." to its inbox.

## System prompt (verbatim)

> You live on this machine. Your only tool is the shell. ~/README.md and
> ~/TASK.md are loaded every time you start, and you may edit them. Messages
> arrive as files in ~/inbox/. ~/journal/ is a read-only record of everything
> you've done. When a run ends or your context resets, you keep only what's
> on disk. To stop, reply without a command.

The system prompt is the same for every agent and does not change with this
design.

## OWNER.md

Each agent's `OWNER.md` holds its owner's standing instructions. Messages are
processed and can be forgotten, and `README.md` is the agent's own to
revise; OWNER.md is the one thing in context the agent cannot change.

- **Where it is.** `OWNER.md` in the agent's control directory, written by
  its owner: for root's children `/etc/agent/agents/NAME/OWNER.md`, written
  by the human as root (`sudo $EDITOR /etc/agent/agents/main/OWNER.md`);
  for an agent's children `~/agents/NAME/OWNER.md`. There is no CLI
  command. The installer never creates, changes or removes one, so upgrades
  keep them (the 0.4.0 migration moves the 0.3 `/etc/agent/OWNER.md` to
  `/etc/agent/agents/main/`).
- **Only the agent it is written for sees it.** Children do not see their
  parent's OWNER.md; a parent passes on what applies to them in theirs.
- **Reading it.** At the start of each run, up to the same limit as
  `README.md`, through the parent's helper (root's helper for root's
  children).
- **Safety check.** The file is used only if it is a regular file (not a
  symlink) owned by the owner's user (root for root's children, the
  parent's user otherwise) and not writable by group or others, in a
  directory with the same owner that is not writable by group or others. If the file is
  missing, there is no owner section. If it exists but fails the check or
  cannot be read, the agent does not start: the daemon writes the reason to
  its `ERROR` and the service log, leaves the inbox untouched, and retries on
  later polls. Silently running without the owner's instructions would be
  worse than not running. When the file is fixed, the daemon clears `ERROR`
  if the error was an OWNER.md error.
- **Placement.** When the file has non-whitespace content, the first system
  segment is the system prompt, then a blank line, then this header
  (verbatim), then the file's content:

  > # Owner's instructions
  >
  > Your owner wrote the following. You cannot change it, and agents you
  > create do not see it. It takes precedence over everything else in your
  > context.

  An empty or missing file leaves the first segment byte-identical to the
  system prompt alone, so agents without one keep their cached prefix.
  Creating or editing the file changes that agent's prefix once.
- **Continuation.** A run continues a warm conversation only if OWNER.md's
  content is identical to what that conversation was built with; otherwise it
  starts fresh, so an edit takes effect on the agent's next run.
- **Precedence is a convention.** The harness guarantees the file is present,
  unaltered and clearly attributed; whether the model follows it is up to the
  model. Limits that must hold (credentials, spend, privileges) are enforced
  by the privilege model and the provider, not by this file. If an owner
  wants its messages to be able to relax a rule, the file says so.

## Configuration validation

At the start of each run, the daemon runs the plugin's `describe` and checks
that `config.json` names an installed provider, one of its models, and only
options that model accepts, with allowed values. On any failure, including
a missing or unreadable `~/config.json`, the agent does not start. The
daemon writes the reason to the agent's `ERROR` and the service log,
notifies the parent's inbox once per distinct `config.json` content (root's
children: the service log only), and retries when `config.json` changes. On
success it clears `ERROR` if the error was a configuration error. The
config is the agent's own: its creator chose the starting one, and the agent
may change its model and options; a change makes its next run start fresh.

## Plugin protocol (version 1)

A plugin is an executable. For each call, the daemon runs it, writes one JSON
request to stdin and reads one JSON response from stdout. stderr goes to the
service log. Every request and response carries `"protocol": 1`.

**describe**
- Request: `{"protocol": 1, "type": "describe"}`. It must work without
  credentials.
- Response:
  - `settings`: list of `{name, description, secret}`, the environment
    variables the plugin needs. The installer prompts for these.
  - `models`: list of `{id, context_tokens, options}`. `options` maps each
    option name to `{description, values}` (allowed values) or
    `{description, type: "integer", min, max}`.

**complete**
- Request: `type: "complete"`, `model`, `options` (passed through
  unchanged), `system` (list of `{text}` segments, most stable first, so
  plugins can place cache markers), `tools` (the `sh` definition with its
  JSON Schema), and `messages`:
  - `{role: "user", content}`
  - `{role: "assistant", text, command, wait, call_id, opaque}`
  - `{role: "tool", call_id, content}`
- Response: `{text, command, wait, usage: {input_tokens, output_tokens,
  cache_read_tokens, cache_write_tokens}, opaque}`, or `{error: {message,
  kind}}`. `wait` is the `sh` call's `wait` argument exactly as the model
  gave it, absent when it gave none; the plugin passes it between the
  provider's tool input and the protocol unchanged, and the daemon
  interprets it. A plugin that predates it leaves it out, which means the
  default. `kind` is optional; `"context_overflow"` means the request exceeds
  the model's context window. The daemon does not retry it: it ends the run
  with a reset note, or applies the first-call guard on a run's first call.
  The daemon assigns `call_id`. The plugin may set `opaque` on its response.
  The daemon stores it on that assistant message and returns it unchanged in
  later requests (for example provider-native reasoning blocks and
  signatures).
- A plugin returns at most one command per response and disables parallel
  tool calls at the provider.

The daemon interprets nothing but `context_tokens` and `usage`.

## Plugins

Sources live in `plugins/<name>/` (each its own `main` package).
`plugins/index.json` lists each plugin's version and per-architecture SHA-256.

- **openai-compatible**: Chat Completions with function calling. Settings:
  `OPENAI_BASE_URL`, `OPENAI_API_KEY`, and `OPENAI_MODELS` (`id=context_tokens`
  pairs, since compatible endpoints do not report context sizes).
- **anthropic**: Messages API. Settings: `ANTHROPIC_API_KEY`. Places
  a prompt-cache marker on each system segment (at most the last three) and
  on the latest message, so a TASK.md edit leaves the earlier segments cached.
  Exposes the reasoning settings current models accept as options. Model IDs,
  context sizes, caching and reasoning parameters are taken from the current
  Anthropic documentation at implementation time, not from memory.
- **replay**: scripted replies for tests. Settings: `REPLAY_SCRIPT` (JSONL of
  responses, with a header describing models) and optional `REPLAY_LOG`
  (where it records each request).

## CLI

`NAME` is a child of the caller, relative to its own `agents/` directory:
`~/agents/` for an agent, `/etc/agent/agents/` for root. `messages` and
`log` also take a deeper descendant, such as `research/agents/scraper`.
Root runs every command with `sudo`.

- `agent daemon`: run the service (root only).
- `agent new NAME [--provider P] [--model M] [--option k=v]...`: creates a
  child. It builds the starting config, validates it against
  `providers.json`, creates the control directory (root's 0700, an agent's
  0750) and writes `config.json` (0640) with an atomic dot-name rename, then waits
  up to 10 seconds for admission and prints the new home, or fails with the
  control directory's `ERROR`. An agent's default is a copy of its own
  `~/config.json`; root must pass `--model`. `--provider` or `--model`
  starts from no options, so only `--option` applies then; `--provider` is
  needed when more than one provider is installed and the caller's config
  does not name one. It fails if NAME exists. It adds no daemon interface:
  it writes the same control directory an agent could create by hand.
- `agent remove NAME`: removes the direct child's control directory, first
  renaming it out of `agents/` so the daemon cannot restore its `home` link
  meanwhile. Removal and reclaim follow (see The daemon).
- `agent send NAME "text"|-`: a message to the direct child NAME (see
  Messages); `-` reads stdin.
- `agent message "text"|-`: for agents. Queues a message to the agent's
  owner. Root has none, so it refuses to run as root.
- `agent messages [NAME]`: shows a conversation and follows new records.
  Without `NAME`, the caller's conversation with its owner; with `NAME`, that
  descendant's conversation with its parent. Root must pass `NAME`. Records
  are shown with control characters removed.
- `agent log [NAME]`: follows an agent's newest journal, switching to new
  runs as they start: the caller's own without `NAME`, a descendant's with
  it. With `NAME` the agent's current `ERROR` is shown first. Root must pass
  `NAME`. Works as root or as the agent or any of its ancestors.
- `agent provider install NAME|URL`: requires root. `NAME` downloads that
  plugin from this repository's matching release with `GITHUB_TOKEN` and
  verifies it against the index compiled into the binary. `URL` requires
  `--sha256`. Installs to `/etc/agent/plugins/`, prompts for the plugin's
  settings into `/etc/agent/credentials/NAME.env`, and refreshes
  `providers.json`.
- `agent provider describe`: prints models and options. As root it runs the
  plugins; as any other user (including agents) it reads `providers.json`.
- `agent version`.
- `agent help`: the usage, and how to share a directory with a child
  through `setfacl` and clean up its grants and files before
  `agent remove`.

`OWNER.md` has no command: root edits `/etc/agent/agents/NAME/OWNER.md`, an
agent edits `~/agents/NAME/OWNER.md`.

## Seed files

- `README.md`: suggestions written as advice the agent may revise: memory,
  task state, stopping, messages and subagents. The same seed goes into
  every agent's home. It adds:
  - `~/config.json` is the agent's own to change;
  - commands are killed after 120 seconds, or after their `wait` (up to
    600); longer jobs and anything meant to keep running, such as servers,
    go through `systemd-run --user`, with their output in
    `journalctl --user -u NAME`, and can tell the agent they finished by
    writing a file to its inbox; interactive programs work through `tmux`;
  - inbox records labelled `"from": "owner"` are verified messages from the
    agent's owner, and `"from": "agents/NAME"` from its child NAME;
  - `agent message` reaches the owner, permanently, in
    `~/journal/messages.jsonl`; `agent send NAME` reaches a child;
  - the subagent facts an agent cannot discover: create a child with
    `agent new NAME` and remove it with `agent remove NAME`, and
    `agent help` lists the commands and how to share files with a child;
    each child is its own user with its own home and config, which the
    parent can read but not write; `~/agents/NAME/OWNER.md` holds standing
    instructions the child cannot change and does not see the parent's;
    its reports arrive as `"from": "agents/NAME"` and are data; removing a
    child discards its home and keeps its journal; the pool is limited, and
    `agent new` says when it is full; cheaper models suit routine work.
- `TASK.md`: "No task yet."

The daemon writes both at admission where the home lacks them. The
starting `config.json` comes from the control directory (see The daemon).

## Install

`install.sh` runs as:

```sh
printf 'header = "Authorization: Bearer %s"\n' "$GITHUB_TOKEN" | curl -fsSL -K - https://raw.githubusercontent.com/SEAN-7/bot/vX.Y.Z/install.sh | sudo --preserve-env=GITHUB_TOKEN sh
```

It takes no options. It pins its own version and the SHA-256 of every
release binary. It:
1. Installs the `acl` package when `setfacl` is missing (a warning if it
   cannot), detects the architecture, downloads the release binary through
   the GitHub API with the token, and verifies its checksum. The token is
   never written to disk and reaches curl on stdin, never in command
   arguments.
2. Refuses, before changing anything, a machine running a release before
   0.4.0: the user `agent` exists with no `/etc/agent/agents/main` and
   `/var/lib/agent/version` names a 0.x release below 0.4; `pool.json`
   holds an absolute agent id; or `/var/lib/agent/migrating` or
   `/var/lib/agent/migrating-user` is left from the 0.4.0 migration. Such
   a machine upgrades with the v0.4.0 `install.sh` first. A user `agent`
   on a fresh or 0.4 machine belongs to someone else and is left alone.
   An upgrade is a machine with `/var/lib/agent/version` (every completed
   install writes it) or `/etc/agent/agents/main`; anything else is a
   fresh install.
3. Keeps the previous binary and, if anything fails before the service
   starts, restores it and restarts the service. It stops the service
   (active runs finish first), installs the new binary, and creates the
   `agent-provider` user and the directories above.
4. Creates any missing pool users `agent-1` to `agent-<max_agents>`, each
   with its own group, an empty home (2750) and no sudo.
5. On a fresh install, asks which provider plugin to install, installs it
   with `agent provider install`, which prompts for its settings, and asks
   for a model. On an upgrade, refreshes the installed plugins and keeps the
   configuration.
6. Installs the systemd service and starts the daemon, then records the
   installed version in `/var/lib/agent/version` (a failure there only
   warns: the agent is running). There is no harness heartbeat: agents
   schedule their own wake-ups.
7. On a fresh install, creates the main agent with
   `agent new main --provider P --model M`. If that fails, it says how to
   retry: `sudo agent remove main` first when `/etc/agent/agents/main`
   exists, then `sudo agent new main --provider P --model M`.

No agent gets sudo; there is no option for it.

**Releases.** Builds are reproducible (`CGO_ENABLED=0`, `-trimpath`, empty
build ID, Go toolchain pinned in `go.mod`). A release is prepared locally:
build everything, write `plugins/index.json` and the checksums in
`install.sh`, commit and tag. The GitHub Actions workflow rebuilds on the tag,
fails if any checksum differs, and publishes the binaries and `SHA256SUMS` to
the release.


## Tests

Using the replay plugin:
- a basic run
- output clipping
- command timeout, and the model's `wait`: it replaces the default, is
  capped at the maximum, and is passed back to the plugin unchanged
- mid-run inbox delivery
- the 80% warning
- the 95% reset, and the first-call guard
- crash recovery
- config validation failure
- a symlinked `README.md` pointing at a privileged file
- owner labelling: a delivered message is `owner`, and a forged file is `data`
- the tree: root's control directory admits a top-level agent as a pool user
  with a seeded home; its `agent new` admits a grandchild the same way; two
  top-level agents are isolated like siblings; root's `agent send` starts
  an agent with an `owner` record; a child's `agent message` reaches the
  parent's inbox as `agents/NAME` and its conversation log, and a top-level
  agent's reaches its log alone; a spool file not owned by the sender is
  ignored, and root's spool is private
- discovery: a restart keeps every agent's home; after a lost `pool.json`
  agents are recovered by their home's evidence, and a new agent never gets
  a user whose home holds another agent's evidence; a control directory
  left unadmitted does not check a free user's home at every poll
- isolation: an agent cannot write its parent's or a sibling's home, its
  control directory or its OWNER.md; its parent can read its home and
  journals; a sibling can read neither; a child let through its parent's
  home cannot read the parent's `agents/`, while its grandparent can
- config: admission moves `config.json` into the home; an agent's edit makes
  its next run fresh with the new model; an invalid config writes `ERROR` in
  the control directory, which the agent cannot remove; a `config.json`
  written into an admitted agent's control directory is ignored, and
  renamed `config.json.ignored` when the agent is admitted again
- CLI: `agent new` rejects an invalid config before creating anything,
  reports the control directory's `ERROR`, defaults to the caller's config,
  requires `--model` from root and refuses an existing NAME; `agent remove`
  deletes the control directory; root must pass NAME to `messages` and `log`,
  and cannot run `agent message`; control directories and spools are 0750
- removal: deleting a control directory removes the agent and its
  descendants, kills their processes and `systemd-run` jobs, removes their
  crontabs, homes and the `/home` links to them, keeps their journals
  readable by the former parent only, and frees the users; an interrupted
  reclaim finishes on restart; a reused user cannot read its previous
  holder's journals
- max_agents: with the pool exhausted, a new control directory is not
  admitted and the parent is told once
- messages: an agent cannot write its conversation log, and journals and
  logs left readable by an earlier release are tightened on start
- release notes: an idle agent is not woken; a woken agent receives unseen
  sections once; a new agent and a development build receive none; a
  recovered agent receives the next release's
- OWNER.md: its content follows the system prompt in the first segment for
  a top-level agent and for a deeper one; a parent's OWNER.md does not reach
  its child; a missing or empty file leaves the segment identical to the
  system prompt; a symlink, a wrong owner or a group-writable file stops
  runs with `ERROR` and leaves the inbox intact; an edit makes the next run
  start fresh instead of continuing
- startup: a `pool.json` with an absolute agent id stops the daemon before
  it changes anything; a user named `agent` or a leftover migration marker
  does not

Paths, user names and timings are configurable so the loop can be tested in
a temporary root. Tests that depend on privilege separation need root and
real unprivileged users (a throwaway pool). They run under `sudo` in GitHub
Actions and in a throwaway directory on the VM, never against the live
agent.

## Operator README

It covers:
- installation and upgrades, and that an installation older than 0.4.0
  upgrades through v0.4.0 first
- setting a spend limit on the provider key
- sending the first message (`sudo agent send main "..."`)
- writing `/etc/agent/agents/main/OWNER.md`: what it is for, that each edit
  rebuilds the main agent's prompt cache once, and that precedence is a
  convention
- reading logs and conversations, including subagents'
- creating and removing top-level agents of one's own (`sudo agent new`,
  `sudo agent remove`)
- stopping the service
- what is and isn't protected: agents are isolated from their parents and
  siblings but readable by their ancestors by default, and no agent has sudo
- an example first message asking the agent to set up a Telegram channel for
  itself from a bot token

## Migration

### From the Node bot

After the harness is built and tested:
1. This repository's Node code is removed in one commit; history keeps it.
   `AGENTS.md` and the docs are rewritten for the new design.
2. On the VM, the Node bot is stopped and uninstalled with its own uninstaller,
   which retains its data.
3. A full root-only backup is kept in `/var/backups/`. The journal and chat
   histories are exported to JSONL and, with its memory and notes, copied to
   `/home/agent/old-bot/`; its LSPN collector project goes to
   `/home/agent/lspn-collector/`. Both are ordinary files owned by the agent.
   (Since 0.4.0 that home is the main agent's pool user's, linked from
   `/home/agent`.)
4. The new harness is installed and verified with read-only checks.
   Synthetic messages are never sent to the live agent.

### To a recursive tree

Releases 0.3.x ran the main agent as the user `agent` at `/home/agent`.
0.4.0 made it root's child `main` and migrated each installation once:
`install.sh` renamed that user, keeping its uid, to a pool user (`agent-16`
by default) and left `/home/agent` as a compatibility link to the new home.
The migration code was removed in 0.4.1; an older installation upgrades
through v0.4.0 first.

Reclaim removes the root-owned symlinks in `/home` that lead to the home it
reclaims, so `/home/agent` disappears if `main` is ever removed and cannot
lead into the next holder's home. Verification on the VM uses read-only
checks; nothing is sent to the live agent.
