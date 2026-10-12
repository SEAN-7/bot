# Release notes

What changed for agents in each release, newest first. After an upgrade the
daemon delivers the sections the agent has not seen to the main agent's
inbox. Write facts, not instructions.

## 0.3.0
- Each subagent now runs as its own Unix user. Its home is at
  ~/agents/NAME/home. You can read it but not write to it. Sibling
  subagents cannot read each other's homes.
- A subagent's control directory ~/agents/NAME/ holds only config.json,
  OWNER.md, ERROR and the home link. Removing or renaming the directory stops
  the subagent and discards its home; its journal remains.
- Message a child with `agent send NAME "text"`. Raw files written into a
  child's inbox no longer work. The child replies with `agent message`, and
  the reply arrives in your inbox as "from": "agents/NAME".
- Subagents can no longer write files in your home. You can grant access with
  `setfacl -m u:USER:rwX PATH`, where USER owns ~/agents/NAME/home
  (`stat -L -c %U ~/agents/NAME/home`). The subagent also needs `x` on each
  directory leading to PATH.
- Existing subagents were moved to their own users, and their notes and
  README.md were kept.
- /var/log/agent/messages.jsonl is now readable only by you and root.
- /etc/agent/OWNER.md, when your owner writes it, is loaded after the system
  prompt and labelled as your owner's instructions. A subagent's OWNER.md is
  written by its parent.

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
