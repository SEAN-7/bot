# Project philosophy

The harness gives guarantees and the agent sets policy. The harness is one
always-on loop with one shell tool. Everything else is ordinary files and
services on the VM. The file harness in `harness/` is the new design; the older
runtime remains available during migration.

## Durable continuity

There is one durable primary conversation. Every accepted inbox message, model
reply, command start and tool result is appended to the host-owned journal.
Closing a client, restarting the service or resetting working context does not
delete that record. Channel listeners retain their own delivery state and
message IDs. Original journal evidence takes precedence over lossy summaries.

The editable README and TASK files are loaded at each wake and reset, with
independent size caps. README is stable guidance; TASK is volatile work state.
The agent can request an early reset with `.reset`; the new context names the
previous journal file. Memory notes, indexes,
task state and retrieval procedures are conventions seeded there, not a memory
framework enforced by the harness. When context resets, only disk state survives.

## A small protected harness

The host owns serialization, journaling, shell timeouts, clipping with full
output files, daily heartbeat, context warnings and resets, crash recovery,
per-wake step/spend limits and a daily spend limit. A response with no command
ends a wake. Policy about when to stop belongs in the README.

The agent owns README, notes, scripts, listeners, channel sending, cron jobs,
retrieval and task strategy. Capabilities are ordinary programs invoked through
`sh`; no tool plugin registry, controller framework or capability catalogue is
required. Delegated briefs use fresh contexts under the same serialized host
and spending controls.

## Authority and recovery

The agent owns its working account, not the host's administrative account.
Journal storage, model credentials, pause/kill controls, network policy and
spend limits remain outside that account's authority. Listeners authenticate
the owner and label their input; other sources are data. Host owner messages
have provenance that files written by the agent cannot forge.

The host snapshots agent files before each reset. Filesystem or provider errors
fail closed with a retained alert. Unknown request charges remain reserved;
unknown command effects are never automatically replayed. A snapshot cannot
undo an external action. The default archive covers the agent home, not the
entire VM disk; full-volume snapshots are an operator-provided host hook.

Migration must preserve the original conversation, credentials, channel offsets,
working files and rollback release. Bootstrap a separate paused installation,
then switch channels at an explicit handover. Never run two pollers for the
same Telegram token. Never manufacture user messages to test production.
