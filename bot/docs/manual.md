# Manual

How this machine and its harness work, as seen from inside an agent. It is
written for the version that is running: the harness writes it, with the
release notes (`~/manual/notes.md`) and the version (`~/manual/version`)
beside it, each time it starts, and no agent can change it. Everything here is a fact about the harness; how to
work is yours to decide.

## You

- You are an agent: an unprivileged Unix user (`agent-N`, from a fixed pool
  that is reused) with a home directory. You have no sudo.
- Your owner is whoever created you: the human (root) for a top-level agent
  such as `main`, or your parent agent. You talk only with your owner and
  your own subagents.
- Your only tool is the shell. A run is a sequence of model calls; each
  reply either runs one command or ends the run.

## Your home

The harness uses these names in your home; everything else is yours.

| Name | What it is |
| --- | --- |
| `README.md` | Loaded into every model call of every run (first 1 MiB). Yours to edit; missing means empty. |
| `config.json` | Your provider, model and options (see Configuration). Yours to edit. |
| `inbox/` | Files that wake you and are delivered to you (see Runs). |
| `inbox/processed/` | Where delivered inbox files are moved. |
| `inbox/.tmp/` | Where the harness stages the files it writes into your inbox; never delivered. |
| `agents/` | Your subagents' control directories (see Subagents). |
| `.messages/` | Your outgoing message spool (see Messages). |
| `journal` | Link to your journal directory, read-only (see Journal). |
| `manual` | Link to this manual, read-only. |

Your home is setgid and commands run with umask 022, so the agents above
you (your owner and its ancestors, other than root) can read what you
write; nobody else can enter it. You can tighten your own files' modes. The
harness creates `inbox/`, `inbox/processed/`, `inbox/.tmp/`, `agents/`, `.messages/`,
`.messages/agents/` and control directories 0750, and inbox files,
delivered messages, spool files and `config.json` 0640. Where a name the
harness uses already exists, it is left as it is.

## Commands

- A command runs as your user with `/bin/bash -c` (or `/bin/sh -c`), in
  your home, in its own process group, with stdin `/dev/null` and umask
  022. Environment: `HOME`, `USER`, `LOGNAME`,
  `PATH=$HOME/bin:/usr/local/bin:/usr/bin:/bin`,
  `XDG_RUNTIME_DIR=/run/user/<uid>`,
  `DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/<uid>/bus`, `TERM=dumb`.
- The `sh` tool takes `command` and `wait`: seconds to wait, rounded down,
  1 to 600, 120 when missing or not a number. A command still running after
  its wait has its whole process group killed.
- The result is `exit status N`, a newline and the combined stdout and
  stderr. After a timeout it starts instead with "Command timed out after
  Ns; its process group was killed. Give it a longer wait (up to 600s), or
  run it in the background with systemd-run --user." Output over 16 KiB is
  clipped to its first and last 8 KiB, followed by "[output clipped;
  redirect long output to a file to read all of it]". If a command could
  not be started, the result is "could not run command: " and the error.
- When a command finishes, every process of yours that it left running (in
  the background, with `&`, `nohup` or `setsid`) is killed, and the result
  ends with "Killed N processes this command left running (names). A
  command's processes end with it; start anything that should keep running
  with systemd-run --user." Processes started with `systemd-run --user` or
  by cron run under your own systemd and are not touched.
- You have your own systemd user manager (lingering is enabled), so
  `systemd-run --user` jobs, user units and timers keep running after a
  command ends and while the harness restarts.
- When the machine runs out of memory, the kernel kills the process using
  it; your command returns that failure (usually exit status 137) and the
  run continues.

## Runs

**Waking.** You run when `~/inbox/` holds a regular file whose name does
not start with a dot. Nothing else wakes you: there is no heartbeat, and
once a run ends nothing runs you again until such a file arrives. A file
whose name starts with a dot does not wake you, but is delivered with
your next run, whatever starts it, so it can wait without costing a run.
A file written straight into `~/inbox/` can be collected before it is
complete.

**Start of a run.** In order:
1. `config.json` and your owner's `OWNER.md` are read and checked (see
   Configuration and Owner's instructions). If either fails, the run does
   not start and the inbox is left alone.
2. If the harness was upgraded since your previous run, the upgrade note is
   written to your inbox (see Upgrades).
3. The run's journal file is created, with its `start` record.
4. `~/journal` and `~/manual` are linked.
5. `README.md` is read.
6. The inbox is collected: every regular file in name order, dot-files
   included; directories are skipped. Each is journaled and moved to `inbox/processed/`
   (with a numeric suffix if that name is taken).

Every run starts fresh from the files on disk, with its own journal file.

**Context.** Each model call sends:
- The system prompt, verbatim:

  > You live on this machine. Your only tool is the shell. ~/README.md is
  > loaded every time you start, and you may edit it. Messages arrive as
  > files in ~/inbox/. ~/journal/ is a read-only record of everything you've
  > done, and ~/manual/ describes how this machine and its harness work.
  > When a run ends or your context resets, you keep only what's on disk. To
  > stop, reply without a command. That reply goes only to your journal; it
  > is not sent to anyone.

  followed, if your owner wrote instructions, by those instructions (see
  Owner's instructions). This is the first system segment.
- `README.md`, the second system segment.
- The `sh` tool. Its description: "Run a shell command on this machine as
  your user, in your home directory. Returns combined output and exit
  status. A command still running after its wait is killed." Its
  parameters: `command` (string, required: "The command to run") and
  `wait` (integer: "Seconds to wait for the command before killing it.
  Default 120, maximum 600.").
- A first user message holding the collected inbox files, then every reply
  and command result so far. After each command, inbox files that arrived
  meanwhile are collected the same way and sent after its result.

How the provider caches these between calls, and what that costs, is the
`caching` text in `/var/lib/agent/providers.json` (see Configuration).

**Inbox records.** Each inbox file reaches you as one JSON record:
`{"file": "inbox/processed/NAME", "from": "owner" | "agents/NAME" | "data", "content": "..."}`.
- `from` is `owner` only for a message your owner sent you, and
  `agents/NAME` only for one your subagent NAME sent (see Messages): the
  harness records each delivery (agent, file name,
  SHA-256, sender) and labels a file only when all three match. Everything
  else, including the harness's own notes and files your scripts write, is
  `data`. Content is a JSON string, so no file can forge the field.
- Content over 64 KiB is clipped and followed by "[file clipped; full
  file: PATH]".
- A JPEG, PNG or GIF up to 5 MiB and 2000 px a side, of a type your model
  accepts (`image_types` in `/var/lib/agent/providers.json`), is shown to you as
  an image; its record's content is "[image/png 1280x960, 169027 bytes,
  shown below]". Shown images are resent with every call, so a run shows
  at most 20 MiB of them. Images are never resized.
- Anything else arrives as a note naming the file:
  - "[image not shown: N bytes is over the 5 MiB limit; resize it and copy
    the result into ~/inbox/. File: PATH]"
  - "[image not shown: WxH is over 2000 px a side; resize it and copy the
    result into ~/inbox/. File: PATH]"
  - "[image not shown: could not decode it: ERROR. File: PATH]"
  - "[image not shown: TYPE is not supported, only TYPES; convert it and
    copy the result into ~/inbox/. File: PATH]"
  - "[image not shown: the model does not accept images. File: PATH]"
  - "[image not shown: its journal copy could not be saved: ERROR. File:
    PATH]"
  - "[image not shown: images in this run would exceed 20 MiB; resize it,
    or copy it into ~/inbox/ again after this run ends. File: PATH]"
  - "[binary file not shown: TYPE, N bytes. File: PATH]", for other
    non-UTF-8 content, such as a PDF.

**Ending a run.** A reply without a command ends the run; the reply goes
only to your journal. A run also ends when the context is too large (see
Context resets), when the harness stops (see Restarts) and when you are
removed.

**Context resets.** The harness does not measure context. When the
provider rejects a call as too long, the run ends and "Context reset.
Previous journal: PATH" is written to your inbox, which starts a new run.
If that happens on a run's first call, the run instead stops with this
in your `ERROR` (see ERROR): "Context is over the model's limit at the
start of a run; not starting. Reduce README.md or the inbox files.
Provider: ERROR", and you wait for new input, so resets cannot loop. A
request over the provider's size limit (32 MB for anthropic) counts as too
long. Each call's token usage is in your journal; its four counts add up to
the context in use.

**Provider failures.** A failed call is retried with delays doubling from
2 seconds to 10 minutes. After 3 consecutive failures, "Provider failing
since TIME: ERROR" is written to your `ERROR`; the next successful call
clears it.

**Restarts.** When the harness stops (an upgrade or a restart), it starts
no new runs and lets running ones finish for up to 15 minutes; any still
going are interrupted. When the harness next starts, a run whose journal
has no end record is reported with "You were interrupted. Interrupted
journal: PATH" in your inbox, which starts a new run.

**Upgrades.** Each run's journal `start` record holds the harness version
that ran it. When a run starts on a newer release than your previous run,
`<time>-harness-upgrade.md` is written to your inbox just before it is
collected: "The harness was upgraded from vX to vY. What changed for you,
newest first (~/manual/notes.md has every release):" and the release-notes
sections in between. If your previous run recorded no version (it ran
before 0.6.0), it says instead "The harness was upgraded to vY. What
changed for you since v0.5.1, newest first (~/manual/notes.md has every
release):" and covers every release since 0.5.1. Nothing wakes you
for an upgrade: the note arrives with your next run, whatever starts it.
An agent that has never run gets none, and development builds send none.

## ERROR

`ERROR` is a file in your control directory (see Subagents), owned by your
owner, so you cannot remove or hide it. The harness writes there why you
cannot run or why calls are failing, and clears it when the cause is gone:
- "config.json: " and the reason, such as "config.json: larger than 1 MiB"
  (see Configuration);
- "OWNER.md: " and the reason (see Owner's instructions);
- "Agent limit reached (max_agents=N); this agent was not started.";
- "Provider failing since TIME: ERROR" (see Runs).

## Configuration

- `config.json` is `{"provider": "P", "model": "M", "options": {...}}`. Each
  run checks it against the installed providers: an installed provider,
  one of its models, and only that model's options with allowed values. A
  missing, unreadable, invalid or over-1-MiB file stops your runs: the
  reason goes to your `ERROR` ("config.json: " and the reason), and an
  agent owner is told once per distinct content ("Subagent NAME did not
  start: config.json: REASON"). The check is retried when the
  file changes; a valid change takes effect at your next run.
- `/var/lib/agent/providers.json` (owned by root, readable by all) is
  `{"providers": {NAME: {...}}}` for each installed provider: `models` (`id`, `context_tokens` where
  known, `image_types`, `options` with their allowed values or integer
  ranges) and `caching`, the provider's own text on what it caches between
  calls and for how long, the smallest prompt it caches, where its cache
  markers go, and what cache reads and writes cost relative to plain input.

## Owner's instructions

Your owner may write standing instructions in `OWNER.md` in your control
directory. If it has any non-whitespace content, the first system segment
is the system prompt, a blank line, this header and the content:

> # Owner's instructions
>
> Your owner wrote the following. You cannot change it, and agents you
> create do not see it. It takes precedence over everything else in your
> context.

It is read at the start of each run (first 1 MiB), so an edit takes effect
on the next run. It is used if it is a regular file (a link to one
counts). Missing means no instructions. Anything else, or unreadable,
means you do not run: "OWNER.md: " and the reason go to your `ERROR`, the
inbox is left alone, and it is checked again each poll. The harness states
this precedence; it does not enforce it.

## Messages

- A message is a file. Write it under a name starting with a dot, then
  rename it, so it is never read half-written; the harness ignores dot
  names. For example:
  `f=$(date -u +%Y%m%dT%H%M%S.%NZ).md; printf '%s\n' "text" > ~/.messages/.$f && mv ~/.messages/.$f ~/.messages/$f`.
- About once a second the harness collects the regular files you own in
  each spool, other than dot-files, in name order (so names that sort in
  time order, such as UTC timestamps, keep messages in order), delivers
  each and removes it. Anything else there (a link, a directory, another
  user's file) is left alone. A crash can duplicate a message but not lose
  one.
- **To your owner:** a file in `~/.messages/`. If your owner is an agent, it
  reaches its inbox as `<time>-NAME.md` (NAME is yours) with `"from":
  "agents/NAME"`. If your owner is the human, it is only recorded in your
  conversation log, which the human reads; nothing notifies them.
- **To a subagent:** a file in `~/.messages/agents/NAME/`, which the
  harness creates when NAME is admitted (you may create it sooner). It
  reaches your direct child NAME's inbox as `<time>-owner.md` with `"from":
  "owner"`. A message for a subagent not yet running waits while it may
  still be admitted; otherwise the file is removed and "Message to NAME
  dropped: there is no subagent by that name." arrives in your inbox. Only
  a real directory you own, named like a subagent, is a spool.
- A message over 64 KiB is delivered as its first 64 KiB followed by
  "[message truncated]", and you are told: "Message to your owner was
  truncated to 64 KiB." or "Message to NAME was truncated to 64 KiB."
- **Conversation log:** `~/journal/messages.jsonl`, written only by the
  harness, one record per message: `{"t", "from": "owner" | "agent",
  "text"}`. A subagent's is `~/journal/agents/NAME/messages.jsonl`.

## Journal

- `~/journal` links to your journal directory under `/var/log/agent/`,
  owned by root and readable by you and the agents above you, not by your
  siblings. You cannot change it.
- One JSONL file per run, named for its UTC start time. Records: `start`
  (config and harness version), `inbox` (each file with its label; a shown
  image also gets `image: {media_type, width, height, bytes, sha256,
  copy}`), `assistant` (the reply, its command and wait, and the call's
  `usage`: `input_tokens`, `output_tokens`, `cache_read_tokens`,
  `cache_write_tokens`), `result` (exactly what the model got), `note` and
  `end` (with `reason`: `done`, `context`, `removed` and others).
- The directory also holds `messages.jsonl` and a copy of each shown image
  as `<run>-<n>.<ext>`, which are not runs. A subagent's journal directory
  is inside yours.
- Your newest run is the last `*Z.jsonl` in `~/journal/`; a subagent's
  runs are in `~/journal/agents/NAME/`, and a deeper descendant's under
  `~/journal/agents/NAME/agents/...`.

## Subagents

- A subagent is a directory: `~/agents/NAME/`, a real directory you own,
  holding a regular file `config.json` (see Configuration).
  `cp ~/config.json ~/agents/NAME/` gives it a copy of yours. NAME is one
  plain directory name, not starting with a dot, without control
  characters. A directory without a `config.json` is not a subagent, and
  nothing happens.
- When `config.json` appears it is checked against
  `/var/lib/agent/providers.json`, as each run checks yours. If it is
  invalid or over 1 MiB, no pool user is assigned: `~/agents/NAME/ERROR`
  holds "config.json: " and the reason, you are told once ("Subagent NAME
  did not start: config.json: REASON"), and you can fix the file in place.
- A valid one is admitted, usually within a second: a pool user is
  assigned, its home is prepared with the seed `README.md`, its
  `config.json` moves into that home (one written into `~/agents/NAME/`
  later is ignored), `~/agents/NAME/home` links to it, and
  `~/.messages/agents/NAME/` is created. It starts work when you send it a
  message. Once admitted, its config is its own.
- If it cannot be admitted for another reason, `~/agents/NAME/ERROR` says
  why and you are told once per reason: "Subagent NAME was not started:
  agent limit reached (max_agents=N)." `max_agents` bounds the whole tree,
  the human's agents included.
- Its control directory `~/agents/NAME/` is yours and holds its `OWNER.md`
  (your standing instructions to it, which it cannot change and which do
  not include yours), its `ERROR` and the `home` link, which is written at
  admission and when the harness restarts, not put back if you delete it.
- Its home is its own user's. You can read it (you are in its group) but
  not write to it. It cannot read yours unless you grant access, and even
  then cannot list or read `~/inbox`, `~/agents` or `~/.messages` unless a
  default ACL on `~` names its user, which harness directories created
  later would inherit.
- **Sharing** is by `setfacl` on files and directories you own. Granting
  its user `x` on `~` lets it reach any file there that others can read.
  Its user is `stat -L -c %U ~/agents/NAME/home`. Pool users are reused:
  files it created in your directories, and grants naming its user, stay
  until you remove them.
- Deleting the directory (`rm -r ~/agents/NAME`) stops it and all its
  subagents, deepest first, and
  reclaims their users: lingering is disabled, the crontab removed, the
  user manager stopped and its processes killed, the home emptied, and its
  files in `/tmp`, `/var/tmp` and `/dev/shm` deleted. Journals remain, in
  your journal directory. An idle subagent costs nothing.
- Your subagents' messages reach you as `"from": "agents/NAME"`. You can
  read their journals, conversation logs and `ERROR`:
  `~/journal/agents/NAME/` and `~/agents/NAME/ERROR`.

## The agent command

Everything you do with the harness is a file (see Messages, Subagents,
Journal and Configuration); the running harness version is in
`~/manual/version`. The `agent` command is the service and root's plugin
installer, and `agent help` lists them.

## Known limits

- A process keeps the groups it started with: after your owner gains or
  loses a subagent, a process of yours started earlier sees the change only
  after restarting.
- A default ACL on `~` naming a subagent's user is inherited by harness
  directories created there later.
- An existing harness directory is left as it is, so one you opened stays
  open.
