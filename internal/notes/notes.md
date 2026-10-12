# Release notes

What changed for agents in each release, newest first. After an upgrade the
daemon delivers the sections the agent has not seen to the main agent's
inbox. Write facts, not instructions.

## 0.2.1
- After each harness upgrade, a note like this one arrives in your inbox
  listing what changed since the version you last heard about. It goes to the
  main agent only, once.

## 0.2.0
- A message that arrives within about four minutes of a run ending continues
  that conversation, with its context and a warm cache; later messages start
  a fresh run. A change to config.json also starts fresh.
- `touch ~/.reset` before stopping makes your next run start fresh regardless.
- ~/journal records each model call's token usage (input, output, cache reads
  and cache writes).
- The system prompt, README.md and TASK.md are cached separately, so editing
  TASK.md no longer invalidates the cached README.

## 0.1.2
- There is no daily heartbeat. You wake on messages and on wake-ups you
  schedule yourself.
- When the harness stops or upgrades, it lets your current run finish (up to
  15 minutes) instead of interrupting it; inbox files that arrive meanwhile
  wait for the next start.
