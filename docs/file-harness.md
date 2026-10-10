# File harness

The host runs `harness/main.mjs`, serially. Each wake starts with the fixed system
prompt plus `~/README.md`, `~/TASK.md` and claimed inbox files. The model gets exactly one
tool, `sh({command})`. A final reply without a command ends that wake. All replies
are journalled; human-facing delivery is a script the agent builds.

`loop.mjs` contains the model loop; `host.mjs` contains durable files, process
execution and snapshots; `model.mjs` adapts the pinned Pi provider client. The
legacy controller, app registry, memory, scheduler and channel apps are not
loaded by this harness. The bundle still includes them for a staged migration.

## Separate installation

Build the normal Linux bundle with `python3 scripts/build-bundle.py dist` and
verify its `.sha256`. Install alongside the existing bot:

```sh
sudo sh ./bot-linux-x64 install-next --provider anthropic --model claude-haiku-5-5 --credential-file /etc/bot/environment
sudo bot-next status
```

Installation creates a locked, unprivileged `bot-next` account, a root-owned
service and a seeded home. The service is enabled and **paused**. No model calls,
heartbeat inbox messages, channel listeners or old-conversation traffic occur
until the operator resumes it. Only the selected provider key is copied into
the new root-only environment file. The old service and its files are untouched.

| Path | Purpose |
| --- | --- |
| `/var/lib/bot-next` | Agent-owned home: README, TASK, inbox, jobs, notes, scripts |
| `/var/lib/bot-next/journal` | Link to the host-owned, agent-readable journal |
| `/var/lib/bot-next-host` | Host state, counters, controls and retained claims |
| `/var/lib/bot-next-host/journal` | Daily JSONL events and full output files |
| `/var/lib/bot-next-host/snapshots` | Root-only home archives at resets |
| `/etc/bot-next/config.json` | Host model, step, output and spending limits |
| `/etc/bot-next/environment` | Root-only model credential |
| `/opt/bot-next/current` | Root-owned selected Linux release |

An OS `flock` prevents two service loops sharing a home. Systemd restarts the
service after a crash or reboot. Each shell is a transient systemd unit under
the agent account, with a 120-second runtime, whole-cgroup termination and no
model credential in its environment. It can use ordinary cron and user services.
Persistent listeners should use their own user service units; installation
enables lingering and supplies the user bus environment to shells. Output is streamed
to disk with backpressure, clipped at 16 KB for context, and stopped at 64 MB
by default; hitting that disk cap is explicitly marked as incomplete output.

## Controls and limits

```sh
sudo bot-next pause       # waits at the next model or shell boundary
sudo bot-next kill        # aborts active work and stops the service
sudo bot-next resume      # clears pause/kill and starts service
sudo bot-next send owner-message.txt
sudo bot-next status
```

Owner input is accepted in a host-only spool with a host-generated source
label. Agent files always enter as agent data even if their contents claim to
be the owner. Channel scripts must verify sender/chat IDs and persist channel
offsets; the harness cannot authenticate a sender from text alone. Write inbox
files to a temporary `.tmp` name then rename atomically. Symlinks, devices,
directories and oversized files are quarantined. Claimed files survive crashes;
stable journal IDs expose possible replay at the admission boundary.

Defaults are 100 model steps and $2 per wake, $10 per UTC day, and 4096 maximum
output tokens per request. All model calls, including delegated briefs, share
the host ledger. Before every request the host reserves the most expensive
full-window input/cache price and maximum output price from the pinned model
registry, including long-context tiers. Successful usage refunds the unused
reservation. Unknown requests keep it across restart. A wake can stop before
the nominal dollar limit when its remaining balance cannot fund the next
reservation. This is a local provider-cost limit based on configured prices,
not an invoice guarantee or a limit on unrelated paid services reached by curl.
Provider-account limits can provide an additional external ceiling.

Caps create a durable alert visible in `bot-next status`, the journal and systemd
logs. Set root-owned `notifyCommand: ["/path/to/host-notifier", "arg"]` for push
delivery; it receives the alert JSON filename as its final argument. No channel
notifier is enabled before bootstrap. Network policy is the host's existing
firewall/cloud policy; this installer does not invent a new egress allowlist.

The first unpaused iteration each UTC day inserts one heartbeat. At 80% of the
context the host asks the agent to update TASK. At 95%, or when the agent creates
`~/.reset`, it snapshots and reloads README and TASK. The new context includes
`Reset. Previous session: <journal file>`, naming the actual preceding JSONL
file. Each file holds dated events with run IDs. The reset marker is cleared
only after snapshot and bootstrap loading succeed; errors leave it for recovery.
It is checked between steps; while idle it is handled at the next inbox wake.
Usage uses provider counts plus conservative text
estimates and reserves output space. A single large input can cross both
thresholds at once: the journal and snapshot survive, but a last save turn is
not guaranteed. Both files are independently capped at 32 KiB by default through
host settings `readmeBytes` and `taskBytes` (maximum 128 KiB each). An oversized
file, or bootstrap content above the model's allowance, blocks execution until
repaired. README is the stable index; TASK holds current goals, milestones and
next steps, with pointers to per-task files when several tasks are in flight.

Default snapshots include both bootstrap files and other agent home files,
excluding the journal, inbox and transient `.reset` marker. They are not whole-disk or
atomic multi-file filesystem snapshots. Concurrent file mutation can fail the
snapshot and pause the harness. Configure root-owned
`snapshotCommand: ["/path/to/volume-snapshot-helper"]` to use filesystem/cloud
snapshots; it receives the home and proposed snapshot path, and a nonzero exit
prevents reset. Archives are retained for the operator; disk retention policy
should be configured for a long-lived installation. Restore only while paused,
after preserving current files, and never overwrite the live journal or inbox.

## Delegation

The agent can call `bot-next delegate brief.txt` through `sh`. It receives a job
receipt immediately. When the primary wake ends, the same host runs the brief
with a fresh README and TASK context and delivers its final text to the primary inbox.
This keeps one active run and one shared daily budget. It is serialized
delegation: a primary that needs the result must stop and await the result wake,
not poll. The agent can build higher-level delegation scripts around this CLI.

## Bootstrap and handover

The existing bot can bootstrap the new home because it currently has operator
access on this VM. Until its sudo access is removed or it is retired, that old
account can still change host controls; the new agent account cannot.

1. Keep `bot-next` paused. Let the old bot inspect the installed contract and
   seed the new README, TASK, notes and scripts as the `bot-next` user.
2. Preserve original evidence: use SQLite backup for the live old journal and
   channel databases, copy notes and bot-owned app sources into an import area,
   and create a manifest of paths and hashes. Keep originals unchanged. Reference
   those archives in README; do not replace exact journal evidence with a summary.
3. Implement/test channel listeners and send scripts against isolated fixtures.
   Preserve Telegram sender filters, binding, update offset and delivery IDs.
   Do not start another production Telegram poller during bootstrap.
4. At a human-approved handover, stop the old listeners, take final consistent
   backups, import channel state and start the new listeners, then resume the
   new harness with a real owner instruction. Verify genuine delivery.
5. Retain the old release and state for rollback. To roll back, stop new listeners
   before restarting old ones and reconcile offsets/delivery receipts first.

Suggested instruction for the human to send to the existing bot:

> Bootstrap the paused file harness at /var/lib/bot-next using the contract in
> /opt/bot-next/current/app/docs/file-harness.md. Preserve and index our original
> journal, notes, commitments, tools and channel state. Build the new README,
> TASK.md and ordinary scripts, using isolated tests. Keep bot-next paused
> and leave current production listeners running. Report the prepared handover
> and exact rollback steps; wait for my instruction before switching channels.

This is a proposed human instruction, not an automatically injected user message.

## Updating and validation

```sh
sudo sh ./bot-linux-x64 update-next --yes
sudo bot-next rollback
```

The managed updater preserves the home, credentials, controls, ledger and
journal, retains the previous release, and restores the previous executable
if startup validation fails. Rollback changes executables, not conversation
state. Operator startup validation uses `--check`, never a paid model call or
synthetic message. Tests in `test/file-harness.test.mjs` exercise the loop with
stubbed responses and temporary homes; Linux tests verify installed process
isolation, output storage and snapshots outside the production conversation.

Updates from the first file-harness draft copy an existing lowercase `task.md`
into `TASK.md` if TASK does not yet exist, preserving the original. An unchanged
README seed is upgraded; an agent-edited README or existing TASK is preserved.
Bootstrap migration runs under the agent account, with no privileged writes
through agent-controlled filenames.
