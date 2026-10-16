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
- **Each agent is its own Unix user**, created by the daemon at admission
  (`useradd`, with the agent id in the comment field) and deleted at
  removal (`userdel -r`); the passwd file is the only record. `max_agents`
  bounds how many exist at once.
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
- **Everything an agent does with the harness is a file.** A message is a
  file the sender writes into the recipient's inbox, a subagent is a
  directory holding a `config.json`, and journals, providers and the manual
  are files to read. The binary is the daemon, its file helper and root's
  plugin installer.
- **Upgrades are not announced.** Nothing is written to an agent for an
  upgrade; the manual and each run's `start` record carry the version.
- **The repository is public.** Installs download release assets without
  credentials and trust only the checksums pinned in `install.sh`.
- **Conversations are pull, not push.** A top-level agent's replies are
  files in its home that the human reads; alerts are something the agent
  builds.
- **The harness presents files; it does not convert or check them.** A
  file reaches the model as text, or as an image when its type is one the
  model declares; anything else (a PDF) arrives as a note with its path.
  Image limits belong to the plugin, which knows its provider's.
- **`OWNER.md` precedence is stated, not enforced.** The harness enforces
  only the privilege model.
- **The cached prefix changes rarely.** The system prompt, the owner
  section's header and the `sh` tool definition are part of every agent's
  cached prompt; changing them rebuilds every cache once.

## Filesystem layout

| Path | Owner, mode | Contents |
| --- | --- | --- |
| `/usr/local/bin/agent` | root, 0755 | The harness binary |
| `/etc/agent/` | root, 0711 | Root's place in the tree: `settings.json` (0600), `credentials/`, `plugins/`, `agents/` |
| `/etc/agent/credentials/NAME.env` | root, 0600 | Plugin settings as `KEY=value` lines |
| `/etc/agent/plugins/` | root:agent-provider, 0750 | Plugin executables (same owner and mode) |
| `/etc/agent/agents/NAME/` | root, 0700 | Root's control directory for its child NAME |
| `/etc/agent/agents/NAME/OWNER.md` | root, 0644 | The human's standing instructions for NAME; optional |
| `/var/lib/agent/providers.json` | root, 0644 | Cached `describe` output, no secrets |
| `/var/lib/agent/manual/` | root, 0755 | `manual.md`, `notes.md` and `version` (files 0644) |
| `/var/lib/agent/version` | root, 0644 | Installed version, written by `install.sh` |
| `/var/log/agent/` | root, 0755 | Journals |
| `/home/agent-N/` | agent-N:agent-N, 2750 | An agent user's home, created at admission and removed with the user |

`settings.json` holds `max_agents` (default 16), which bounds the whole
tree, main included. It is read when the daemon starts; a change takes
effect at the next restart. What the harness keeps in an agent's home and control
directory, and their modes, are in the manual ("Your home", "Subagents").

## Privilege model

The daemon runs as root; each agent is an unprivileged user `agent-N` of
its own.

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
keys journals, delivery records and the user's comment field. Root has
control directories but no loop, inbox, journal or owner.

- **Startup.** Require root. Resolve the provider user, start root's
  helper, write the manual (see Manual), migrate a `pool.json` left by a
  release before 0.9.0 (its active entries' users get their agent ids as
  comments, every other `agent-N` user is deleted, and the file goes; the
  code leaves in 0.10.0), then poll about once a second: read the agent
  users from the passwd file, finish any deletion in progress, walk the
  tree.
- **Discovery.** Walk the tree breadth-first, listing each node's `agents/`
  through its own helper without following symlinks. A control directory is
  a real directory owned by the node's user in an `agents/` that is itself
  one, named by one plain name not starting with `.` and without control
  characters. One not yet running whose id names a user is admitted with
  it (a restart: the home is kept), or else with a new user if it holds a
  regular `config.json`; otherwise it is left alone. Failures are logged once
  and retried next poll. A known agent is kept unless its control directory
  is certainly gone. A `config.json` in an admitted agent's control directory
  is ignored.
- **Admission.** Unless the agent already has a user: refuse when
  `max_agents` users exist, else create the user `agent-N` for the lowest
  N no user has, its id in the comment. Prepare the home (2750, setgid);
  start the user's helper; clear what a previous user of the name may have
  left in the home; set the group's members; create the harness
  directories where missing (`mkdir` with their modes: `inbox` 1770, the
  rest 0750; an existing directory of the user's is left as it is,
  anything else of that name fails the step); write the seed `README.md`
  where missing; grant the user `wx` on its parent's inbox (see Messages;
  not for root's children); move the starting `config.json` into the home
  unless it already has one; enable lingering; create the journal
  directory; write the `home` link (never recreating a control directory
  that was removed). A failed step is retried next poll. The control directory's
  device and inode are part of the agent's identity, so renaming or
  recreating it makes a new agent. A deleted `home` link is written again
  only at the next admission (a restart), so `rm -r` of a control directory
  never races it.
- **Admission errors.** `max_agents` users existing ("Agent limit reached")
  keeps the agent out: the reason goes to its `ERROR` and the service log, and the
  parent's inbox is told once per reason (root's children: the service log
  only). A starting `config.json` over 1 MiB fails adoption, which is
  logged once and retried each poll until the parent shrinks it in place.
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
  then its user is deleted. Journals remain.
- **Deletion.** Mark the user removed (its comment becomes `removed:` and
  the id, so a control directory made again under that id gets a new user
  rather than this one); disable lingering; remove its crontab; stop its
  user manager and kill its processes; remove its files in `/tmp`,
  `/var/tmp` and `/dev/shm`, as the user; move its journal directory to
  the nearest remaining ancestor's group; `userdel -r`, which removes the
  home and the group and takes the user out of its descendants' groups. An
  interrupted deletion finishes on the next start, from the mark; a failed
  one is retried every poll.

### Known limits

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
  the agent's home directory and renamed into the inbox, so a run never
  collects one half written. A crash leaves at worst a dot-file in the
  home.
- **Labels.** An inbox file is labelled by the uid the helper's listing
  reports as its owner: the parent's uid (root's for a top-level agent) is
  `owner`, the uid of a running child of the agent is `agents/NAME`, and
  anything else is `data`. The kernel set the uid, so nothing an agent
  writes carries another's label.
- **Images.** Detected with `http.DetectContentType` and shown when the
  type is in the model's `image_types`; a file longer than the 16 MiB read
  limit is a binary note. Size and decoding limits are the plugin's. A
  shown image is copied into the journal directory first; one whose copy
  cannot be saved is not shown. The anthropic API's 32 MB request limit is
  reported by its plugin as `context_overflow`.
- **First-call guard.** A `context_overflow` on a run's first call writes
  `ERROR` instead of the reset note and waits for new input, so resets
  cannot loop.
- **Provider failures** are retried in the loop; after 3 consecutive
  failures the error goes to `ERROR` and the service log.
- **Strings.** Every note, the system prompt and the `sh` tool are in
  `internal/daemon/messages.go` and `internal/proto`; the manual quotes
  them, and a test keeps the two in step.

## Messages

What agents send and receive is the manual's "Messages". The daemon is not
in the path: the sender writes the file into the recipient's inbox, and
the kernel's record of who wrote it is the label (see Labels).

- **Down.** An agent's `inbox/` is 1770 in its own group, whose members are
  its ancestors (see Groups), so its parent writes straight into it; the
  sticky bit keeps anyone from removing or renaming another's file. Root
  writes into a top-level agent's inbox as root.
- **Up.** At admission the daemon grants the child's uid `wx` on the
  parent's `inbox/` by running `setfacl -m u:UID:wx ~/inbox` as the parent
  through `shell.Run` (never as root: a planted link would reach nothing
  the parent could not), and each run links `~/owner` in the child's home
  to that inbox. A grant that fails holds admission, logged once and
  retried. At removal the grant is revoked the same way, when the parent is
  still running; a failure is logged and not retried, since a uid that
  comes back for another agent is labelled `data` anyway. Root has no
  inbox, so a top-level agent has no `~/owner`.
- Nothing is logged: the inbound side is in the recipient's journal, with
  its label.

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
unchanged. What the agent sees is in the manual. The control directory is
the parent's and the agent has no way into it, so nothing checks its
owner or mode.

## Configuration validation

Each run checks `config.json` against `providers.json`, the cached
`describe` output: the provider is installed and the model is one of its.
No plugin runs at a run's start; options are the plugin's to reject, with
`kind: "invalid_request"`. On failure the reason also goes to the service
log; root's children's failures go to the log only.

## Plugin protocol

The protocol (version 1) is a public contract, in [plugins.md](plugins.md)
with what plugin authors need: `describe`, `complete`, the error kinds and
the image rule. How the daemon acts on it: a `context_overflow` ends the
run with a reset or triggers the first-call guard; an `invalid_request`
ends the run with reason `invalid` and the message in `ERROR`; anything
else is retried. The daemon interprets only `image_types` and the error
kinds; `usage` is journaled and `caching` cached in `providers.json`,
neither interpreted. An older plugin that omits `caching` still works, so
the version is unchanged.

## Plugins

Sources live in `plugins/<name>/`; releases publish each as
`plugin-<name>-linux-<arch>`, and `install.sh` verifies the ones it
installs against the checksums pinned in it. `agent provider install`
itself verifies nothing: root installs any plugin it chooses, from a file
or a URL.

- **anthropic**: Messages API, `ANTHROPIC_API_KEY`. Cache markers on each
  system segment (at most the last three) and the latest message. Declares
  JPEG, PNG and GIF; each image follows its message's text as a text block
  naming the file and an image block. An image over 5 MiB, undecodable, or
  over 8000 px a side is replaced by a text block saying so and naming the
  file, so the request still goes through. HTTP 413 is `context_overflow`
  and a 400 that is not an overflow is `invalid_request`. Exposes the models' reasoning settings as
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
helper), `agent provider install NAME SOURCE` (root: install the plugin
binary at SOURCE, a file or an http(s) URL of at most 64 MiB, as NAME,
prompt for missing settings and refresh `providers.json`) and
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

`install.sh` takes no options and pins its version and the checksums of
the `agent` binary and the release's plugins. It:
1. Installs `acl` if `setfacl` is missing, downloads the binary from the
   release into a root-only temporary directory and verifies it.
2. Treats a machine with `/var/lib/agent/version` or `/etc/agent/agents/main`
   as an upgrade, and refuses, before changing anything, one older than
   0.4.1 (upgrade with the v0.4.1 `install.sh` first).
3. Stops the service (runs finish first), installs the binary, keeping the
   previous one to restore if anything fails before the service starts, and
   creates `agent-provider` and the directories above.
4. Fresh install: asks for a provider, downloads it, verifies it against
   the pinned checksum, installs it with `agent provider install` (which
   prompts for its settings), and asks for a model. Upgrade: reinstalls
   each installed plugin the same way, keeping the old one with a warning
   if that fails.
5. Installs and starts the service, then writes `/var/lib/agent/version`.
6. Fresh install: creates `/etc/agent/agents/main` (0700) and writes its
   `config.json`, saying how to retry if that fails; the daemon admits it.

**Releases** are reproducible (`CGO_ENABLED=0`, `-trimpath`, empty build ID,
toolchain pinned in `go.mod`). `scripts/release.sh` builds everything and
writes the checksums in `install.sh`; the tag's workflow rebuilds and
refuses to publish if any checksum differs.

## Tests

Paths, user names and timings are configurable, so tests run in a temporary
root against the replay plugin. Privilege tests need root
(`AGENT_ROOT_TESTS=1`) and create real users under a test prefix; they run
in CI, and on a machine with live agents they need their own temporary
root.
