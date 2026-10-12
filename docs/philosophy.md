# Project philosophy

The harness provides a few guarantees; the agent provides all policy.

## Three ideas

1. **An agent is a directory**: `README.md`, `TASK.md`, `config.json` and
   `inbox/`. The main agent is `/home/agent`; subagents live under any agent's
   `agents/` folder with the same layout. Creating the directory and dropping
   a file in its inbox starts one; removing the directory stops it.
2. **Everything that happens is a file in an inbox**: owner messages,
   schedules, heartbeats, child reports, crash recovery and context resets.
   There is no other event channel.
3. **The harness has no provider code.** Model access is a plugin that speaks a
   small versioned JSON protocol.

## What the harness guarantees

- Commands run as the unprivileged `agent` user, with a timeout that kills
  the whole process group.
- The agent cannot read provider credentials or alter its journals.
- All daemon file access inside agent directories happens with the agent's
  privileges, so a planted symlink reaches nothing the agent couldn't.
- Messages sent with `agent send` are labelled as the owner's; nothing the
  agent writes can carry that label.
- The owner's standing instructions in `/etc/agent/OWNER.md` reach every
  agent first on every run, and no agent can change them.
- Every model message and command result is journaled; an interrupted run is
  reported to the agent on restart.
- Context resets and interruptions lose nothing that is on disk.

## What the agent decides

Memory, scheduling, messaging, tools, subagents, when to stop and how to
work: all of it is files and scripts the agent writes for itself, starting
from suggestions in its seed README that it is free to revise.

## How to judge a change

Ask whether it protects one of the guarantees above, or whether it is
behaviour the agent could build as a script. If the agent could build it,
leave it out. Prefer the smallest mechanism that keeps a guarantee, and keep
the harness small enough to read in one sitting.
