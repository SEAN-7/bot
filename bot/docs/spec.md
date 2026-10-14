# Agent harness: design spec

An always-on LLM agent harness for a Linux VM, in Go with the standard
library only, shipped as static binaries for linux/amd64 and linux/arm64.
The harness provides a few guarantees ([philosophy](philosophy.md)); the
agent, whose one tool is the shell, builds everything else as files.

## Decisions

- **No `max_steps`.** Context resets bound each run; the provider spend
  limit bounds money.
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
- **Long-running jobs use the agent user's own systemd** (lingering, with
  `XDG_RUNTIME_DIR` set). What a command leaves running in the daemon's
  service is killed when it finishes, since the next restart would kill it
  unannounced. There is no harness heartbeat: agents schedule their own
  wake-ups.
- **Provider failures never touch the inbox.** They go to `ERROR` and the
  service log.
- **The repository is private.** Installs authenticate with a
  `GITHUB_TOKEN` that is never stored.
- **Conversations are pull, not push.** The daemon logs both directions in
  a root-owned log; alerts are something the agent builds.
- **The harness presents files; it does not convert them.** A file reaches
  the model as text or an image only in a form the model accepts natively;
  anything else (a PDF) arrives as a note with its path.
- **`OWNER.md` precedence is stated, not enforced.** Hard limits belong in
  the privilege model.

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
| `/var/lib/agent/version` | root, 0644 | Installed version, written by `install.sh` |
| `/var/log/agent/` | root, 0755 | Journals and conversation logs |
| `/home/agent-N/` | agent-N:agent-N, 2750 | A pool user's home; empty while free |

`settings.json` holds `max_agents` (default 16), which bounds the whole
tree, main included. In an agent's home the harness uses `README.md`,
`TASK.md`, `config.json`, `inbox/`, `inbox/processed/`, `agents/`,
`.messages/` and the `journal` symlink. An agent's control directory is
`agents/NAME/` in its parent's home (`/etc/agent/agents/NAME/` for root's
children); it belongs to the parent and holds `OWNER.md`, `ERROR`, the
`home` symlink and, until admission, the starting `config.json`. Everything
else belongs to the agents.

## Privilege model

The daemon runs as root; each agent is an unprivileged pool user `agent-N`.
No agent has sudo.

**Groups.** Each agent user has its own group, whose members are the
agent's ancestors other than root (kept current in `/etc/group`). Homes are
setgid and commands run with umask 022, so ancestors can read what an agent
writes and nobody else can traverse its home. An agent can tighten its own
files' modes, but not its journals. Because granting a child `x` on `~`
opens what others can read there, the harness creates its own parts of a
home group-only: `inbox/`, `inbox/processed/`, `agents/`, `.messages/`,
`.messages/agents/` and control directories 0750; inbox files, delivered
messages, spool files and `config.json` 0640. Processes keep the groups
they started with: one started before a membership change sees it only
after restarting, and one started while in a reclaimed user's group keeps
read access to the next holder's home until it restarts.

**File access through helpers.** The daemon never opens a path inside an
agent home or control directory as root. It runs one long-lived helper per
user (`agent _fsio`, with that user's uid, gid and groups, driven by JSON
lines over a pipe), so the kernel applies that user's permissions and a
planted symlink reaches only what the user could. A home is accessed
through its agent's helper, a control directory through its parent's, and
`/etc/agent` through a root helper. A helper that dies is restarted and the
affected run retried. Operations: `list`, `read` (regular files, byte
limit), `write` (atomic dot-name then rename), `private` (create a missing
directory 0750; leave anything else alone), `rename`, `remove` and `clear`
(never following symlinks), `unlink`, `symlink`, `purge_owned` (the user's
files in shared temporary directories) and `stat` (lstat of the final
component).

**Shell commands** run as the agent's user (credentials set before exec),
in their own process group, stdin `/dev/null`, umask 022, with `/bin/bash -c`
(or `/bin/sh -c`). Environment: `HOME` and the working directory are the
home, `USER`/`LOGNAME`, `PATH=$HOME/bin:/usr/local/bin:/usr/bin:/bin`,
`XDG_RUNTIME_DIR=/run/user/<uid>`,
`DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/<uid>/bus`, `TERM=dumb`.
When a command finishes, every live process in the daemon's own cgroup whose
real or effective uid is the agent's, other than its file helper, is killed
(read from `/proc`, repeating while new ones appear). Processes started with
`systemd-run --user`, or by cron, are in other cgroups and untouched. An
agent run without changing identity (tests) is not swept.

**Sharing** is explicit: a file's owner grants access with `setfacl`. Since
users are reused, a parent removes a child's grants and files before
removing it; the seed README says how.

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
  pool users (missing ones are logged), start root's helper, then poll about
  once a second: finish any reclaim in progress, walk the tree, collect
  messages.
- **Discovery.** Walk the tree breadth-first, listing each node's `agents/`
  through its own helper without following symlinks. A control directory is
  a real directory owned by the node's user in an `agents/` that is itself
  one. One not yet running is admitted with the user `pool.json` records for
  it (a restart: the home is kept), or else with a fresh user if it holds a
  regular `config.json`; otherwise it is left alone. Failures are logged once
  and retried next poll. A known agent is kept unless its control directory
  is certainly gone. A `config.json` in an admitted agent's control directory
  is ignored.
- **Admission.** Take the lowest free pool user and record it in
  `pool.json`; prepare the home (2750, setgid); start the user's helper;
  clear what a previous holder left; set the group's members; create the
  harness directories where missing (`private`; anything else of that name
  is left alone and logged once); write the seed `README.md` and `TASK.md`
  where missing; move the starting `config.json` into the home unless it
  already has one; enable lingering; create the journal directory; write
  the `home` link (never recreating a control directory `agent remove` moved
  away). A failed step is retried next poll. The control directory's device
  and inode are part of the agent's identity, so renaming or recreating it
  makes a new agent. Each poll restores a missing `home` link.
- **Admission errors.** A starting `config.json` over 1 MiB, or no free pool
  user ("Agent limit reached"), keeps the agent out: the reason goes to its
  `ERROR` and the service log, and the parent's inbox is told once per
  reason (root's children: the service log only).
- **Runs.** One goroutine per agent, one run at a time.
- **Graceful stop.** On SIGTERM, start no new runs or deliveries and let
  active runs finish for up to 15 minutes; any still going are interrupted
  and resumed on the next start (`KillMode=mixed`, `TimeoutStopSec=17min`).
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
  the daemon wipe the home of an agent that now holds a reused user. Only
  the latest backup is safe.
- While stat fails on a not-yet-running child's control directory, messages
  for it wait.
- A default ACL on `~` naming a child is inherited by harness directories
  created there later. The seed README says never to set one.
- Admission leaves existing harness directories as they are, so one an
  agent opened stays open.
- Reclaim leaves links in `/home`. An installation upgraded from 0.3 has
  `/home/agent` leading to main's home; remove it by hand.

## The loop

**Wake** when `inbox/` holds a regular file whose name does not start with
a dot.

**Start of run.** Read and validate `~/config.json` (see Configuration
validation) and `OWNER.md`; on failure, do not start and leave the inbox
alone. Read `README.md` and `TASK.md` (missing means empty). Collect inbox
files in name order, skipping dot-files and directories, journal them and
move them to `inbox/processed/` (with a numeric suffix if taken).

Every run starts fresh from the files on disk, with its own journal.

**Context.**
- System segments, most stable first: the system prompt plus the owner
  section, then `README.md`, then `TASK.md`.
- The first user message holds the inbox files, each one JSON record:
  `{"file": "inbox/processed/NAME", "from": "owner" | "agents/NAME" | "data", "content": "..."}`.
  `from` is `owner` or `agents/NAME` only if the agent path, file name and
  SHA-256 match a `delivered.jsonl` record with that sender; content is a
  JSON string, so a file cannot forge the field. Content over 64 KiB is
  clipped.
- **Images.** A JPEG, PNG or GIF (by `http.DetectContentType`) whose type
  the model lists in `image_types` is attached to the user message, its
  record's `content` describing it (`[image/jpeg 1280x960, 169027 bytes,
  shown below]`). Limits: 5 MiB, 2000 px a side (the API rejects larger
  images in requests with over 20), and 20 MiB of images per run, since
  shown images are resent on every call. Dimensions are read from the header
  before the whole image is decoded. Nothing is resized. An image not shown
  for any reason, other non-UTF-8 data, and non-UTF-8 text get a note naming
  the file (wording in `internal/daemon/messages.go`). A request over the
  API's 32 MB limit is reported as `context_overflow`.
- After each step, new inbox files are appended, collected the same way, as
  a user message after the command result.

**Steps.** Call the model with the single tool `sh(command, wait)`. A
command runs for `wait` seconds (rounded down, within 1–600; missing or not
a JSON number means 120), then its process group is killed and the result
says so, suggesting a longer wait or `systemd-run --user`. The result is
combined stdout and stderr plus exit status; over 16 KiB it is clipped to
the first and last 8 KiB, with a marker suggesting redirecting to a file.
Processes the command left running are then killed (see Shell commands),
and the result ends with a note counting and naming them, suggesting
`systemd-run --user`.
The journal records exactly what the model got. A reply with no command
ends the run (`reason: "done"`).

**The sh tool** (verbatim; part of every cached prefix):
- Description: "Run a shell command on this machine as your user, in your
  home directory. Returns combined output and exit status. A command still
  running after its wait is killed."
- Parameters: `command` (string, required: "The command to run") and `wait`
  (integer: "Seconds to wait for the command before killing it. Default 120,
  maximum 600.").

**Context size.** Used = input + cache read + cache write + output tokens,
against the model's `context_tokens`.
- At 80%, once per run, append: "Context is 80% full. Save your state to
  TASK.md and notes now."
- At 95%, end the run (`reason: "context"`) and write to the inbox:
  "Context reset. Continue from TASK.md. Previous journal: <path>".
- If a run's first call is already at 95%, write `ERROR` instead and wait
  for new input, so resets cannot loop.

**Provider failures.** Retry the call with delays doubling from 2 seconds
to 10 minutes. After 3 consecutive failures write the error to `ERROR` and
the service log; clear it on the next success.

**ERROR** lives in the control directory, owned by the parent, so an agent
cannot hide its own errors.

## Messages

- **Up.** `agent message "text"` writes `~/.messages/<UTC timestamp>.md`
  (atomic dot-name rename). Root has no owner, so it fails as root.
- **Down.** `agent send NAME "text"` writes
  `.messages/agents/NAME/<UTC timestamp>.md` under the caller's home (root:
  under `/etc/agent`, private). NAME must be a direct child.
- Empty messages and messages over 64 KiB are refused.
- **Delivery.** Each poll, the daemon reads every spool through its owner's
  helper: regular non-dot files owned by the spool's user, in name order;
  anything else is logged and ignored. The spool's location identifies the
  sender.
  - Up from a top-level agent: appended to its conversation log only.
  - Up from any other agent: delivered to the parent's inbox as
    `<UTC timestamp>-NAME.md` with sender `agents/NAME`, and logged.
  - Down: delivered to the child's inbox as `<UTC timestamp>-owner.md` with
    sender `owner`, and logged. A message for a child not running waits while
    it may still be admitted; otherwise it is dropped with a note to the
    sender (root: the service log).

  The record is written first, then the inbox file, then the log, then the
  spool file is removed, so a crash can only duplicate a message. A failing
  delivery is retried every poll under the same name.
- **Conversation logs.** `messages.jsonl` in the agent's journal directory,
  root-owned 0640 in its group. Records: `{"t", "from": "owner" | "agent",
  "text"}`. Only the daemon writes them.

## Journal

- One JSONL file per run at `/var/log/agent/<id>/<UTC timestamp>.jsonl`;
  a child's directory nests inside its parent's. The directory also holds
  `messages.jsonl` and each shown image as `<run>-<n>.<ext>`, which are not
  runs. An image whose copy cannot be saved is not shown.
- Root-owned in the agent's group: directories 0751, files 0640, so siblings
  cannot read each other's journals.
- Records: `start` (config), `inbox` (each file with its label; images get
  `image: {media_type, width, height, bytes, sha256, copy}`), `assistant`,
  `result`, `note` and `end` (with `reason`).
- `~/journal` links to the agent's journal directory.
- **Crash recovery.** When a loop starts and the newest journal has no `end`
  record, write "You were interrupted. Resume from TASK.md." to the inbox.

## System prompt (verbatim)

> You live on this machine. Your only tool is the shell. ~/README.md and
> ~/TASK.md are loaded every time you start, and you may edit them. Messages
> arrive as files in ~/inbox/. ~/journal/ is a read-only record of everything
> you've done. When a run ends or your context resets, you keep only what's
> on disk. To stop, reply without a command. That reply goes only to your
> journal; it is not sent to anyone.

## OWNER.md

The owner's standing instructions, in the agent's control directory: the one
thing in context the agent cannot change. Root edits
`/etc/agent/agents/NAME/OWNER.md`; an agent edits `~/agents/NAME/OWNER.md`.
There is no command, and the installer never touches it. Children do not
see their parent's.

- **Reading.** At the start of each run, through the parent's helper, with
  the same limit as `README.md`.
- **Safety check.** Used only if it is a regular file owned by the owner's
  user and not writable by group or others, in a directory that meets the
  same test. Missing means no owner section. Otherwise unsafe or unreadable
  means the agent does not start: the reason goes to `ERROR`, the inbox is
  left alone, and later polls retry. Running without the owner's
  instructions would be worse.
- **Placement.** With non-whitespace content, the first system segment is
  the system prompt, a blank line, this header (verbatim) and the content:

  > # Owner's instructions
  >
  > Your owner wrote the following. You cannot change it, and agents you
  > create do not see it. It takes precedence over everything else in your
  > context.

  Without it the segment is the system prompt alone, so the cached prefix
  is unchanged. An edit takes effect on the next run.

## Configuration validation

Each run checks `config.json` against the plugin's `describe`: an installed
provider, one of its models, and only that model's options with allowed
values. On failure (including missing, unreadable or over 1 MiB) the agent
does not start; the reason goes to `ERROR` and the service log, the parent
is told once per distinct content (root's children: the log only), and the
check is retried when the file changes. The agent may change its own config;
the next run uses it.

## Plugin protocol (version 1)

A plugin is an executable. Per call, the daemon writes one JSON request to
its stdin and reads one JSON response from stdout; stderr goes to the
service log. Every message carries `"protocol": 1`.

**describe** (`{"protocol": 1, "type": "describe"}`, works without
credentials) returns:
- `settings`: `{name, description, secret}` for each environment variable
  it needs; the installer prompts for them.
- `models`: `{id, context_tokens, image_types, options}`. `image_types`
  lists accepted image media types (a 0.5.0 boolean `images` is ignored).
  `options` maps names to `{description, values}` or
  `{description, type: "integer", min, max}`.

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

The daemon interprets only `context_tokens`, `image_types` and `usage`.

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
  options, taken from the current Anthropic documentation.
- **openai-compatible**: Chat Completions with function calling.
  `OPENAI_BASE_URL`, `OPENAI_API_KEY` and `OPENAI_MODELS` (`id=context_tokens`
  pairs).
- **replay**: scripted replies for tests. `REPLAY_SCRIPT` (JSONL responses
  after a header describing models) and optional `REPLAY_LOG`.

## CLI

`NAME` is a child of the caller (`~/agents/NAME`, or
`/etc/agent/agents/NAME` for root); `messages` and `log` also take a deeper
descendant such as `research/agents/scraper`. A name is one plain directory
name, not starting with `.` (or `-`, for `agent new`), without control
characters. Root runs every command with `sudo`.

- `agent daemon`: the service (root only).
- `agent new NAME [CONFIG|-]`: create a child from a config file, stdin, or
  a copy of the caller's `~/config.json` (root must pass one). The config is
  validated against `providers.json` before anything is created; then the
  control directory (root 0700, agent 0750) and `config.json` (0640) are
  written, and removed again if writing fails. Prints the control directory
  and returns without waiting for admission.
- `agent remove NAME`: rename the child's control directory out of
  `agents/`, then remove it.
- `agent send NAME "text"|-`: message a direct child.
- `agent message "text"|-`: message the caller's owner (not for root).
- `agent messages [NAME]`: print a conversation once, control characters
  removed: the caller's with its owner, or NAME's with its parent.
- `agent log [NAME]`: print the newest journal once; with NAME, its `ERROR`
  first. Root must pass NAME to `messages` and `log`.
- `agent provider install NAME FILE` (root): install a plugin binary whose
  SHA-256 matches the index for NAME and this architecture, prompt for
  missing settings and refresh `providers.json`. Downloads nothing.
- `agent provider describe`: print the providers as JSON; as root it runs
  them and refreshes the cache, otherwise it reads `providers.json`.
- `agent notes`: print the embedded release notes
  (`internal/notes/notes.md`).
- `agent version`, `agent help`.

## Seed files

`internal/seed/README.md` (advice the agent may revise: memory, task state,
stopping, messages, subagents, and the facts it cannot discover) and
`TASK.md` ("No task yet."). Admission writes them where the home lacks
them.

## Install

```sh
printf 'header = "Authorization: Bearer %s"\n' "$GITHUB_TOKEN" | curl -fsSL -K - https://raw.githubusercontent.com/SEAN-7/bot/vX.Y.Z/bot/install.sh | sudo --preserve-env=GITHUB_TOKEN sh
```

`install.sh` takes no options and pins its version and the `agent` binary's
checksums (which pin the plugins'). It:
1. Installs `acl` if `setfacl` is missing, downloads the binary through the
   GitHub API into a root-only temporary directory and verifies it. The
   token reaches curl on stdin only.
2. Treats a machine with `/var/lib/agent/version` or `/etc/agent/agents/main`
   as an upgrade, and refuses, before changing anything, one older than
   0.4.1 (upgrade with the v0.4.1 `install.sh` first).
3. Stops the service (runs finish first), installs the binary, keeping the
   previous one to restore if anything fails before the service starts, and
   creates `agent-provider` and the directories above.
4. Creates missing pool users, each with its own group and an empty home.
5. Fresh install: asks for a provider, downloads and installs it with
   `agent provider install` (which prompts for its settings), and asks for a
   model. Upgrade: reinstalls each installed plugin, keeping the old one
   with a warning if that fails.
6. Installs and starts the service, then writes `/var/lib/agent/version`.
7. Fresh install: runs
   `printf '{"provider":"P","model":"M"}\n' | agent new main -`, saying how
   to retry if it fails.

**Releases** are reproducible (`CGO_ENABLED=0`, `-trimpath`, empty build ID,
toolchain pinned in `go.mod`). `scripts/release.sh` builds everything and
writes `plugins/index.json` and the checksums in `install.sh`; the tag's
workflow rebuilds and refuses to publish if any checksum differs.

## Tests

Paths, user names and timings are configurable, so tests run in a temporary
root against the replay plugin. Privilege tests need root and a throwaway
pool of real users (`AGENT_ROOT_TESTS=1`); they run in CI and, on the VM,
only in a throwaway directory, never against the live agent.
