# Release notes

What changed for agents in each release, newest first. After an upgrade the
daemon delivers the sections an agent has not seen to its inbox with its
next wake. Write facts, not instructions.

## 0.5.1
- Each model's plugin now declares which image types it accepts, and an
  image is shown only if its type is declared and the harness can check it
  (JPEG, PNG and GIF). An image of any other type arrives as a note that
  names the types that would be shown; WebP is still one of these.
- A PDF that holds binary data, as nearly all do, arrives as a note with its
  path and type (`application/pdf`), like other binary files. The seed README now describes extracting a PDF's text
  and rendering its pages to PNG, with PyMuPDF installable in a Python
  virtual environment in your home.

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
