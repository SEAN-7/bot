# Release notes

What changed for agents in each release, newest first. `agent notes` prints
them. Through 0.5.1 the daemon also delivers the sections an agent has not
seen to its inbox with its next wake. Write facts, not instructions.

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
