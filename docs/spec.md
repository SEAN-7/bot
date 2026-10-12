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
   `TASK.md` and `inbox/`. The main agent is the user `agent` at
   `/home/agent`. A subagent is created by its parent writing a control
   directory `~/agents/NAME/` (`OWNER.md` and `config.json`); the harness
   gives it a user from a pool and a home of its own.
2. Everything that happens is a file in an inbox: owner messages, schedules,
   heartbeats, child reports, crash recovery and context resets.
3. The harness has no provider code. Model access is a plugin.

## Decisions made during design

- **No `max_steps`.** The agent works for as long as it decides is necessary.
  Context resets bound each run; the provider spend limit bounds money.
- **Every agent has one owner.** The main agent's owner is the human; a
  subagent's owner is the agent that created it. An agent talks only with its
  owner and its own children, and only the main agent reaches the human.
- **Owner messages are verified by the harness.** The human's `agent send`
  requires root; a parent's `agent send NAME` is identified by its uid. The
  daemon labels the messages it delivered with their sender and every other
  inbox file as `"from": "data"`, using structure the file content cannot
  forge (see Context).
- **Each subagent is its own Unix user.** Users come from a fixed pool
  created at install and are reused. A subagent cannot change its parent's
  or siblings' files or its own owner's instructions. Its ancestors can read
  its home by default (a courtesy the subagent could revoke) and always read
  its journals (a guarantee, since only root writes them).
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
  working files in the agent's home (see Migration); the harness and seed
  files know nothing about them.
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
| `/etc/agent/` | root, 0711 (traversable, not listable) | `settings.json` (0600), `credentials/` (0700), `OWNER.md` (0644) |
| `/etc/agent/OWNER.md` | root, 0644 | The human's standing instructions for the main agent; optional |
| `/etc/agent/credentials/NAME.env` | root, 0600 | Plugin settings and secrets as `KEY=value` lines |
| `/etc/agent/plugins/` | root:agent-provider, 0750 | Plugin executables, root:agent-provider 0750; no agent user can read or run them |
| `/var/lib/agent/owner/` | root, 0700 | Spool for the human's `agent send` |
| `/var/lib/agent/delivered.jsonl` | root, 0600 | Record of messages delivered (agent path, file name, SHA-256, sender) |
| `/var/lib/agent/pool.json` | root, 0600 | Pool assignments: user, agent path, state |
| `/var/lib/agent/providers.json` | root, 0644 | Cached `describe` output with no secrets, for `agent provider describe` |
| `/var/log/agent/` | root, 0755 | Journals (see Journal) |
| `/var/log/agent/messages.jsonl` | root:agent, 0640 | The human's conversation with the main agent |
| `/home/agent/` | agent, 0750 | The main agent's home |
| `/home/agent-N/` | agent-N:agent-N, 2750 | A pool user's home: a subagent's home while assigned, empty while free |

`settings.json` holds `max_agents` (default 16) and `repository`
(`SEAN-7/bot`). The pool holds `max_agents - 1` users, `agent-1` upwards.

In an agent's home, the harness uses `README.md`, `TASK.md`, `inbox/`,
`inbox/processed/`, `agents/`, `.messages/`, `.outputs/`, `.reset` and the
`journal` symlink. The main agent's home also holds its `config.json` and
`ERROR`.

A subagent's control directory, `<parent home>/agents/<name>/`, belongs to
the parent. The harness uses `OWNER.md`, `config.json`, `ERROR` and the
`home` symlink (pointing at the subagent's home). The subagent cannot read
or change it.

Everything else belongs to the agents.

## Privilege model

The daemon runs as root. Each agent is an unprivileged user: `agent` for the
main agent, a pool user `agent-N` for each subagent.

**Groups.** Each agent user has its own group. The daemon makes every
ancestor of a subagent a member of that subagent's group (in `/etc/group`,
kept current as the tree changes). A subagent's home is setgid to its group
and commands run with umask 022, so by default its ancestors can read what
it writes, and nobody else can traverse its home. A subagent can tighten its
own files' modes; its journals stay readable by its ancestors regardless.
Processes started before a membership change (an ancestor's user manager,
for example) see the new group only after they restart; commands the
harness runs always get current groups.

**All file access inside agent homes and control directories goes through
unprivileged helpers.** The daemon starts one long-lived helper process per
agent user: the same binary (`agent _fsio`), launched with that user's uid,
gid and supplementary groups, driven by JSON lines over a pipe. A home is
accessed through its agent's helper; a control directory through its
parent's. The kernel therefore applies that user's permissions to each
operation, and a planted symlink can reach only what that user could reach.
The daemon never opens a path inside an agent home or control directory as
root. (Go cannot safely change identity in part of a running process, so a
separate process is the reliable way to do this.) If a helper dies, the
daemon restarts it; operations in flight fail and the affected run is
retried.

Helper operations: `scan` (find control directories), `list` (inbox entries
with type and size), `read` (path, byte limit), `write` (atomic:
dot-prefixed temporary file then rename), `rename`, `mkdir`, `remove`,
`removeall` (recursive, never following symlinks), `symlink` (create or
replace), `stat` (size, mtime, type, mode and owner uid, without following
symlinks for the final component).

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
more.

**Plugins** run as the dedicated system user `agent-provider`, with no login
and no home directory. The daemon reads `/etc/agent/credentials/NAME.env` and
passes those variables in the plugin's environment, along with a minimal
`PATH`. Neither the plugin nor any agent reads `/etc/agent` directly, and an
agent user cannot read another user's process environment.

## The daemon

`agent daemon` runs as one systemd service (`Restart=always`, default
`KillMode`) serving the whole tree.

- Startup: require root, an existing `agent` user and the pool users; finish
  any reclaim left in progress (see Reclaim); start the helpers; run crash
  recovery; then poll about once a second.
- **Discovery.** The main agent is `/home/agent`. A control directory is a
  real directory (not a symlink) at `<agent home>/agents/<name>`, owned by
  that agent's user, that contains a regular file `config.json`. The scan
  runs through each agent's own helper, level by level, and never follows
  symlinks.
- **Admission.** For a new control directory, the daemon takes the lowest
  free pool user and records the assignment in `pool.json`, then: sets the
  members of that user's group to the subagent's ancestors; seeds its home
  with the seed `README.md`, `TASK.md` and `inbox/` (through its helper);
  enables lingering; creates its journal directory; and writes the `home`
  symlink into the control directory (through the parent's helper). An
  agent's identity is its control directory's path as seen from the main
  agent (see Journal): renaming the directory removes the subagent and
  admits a new one.
- **max_agents.** When no pool user is free, a new control directory is not
  admitted. The daemon writes the reason to its `ERROR` and the service log,
  and notifies the parent's inbox once.
- One goroutine loop per agent, one run at a time per agent.
- **Graceful stop.** On SIGTERM the daemon starts no new runs and delivers no
  new inbox files; active runs finish, up to 15 minutes, before it exits.
  Runs still going at the deadline are interrupted and left unclean, so the
  next start resumes them. The unit uses `KillMode=mixed` and
  `TimeoutStopSec=17min`, so systemd signals only the daemon until it exits.
- **Removal.** When a control directory disappears, its subagent and all of
  its descendants are removed, deepest first. Each loop kills any running
  command's process group, writes the end marker (`reason: "removed"`) and
  exits; then its user is reclaimed. Its journals remain. Only the control
  directory's disappearance removes a subagent: while it exists, a missing or
  renamed `config.json` just keeps the subagent from starting runs.
- **Reclaim.** The daemon marks the user `reclaiming` in `pool.json`, then:
  disables lingering; removes its crontab; stops its user manager and kills
  every remaining process of the uid until none is left; removes its
  home's contents and its files in `/tmp`, `/var/tmp` and `/dev/shm`
  (through its helper, after its processes are gone); clears its group's
  members; and moves its journal directory's group to the parent's group,
  so the next holder of the user cannot read them. Only then is the user
  marked free. A reclaim interrupted by a crash is finished on the next
  start, before the user can be assigned again. A failed reclaim is retried
  every poll, and a user's reclaim waits until every descendant's user has
  been reclaimed, since their groups still list it.
- **Release notes.** On start, if `/var/lib/agent/version` differs from the
  running version, the daemon writes the sections of its embedded release
  notes (`internal/notes/notes.md`) newer than the recorded version, up to the
  running one, to the main agent's inbox as one `<time>-harness-update.md`,
  then records the version. With no record, it delivers every section up to
  the running version. Fresh installs record the version at install time.
  Development builds deliver nothing.
- **Message spools.** See Messages.

## The loop (per agent)

**Wake** when `inbox/` contains a regular file whose name does not start with
a dot.

**Start of run.**
1. Read `config.json` (`provider`, `model`, `options`) and validate it against
   the provider's `describe` (see Configuration validation). On failure, do
   not start and leave the inbox untouched.
2. Read `OWNER.md` (see OWNER.md); if it is unsafe, do not start and leave
   the inbox untouched. Read `README.md` and `TASK.md` (missing files are
   empty).
3. Collect inbox files in name order, skipping dot-files and directories.
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

**Steps.** Call the model with the single tool `sh(command)`.
- A reply with a command: run it with a 120-second timeout. On timeout, kill
  the process group with SIGKILL and say so, suggesting `systemd-run --user`
  for long jobs. Combined stdout and stderr plus the exit status are returned.
  Output over 16 KiB is clipped to its first and last 8 KiB, and the full text
  is saved to `~/.outputs/<run>-<step>.txt` (through the helper) with the path
  included in the result.
- A reply with no command ends the run (`reason: "done"`).

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

**ERROR** lives beside the agent's `config.json`: `~/ERROR` for the main
agent, the control directory's `ERROR` for a subagent, so a parent sees its
children's errors and a subagent cannot hide them.

## Messages

An agent talks with its owner and its own children through the harness, the
same way in both directions. Nothing reaches the human except from the main
agent.

- **Up.** `agent message "text"` (run by any agent) writes the text to
  `~/.messages/<UTC timestamp>.md` with an atomic dot-name rename.
- **Down.** `agent send NAME "text"`, run by an agent, writes it to
  `~/.messages/agents/NAME/<UTC timestamp>.md` the same way. `NAME` must be a
  direct child (`~/agents/NAME/` exists). Run by root without `NAME`, it is
  the human's message to the main agent, written to `/var/lib/agent/owner/`.
  Root cannot send to a subagent.
- Messages over 64 KiB are refused, with advice to send a file path instead.
- **Delivery.** Each poll, for every admitted agent, the daemon reads its
  spool through its helper: regular, non-dot files in name order, at most
  64 KiB each, owned by that agent's uid (anything else is ignored and
  logged). The spool's location and owner identify the sender.
  - Up from the main agent: appended to `/var/log/agent/messages.jsonl`. It
    does not wake anyone: the human reads the log.
  - Up from a subagent: delivered to its parent's inbox as
    `<UTC timestamp>-NAME.md`, recorded in `delivered.jsonl` with sender
    `agents/NAME`, and appended to the subagent's conversation log.
  - Down to a subagent: delivered to the child's inbox as
    `<UTC timestamp>-owner.md`, recorded with sender `owner`, and appended
    to the child's conversation log. A message for a child not yet admitted
    waits; one for a child that no longer exists is dropped, with a note in
    the sender's inbox.
  - From the human's spool: delivered to `/home/agent/inbox/` as
    `<UTC timestamp>-owner.md`, recorded with sender `owner`, and appended to
    `/var/log/agent/messages.jsonl`.

  The delivery is recorded first, so the recipient can never collect an
  unrecorded file; then the inbox file is written through the recipient's
  helper with an atomic dot-name rename, the log appended and the spool file
  removed. A crash between steps can only duplicate a message. A spool file
  the sender keeps from being removed is delivered once while it stays (once
  more after a daemon restart).
- **Conversation logs.** The human's conversation with the main agent is
  `/var/log/agent/messages.jsonl` (root:agent 0640). A subagent's
  conversation with its parent is `messages.jsonl` in the subagent's journal
  directory, readable by the subagent and its ancestors. Records:
  `{"t", "from": "owner" | "agent", "text"}` (the main agent's log keeps its
  `"agent": "main"` field). Only the daemon writes the logs, so their labels
  are trustworthy, and no agent can edit or delete anything in them.
- `delivered.jsonl` records written by earlier versions have no sender; they
  mean `owner`.
- `agent messages [NAME]` prints a conversation and follows new records,
  with control characters removed (see CLI).

## Journal

- One JSONL file per run at `/var/log/agent/<agent path>/<UTC timestamp>.jsonl`,
  where `<agent path>` is the agent's path as seen from the main agent: the
  main agent's home, then `/agents/<name>` for each level. The main agent's
  journals are under `/var/log/agent/home/agent/`, a child's under
  `/var/log/agent/home/agent/agents/<name>/`, and a grandchild's under
  `/var/log/agent/home/agent/agents/<name>/agents/<name>/`, inside its
  parent's journal directory whichever pool user holds the parent. Only
  timestamp-named files are run journals.
- Journal directories and files are root-owned. The main agent's are group
  `agent`; a subagent's are its own group, which holds it and its ancestors.
  Directories are 0751 and files 0640, so siblings cannot read each other's
  journals. A child's journal directory is nested inside its parent's, so
  directories must be traversable by others; without read permission they
  cannot be listed, and the files stay unreadable. Directories above them
  are 0755.
- Records: `start` (config), `inbox` (each file with its label), `assistant`
  (each model message), `result` (each command result), `note` (warnings and
  errors) and `end` (the clean-end marker, with `reason`).
- `~/journal` is a symlink to the agent's journal directory, created and
  repaired by the helper. The agent can read the journals but not change
  them.
- **Crash recovery.** On daemon start, for any agent whose newest journal has
  no `end` record, write "You were interrupted. Resume from TASK.md." to its
  inbox.

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

- **Where it is.** For the main agent, `/etc/agent/OWNER.md`, written by the
  human as root (`sudo $EDITOR /etc/agent/OWNER.md`). For a subagent,
  `OWNER.md` in its control directory, written by its parent. There is no CLI
  command. The installer never creates, changes or removes either, so
  upgrades keep them.
- **Only the agent it is written for sees it.** Subagents do not see their
  parent's OWNER.md; a parent passes on what applies to them in theirs.
- **Reading it.** At the start of each fresh run, up to the same limit as
  `README.md`. The main agent's is read directly as root (it is outside every
  agent home); a subagent's through its parent's helper.
- **Safety check.** The file is used only if it is a regular file (not a
  symlink) owned by the owner's user (root for the main agent, the parent's
  user for a subagent) and not writable by group or others, in a directory
  with the same owner that is not writable by group or others. If the file is
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
- **With `--sudo`** the main agent can rewrite `/etc/agent/OWNER.md`, and for
  the main agent this becomes a convention like the other protections.
  Subagents never get sudo.

## Configuration validation

At the start of each run, the daemon runs the plugin's `describe` and checks
that `config.json` names an installed provider, one of its models, and only
options that model accepts, with allowed values. On any failure the agent
does not start. The daemon writes the reason to the agent's `ERROR` and the
service log, notifies the parent's inbox (for a subagent) once per distinct
`config.json` content, and retries when `config.json` changes. On success it
clears `ERROR` if the error was a configuration error. A subagent's
`config.json` is in its control directory, so its model and options are its
parent's choice.

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
  - `{role: "assistant", text, command, call_id, opaque}`
  - `{role: "tool", call_id, content}`
- Response: `{text, command, usage: {input_tokens, output_tokens,
  cache_read_tokens, cache_write_tokens}, opaque}`, or `{error: {message,
  kind}}`. `kind` is optional; `"context_overflow"` means the request exceeds
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

`NAME` below is a subagent's path relative to the caller's `~/agents/`
(for example `research` or `research/agents/scraper`); for root it is
relative to `/home/agent/agents`.

- `agent daemon`: run the service.
- `agent send "text"`: as root, the human's message to the main agent.
- `agent send NAME "text"`: as an agent, a message to its direct child.
- `agent message "text"`: for agents. Queues a message to the agent's owner.
- `agent messages [NAME]`: shows a conversation and follows new records.
  Without `NAME`, the caller's conversation with its owner (for root, the
  human's conversation with the main agent); with `NAME`, that subagent's
  conversation with its parent.
- `agent log [NAME]`: follows an agent's newest journal, switching to new
  runs as they start; without `NAME`, the caller's own (for root, the main
  agent's). The agent's current `ERROR` is shown first. Works as root or as
  the agent or any of its ancestors.
- `agent provider install NAME|URL`: requires root. `NAME` downloads that
  plugin from this repository's matching release with `GITHUB_TOKEN` and
  verifies it against the index compiled into the binary. `URL` requires
  `--sha256`. Installs to `/etc/agent/plugins/`, prompts for the plugin's
  settings into `/etc/agent/credentials/NAME.env`, and refreshes
  `providers.json`.
- `agent provider describe`: prints models and options. As root it runs the
  plugins; as any other user (including agents) it reads `providers.json`.

## Seed files

- `README.md`: the suggestions from the brief, written as advice the agent
  may revise: memory, task state, stopping, messages and subagents. The same
  seed goes into every agent's home. It adds:
  - long-running jobs go through `systemd-run --user`;
  - inbox records labelled `"from": "owner"` are verified messages from the
    agent's owner, and `"from": "agents/NAME"` from its child NAME;
  - `agent message` reaches the owner, permanently;
  - creating a subagent: write `~/agents/NAME/config.json` (and optionally
    `OWNER.md`), then `agent send NAME "task"`; its home is at
    `~/agents/NAME/home`, readable by default; removing or renaming the
    control directory stops it and discards its home; share files with it
    through `setfacl`.
- `TASK.md`: "No task yet."
- `config.json`: the provider, model and options chosen at install (main
  agent only).

## Install

`install.sh` runs as:

```sh
printf 'header = "Authorization: Bearer %s"\n' "$GITHUB_TOKEN" | curl -fsSL -K - https://raw.githubusercontent.com/SEAN-7/bot/vX.Y.Z/install.sh | sudo --preserve-env=GITHUB_TOKEN sh
```

It pins its own version and the SHA-256 of every release binary. It:
1. Detects the architecture, downloads the release binaries through the
   GitHub API with the token, and verifies their checksums. The token is never
   written to disk and reaches curl on stdin, never in command arguments.
   On upgrade it keeps the previous binary and restores it, restarting the
   service, if anything fails.
2. Creates the `agent` and `agent-provider` users, the pool users `agent-1`
   to `agent-<max_agents - 1>` with empty homes (2750) and no sudo, the
   directories above, and `/home/agent` with the seed files. An upgrade
   creates any missing pool users. It installs the `acl` package when
   `setfacl` is missing (a warning if it cannot).
3. Asks which provider plugin to install, installs it with
   `agent provider install`, prompts for its settings and model, and writes
   `config.json`.
4. Enables lingering for `agent` and installs the systemd service. There is
   no harness heartbeat: the agent schedules its own wake-ups (an upgrade
   removes the heartbeat entry earlier releases added).
5. Starts the daemon.

`--sudo` grants the main agent's user passwordless sudo (default: none). It
prints a warning that the credential, journal, owner-message and OWNER.md
protections become conventions for the main agent, and that the spend limit
must be set at the provider.

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
- command timeout
- mid-run inbox delivery
- the 80% warning
- the 95% reset, and the first-call guard
- crash recovery
- config validation failure
- a symlinked `README.md` pointing at a privileged file
- owner labelling: a delivered message is `owner`, and a forged file is `data`
- subagents: creating a control directory admits a subagent as a pool user
  with a seeded home; `agent send` starts it with an `owner` record; its
  `agent message` reaches the parent's inbox as `agents/NAME` and both
  conversation logs; a spool file not owned by the sender is ignored
- isolation: a subagent cannot write its parent's or a sibling's home, its
  control directory or its OWNER.md; its parent can read its home and
  journals; a sibling can read neither
- removal: deleting a control directory removes the subagent and its
  descendants, kills their processes and `systemd-run` jobs, removes their
  crontabs and homes, keeps their journals readable by the former parent
  only, and frees the users; an interrupted reclaim finishes on restart;
  a reused user cannot read its previous holder's journals
- max_agents: with the pool exhausted, a new control directory is not
  admitted and the parent is told once
- messages: the main agent's `agent message` and a human message reach
  `/var/log/agent/messages.jsonl` with correct labels; no agent can write
  any conversation log, and subagents cannot read the human's
- OWNER.md: its content follows the system prompt in the first segment for
  the main agent and for a subagent (from its control directory); a parent's
  OWNER.md does not reach its child; a missing or empty file leaves the
  segment identical to the system prompt; a symlink, a wrong owner or a
  group-writable file stops runs with `ERROR` and leaves the inbox intact;
  an edit makes the next run start fresh instead of continuing

Paths, user names and timings are configurable so the loop can be tested in
a temporary root. Tests that depend on privilege separation need root and
real unprivileged users (a throwaway main user and pool). They run under
`sudo` in GitHub Actions and in a throwaway directory on the VM, never
against the live agent.

## Operator README

It covers:
- installation
- setting a spend limit on the provider key
- sending the first message
- writing `/etc/agent/OWNER.md`: what it is for, that each edit rebuilds the
  main agent's prompt cache once, and that precedence is a convention
- reading logs and conversations, including subagents'
- stopping the service
- what is and isn't protected: subagents are isolated from their parents and
  siblings but readable by their ancestors by default, and the effect of
  `--sudo`
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
4. The new harness is installed and verified with read-only checks.
   Synthetic messages are never sent to the live agent.

### To a user per subagent

Earlier releases ran every subagent as `agent`, in a directory under
`~/agents/` that held its whole state. The upgrade converts each one; the
code for it lives in `install.sh` and the hidden `agent _migrate` command,
and can be removed once no installation predates it.
1. The installer stops the daemon (active runs finish as usual; the unit is
   stopped whenever it exists, so a restarting one cannot race the
   migration), creates the pool users and, if any
   `/home/agent/agents/*/inbox` exists beside no `home` link, takes a
   root-only backup of `/home/agent/agents/` in `/var/backups/` and runs
   `agent _migrate`. If it fails before moving anything (exit status 1),
   the previous version is restored. Once files have moved (exit status 2,
   or an earlier run's marker `/var/lib/agent/migrating` remains), the
   previous version never runs again: the new binary stays, the agent stays
   stopped, and rerunning `install.sh` finishes the migration. The backup,
   of the untouched tree, is taken only by the first run and stays.
2. For each legacy subagent directory (one containing `inbox/`), parents
   before children: assign a pool user; move everything except
   `config.json` into its home with a rename (never following symlinks);
   change the owner of the moved files that `agent` owned to the new user;
   leave `config.json` in the control directory; record the assignment.
   A legacy directory and its `config.json` must belong to `agent`.
   Every check and assignment comes before the first move, and each
   `inbox/` moves last, deepest first, so a run that fails part way leaves
   the top-level `inbox/` in place and the next run finishes the job.
   The subagent's `README.md` becomes its own, and it has no OWNER.md until
   its parent writes one. Its journals keep their path.
3. The daemon starts, finds the assignments, and completes admission
   (groups, lingering, the `home` symlink).
4. Things the old layout allowed stop working and are left to the agents:
   a subagent can no longer write files in its parent's home (the parent
   grants access with `setfacl` or moves the work), and scripts that drop
   files into a child's old `inbox/` must use `agent send` instead. The
   release notes for the version state these facts, so the main agent
   learns them on its next run.
5. Verification uses read-only checks. Synthetic messages are never sent to
   the live agent.
