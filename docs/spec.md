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
- **Subagents are not isolated from each other.** All agents run as the one
  `agent` user. Subagents divide work; they are not a security boundary.
- **Provider failures are visible but never touch the inbox.** Retries back
  off to a 10-minute ceiling; persistent failures are written to `~/ERROR` and
  the service log and shown by `agent log`.
- **The repository stays private.** Installation and plugin downloads
  authenticate with a `GITHUB_TOKEN` that is never stored on the VM.
- **The old Node bot is retired with its history archived** read-only for the
  new agent (see Migration).

## Filesystem layout on the VM

| Path | Owner, mode | Contents |
| --- | --- | --- |
| `/usr/local/bin/agent` | root, 0755 | The harness binary |
| `/etc/agent/` | root, 0711 (traversable, not listable) | `settings.json` (0600), `credentials/` (0700) |
| `/etc/agent/credentials/NAME.env` | root, 0600 | Plugin settings and secrets as `KEY=value` lines |
| `/etc/agent/plugins/` | root:agent-provider, 0750 | Plugin executables, root:agent-provider 0750; the agent user cannot read or run them |
| `/var/lib/agent/owner/` | root, 0700 | Spool for `agent send` |
| `/var/lib/agent/delivered.jsonl` | root, 0600 | Record of owner messages delivered (agent path, file name, SHA-256) |
| `/var/lib/agent/providers.json` | root, 0644 | Cached `describe` output with no secrets, for `agent provider describe` |
| `/var/log/agent/` | root, 0755 | Journals; directories 0755, files 0644 |
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
2. Read `README.md` and `TASK.md` (missing files are empty).
3. Collect inbox files in name order, skipping dot-files and directories.
   Write them to the journal, then move them to `inbox/processed/` (adding a
   numeric suffix if a name is already taken).

**Context.**
- System segments, most stable first: the system prompt (verbatim, below),
  then `README.md`, then `TASK.md`.
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

**Context size.** After each call, context used = input + cache read + cache
write + output tokens from the plugin's usage, compared with the model's
context size from `describe`.
- At 80% (once per run), append to the next user content: "Context is 80%
  full. Save your state to TASK.md and notes now."
- At 95%, end the run (`reason: "context"`) and write to the agent's inbox:
  "Context reset. Continue from TASK.md. Previous journal: <path>".
- Guard: if the first call of a run is already at 95%, write the reason to
  `~/ERROR` and the service log instead of a reset note, and wait for new
  inbox input. This prevents an endless reset loop.

**Provider failures.** Retry the same call with delays doubling from 2 seconds
to a 10-minute ceiling. After 3 consecutive failures, write the provider's
error to `~/ERROR` and the service log; clear `~/ERROR` on the next success.
Never write inbox messages for provider errors.

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
  cache_read_tokens, cache_write_tokens}, opaque}`, or `{error: {message}}`.
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
  prompt-cache markers on the system segments and the latest message.
  Exposes the reasoning settings current models accept as options. Model IDs,
  context sizes, caching and reasoning parameters are taken from the current
  Anthropic documentation at implementation time, not from memory.
- **replay**: scripted replies for tests. Settings: `REPLAY_SCRIPT` (JSONL of
  responses, with a header describing models) and optional `REPLAY_LOG`
  (where it records each request).

## CLI

- `agent daemon`: run the service.
- `agent send "text"`: requires root. Writes an owner message to the spool.
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
  - the old bot's history is in `~/archive/old-bot/` (main agent only).
- `TASK.md`: "No task yet."
- `config.json`: the provider, model and options chosen at install.

## Install

`install.sh` runs as:

```sh
curl -fsSL -H "Authorization: Bearer $GITHUB_TOKEN" https://raw.githubusercontent.com/SEAN-7/bot/vX.Y.Z/install.sh | sudo GITHUB_TOKEN="$GITHUB_TOKEN" sh
```

It pins its own version and the SHA-256 of every release binary. It:
1. Detects the architecture, downloads the release binaries through the
   GitHub API with the token, and verifies their checksums. The token is never
   written to disk.
2. Creates the `agent` and `agent-provider` users, the directories above, and
   `/home/agent` with the seed files.
3. Asks which provider plugin to install, installs it with
   `agent provider install`, prompts for its settings and model, and writes
   `config.json`.
4. Enables lingering for `agent`, installs the systemd service, and adds a
   daily crontab entry for `agent` that writes "Heartbeat: anything pending?"
   to the inbox with an atomic dot-name rename.
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

Paths, user names and timings are configurable so the loop can be tested in
a temporary root. Tests that depend on privilege separation need root and a
real unprivileged user. They run under `sudo` in GitHub Actions and in a
throwaway directory on the VM, never against the live agent.

## Operator README

It covers:
- installation
- setting a spend limit on the provider key
- sending the first message
- reading logs
- stopping the service
- what is and isn't protected: no isolation between subagents, and the effect
  of `--sudo`
- an example first message asking the agent to set up a Telegram channel for
  itself from a bot token

## Migration

After the harness is built and tested:
1. This repository's Node code is removed in one commit; history keeps it.
   `AGENTS.md` and the docs are rewritten for the new design.
2. On the VM, the Node bot is stopped and uninstalled with its own uninstaller,
   which retains its data.
3. Its journal is exported to JSONL, and its memory and workspace are copied
   to `/home/agent/archive/old-bot/`, owned by root and readable by the agent.
4. The new harness is installed and verified with read-only checks.
   Synthetic messages are never sent to the live agent.
