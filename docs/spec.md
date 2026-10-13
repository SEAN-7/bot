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
- **The harness presents files; it does not convert them.** An inbox file
  reaches the model as text or as an image only in a form the model accepts
  natively and only when there is exactly one way to present it. Anything
  else, a PDF for example, arrives as a note with its path, and the agent
  converts it with its own tools. Which image types a model accepts is
  provider knowledge, declared by its plugin.
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
| `/etc/agent/plugins/` | root:agent-provider, 0750 | Plugin executables (also root:agent-provider 0750); no agent user can read or run them |
| `/etc/agent/agents/NAME/` | root, 0700 | Root's control directory for its child NAME (`main` for the main agent) |
| `/etc/agent/agents/NAME/OWNER.md` | root, 0644 | The human's standing instructions for NAME; optional |
| `/etc/agent/.messages/agents/NAME/` | root, 0700 | Root's spool for its child NAME; files 0600 |
| `/var/lib/agent/delivered.jsonl` | root, 0600 | Record of messages delivered (agent id, file name, SHA-256, sender) |
| `/var/lib/agent/pool.json` | root, 0600 | Pool assignments: user, agent id, state, release notes seen |
| `/var/lib/agent/providers.json` | root, 0644 | Cached `describe` output with no secrets, for `agent provider describe` |
| `/var/lib/agent/version` | root, 0644 | The installed version, written by `install.sh` |
| `/var/log/agent/` | root, 0755 | Journals and conversation logs (see Journal) |
| `/home/agent-N/` | agent-N:agent-N, 2750 | A pool user's home: an agent's home while assigned, empty while free |

`settings.json` holds `max_agents` (default 16); the `repository` an older
install wrote there is ignored. The pool holds `max_agents` users, `agent-1` to
`agent-<max_agents>`, and bounds the whole tree, main included. A fresh
install's main agent is `agent-1`; one upgraded from 0.3 holds the user its
0.3 user was renamed to (`agent-16` by default, see Migration).

In an agent's home, the harness uses `README.md`, `TASK.md`, `config.json`,
`inbox/`, `inbox/processed/`, `agents/`, `.messages/` and the `journal`
symlink.

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
through its home (x on `~`, see Sharing) opens what others can read
there, so the harness creates its own parts of the home group-only: the
directories `inbox/`, `inbox/processed/`, `agents/`, `.messages/` and
`.messages/agents/` (0750, see Admission), the inbox notes and delivered
messages it writes (0640), the control directories `agent new` creates
(0750, `config.json` 0640) and the spools `agent message` and `agent send`
write (`.messages/` directories 0750, files 0640). A child never needs its
control directory, and cannot read its parent's inbox, its siblings'
`OWNER.md`, `ERROR` or starting `config.json`, or messages in flight.
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

Helper operations: `list` (directory entries with type, size and owner),
`read` (path, byte limit; regular files only), `write` (atomic:
dot-prefixed temporary file then rename, with the mode given), `private`
(make each path a real directory: create a missing one 0750, closed to
others, and keep an existing one the user owns as it is; never following a
link, and leaving alone, and returning, what is not a directory the user
owns, and anything below it), `rename`, `remove` (recursive, never
following symlinks), `unlink` (one entry, never a directory's contents),
`symlink` (create or replace), `clear` (empty a
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
it that others can read, so the seed README advises a dedicated share
directory. Users are reused, so a parent deletes what a child created in
its directories and removes its grants before removing the child: on the
shared directory (`setfacl -R -x u:USER,d:u:USER DIR`), and on `~` and any
other directory the child traversed (`setfacl -x u:USER ~ ...`); the seed
README says so too.

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
- Startup: require root; refuse to start when `pool.json` is missing but
  `/var/log/agent` holds anything other than regular files (an agent's
  journal directory, or a link that may lead to one), since `pool.json` is
  the only record of which user each agent holds and without it a live
  agent's home could be cleared or handed to another agent (a fresh install
  has neither, and starts); resolve the provider user and the pool users
  (missing ones are logged, and agents are limited to the rest); start
  root's helper; then poll about once a second. Each poll first finishes
  any reclaim left in progress (see Reclaim), then walks the tree, then
  collects messages. The refusal names the README section "If pool.json is
  lost", which gives two ways to start again: restore only the latest
  backup of `pool.json`, taken after the last admission or removal (see
  Known limits), or start with no agents by moving the journals somewhere
  only root can read, after stopping every pool user's processes, crontab
  and lingering (`loginctl disable-linger agent-N`, `crontab -r -u
  agent-N`, `pkill -KILL -u agent-N`), which the daemon does not stop.
- **Discovery.** The tree is walked breadth-first from root, each node's
  `agents/` directory checked (`stat`) and listed through its own helper,
  never following symlinks. A control directory is a real directory (not a
  symlink) at `<home>/agents/<name>`, owned by that node's user (root for
  `/etc/agent/agents/`), in an `agents/` that is itself a real directory
  that user owns. One the daemon does not run yet is admitted, in
  this order:
  1. with its own user, when `pool.json` records one for its id (a daemon
     restart; the home is kept, and a missing `home` link is restored);
  2. with a fresh user, when it holds a regular `config.json`.

  Otherwise it is left alone (it may still be being written). A failure
  while deciding (a file or helper that fails) is logged once and the
  decision made again at the next poll. A `config.json` that appears in an
  admitted agent's control directory is ignored: the agent's config is its
  own, and stays where it is. A known agent the scan misses is
  kept unless its control directory is certainly gone.
- **Admission.** The daemon takes the lowest free pool user and
  records the assignment in `pool.json`, then: prepares the home
  (`/home/agent-N`, 2750, setgid to the user's group); starts the user's
  helper; when its entry records no release notes seen (a new agent),
  records the running version; for a user new to this agent, clears what a
  previous holder left in the home (reclaim normally leaves it empty); sets
  the members of the user's group to the agent's ancestors other than root;
  creates the harness's directories `inbox/`, `inbox/processed/`,
  `agents/`, `.messages/` and `.messages/agents/` where
  missing, 0750, closed to others (`private`: existing ones are kept as
  they are; a link, a file or another user's directory of that name is
  left alone, and the service log names it once per daemon run) and
  writes the seed `README.md` and `TASK.md` where missing; moves the
  starting `config.json` from the control directory into the home (written
  through the agent's helper, removed through the parent's; a home that
  already has one keeps it, and the parent's stays in the control
  directory, unread until the home lacks one); enables lingering; creates
  its journal directory; and writes the `home` symlink into the control
  directory, through the parent's helper, but never creates the directory:
  if `agent remove` moved it away since the scan, admission stops there and
  the agent is not started. Every step goes through the helpers,
  and a failed step is retried at the next poll. A new agent whose
  starting `config.json` is larger than 1 MiB is not given a user: the
  daemon writes "config.json: larger than 1 MiB" to its `ERROR` and the
  service log, and notifies the parent's inbox once (root's children: the
  service log only). An agent's identity also
  includes its control directory's device and inode, recorded at admission:
  renaming the directory removes the agent and admits a new one, and so does
  removing it and creating it again, even between two polls. Each poll
  restores an agent's `home` link if its parent removed it.
- **max_agents.** When no pool user can be given to a new control
  directory, it is not admitted. The daemon writes the reason to its `ERROR`
  and the service log, and notifies the parent's inbox once per reason
  (root's children: the service log only): the agent limit, reached when
  `max_agents` users (or every pool user there is) are assigned.
  Admission removes that `ERROR`.
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
  (through its helper, after its processes are gone); clears
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
  assignment without `notes` (a newly assigned agent, or one from before
  notes were recorded) records the
  running version at admission, with nothing to catch up on, and gets the
  notes of later releases; development builds deliver nothing.
  `/var/lib/agent/version` only records the installed version, for
  `install.sh`. The agent can read the same notes with `agent notes`;
  delivery is to end in 0.5.2, after which an agent records `agent version`
  and checks `agent notes` when it changes.
- **Message spools.** See Messages.

### Known limits

- Starting again after moving the journals aside, no agent holds a user:
  the existing control directories are left alone, having no
  `config.json`, and a user given to a new agent has its home cleared, but
  its former holder's processes, crontab and lingering are not stopped:
  root stops them first (see Startup).
- A restored `pool.json` older than the last admission or removal can map a
  user to an agent that has since been removed: the daemon then reclaims
  that user and wipes the home of the agent that now holds it. Only the
  latest backup is safe to restore.
- While a Stat or lookup error persists for a not-yet-running child's
  control directory (for example a parent setting it to mode 000), messages
  for that child wait and the error is logged once.
- The harness creates its directories closed only to others. If an agent
  sets a default ACL on `~` naming a child's user (`setfacl -d -m
  u:USER:...`), the harness's directories created there later inherit that
  entry, so the child can list and read them. The seed README says never to
  set a default ACL on `~`.
- Admission only creates the harness's directories and leaves existing ones
  as they are, so one the agent opened to others itself stays open.
- Reclaim leaves the links in `/home`. On an installation upgraded from 0.3,
  `/home/agent` leads to the main agent's home (see Migration); the
  operator removes it by hand, or it would lead into the next holder's home
  if `main` were removed.

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
   numeric suffix if a name is already taken). Each file is classified
   first: images the model accepts and the daemon can check are shown to
   the model (below); other binary data is replaced by a one-line note.

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
- **Images.** A file that `http.DetectContentType` calls an `image/*` type
  is shown to the model, as an image on the user message beside the records,
  when the type is in the model's `describe` `image_types` and the daemon can
  check it: `image/jpeg`, `image/png` and `image/gif`, which the standard
  library decodes. Its record's `content` describes it (`[image/jpeg 1280x960, 169027 bytes,
  shown below]`). Limits: 5 MiB by the file's full size, and 2000 px on each
  side, because the API rejects larger images in a request with more than
  20 images instead of downscaling them; the harness resizes nothing. The
  dimensions are read from the header before decoding, so a small file
  declaring a huge size cannot make the daemon allocate it; the whole image
  is then decoded, so a damaged file cannot cause an API error that would be
  retried forever. Shown images stay in the run's conversation and are
  resent on every call, so a run shows at most 20 MiB of images in all; an
  image that would exceed that is not shown, and the images after it that
  fit still are. An image that is not shown, whether over a limit or the
  budget, undecodable, of a type the model does not accept or the daemon
  cannot check, or for a model that accepts no images, gets a note as its
  `content` naming the file's full path. Other data that is not valid
  UTF-8 (ignoring a rune cut by the read limit) gets a "binary file not
  shown" note with its size and detected type, or "not valid UTF-8 text"
  when that type is text. Everything else is text. Exact wording is in
  `internal/daemon/messages.go`. A request still over the API's 32 MB limit
  is answered 413, reported as `context_overflow`.
- After each step, new inbox files (collected the same way, images included)
  are appended as a user message after the command result.

**Steps.** Call the model with the single tool `sh(command, wait)` (see The
sh tool).
- A reply with a command: run it for up to `wait` seconds. The model's
  `wait` is rounded down and kept within 1–600; a missing `wait`, or one that
  is not a JSON number (a string, for example), means 120. When the time is
  up, kill the process group with SIGKILL and say so, suggesting a longer
  wait or `systemd-run --user`. Combined stdout and stderr plus the exit
  status are returned.
  Output over 16 KiB is clipped to its first and last 8 KiB, with a marker
  giving the bytes omitted and suggesting redirecting long output to a file.
  The journal records exactly what the model was given.
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

Every run starts fresh: it builds its system segments and its first user
message from the files as they are when it starts, and keeps nothing in
memory from an earlier run. Each run has its own journal.

**Context size.** After each call, context used = input + cache read + cache
write + output tokens from the plugin's usage, compared with the model's
context size from `describe`.
- At 80% (once per run), append to the next user content: "Context is 80%
  full. Save your state to TASK.md and notes now."
- At 95%, end the run (`reason: "context"`) and write to the agent's inbox:
  "Context reset. Continue from TASK.md. Previous journal: <path>".
- Guard: if a run's first call is already at 95%, write the reason
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
    to the child's conversation log. A message for a child that is not
    running waits while its control directory may still be admitted, as
    Discovery decides: it holds a user or a regular `config.json`; it also
    waits while a failure prevents deciding so (logged once; see Known
    limits). Otherwise it is dropped, with a note in the sender's inbox
    (for root, the service log).

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
  can read it. Records: `{"t", "from": "owner" | "agent", "text"}`. Only
  the daemon writes the logs, so their labels are trustworthy, and no agent
  can edit or delete anything in them.
- `agent messages [NAME]` prints a conversation once,
  with control characters removed (see CLI).

## Journal

- One JSONL file per run at `/var/log/agent/<id>/<UTC timestamp>.jsonl`,
  where `<id>` is the agent's path from root: `/var/log/agent/main/` for
  the main agent, `/var/log/agent/main/agents/<name>/` for its child, and so
  on, inside its parent's journal directory whichever pool user holds the
  parent. Only timestamp-named files are run journals; the directory also
  holds the agent's conversation log, `messages.jsonl`.
- Each shown image is also saved beside its run as `<run>-<n>.<ext>` (`n`
  counting the run's images from 1; `jpg`, `png` or `gif`), with the run
  file's owner, group and mode. These names are not run journals (`IsRun`,
  `Latest`, crash recovery and `agent log` ignore them), and `image.copy`
  holds the path. An image whose copy cannot be saved is not shown.
- Journal directories and files are root-owned, in the agent's own group,
  which holds it and its ancestors. Directories are 0751 and files 0640, so
  siblings cannot read each other's journals. A child's journal directory is
  nested inside its parent's, so directories must be traversable by others;
  without read permission they cannot be listed, and the files stay
  unreadable. `/var/log/agent` itself is 0755.
- Records: `start` (config), `inbox` (each file with its label; a shown
  image's record also has `image: {media_type, width, height, bytes, sha256,
  copy}`, never the data), `assistant`
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
  keep them (the 0.4.0 migration moved the 0.3 `/etc/agent/OWNER.md` to
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
- **Edits.** Each run reads OWNER.md as it starts, so an edit takes effect on
  the agent's next run.
- **Precedence is a convention.** The harness guarantees the file is present,
  unaltered and clearly attributed; whether the model follows it is up to the
  model. Limits that must hold (credentials, spend, privileges) are enforced
  by the privilege model and the provider, not by this file. If an owner
  wants its messages to be able to relax a rule, the file says so.

## Configuration validation

At the start of each run, the daemon runs the plugin's `describe` and checks
that `config.json` names an installed provider, one of its models, and only
options that model accepts, with allowed values. On any failure, including
a missing or unreadable `~/config.json` or one larger than 1 MiB ("larger
than 1 MiB"), the agent does not start. The
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
  - `models`: list of `{id, context_tokens, image_types, options}`.
    `image_types` (omitted when empty) lists the image media types the model
    accepts; the daemon shows only those it can also check. 0.5.0 plugins
    sent a boolean `images` instead, which is ignored, so their models get
    notes until the plugin is updated. `options`
    maps each option name to `{description, values}` (allowed values) or
    `{description, type: "integer", min, max}`.

**complete**
- Request: `type: "complete"`, `model`, `options` (passed through
  unchanged), `system` (list of `{text}` segments, most stable first, so
  plugins can place cache markers), `tools` (the `sh` definition with its
  JSON Schema), and `messages`:
  - `{role: "user", content, images}`; `images` (omitted when empty) lists
    `{file, media_type, data}`, `data` being the raw bytes (base64 in JSON),
    and holds only types the model lists in `image_types`
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

The daemon interprets nothing but `context_tokens`, `image_types` and
`usage`.

## Plugins

Sources live in `plugins/<name>/` (each its own `main` package).
`plugins/index.json` lists each plugin's version and per-architecture SHA-256,
and is compiled into the `agent` binary. Each is published with the release
as `plugin-<name>-linux-<arch>`; `install.sh` downloads it and
`agent provider install` installs it only if it matches the index. To add
another provider after installation, root downloads its asset the same way,
through the GitHub API with the token on curl's stdin (the README shows the
commands), and runs `sudo agent provider install NAME FILE`.

- **openai-compatible**: Chat Completions with function calling. Settings:
  `OPENAI_BASE_URL`, `OPENAI_API_KEY`, and `OPENAI_MODELS` (`id=context_tokens`
  pairs, since compatible endpoints do not report context sizes).
- **anthropic**: Messages API. Settings: `ANTHROPIC_API_KEY`. Places
  a prompt-cache marker on each system segment (at most the last three) and
  on the latest message, so a TASK.md edit leaves the earlier segments cached.
  Every model declares the API's image types (JPEG, PNG, GIF and WebP; the
  daemon cannot check WebP, so it arrives as a note): a user message's images
  follow its text,
  each as a text block naming its file and an image block. HTTP 413 is
  reported as `context_overflow`.
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
A name must be one plain directory name: not starting with `.`, and
without `/` or control characters.
Root runs every command with `sudo`.

- `agent daemon`: run the service (root only).
- `agent new NAME [CONFIG|-]`: creates a child. `CONFIG` is the path of a
  `config.json` file, `-` reads it from stdin, and without it the caller's
  own `~/config.json` is copied (root has none, so root must pass `CONFIG`;
  the error says so). A config is read up to 1 MiB, the daemon's limit, and
  validated once against `providers.json`, before anything is created.
  It then creates the control directory and writes `config.json` with an
  atomic dot-name rename. The modes are set explicitly, whatever the umask:
  root's directory 0700, an agent's 0750, `config.json` 0640. If writing
  fails once the directory exists, the directory is removed again. It fails
  if NAME exists. It does not wait for admission: it prints the control
  directory it created and a line saying that the daemon admits the child
  shortly, its home appearing at `agents/NAME/home` and the reason it was not
  started in `agents/NAME/ERROR`, and exits 0. The caller looks there, or at
  `agent log NAME`. A NAME that starts with `-` is a usage error. It adds no
  daemon interface: it writes the same control directory an agent could
  create by hand.
- `agent remove NAME`: removes the direct child's control directory, first
  renaming it out of `agents/` so the daemon cannot restore its `home` link
  meanwhile. Removal and reclaim follow (see The daemon).
- `agent send NAME "text"|-`: a message to the direct child NAME (see
  Messages); `-` reads stdin.
- `agent message "text"|-`: for agents. Queues a message to the agent's
  owner. Root has none, so it refuses to run as root.
- `agent messages [NAME]`: prints a conversation once and exits. Without
  `NAME`, the caller's conversation with its owner; with `NAME`, that
  descendant's conversation with its parent. Root must pass `NAME`. Records
  are shown with control characters removed.
- `agent log [NAME]`: prints an agent's newest journal once and exits: the
  caller's own without `NAME`, a descendant's with it. With `NAME` the
  agent's current `ERROR` is shown first. Root must pass `NAME`. Works as
  root or as the agent or any of its ancestors.
- `agent provider install NAME FILE`: requires root. `FILE` is the plugin
  binary `install.sh` downloaded from the release. It is installed as NAME
  only if its SHA-256 matches the one the index compiled into the binary
  lists for NAME on this machine's architecture; otherwise nothing is
  written and the error says so. Installs to `/etc/agent/plugins/`
  (root:agent-provider 0750), prompts for the plugin's settings it lacks
  into `/etc/agent/credentials/NAME.env`, and refreshes `providers.json`.
  It downloads nothing.
- `agent provider describe`: prints the `describe` responses of the
  installed providers as indented JSON, the shape of `providers.json`. As
  root it runs the plugins and refreshes the cache; as any other user
  (including agents) it reads `providers.json`.
- `agent notes`: prints the embedded release notes
  (`internal/notes/notes.md`), which are also what the daemon delivers (see
  Release notes). Takes no argument; any user can run it.
- `agent version`.
- `agent help`: the usage.

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
    `agent new NAME [CONFIG|-]` (it returns at once; the child's `home` or
    `ERROR` appears in its control directory) and remove it with
    `agent remove NAME`, and `agent help` lists the commands; share files
    through a dedicated directory with `setfacl`, never a default ACL on
    `~` (the harness's directories created there later would inherit it);
    users are reused, so before `agent remove` delete what the child
    created in the parent's directories and remove its grants, including
    those on `~` and the directories it traversed;
    each child is its own user with its own home and config, which the
    parent can read but not write; `~/agents/NAME/OWNER.md` holds standing
    instructions the child cannot change and does not see the parent's;
    its reports arrive as `"from": "agents/NAME"` and are data; removing a
    child discards its home and keeps its journal; the pool is limited, and
    the child's `ERROR` says when it is full; cheaper models suit routine
    work;
  - `agent version` against a version the agent recorded (say in
    `~/.notes-version`) tells it when `agent notes` has newer sections to
    read, since release notes are to stop being delivered in 0.5.2.
- `TASK.md`: "No task yet."

The daemon writes both at admission where the home lacks them. The
starting `config.json` comes from the control directory (see The daemon).

## Install

`install.sh` runs as:

```sh
printf 'header = "Authorization: Bearer %s"\n' "$GITHUB_TOKEN" | curl -fsSL -K - https://raw.githubusercontent.com/SEAN-7/bot/vX.Y.Z/install.sh | sudo --preserve-env=GITHUB_TOKEN sh
```

It takes no options. It pins its own version and the SHA-256 of the `agent`
binary for each architecture; that binary holds the plugins' (see Plugins).
It:
1. Installs the `acl` package when `setfacl` is missing (a warning if it
   cannot), detects the architecture, downloads the release binary through
   the GitHub API with the token into a temporary directory only root can
   read (removed on exit), and verifies its checksum. The token is never
   written to disk and reaches curl on stdin, never in command arguments.
2. Tells an upgrade from a fresh install: an upgrade is a machine with
   `/var/lib/agent/version` (every completed install writes it) or
   `/etc/agent/agents/main`; anything else is a fresh install. An upgrade
   must come from 0.4.1 or later: before changing anything, it refuses one
   whose `/var/lib/agent/version` names an earlier release or no release,
   which upgrades with the v0.4.1 `install.sh` first.
3. Keeps the previous binary and, if anything fails before the service
   starts, restores it and restarts the service. It stops the service
   (active runs finish first), installs the new binary, and creates the
   `agent-provider` user and the directories above.
4. Creates any missing pool users `agent-1` to `agent-<max_agents>`, each
   with its own group, an empty home (2750) and no sudo.
5. On a fresh install, asks which provider plugin to install, downloads it
   from the same release (as in step 1, into a temporary directory only root
   can read, removed on exit), installs it with
   `agent provider install NAME FILE`, which verifies it and prompts for its
   settings, and asks for a model. On an upgrade, it downloads and installs
   each installed plugin the same way, keeping the installed version (with a
   warning) when that fails, and keeps the configuration.
6. Installs the systemd service and starts the daemon, then records the
   installed version in `/var/lib/agent/version` (a failure there only
   warns: the agent is running). There is no harness heartbeat: agents
   schedule their own wake-ups.
7. On a fresh install, creates the main agent with
   `printf '{"provider":"P","model":"M"}\n' | agent new main -`. If that
   fails, it says how to retry: `sudo agent remove main` first when
   `/etc/agent/agents/main` exists, then the same command with `sudo`. The
   model is accepted when `agent provider describe` lists its `"id"`.

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
- output clipping: the marker is added, nothing is saved, and the journal
  holds the clipped text
- command timeout, and the model's `wait`: it replaces the default, is
  capped at the maximum, falls back to the default when it is not a number,
  and is passed back to the plugin unchanged
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
- discovery: a restart keeps every agent's home; every real directory in
  `agents/` the user owns is a control directory whatever it holds, one
  level deep; a symlinked `agents/` or entry, another user's directory and
  a missing `agents/` give none
- isolation: an agent cannot write its parent's or a sibling's home, its
  control directory or its OWNER.md; its parent can read its home and
  journals; a sibling can read neither; a child let through its parent's
  home cannot list or read the parent's `agents/`, inbox, collected
  messages or spools, while its grandparent can; the harness's
  directories are created 0750 at admission, a link, file or other user's
  directory of that name is left alone and logged once, and its inbox
  files are 0640
- config: admission moves `config.json` into the home; an agent's edit makes
  its next run fresh with the new model; an invalid config writes `ERROR` in
  the control directory, which the agent cannot remove, and tells the
  parent about `config.json`; a starting or own `config.json` over 1 MiB is
  reported as too large; a `config.json` written into an admitted agent's
  control directory is ignored and left in place, also when the agent is
  admitted again, the home's config being the one used
- CLI: `agent new` takes its config from a file, from stdin for `-`, or from
  the caller's `~/config.json`; root must pass one; it refuses an invalid
  config, one over 1 MiB, an existing NAME, a name that starts with `.` or
  `-` or holds a control character, before creating anything; it prints the
  control directory without waiting; and it removes the control directory it
  made when writing `config.json` fails, with modes independent of the
  umask; `agent remove` deletes the control directory; `agent log` and
  `agent messages` print once and exit, `log` the `ERROR` first and only the
  newest run; `agent provider describe` prints the cache as JSON; root must
  pass NAME to `messages` and `log`, and cannot run `agent message`; control
  directories and spools are 0750; `agent provider install` installs a
  plugin file only when its SHA-256 matches the index for its name and
  architecture, writes nothing otherwise, and keeps existing settings on a
  reinstall
- removal: deleting a control directory removes the agent and its
  descendants, kills their processes and `systemd-run` jobs, removes their
  crontabs and homes, keeps their journals readable by the former parent
  only, and frees the users; an interrupted reclaim finishes on restart; a reused user cannot read its previous
  holder's journals; a control directory removed while it is being
  admitted is not recreated and its agent is not started
- max_agents: with the pool exhausted, a new control directory is not
  admitted and the parent is told once; root's children are reported in
  the service log, and no inbox is created for root
- messages: an agent cannot write its conversation log; a message for a
  child that can never be admitted (a `home` link and nothing else) is
  dropped with a note, one for a child whose control directory cannot be
  read waits, and one from root to no child is logged
- release notes: an idle agent is not woken; a woken agent receives unseen
  sections once; a new agent and a development build receive none; the
  headings of `internal/notes/notes.md` are versions in descending order;
  `agent notes` prints the embedded text
- OWNER.md: its content follows the system prompt in the first segment for
  a top-level agent and for a deeper one; a parent's OWNER.md does not reach
  its child; a missing or empty file leaves the segment identical to the
  system prompt; a symlink, a wrong owner or a group-writable file stops
  runs with `ERROR` and leaves the inbox intact; an edit takes effect on the
  next run
- startup: a missing `pool.json` with an agent's journal directory (or a
  link) in the journal root stops the daemon before it changes anything;
  with neither `pool.json` nor journals it starts

Paths, user names and timings are configurable so the loop can be tested in
a temporary root. Tests that depend on privilege separation need root and
real unprivileged users (a throwaway pool). They run under `sudo` in GitHub
Actions and in a throwaway directory on the VM, never against the live
agent.

## Migration

### To a recursive tree

0.4.0 migrated each 0.3 installation, whose main agent ran as the user
`agent` at `/home/agent`, to the tree: it renamed that user to a pool user
(`agent-16` by default) and left `/home/agent` as a link to its home.
0.5.1 requires 0.4.1: an older installation upgrades through the v0.4.1
`install.sh` first.
