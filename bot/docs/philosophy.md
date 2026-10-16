# Project philosophy

The harness provides a few guarantees; the agent provides all policy.

## Three ideas

1. **An agent is a user and a home** holding `README.md` and `inbox/`. Its
   owner, root or another agent, creates it by making a directory holding
   its config; the harness gives it a Unix user of its own. Deleting the
   directory stops it and deletes the user. Root is the top of the tree and the main agent its
   first child, so the human does exactly what an agent does.
2. **Everything that happens is a file in an inbox**: owner messages,
   schedules, heartbeats, child reports, crash recovery and context resets.
   Whoever sends it writes it there, and the kernel's record of who wrote a
   file is its label. There is no other event channel.
3. **The harness has no provider code.** Model access is a plugin that speaks a
   small versioned JSON protocol.

## What the harness guarantees

- Commands run as the agent's own unprivileged user, with a time limit the
  agent chooses per command (up to 10 minutes) that kills the whole process
  group. What a command leaves running ends with it, and the agent is told;
  long-running work goes in the agent's own systemd.
- Every agent has one owner: the human (root) for a top-level agent such as
  the main agent, its parent for a subagent. Messages between them are
  labelled with their sender; nothing an agent writes can carry another's
  label.
- An agent's standing instructions in its `OWNER.md` reach it first on every
  run, and it cannot change them.
- A subagent cannot change its parent's or siblings' files; its ancestors
  can always read its journals.
- No agent has sudo, and none can read provider credentials or alter
  journals.
- All daemon file access inside agent homes happens with an agent's
  privileges, so a planted symlink reaches nothing that agent couldn't.
- Every model message and command result is journaled; an interrupted run,
  and a harness upgrade, are reported to the agent at its next run.
- Context resets and interruptions lose nothing that is on disk.

## What the agent decides

Memory, its model, scheduling, messaging, tools, subagents, when to stop
and how to work: all of it is files and scripts the agent writes for itself.
Its README starts nearly empty; how the harness works is in its manual.

## How to judge a change

Ask whether it protects one of the guarantees above, or whether it is
behaviour the agent could build as a script. If the agent could build it,
leave it out. Prefer the smallest mechanism that keeps a guarantee, and keep
the harness small enough to read in one sitting.
