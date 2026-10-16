# Agent harness: design spec

An always-on LLM agent harness for a Linux VM, in Go with the standard
library only, shipped as static binaries for linux/amd64 and linux/arm64.
The harness provides a few guarantees ([philosophy](philosophy.md)); the
agent, whose one tool is the shell, builds everything else as files.

Everything an agent can observe is specified in [the manual](manual.md),
which agents read at `~/manual/`. This spec covers how the harness
implements it and what agents cannot see. A fact lives in one of the two,
never both.

## Decisions

- **No `max_steps`.** Context resets bound each run; the provider spend
  limit bounds money.
- **No context policy.** A run ends for context only when the provider
  rejects it as too long. The agent sees its usage in its journal and
  decides when to start fresh sooner.
- **Root is the top of one tree.** The human, as root, uses the same
  commands an agent uses for its children. `main`, created by a fresh
  install, is otherwise an ordinary child of root.
- **One owner per agent.** Root for a top-level agent, the creator
  otherwise. An agent talks only with its owner and its own children, so
  only top-level agents reach the human.
- **Each agent is its own Unix user**, from a fixed pool of `max_agents`
  users created at install and reused.
- **Each agent owns its config.** Its creator writes the starting
  `config.json`; it moves into the agent's home at admission. A parent
  stops a child only by removing it.
- **One file of the agent's is loaded.** `README.md` is the agent's whole
  standing context; how it keeps knowledge and task state is its own
  choice.
- **Long-running jobs use the agent user's own systemd** (lingering, with
  `XDG_RUNTIME_DIR` set). What a command leaves running in the daemon's
  service is killed when it finishes, since the next restart would kill it
  unannounced. There is no harness heartbeat: agents schedule their own
  wake-ups.
- **Provider failures never touch the inbox.** They go to `ERROR` and the
  service log.
- **The agent can read how the harness works.** The manual and the release
  notes are embedded in the binary and written as a read-only directory,
  so they always describe the running version. What a provider caches and
  what it costs comes from its plugin's `describe`.
- **Everything an agent does with the harness is a file.** Messages are
  files in spools, a subagent is a directory holding a `config.json`, and
  journals, providers and the manual are files to read. The binary is the
  daemon, its file helper and root's plugin installer.
- **Upgrades are reported, not announced.** An agent learns what changed
  at its next run; nothing wakes it for an upgrade.
- **The repository is public.** Installs download release assets without
  credentials and trust only the checksums pinned in `install.sh`.
- **Conversations are pull, not push.** The daemon logs both directions in
  a root-owned log; alerts are something the agent builds.
- **The harness presents files; it does not convert them.** A file reaches
  the model as text or an image only in a form the model accepts natively;
  anything else (a PDF) arrives as a note with its path.
- **`OWNER.md` precedence is stated, not enforced.** The harness enforces
  only the privilege model.
- **The cached prefix changes rarely.** The system prompt, the owner
  section's header and the `sh` tool definition are part of every agent's
  cached prompt; changing them rebuilds every cache once.

## Filesystem layout

| Path | Owner, mode | Contents |
| --- | --- | --- |
| `/usr/local/bin/agent` | root, 0755 | The harness binary |
| `/etc/agent/` | root, 0711 | Root's place in the tree: `settings.json` (0600), `credentials/`, `plugins/`, `agents/`, `.messages/` |
| `/etc/agent/credentials/NAME.env` | root, 0600 | Plugin settings as `KEY=value` lines |
| `/etc/agent/plugins/` | root:agent-provider, 0750 | Plugin executables (same owner and mode) |
| `/etc/agent/agents/NAME/` | root, 0700 | Root's control directory for its child NAME |
| `/etc/agent/agents/NAME/OWNER.md` | root, 0644 | The human's standing instructions for NAME; optional |
| `/etc/agent/.messages/agents/NAME/` | root, 0700 | Root's spool for NAME; files 0600 |
| `/var/lib/agent/delivered.jsonl` | root, 0600 | Delivered messages: agent id, file name, SHA-256, sender |
| `/var/lib/agent/pool.json` | root, 0600 | Pool assignments: user, agent id, state |
| `/var/lib/agent/providers.json` | root, 0644 | Cached `describe` output, no secrets |
| `/var/lib/agent/manual/` | root, 0755 | `manual.md`, `notes.md` and `version` (files 0644) |
| `/var/lib/agent/version` | root, 0644 | Installed version, written by `install.sh` |
| `/var/log/agent/` | root, 0755 | Journals and conversation logs |
| `/home/agent-N/` | agent-N:agent-N, 2750 | A pool user's home; empty while free |

`settings.json` holds `max_agents` (default 16), which bounds the whole
tree, main included. It is read when the daemon starts; a change takes
effect at the next restart. What the harness keeps in an agent's home and control
directory, and their modes, are in the manual ("Your home", "Subagents").

## Privilege model

The daemon runs as root; each agent is an unprivileged pool user `agent-N`.

**Groups.** Each agent user has its own group, whose members are the
agent's ancestors other than root, kept current in `/etc/group`. A process
started while in a reclaimed user's group keeps read access to the next
holder's home until it restarts.

**File access through helpers.** The daemon never opens a path inside an
agent home or control directory as root. It runs one long-lived helper per
user (`agent _fsio`, with that user's uid, gid and groups, driven by JSON
lines over a pipe), so the kernel applies that user's permissions and a
planted symlink reaches only what the user could. A home is accessed
through its agent's helper, a control directory through its parent's, and
`/etc/agent` through a root helper. A helper that dies is restarted and the
affected run retried. Operations: `list`, `read` (regular files, byte
limit), `write` (atomic: a temporary dot-file in the target's directory, or
in a staging directory the caller names, then a rename), `private` (create a missing
directory 0750; leave anything else alone), `rename`, `remove` and `clear`
(never following symlinks), `unlink`, `symlink`, `purge_owned` (the user's
files in shared temporary directories) and `stat` (lstat of the final
component).

**Shell commands.** Credentials are set before exec. When a command
finishes, every live process in the daemon's own cgroup whose real or
effective uid is the agent's, other than its file helper, is killed (read
from `/proc`, repeating while new ones appear); processes in other cgroups
(the user's systemd, cron) are untouched. An agent run without changing
identity (tests) is not swept.

**Plugins** run as the system user `agent-provider` (no login, no home),
with the credentials file's variables and a minimal `PATH` in their
environment. No agent can read `/etc/agent` or another user's environment.

## The daemon

`agent daemon` is one systemd service (`Restart=always`) for the whole tree.
An agent's id is its path from root (`main`, `main/agents/research`); it
keys journals, pool assignments and delivery records. Root has control
directories and spools but no loop, inbox, journal or owner.

- **Startup.** Require root. Refuse to start when `pool.json` is missing but
  `/var/log/agent` holds anything other than regular files, since without it
  a live agent's home could be cleared or handed to another agent; the error
  names the README section "If pool.json is lost". Resolve the provider and
  pool users (missing ones are logged), start root's helper, write the
  manual (see Manual), create root's spools `/etc/agent/.messages` and
  `.messages/agents` where missing (`private`), then poll about
  once a second: finish any reclaim in progress, walk the tree, collect
  messages.
- **Discovery.** Walk the tree breadth-first, listing each node's `agents/`
  through its own helper without following symlinks. A control directory is
  a real directory owned by the node's user in an `agents/` that is itself
  one, named by one plain name not starting with `.` and without control
  characters. One not yet running is admitted with the user `pool.json` records for
  it (a restart: the home is kept), or else with a fresh user if it holds a
  regular `config.json`; otherwise it is left alone. Failures are logged once
  and retried next poll. A known agent is kept unless its control directory
  is certainly gone. A `config.json` in an admitted agent's control directory
  is ignored.
- **Starting config.** Before a new agent is given a user, its starting
  `config.json` is read through the parent's helper and checked against
  `providers.json` (no plugin runs from the poll; without the cache the
  check is skipped and the first run's gate reports). An invalid one, like
  one over 1 MiB, keeps the agent out (see Admission errors) and is checked
  again each poll, so the parent fixes it in place.
- **Admission.** Take the lowest free pool user and record it in
  `pool.json`; prepare the home (2750, setgid); start the user's helper;
  clear what a previous holder left; set the group's members; create the
  harness directories where missing (`inbox/.tmp` among them; `private`;
  anything else of that name
  is left alone and logged once); write the seed `README.md` where
  missing; move the starting `config.json` into the home unless it
  already has one; enable lingering; create the journal directory; write
  the `home` link (never recreating a control directory that was removed);
  create the parent's spool `.messages/agents/NAME` (`private`; a failure
  is logged). A failed step is retried next poll. The control directory's
  device and inode are part of the agent's identity, so renaming or
  recreating it makes a new agent. A deleted `home` link is written again
  only at the next admission (a restart), so `rm -r` of a control directory
  never races it.
- **Admission errors.** An invalid starting `config.json` or one over 1 MiB,
  or no free pool user ("Agent limit reached"), keeps the agent out: the reason goes to its
  `ERROR` and the service log, and the parent's inbox is told once per
  reason (root's children: the service log only).
- **Runs.** One goroutine per agent, one run at a time.
- **Graceful stop.** On SIGTERM, start no new runs or deliveries and let
  active runs finish for up to 15 minutes; any still going are interrupted
  and resumed on the next start (`KillMode=mixed`, `TimeoutStopSec=17min`).
- **Out of memory.** The unit sets `OOMPolicy=continue`: when the kernel
  kills a process for memory, only that process dies, and its command
  returns the failure to its agent.
- **Removal.** When a control directory disappears or is replaced, its agent
  and all descendants are removed, deepest first: each loop kills its
  running command, writes the end marker (`reason: "removed"`) and exits,
  then its user is reclaimed. Journals remain.
- **Reclaim.** Mark the user `reclaiming`; disable lingering; remove its
  crontab; stop its user manager and kill its processes; remove its home's
  contents (making its directories writable first) and its files in `/tmp`,
  `/var/tmp` and `/dev/shm`; move its journal directory to the nearest
  remaining ancestor's group. Its group is cleared and the user freed only
  after every descendant is reclaimed. An interrupted reclaim finishes on
  the next start; a failed one is retried every poll.

### Known limits

- After starting with the journals moved aside, the daemon does not stop
  what the pool users were running; root does (README, "If pool.json is
  lost").
- A restored `pool.json` older than the last admission or removal can make
  the daemon wipe the home of an agent that now holds a reused user; the
  latest backup cannot.
- While stat fails on a not-yet-running child's control directory, messages
  for it wait.
- Reclaim leaves links in `/home`. An installation upgraded from 0.3 has
  `/home/agent` leading to main's home, which nothing removes.

## The loop

What a run does, as the agent sees it, is the manual's "Runs". How:

- **Waking and collecting.** Waking needs a regular inbox file whose name
  does not start with a dot; collection takes every regular file, dot-files
  included, and skips directories.
- **Staging.** Every file the harness writes into an inbox (a message, a
  note) is written through the agent's helper with its temporary file in
  `inbox/.tmp`, which collection skips, so a run never collects one half
  written; a missing `inbox/.tmp` is created and the write repeated.
  Admission removes files left there, before the agent's loop starts and
  from the poll goroutine, so no write is in progress.
- **Labels.** An inbox file is read (up to 16 MiB) and hashed; it is
  labelled `owner` or `agents/NAME` only if the agent path, file name and
  SHA-256 match a `delivered.jsonl` record with that sender.
- **Images.** Detected with `http.DetectContentType`; dimensions are read
  from the header before the whole image is decoded. A shown image is
  copied into the journal directory first; one whose copy cannot be saved
  is not shown. The anthropic API's 32 MB request limit is reported by its
  plugin as `context_overflow`.
- **Upgrades.** Before collecting the inbox, the daemon reads the first
  line of the agent's newest run file (the `start` record) for its
  `version`. When the running version and that one are both release
  versions and the previous is older, it writes the upgrade note through
  the agent's helper, holding the `## X.Y.Z` sections of the embedded
  release notes newer than the previous version, up to the running one; no
  recorded version counts as 0.5.1, the last release that delivered notes.
  The note is written before the new run's `start` record, so a crash
  between them can only repeat it. No state is kept outside the journal.
- **First-call guard.** A `context_overflow` on a run's first call writes
  `ERROR` instead of the reset note and waits for new input, so resets
  cannot loop.
- **Provider failures** are retried in the loop; after 3 consecutive
  failures the error goes to `ERROR` and the service log.
- **Strings.** Every note, the system prompt and the `sh` tool are in
  `internal/daemon/messages.go` and `internal/proto`; the manual quotes
  them, and a test keeps the two in step.

## Messages

What agents send and receive is the manual's "Messages". Delivery:

- **Up** is `~/.messages/`; **down** is `.messages/agents/NAME/` under the
  sender's home (root: under `/etc/agent`). Root has no up spool.
- Each poll, the daemon reads every spool through its owner's helper:
  regular non-dot files owned by the spool's user, in name order; anything
  else is logged and ignored. The spool's location identifies the sender.
  Up from a top-level agent is appended to its conversation log only; a
  message for a child that can no longer be admitted is dropped with a note
  to the sender (root: the service log). A file over 64 KiB is read to its
  first 64 KiB, delivered with "[message truncated]", and the sender is
  told with a note once it is delivered (root: the service log).
- The `delivered.jsonl` record is written first, then the inbox file, then
  the conversation log, then the spool file is removed, so a crash can only
  duplicate a message. A failing delivery is retried every poll under the
  same name.
- **Conversation logs** are `messages.jsonl` in the agent's journal
  directory, root-owned 0640 in its group, written only by the daemon.

## Journal

- One JSONL file per run at `/var/log/agent/<id>/<UTC timestamp>.jsonl`; a
  child's directory nests inside its parent's. Root-owned in the agent's
  group: directories 0751, files 0640.
- **Crash recovery.** When a loop starts and the newest journal has no `end`
  record, the interrupted note is written and the journal marked
  interrupted.

## OWNER.md

Root edits `/etc/agent/agents/NAME/OWNER.md`; an agent edits
`~/agents/NAME/OWNER.md`. There is no command, and the installer never
touches it. It is read through the parent's helper. Without content, the
first segment is the system prompt alone, so the cached prefix is
unchanged. What the agent sees, and the safety check, are in the manual.

## Configuration validation

Each run checks `config.json` against the cached `describe` output. On
failure the reason also goes to the service log; root's children's
failures go to the log only.

## Plugin protocol (version 1)

A plugin is an executable. Per call, the daemon writes one JSON request to
its stdin and reads one JSON response from stdout; stderr goes to the
service log. Every message carries `"protocol": 1`.

**describe** (`{"protocol": 1, "type": "describe"}`, works without
credentials) returns:
- `settings`: `{name, description, secret}` for each environment variable
  it needs; the installer prompts for them.
- `models`: `{id, context_tokens, image_types, options}`. `context_tokens`
  is the context window where known, for agents choosing a model.
  `image_types` lists accepted image media types (a 0.5.0 boolean `images`
  is ignored).
  `options` maps names to `{description, values}` or
  `{description, type: "integer", min, max}`.
- `caching` (optional): text for agents on what the provider caches between
  calls and for how long, the smallest prompt it caches, where the plugin
  puts cache markers, and what cache reads and writes cost relative to
  plain input. An older plugin omits it and an older daemon ignores it, so
  the protocol version is unchanged.

**complete** sends `model`, `options` (passed through), `system` (`{text}`
segments, most stable first, for cache markers), `tools` (the `sh`
definition) and `messages`:
- `{role: "user", content, images}`, `images` being `{file, media_type,
  data}` with `data` the raw bytes (base64 in JSON)
- `{role: "assistant", text, command, wait, call_id, opaque}`
- `{role: "tool", call_id, content}`

The response is `{text, command, wait, usage: {input_tokens, output_tokens,
cache_read_tokens, cache_write_tokens}, opaque}` or `{error: {message,
kind}}`. `wait` is the model's argument passed through unchanged (absent
means the default). `kind: "context_overflow"` is not retried: it ends the
run with a reset, or triggers the first-call guard. The daemon assigns
`call_id`; `opaque` is stored on the assistant message and returned in later
requests (reasoning blocks, signatures). A plugin returns at most one
command and disables parallel tool calls.

The daemon interprets only `image_types`; `usage` is journaled and
`caching` cached in `providers.json`, neither interpreted.

## Plugins

Sources live in `plugins/<name>/`. `plugins/index.json` lists each plugin's
version and per-architecture SHA-256 and is compiled into `agent`; releases
publish each as `plugin-<name>-linux-<arch>`, and `agent provider install`
accepts only a file matching the index.

- **anthropic**: Messages API, `ANTHROPIC_API_KEY`. Cache markers on each
  system segment (at most the last three) and the latest message. Declares
  JPEG, PNG, GIF and WebP (WebP still arrives as a note); each image follows
  its message's text as a text block naming the file and an image block.
  HTTP 413 is `context_overflow`. Exposes the models' reasoning settings as
  options, taken from the current Anthropic documentation. Its `caching`
  text gives the 5-minute lifetime, the read and write multipliers and each
  model's minimum cacheable prompt, from the current documentation.
- **openai-compatible**: Chat Completions with function calling.
  `OPENAI_BASE_URL`, `OPENAI_API_KEY` and `OPENAI_MODELS` (model ids, each
  optionally `id=context_tokens`). The system segments are joined by blank
  lines into one system message; it sends no cache markers, reports the
  server's `cached_tokens` as `cache_read_tokens`, and `cache_write_tokens`
  is always 0. Its `caching` text says so and describes OpenAI's automatic
  prefix caching.
- **replay**: scripted replies for tests. `REPLAY_SCRIPT` (JSONL responses
  after a header describing models) and optional `REPLAY_LOG`.

## The binary

`agent daemon` (the service, root only), the hidden `agent _fsio` (the file
helper), `agent provider install NAME FILE` (root: install a plugin binary
whose SHA-256 matches the index for NAME and this architecture, prompt for
missing settings and refresh `providers.json`; downloads nothing) and
`agent help`. The commands 0.7.0 kept (`new`, `remove`, `send`, `message`,
`messages`, `log`, `provider describe`, `version`) were removed in 0.8.0.

## Manual

`docs/manual.md`, `internal/notes/notes.md` (the release notes, newest
first) and `version` (the running version) are embedded in the binary. At
startup the daemon writes each into
`/var/lib/agent/manual/` (root, 0755, created if missing; files 0644)
atomically, through a temporary file and a rename, and removes any other
file there, so the manual always matches the running version and no agent
can change it. A failure is logged and retried at the next start. Each run
links `~/manual` to it, as it links `~/journal`. The manual is written
outside agent homes, so no helper is involved.

## Seed files

`internal/seed/README.md`, written by admission where the home lacks one:
a short starting README that points to `~/manual/` and says the file is the
agent's. There is no seed `TASK.md`; one an existing agent has is an
ordinary file of its own, which the harness neither reads nor writes.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/SEAN-7/bot/vX.Y.Z/bot/install.sh | sudo sh
```

`install.sh` takes no options and pins its version and the `agent` binary's
checksums (which pin the plugins'). It:
1. Installs `acl` if `setfacl` is missing, downloads the binary from the
   release into a root-only temporary directory and verifies it.
2. Treats a machine with `/var/lib/agent/version` or `/etc/agent/agents/main`
   as an upgrade, and refuses, before changing anything, one older than
   0.4.1 (upgrade with the v0.4.1 `install.sh` first).
3. Stops the service (runs finish first), installs the binary, keeping the
   previous one to restore if anything fails before the service starts, and
   creates `agent-provider` and the directories above, root's spools
   `/etc/agent/.messages` and `.messages/agents` included (0700).
4. Creates missing pool users, each with its own group and an empty home.
5. Fresh install: asks for a provider, downloads and installs it with
   `agent provider install` (which prompts for its settings), and asks for a
   model. Upgrade: reinstalls each installed plugin, keeping the old one
   with a warning if that fails.
6. Installs and starts the service, then writes `/var/lib/agent/version`.
7. Fresh install: creates `/etc/agent/agents/main` (0700) and writes its
   `config.json`, saying how to retry if that fails; the daemon admits it.

**Releases** are reproducible (`CGO_ENABLED=0`, `-trimpath`, empty build ID,
toolchain pinned in `go.mod`). `scripts/release.sh` builds everything and
writes `plugins/index.json` and the checksums in `install.sh`; the tag's
workflow rebuilds and refuses to publish if any checksum differs.

## Tests

Paths, user names and timings are configurable, so tests run in a temporary
root against the replay plugin. Privilege tests need root and a throwaway
pool of real users (`AGENT_ROOT_TESTS=1`); they run in CI, and on a
machine with live agents they need their own temporary root and users.
