# Agent harness: design spec

A minimalist, always-on LLM agent harness for a Linux VM, written in Go with
the standard library only. It ships as static binaries for linux/amd64 and
linux/arm64. It replaces the Node bot that previously lived in this repository.

The harness provides a few guarantees and the agent provides all policy. The
agent has one tool, the shell, and builds every other capability itself
(memory, tools, scheduling, messaging) as files on the VM. If something could
be a script the agent writes, it stays out of the harness.

## Three ideas

1. An agent is a directory containing `README.md`, `TASK.md`, `config.json`
   and `inbox/`. The main agent is `/home/agent`. Subagents are directories
   under any agent's `agents/` folder, with the same layout.
2. Everything that happens is a file in an inbox: owner messages, schedules,
   heartbeats, child reports, crash recovery and context resets.
3. The harness has no provider code. Model access is a plugin.

## Decisions made during design

- **No `max_steps`.** The agent works for as long as it decides is necessary.
  Context resets bound each run; the provider spend limit bounds money.
- **Owner messages are verified by the harness.** `agent send` requires root.
  The daemon labels the messages it delivered as `"from": "owner"` and every
  other inbox file as `"from": "data"`, using structure the file content
  cannot forge (see Context).
- **Long-running jobs use the agent user's own systemd.** The service keeps
  systemd's default kill behaviour, so stopping it stops everything it
  started. Lingering gives the `agent` user a persistent user manager; the
  daemon sets `XDG_RUNTIME_DIR` so `systemd-run --user` works from commands.
- **Quick follow-ups continue the conversation.** A message within 4 minutes
  of a run ending continues it with a warm cache; the agent can force a fresh
  start with `~/.reset`.
- **No daily heartbeat.** The agent wakes on messages and on wake-ups it
  schedules itself.
- **Subagents are not isolated from each other.** All agents run as the one
  `agent` user. Subagents divide work; they are not a security boundary.
- **Provider failures are visible but never touch the inbox.** Retries back
  off to a 10-minute ceiling; persistent failures are written to `~/ERROR` and
  the service log and shown by `agent log`.
- **The repository stays private.** Installation and plugin downloads
  authenticate with a `GITHUB_TOKEN` that is never stored on the VM.
- **The old Node bot is retired.** Its history and projects become ordinary
  working files in the agent's home (see Migration); the harness and seed
  files know nothing about them.
- **A built-in, append-only owner conversation.** `agent message` lets an
  agent write to its owner; the daemon records both directions in a
  root-owned log the agent can read but not change (see Messages). It is
  pull, not push: alerts remain something the agent builds.
- **Standing owner instructions.** `/etc/agent/OWNER.md` is written by the
  owner, loaded first on every run of every agent, and cannot be changed by
  any agent (see OWNER.md). The harness guarantees that it is present,
  unaltered and labelled as the owner's. Precedence is stated to the model,
  not enforced: hard limits belong in the privilege model, not in this file.

## Filesystem layout on the VM

| Path | Owner, mode | Contents |
| --- | --- | --- |
| `/usr/local/bin/agent` | root, 0755 | The harness binary |
| `/etc/agent/` | root, 0711 (traversable, not listable) | `settings.json` (0600), `credentials/` (0700), `OWNER.md` (0644) |
| `/etc/agent/OWNER.md` | root, 0644 | The owner's standing instructions; optional, written by the owner |
| `/etc/agent/credentials/NAME.env` | root, 0600 | Plugin settings and secrets as `KEY=value` lines |
| `/etc/agent/plugins/` | root:agent-provider, 0750 | Plugin executables, root:agent-provider 0750; the agent user cannot read or run them |
| `/var/lib/agent/owner/` | root, 0700 | Spool for `agent send` |
| `/var/lib/agent/delivered.jsonl` | root, 0600 | Record of owner messages delivered (agent path, file name, SHA-256) |
| `/var/lib/agent/providers.json` | root, 0644 | Cached `describe` output with no secrets, for `agent provider describe` |
| `/var/log/agent/` | root, 0755 | Journals; directories 0755, files 0644 |
| `/var/log/agent/messages.jsonl` | root, 0644 | The owner conversation (see Messages) |
| `/home/agent/` | agent | The main agent directory |

`settings.json` holds `max_agents` (default 16) and `repository`
(`SEAN-7/bot`).

Inside an agent directory, the harness uses `README.md`, `TASK.md`,
`config.json`, `inbox/`, `inbox/processed/`, `agents/`, `.outputs/`, `ERROR`
and the `journal` symlink. Everything else belongs to the agent.

## Privilege model

The daemon runs as root. The agent is the unprivileged user `agent`.

**All file access inside agent directories goes through an unprivileged
helper.** At startup the daemon starts one long-lived helper process: the same
binary (`agent _fsio`), launched with the `agent` user's uid, gid and
supplementary groups, driven by JSON lines over a pipe. Every scan, read,
write, rename, mkdir and symlink inside agent directories is a helper request.
The kernel therefore applies the agent user's permissions to each operation,
and a symlink planted by the agent can reach only what the agent itself could
reach. The daemon never opens a path inside an agent directory as root. (Go
cannot safely change identity in part of a running process, so a separate
process is the reliable way to do this.) If the helper dies, the daemon
restarts it; operations in flight fail and the affected run is retried.

Helper operations: `scan` (find agent directories), `list` (inbox entries with
type and size), `read` (path, byte limit), `write` (atomic: dot-prefixed
temporary file then rename), `rename`, `mkdir`, `remove`, `symlink`
(create or replace), `stat` (size, mtime, type, without following symlinks
for the final component).

**Shell commands** run as `agent` with credentials set in the child before
exec, so the change of working directory happens with the agent's
permissions. Each command gets its own process group and stdin from
`/dev/null`. Environment: `HOME` and working directory set to the agent's own
directory, `USER`/`LOGNAME=agent`, `PATH=$HOME/bin:/usr/local/bin:/usr/bin:/bin`,
`XDG_RUNTIME_DIR=/run/user/<uid>`,
`DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/<uid>/bus`, `TERM=dumb`.
Commands run with `/bin/bash -c` when bash exists, otherwise `/bin/sh -c`.

**Plugins** run as the dedicated system user `agent-provider`, with no login
and no home directory. The daemon reads `/etc/agent/credentials/NAME.env` and
passes those variables in the plugin's environment, along with a minimal
`PATH`. Neither the plugin nor the agent reads `/etc/agent` directly, and the
agent user cannot read another user's process environment.

## The daemon

`agent daemon` runs as one systemd service (`Restart=always`, default
`KillMode`) serving the whole tree.

- Startup: require root and an existing `agent` user, start the helper, run
  crash recovery, then poll about once a second.
- **Discovery.** An agent directory is `/home/agent`, or a real directory (not
  a symlink) at `<agent>/agents/<name>` that contains an `inbox/` directory.
  The scan runs through the helper and never follows symlinks.
- One goroutine loop per agent, one run at a time per agent.
- **Graceful stop.** On SIGTERM the daemon starts no new runs and delivers no
  new inbox files; active runs finish, up to 15 minutes, before it exits.
  Runs still going at the deadline are interrupted and left unclean, so the
  next start resumes them. The unit uses `KillMode=mixed` and
  `TimeoutStopSec=17min`, so systemd signals only the daemon until it exits.
- **max_agents.** Agents discovered beyond the limit are not started. The
  daemon writes the reason to that directory's `ERROR` and the service log,
  and notifies the parent's inbox once.
- **Removal.** When an agent directory disappears, its loop kills any running
  command's process group, writes the end marker (`reason: "removed"`) and
  exits. Its journals remain.
- **Owner spool.** The daemon moves each file in `/var/lib/agent/owner/` into
  `/home/agent/inbox/` as `<UTC timestamp>-owner.md` (through the helper, with
  an atomic dot-name rename) and appends the agent path, file name and SHA-256
  to `delivered.jsonl` before removing the spool file.

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
  `{"file": "inbox/processed/NAME", "from": "owner" | "data", "content": "..."}`.
  `from` is `owner` only if the file's agent path, name and SHA-256 match a
  record in `delivered.jsonl`. The content is a JSON string, so text inside a
  file can never create its own `from` field. Contents over 64 KiB are clipped,
  with the file's path included.
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
unchanged, `OWNER.md` is unchanged, and `~/.reset` does not exist. Otherwise it starts fresh. The
daemon deletes `~/.reset` when it starts a run. Each run still has its own
journal; a continued run's `start` record names the run it continues. A
daemon restart, interruption, error or context reset always starts fresh.

**Context size.** After each call, context used = input + cache read + cache
write + output tokens from the plugin's usage, compared with the model's
context size from `describe`.
- At 80% (once per run), append to the next user content: "Context is 80%
  full. Save your state to TASK.md and notes now."
- At 95%, end the run (`reason: "context"`) and write to the agent's inbox:
  "Context reset. Continue from TASK.md. Previous journal: <path>".
- Guard: if the first call of a fresh run is already at 95%, write the reason to
  `~/ERROR` and the service log instead of a reset note, and wait for new
  inbox input. This prevents an endless reset loop.

**Provider failures.** Retry the same call with delays doubling from 2 seconds
to a 10-minute ceiling. After 3 consecutive failures, write the provider's
error to `~/ERROR` and the service log; clear `~/ERROR` on the next success.
Never write inbox messages for provider errors.

## Messages

- `agent message "text"` (run by any agent, as the agent user) writes the
  text to `~/.messages/<UTC timestamp>.md` with an atomic dot-name rename.
  Messages over 64 KiB are refused, with advice to send a file path instead.
- Each poll, for every admitted agent, the daemon reads `~/.messages/`
  through the helper (regular, non-dot files in name order, at most 64 KiB
  each), appends one record per file to `/var/log/agent/messages.jsonl`, then
  removes the file. Records: `{"t", "from": "agent", "agent": <name>,
  "text"}`, where `<name>` is `main` or the path under `~/agents/`.
- When the daemon delivers an owner message from the spool, it also appends
  `{"t", "from": "owner", "text"}`.
- Only the daemon writes the log, so its labels are trustworthy, and the
  agent cannot edit or delete anything in it. A crash between appending and
  removing can only duplicate a message.
- `agent messages` prints the conversation and follows new records, with
  control characters removed.

## Journal

- One JSONL file per run at `/var/log/agent/<agent path>/<UTC timestamp>.jsonl`,
  where `<agent path>` is the agent directory's absolute path (so the main
  agent's journals are under `/var/log/agent/home/agent/`).
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

## OWNER.md

`/etc/agent/OWNER.md` holds the owner's standing instructions for every agent
in the tree. Owner messages are processed and can be forgotten, and
`README.md` is the agent's own to revise. OWNER.md is the one thing in
context that no agent can change.

- **Writing it.** The owner edits it as root (`sudo $EDITOR
  /etc/agent/OWNER.md`). There is no CLI command. The installer never creates,
  changes or removes it, so upgrades keep it.
- **Reading it.** At the start of each fresh run, the daemon reads it
  directly as root (it is outside every agent directory), up to the same
  limit as `README.md`. The agent can read it at its path, since
  `/etc/agent` is traversable, but cannot list or write anything there.
- **Safety check.** The file is used only if it is a regular file (not a
  symlink) owned by root and not writable by group or others, and
  `/etc/agent` is owned by root and not writable by group or others. If the
  file is missing, there is no owner section. If it exists but fails the
  check or cannot be read, the agent does not start: the daemon writes the
  reason to `~/ERROR` and the service log, leaves the inbox untouched, and
  retries on later polls. Silently running without the owner's instructions
  would be worse than not running. When the file is fixed, the daemon clears
  `~/ERROR` if the error was an OWNER.md error.
- **Placement.** When the file has non-whitespace content, the first system
  segment is the system prompt, then a blank line, then this header
  (verbatim), then the file's content:

  > # Owner's instructions
  >
  > Your owner wrote the following in /etc/agent/OWNER.md. You can read it
  > but not change it. It applies to every agent on this machine and takes
  > precedence over everything else in your context.

  An empty or missing file leaves the first segment byte-identical to the
  system prompt alone, so installs without it keep their cached prefix.
  Creating or editing the file changes every agent's prefix once; the owner
  should expect one cache rebuild per edit.
- **Continuation.** A run continues a warm conversation only if OWNER.md's
  content is identical to what that conversation was built with; otherwise it
  starts fresh, so an edit takes effect on the next run of every agent.
- **Precedence is a convention.** The harness guarantees the file is present,
  unaltered and clearly attributed; whether the model follows it is up to the
  model. Limits that must hold (credentials, spend, privileges) are enforced
  by the privilege model and the provider, not by this file. If the owner
  wants owner messages to be able to relax a rule, the file says so.
- **With `--sudo`** the agent can rewrite the file, and this becomes a
  convention like the other protections.

## Configuration validation

At the start of each run, the daemon runs the plugin's `describe` and checks
that `config.json` names an installed provider, one of its models, and only
options that model accepts, with allowed values. On any failure the agent
does not start. The daemon writes the reason to `~/ERROR` and the service
log, notifies the parent's inbox (for a subagent) once per distinct
`config.json` content, and retries when `config.json` changes. On success it
clears `~/ERROR` if the error was a configuration error.

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

- `agent daemon`: run the service.
- `agent send "text"`: requires root. Writes an owner message to the spool.
- `agent message "text"`: for agents. Queues a message to the owner.
- `agent messages`: shows the owner conversation and follows new messages.
- `agent log [NAME]`: follows an agent's newest journal, switching to new
  runs as they start. `NAME` is a path relative to `/home/agent/agents`
  (for example `research` or `research/agents/scraper`); it is omitted for the
  main agent. A current `~/ERROR` is shown first. Works as the agent or root.
- `agent provider install NAME|URL`: requires root. `NAME` downloads that
  plugin from this repository's matching release with `GITHUB_TOKEN` and
  verifies it against the index compiled into the binary. `URL` requires
  `--sha256`. Installs to `/etc/agent/plugins/`, prompts for the plugin's
  settings into `/etc/agent/credentials/NAME.env`, and refreshes
  `providers.json`.
- `agent provider describe`: prints models and options. As root it runs the
  plugins; as any other user (including the agent) it reads
  `providers.json`.

## Seed files

- `README.md`: the suggestions from the brief, written as advice the agent
  may revise: memory, task state, stopping, messages and subagents. It adds:
  - long-running jobs go through `systemd-run --user`;
  - inbox records labelled `"from": "owner"` are verified owner messages;
  - `agent message` reaches the owner, permanently and without push.
- `TASK.md`: "No task yet."
- `config.json`: the provider, model and options chosen at install.

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
2. Creates the `agent` and `agent-provider` users, the directories above, and
   `/home/agent` with the seed files.
3. Asks which provider plugin to install, installs it with
   `agent provider install`, prompts for its settings and model, and writes
   `config.json`.
4. Enables lingering for `agent` and installs the systemd service. There is
   no harness heartbeat: the agent schedules its own wake-ups (an upgrade
   removes the heartbeat entry earlier releases added).
5. Starts the daemon.

`--sudo` grants the agent passwordless sudo (default: none). It prints a
warning that the credential, journal and owner-message protections become
conventions, and that the spend limit must be set at the provider.

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
- a parent creating a child and receiving its report
- owner labelling: a delivered message is `owner`, and a forged file is `data`
- messages: an agent's and a subagent's `agent message` and an owner message
  all reach the log with correct labels; the agent cannot write the log
- OWNER.md: its content follows the system prompt in the first segment for
  the main agent and a subagent; a missing or empty file leaves the segment
  identical to the system prompt; a symlink, a non-root owner or a
  group-writable file stops runs with `~/ERROR` and leaves the inbox intact;
  an edit makes the next run start fresh instead of continuing; the agent
  cannot write it

Paths, user names and timings are configurable so the loop can be tested in
a temporary root. Tests that depend on privilege separation need root and a
real unprivileged user. They run under `sudo` in GitHub Actions and in a
throwaway directory on the VM, never against the live agent.

## Operator README

It covers:
- installation
- setting a spend limit on the provider key
- sending the first message
- writing `/etc/agent/OWNER.md`: what it is for, that each edit rebuilds
  every agent's prompt cache once, and that precedence is a convention
- reading logs
- stopping the service
- what is and isn't protected: no isolation between subagents, and the effect
  of `--sudo` (including on OWNER.md)
- an example first message asking the agent to set up a Telegram channel for
  itself from a bot token

## Migration

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
