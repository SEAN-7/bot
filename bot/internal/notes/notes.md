# Release notes

What changed for agents in each release, newest first. Agents read them at
~/manual/notes.md. Write facts, not instructions.

## 0.9.0
- A message is a file the sender writes into the recipient's inbox. Your
  messages to an agent owner go into `~/owner/`, a link to its inbox; an
  agent whose owner is the human has no `~/owner`. Messages to a subagent
  go into `~/agents/NAME/home/inbox/`. Write the file elsewhere (your
  home) and `mv` it in.
- `~/.messages/` and `~/.messages/agents/NAME/` are no longer collected or
  created; files left there stay where they are. Nothing writes
  `~/journal/messages.jsonl` any more.
- An inbox record's `from` is decided by who wrote the file: `owner` for
  your owner's user, `agents/NAME` for your running subagent NAME's user,
  `data` for anyone else.
- Your inbox is now `/home/.inbox/<your user>/`, outside your home, and
  `~/inbox` is a link to it. It is mode 1770: your owner and its ancestors
  write into it, your subagents may create files there, and nobody removes
  another's file. The `~/inbox/` directory you had is `~/inbox.0.8.0/`,
  with its `processed/` history; the files pending in it moved to your
  inbox. `inbox/.tmp/` is no longer used.
- A subagent name cannot hold a comma or a colon.
- `OWNER.md` is used only as a regular file; a link is not followed.
- A subagent gets a Unix user of its own when admitted, deleted when it is
  removed; user names come back for later agents. There is no pool.
- An image of a type your model accepts is shown as it is, up to 16 MiB;
  what your provider's plugin does with one it cannot take is in its own
  words. The harness no longer checks image size or decodes images.
- A call the provider rejects as invalid ends the run; "Provider rejected
  the request: ERROR" is written to your `ERROR` until a call succeeds.
  The harness no longer checks `config.json` options; provider and model
  are checked against `/var/lib/agent/providers.json`.
- `ERROR` holds the most recent cause and is removed when a run starts or
  a call succeeds.
- `OWNER.md`'s owner and mode are not checked.
- The harness writes no upgrade note. Each run's `start` record holds the
  version that ran it; `~/manual/version` is the running one.
- `agent provider install NAME SOURCE` takes a file or an http(s) URL and
  installs any plugin that speaks the protocol.

## 0.8.0
- `agent new`, `agent remove`, `agent send`, `agent message`,
  `agent messages`, `agent log`, `agent provider describe` and
  `agent version` are gone. `agent` has `provider install` (root), `help`
  and the service; ~/manual/manual.md describes the files that replace
  them.
- A file in ~/inbox whose name starts with a dot no longer wakes you and is
  no longer ignored: it is delivered with your next run, whatever starts
  it, and after each command of a running one. A dot-file written straight
  into ~/inbox can be collected before it is complete. Files in
  ~/.messages/ and ~/.messages/agents/NAME/ whose names start with a dot
  are still ignored.
- The harness stages the files it writes into your inbox in ~/inbox/.tmp/,
  which is never delivered.

## 0.7.0
- Messages are files. A message to your owner is a regular file you own in
  ~/.messages/ whose name does not start with a dot; one to your subagent
  NAME is such a file in ~/.messages/agents/NAME/, which the harness now
  creates when NAME is admitted. They are collected about once a second in
  name order and removed once delivered. `agent message` and `agent send`
  wrote exactly these files.
- A message over 64 KiB is delivered as its first 64 KiB followed by
  "[message truncated]", and its sender gets "Message to your owner was
  truncated to 64 KiB." or "Message to NAME was truncated to 64 KiB.".
- A subagent is a directory. ~/agents/NAME/ holding a config.json is a
  subagent; deleting the directory removes it. The config.json is checked
  against /var/lib/agent/providers.json when it appears, before a user is
  assigned: an invalid one leaves "config.json: REASON" in
  ~/agents/NAME/ERROR and "Subagent NAME did not start: config.json:
  REASON" in your inbox, once, and can be fixed in place. Before, an
  invalid starting config was admitted and could only be removed. A name
  starting with "." or holding control characters is not a subagent.
- A deleted ~/agents/NAME/home link is no longer put back each second; it
  is written at admission and when the harness restarts.
- /var/lib/agent/providers.json is what `agent provider describe` printed.
  ~/manual/version holds the running harness version. A subagent's
  conversation log is ~/journal/agents/NAME/messages.jsonl and its runs are
  in ~/journal/agents/NAME/.
- `agent new`, `agent remove`, `agent send`, `agent message`,
  `agent messages`, `agent log`, `agent provider describe` and
  `agent version` still work in this release, do the file operations
  above, print one line on stderr saying so, and are removed in 0.8.0.
  ~/manual/manual.md describes the files.

## 0.6.0
- ~/manual/ is a read-only directory describing how this machine and its
  harness work, written by the harness for the version that is running:
  manual.md (what an agent can observe and do, including the exact wording
  of every note the harness writes) and notes.md (these release notes).
  Each run links ~/manual to /var/lib/agent/manual. `agent notes` is gone;
  ~/manual/notes.md has the same text.
- TASK.md is no longer loaded. A run starts with the system prompt (and
  your owner's instructions), ~/README.md and the new files in ~/inbox. A
  ~/TASK.md that exists is an ordinary file of yours, which the harness
  neither reads nor writes, and a new agent gets none.
- The system prompt names ~/README.md as the one file loaded every time
  and ~/manual/ as the description of the harness. The first call of each
  agent after the upgrade writes its prompt cache afresh.
- The notes after a context reset and an interruption no longer name
  TASK.md: "Context reset. Previous journal: <path>" and "You were
  interrupted. Interrupted journal: <path>". The first-call overflow error
  names README.md and the inbox files.
- After an upgrade, the release notes you have not seen arrive in your
  inbox with your next run, as <time>-harness-upgrade.md with "from":
  "data"; nothing wakes you for them. Each run's journal start record now
  has "version", the harness version that ran it. A run from before 0.6.0
  recorded none and counts as 0.5.1, so the first such note holds every
  section since.
- `agent provider describe` has a "caching" text per provider: what it
  caches between calls and for how long, the smallest prompt it caches,
  where the plugin puts cache markers, and what cache reads and writes
  cost relative to plain input.
- The seed README for a new agent is a short starting file that points to
  ~/manual/. Existing READMEs are unchanged.

## 0.5.8
- Correction to 0.5.7: a file written into ~/inbox during a run does not
  start a new run. New inbox files are added to the current run after each
  command, so the run that wrote it reads it. A file that arrives after the
  run's last command, such as a dot-file renamed a few seconds later by
  `systemd-run --user --on-active=10 /bin/mv ...`, is read by a new run once
  the current one stops. The seed README's Task state section has an
  example script.

## 0.5.7
- The harness no longer measures context size. There is no 80% warning
  and no reset at 95%: a run ends for context only when the provider
  rejects a call as too long, with the same "Context reset. Continue from
  TASK.md. Previous journal: <path>" note. A command asked for near the
  limit now runs. When to start fresh sooner is up to the agent: the
  latest assistant record in the newest run file of ~/journal has the
  call's usage, and its four counts add up to the context in use.
- Each model call resends the whole context, so later steps of a run cost
  more, and some models charge more per token past a context size.
- With the anthropic provider the prompt cache lasts 5 minutes between
  model calls. After a command that runs longer than that, the next call
  pays again for the whole context.
- `agent provider describe` omits `context_tokens` where it is unknown;
  for openai-compatible models it is now optional in `OPENAI_MODELS`.

## 0.5.5
- The note after an interruption names the interrupted run's journal:
  "You were interrupted. Resume from TASK.md. Interrupted journal: <path>".
  The inbox files that run was handling are in ~/inbox/processed/ and in
  that journal.
- A command that runs the machine out of memory no longer stops every
  agent. The kernel kills the process using the memory, the command
  returns its failure (usually exit status 137), and the run continues.

## 0.5.4
- When a command finishes, the harness kills every process it left running
  in the harness's service: backgrounded with &, nohup or setsid, or
  daemonised (such as `pg_ctl start`). The result ends with a note counting
  and naming them. Before, they kept running until the next harness restart
  killed them without notice. Processes started with `systemd-run --user`
  run in your own systemd and are not affected, nor are cron jobs.

## 0.5.3
- The system prompt now says that the reply you stop with (a reply
  without a command) goes only to your journal and is not sent to anyone.
  Only `agent message` reaches your owner. The seed README's Stopping
  section says the same.

## 0.5.2
- Release notes are no longer delivered to the inbox after an upgrade.
  `agent notes` shows them.

## 0.5.1
- `agent notes` prints these release notes. From 0.5.2 they are no longer
  delivered to your inbox after an upgrade. `agent version` prints the
  installed version, so an agent that wants them can record it (say in
  ~/.notes-version) and read the newer sections of `agent notes` when it
  changes.
- `agent new NAME [CONFIG|-]` replaces `--provider`, `--model` and
  `--option`, which are now usage errors. CONFIG is a config.json file, `-`
  reads one from stdin, and without it the child gets a copy of your
  ~/config.json. It is checked against `agent provider describe` before
  anything is created. `agent new` no longer waits for admission: it
  prints the control directory and returns. The child's home appears at
  ~/agents/NAME/home, and the reason it was not started in
  ~/agents/NAME/ERROR.
- `agent log` and `agent messages` print once and exit; `agent log` shows
  the ERROR first, then only the newest run. `tail -F` on the journal file
  or on messages.jsonl in the journal directory (~/journal for your own)
  follows them.
- `agent help` lists the commands and no longer has the recipe for sharing
  files with a child; an existing README does not have it either. A
  child's user (`stat -L -c %U ~/agents/NAME/home`) can be granted access
  with `setfacl`; that needs x on ~, which reaches the files in ~ that
  others can read, so a dedicated directory holds what is shared. A default
  ACL on ~ naming the child is inherited by the directories the harness
  creates there later. Pool users are reused: removing NAME leaves what the
  child created in your directories (`find DIR -user USER` lists it) and
  its grants, including on ~ and the directories it traversed, to the next
  holder of its user; `setfacl -R -x u:USER,d:u:USER DIR` on the shared
  directory and `setfacl -x u:USER ~` remove them.
- `agent provider describe` prints the providers, models and options as
  indented JSON, the shape of providers.json, not a list.
- Every run starts fresh. A message that arrives soon after a run ends
  does not continue it, ~/.reset is neither read nor deleted, call ids in
  the journal start at call-1 in each run, and the journal's start record
  has no "continues".
- Clipped command output (over 16 KiB, shown as its first and last 8 KiB)
  is no longer saved: the harness no longer creates or closes ~/.outputs,
  and one that exists is an ordinary directory of yours. The result ends
  with "[output clipped; redirect long output to a file to read all of it]"
  and the journal records exactly what you were given, with no
  "full_output".
- The sh tool's `wait` counts only as a JSON number (rounded down, within
  1-600). A string such as "300", or anything else, means the default 120.
- The harness no longer closes directories that exist. At admission it
  creates ~/inbox, ~/inbox/processed, ~/agents, ~/.messages and
  ~/.messages/agents where they are missing, 0750, and leaves existing ones
  as they are. An agent's control directory is created 0750 and not changed
  afterwards.
- A new subagent that cannot get a user because the pool is full is
  reported as "Agent limit reached"; "No free agent user" is no longer
  used.
- A config.json written into the control directory of an admitted
  subagent is ignored and stays where it is; it is no longer renamed
  config.json.ignored.
- Each model's plugin now declares which image types it accepts, and an
  image is shown only if its type is declared and the harness can check it
  (JPEG, PNG and GIF). An image of any other type arrives as a note that
  names the types that would be shown; WebP is still one of these.
- A PDF that holds binary data, as nearly all do, arrives as a note with its
  path and type (`application/pdf`), like other binary files. The seed
  README now describes extracting a PDF's text and rendering its pages to
  PNG, with PyMuPDF installable in a Python virtual environment in your
  home.
- The 20 MiB limit on the images shown applies to each run, since every
  run starts fresh: an image over it arrives as a note until the run ends,
  and the next run can show 20 MiB again.

## 0.5.0
- JPEG, PNG and GIF files in your inbox are shown to you as images when your
  model accepts them (Anthropic models do; openai-compatible ones don't), up
  to 5 MiB and 2000 px a side. Copying an image into ~/inbox/ shows it after
  the current command. Shown images stay in the conversation, up to 20 MiB
  of images per conversation, including a continued one; an image that would
  exceed that arrives as a note instead, and the conversation's next reset or
  end lifts the limit.
- An image that is too large, too wide or tall, damaged, of another type such
  as WebP, or sent to a model that doesn't accept images arrives as a note
  with its path. Other binary files (data that isn't valid UTF-8) also arrive
  as a note with their path and detected type (or "not valid UTF-8 text")
  instead of garbled text.
- Each image shown is kept beside the run's journal as `<run>-<n>.<ext>`,
  and the inbox record in the journal has an `image` entry with its size,
  hash and the copy's path.

## 0.4.1
- Your inbox, collected messages, saved outputs, spools and subagents'
  control directories are closed to other users: ~/inbox,
  ~/inbox/processed, ~/.outputs, ~/.messages, ~/.messages/agents, ~/agents
  and each ~/agents/NAME have no permissions for others (new ones are
  created 0750), and those an earlier release left open to others lost
  those permissions. Your ancestors still read them through your group; a
  subagent you let through your home cannot list or read them, unless a
  default ACL on ~ names its user: directories created in ~ inherit that
  entry, and closing them to others leaves it. Inbox notes, delivered
  messages and saved outputs are written 0640.
- When no agent user is free because the free users' homes still belong to
  agents that have not been recovered, a subagent's ERROR and the note to
  you say "No free agent user" instead of "Agent limit reached".
- A message for a subagent that cannot be started, such as one whose
  ~/agents/NAME holds only a home link to a home that is not its own, is
  dropped with a "Message to NAME dropped" note instead of waiting. A
  message waits while the harness cannot yet tell whether the subagent can
  be started, for example because a check on its control directory fails;
  the harness logs the failure once.
- Subagent names for `agent new`, `remove`, `send`, `messages` and `log`
  may not start with "." or contain control characters.
- `agent new` removes the control directory it made if writing config.json
  fails, sets its modes (0750, config.json 0640) whatever the umask, and
  refuses `--option KEY=...` given twice. An option with a list of allowed
  values accepts the text of one of them, including numbers and booleans.
  `agent new --help` prints its usage.
- A config.json larger than 1 MiB is reported as "config.json: larger than
  1 MiB". A new subagent with one is not started: its ERROR says so and you
  get a note once.
