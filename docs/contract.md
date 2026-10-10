# Process application contract (API 3)

The bot has one durable conversation and one model-facing tool, `shell`. The protected supervisor owns the journal, serial event queue, process cleanup, action outcomes and installation recovery. A protected runtime supervises one replaceable controller process. No core process imports app code. There is no plugin loader, in-process hook registration, model/context registration or tool registration API.

Shell calls wait up to 1000 ms by default (explicit `yieldTimeMs` up to 30000 ms), then return a truthful `running` receipt and session ID while the supervised command continues. New input interrupts launch or polling waits without cancelling the command, so the controller can incorporate it at the next context boundary. A yielded command that finishes without a waiting caller enqueues one durable `shell` notification with final status and output since the last read. The final result remains available for an optional poll; notification output is bounded to 80000 characters, independently of the configured per-read output limit. These notifications are reference data, not human channel messages. Continue independent work or stay idle until completion instead of repeatedly polling. No unresolved model tool call is carried into the next model request. Runtime restart stops sessions; never replay a missing session automatically, and inspect external effects before retrying interrupted work.

## Executable apps and skills

An app is a directory containing `app.json`, a `SKILL.md` guide, and its ordinary executable files. For example:

```json
{
  "id": "local-time",
  "version": "1.0.0",
  "description": "Read the current UTC time.",
  "command": ["python3", "main.py"],
  "preflight": ["--preflight"]
}
```

The command is an argv array, executed without a shell from the app directory. `node` resolves to the installed Node binary; other executables use the inherited PATH. The runtime must already provide any interpreter or binary an app needs. An app may define any CLI; JavaScript exports are not a core interface. The bundled JavaScript apps optionally share their own `_lib` helpers. `bot-app APP [ARGS...]` launches the configured executable and forwards stdin/stdout/stderr and exit status. `bot-app --help` lists apps and guides. Read the guide before use.

Programs live in `drafts/apps/ID` (the historical directory name). Enable core-bridge access with `config.apps[ID].enabled=true` in drafts/config.json. Ordinary invocations use these editable files immediately; no apply step is required. Existing processes use their loaded code until restarted. The running controller uses a saved copy of only its own directory under `revisions/REV/apps/ID`. Keep controller dependencies inside that package or the installed runtime; sibling apps and shared `_lib` are not copied. Workspace scripts and third-party tools can also run directly without packaging. Controller snapshots cannot contain symlinks. Legacy `module.json` and JavaScript `entry` manifests are unsupported.

Bundled upstream sources are separate from editable app copies. `bot-app --upstream [APP]` reads the running core's package version and bundled app versions, source paths and corresponding working-copy paths. `BOT_UPSTREAM_ROOT` identifies that source tree, including in source installations with a saved core snapshot. Core-only snapshots may have no bundled apps. This command does not run an app or modify any files. Normal managed core updates preserve editable app copies. The bot decides whether, when and how to update or merge them using its own tools; it also owns any baseline or version-control history it needs.

`preflight` is an optional check the author can run. It is mandatory only when declared by a controller being replaced, and is an argument array appended to its command. It must exit zero after validating its package and configuration without external actions. It runs with a disposable state snapshot and, in protected mode, denied network and filesystem writes. `BOT_PREFLIGHT_FILE` points to that snapshot. `botctl` handles configuration/state inspection locally in this mode and rejects publication, enqueue and updates. State edits affect only the validation process. Do not call providers during validation.

## Core CLI

Within supervised execution, `botctl APP OPERATION '[JSON]'` (or `--stdin`) requests a durable primitive. Any language can invoke it. Bundled apps invoke this CLI through an optional helper. `botctl APP --session` accepts JSON lines `{id,operation,args}` and returns `{id,result}` or `{id,error}`, reusing a process for several operations. The CLI owns the HTTP transport. The bridge is loopback-only, uses an ephemeral runtime credential, and has no operator or human-input operation.

| Operation | Arguments | Result |
| --- | --- | --- |
| `config` | `{}` | Enabled app settings |
| `history` | `{}` | Durable conversation history |
| `contribute` | `{key,content}` | Journals bounded, idempotent reference data |
| `state.get` | `{key,fallback?}` | Stored value or fallback |
| `state.set` | `{key,value}` | Stores a JSON value |
| `state.delete` | `{key}` | Removes the value |
| `publish` | `{content}` | Durably publishes from an active action |
| `enqueue` | `{event:{id?,text,metadata?}}` | Wakes the same conversation with app provenance |
| `update.request` | `{}` | Requests controller replacement after the active turn |

Ordinary app settings and enabled flags are read from drafts/config.json on each bridge request; the running controller keeps its saved startup settings. State defaults to namespace `app:ID`. The operator migration sets `stateNamespace` to retain the old namespaces without rewriting state. Apps share the bot execution authority; the app ID establishes state and event provenance, not an isolation boundary between mutually hostile apps. Apps never receive the operator token. Human messages come through the separately authenticated frontend, never through an app operation.

`BOT_APPS`, `BOT_WORKSPACE`, `BOT_DRAFTS`, `BOT_UPSTREAM_ROOT`, `BOT_NODE`, `BOT_CORE_CLI`, `BOT_APP_URL` and `BOT_APP_TOKEN` describe the active environment. `BOT_CALL_ID` identifies the active shell action. Communication apps use it to record delivery receipts in their own storage; the generic publish primitive remains available for other applications. Send in a separate shell call and await completion. Durable receipts survive a later crash; an unknown external action must be inspected before retrying.

## Controller process protocol 1

`config.controller` selects one enabled app. The runtime launches its command with `--serve` and exchanges newline-delimited JSON on stdin/stdout, bounded to 20 MB. Stdout is exclusively protocol data; diagnostics go to stderr. The controller may use any implementation language. Core requests and supervisor RPC arguments remain bounded separately.

The core sends `{type:"init",protocol:1,config,tools}`. `tools` contains exactly the fixed shell schema. The controller replies `{type:"ready",protocol:1}` only after validating its configuration. The core then sends `{type:"turn",event}`, one at a time. Apps deliver future work by enqueueing notifications; the core has no idle tick protocol. `{type:"stop"}` asks the controller to stop. Core shutdown kills all owned descendants and separately tracked shell process groups.

A controller requests `{type:"request",id,method,args}`; the core replies `{type:"result",id,result}` or `{type:"result",id,error:{message,retryable,blocked}}`.

| Method | Arguments | Guarantee |
| --- | --- | --- |
| `history` | `{}` | Admits pending input into the active conversation and returns durable history |
| `contribute` | `{module:APP,key,content}` | Journals bounded, idempotent reference data from an enabled app |
| `append` | `{message}` | Allows assistant/context records during an active event; rejects forged user/tool records |
| `action.run` | `{eventId,call:{id,name,arguments}}` | Journals action start and result; executes only validated `shell` requests, one at a time |
| `input.acknowledge` | `{eventId}` | Records delivery receipts for admitted inputs; makes no claim about objective completion |
| `turn.commit` | `{eventId,message}` | Atomically records the final response and completes the active event and inputs admitted through controller history |

The bundled controller uses `input.acknowledge` when no tool results or notifications remain to drive another model call. It never writes a final assistant reply. `turn.commit` remains available to independent controllers using the older response protocol.

The controller sends `{type:"progress",phase}` as work changes, `{type:"turnDone",eventId}` after acknowledging delivery or a compatible commit, `{type:"turnError",eventId,error,retryable?,blocked?}` before commit on a recoverable provider error, or `{type:"fatal",error}` on failure. The core validates event identity, serialization and commit ordering. It never automatically re-executes a started action after interruption.

Temporary retries retain input order. Blocked inputs remain durable but allow later inputs to proceed until their retry deadline or an operator restart. Resuming an older input after newer work adds a context reference identifying that input, without duplicating the human message; communication apps retain reply association while reads include new arrivals.

The bundled `agent` app owns the tool-only loop. Its `hooks.complete` is a required argv command; `hooks.prepare` is optional. Commands receive JSON on stdin and return JSON on stdout, with diagnostics on stderr. Preparation receives `{messages,tools}` and returns the completion request, optionally with `append` records. The controller journals those records, rechecks pending inputs, then invokes completion. Completion returns `{text,toolCalls,metadata?,usage?}`. Plain model text accompanying tools is discarded; a text-only response is rejected and retried. With no requested tools, the controller acknowledges input delivery and stays idle. There is no task-completion test or tool-step limit.

`services` contains argv commands, such as `["bot-app","scheduler","watch"]`, supervised independently of active turns. These hooks and service conventions belong to the replaceable controller, not the protected core. The default context app owns contributor discovery, channel projections, memory refresh and compaction; its settings select `contributors`, `projections` and `modelApp`.

Communication apps own their message storage and delivery state in workspace.

The bundled context app accepts contributor entries with `placement:"system"`.
It retains these as a frozen `promptSnapshot` in context app state and passes
their text to model completion as `systemContext`, separate from compactable
messages. Context preparation returns `boundary` (the last summarized journal
ID); a changed boundary refreshes system contributions after successful
compaction, including recovery from a restart between the two state writes.
The bundled model provider appends that reference block to its system prompt.
These fields are app conventions; the protected core has no memory policy.

Terminal chat and Telegram are independent conversations. Their incoming messages
generate reference notifications in the core's durable working queue. The bot
uses the corresponding app's read/send commands to communicate. Legacy journal
messages remain available for recovery and are copied into terminal-chat storage.

## Activation and recovery

Ordinary app edits, installations and upgrades take effect on the next invocation. There is no mandatory preflight, activation, probation or controller restart. An app failure is handled at its invocation; tests are chosen by the author. Already running programs must be restarted to load edits.

Run `bot-app updates apply '{}'` only to replace the running controller or its startup configuration. Controller replacements stage at a turn boundary, check that controller and run on probation through genuine work. The previous controller revision and state checkpoint remain available. Recovery restores only the outgoing/incoming controller state namespaces. Ordinary app state, contribution deduplication records and independently edited files survive rollback; apps own their migrations. Applying an update does not enqueue a follow-up model turn; arrange a genuine work continuation when needed. Operator core updates use startup/heartbeat validation without injecting messages or calling a model.

The core is operator-owned. `bot apps install DIR` installs an app for its next invocation (controller installations queue controller replacement); there is no module installation command. `bot apply`, `bot recover` and `bot core install DIR` are operator commands for source installations. Managed Linux updates use the administrator installer, retain the old executable, and preserve conversation and credentials. API 2 to API 3 migration verifies exact legacy package hashes before replacement, retains all drafts and the prior contract, and preserves state namespaces. Edited or custom legacy packages require explicit conversion and are never overwritten automatically.

Cross-API recovery requires the retained matching executable. The current runtime rejects incompatible recovery before stopping a healthy worker. The managed installer restores the old executable and exact draft backup if its candidate fails. State rollback cannot undo external effects. Unknown actions remain durably recorded.

## Protection

The parent supervisor owns control files and the journal. The runtime and all app descendants run behind macOS Seatbelt or Linux Bubblewrap when enabled; controller revisions and core are read-only; ordinary app programs are editable. Source mode fails closed without a sandbox unless `--no-sandbox` is explicitly selected. Managed Linux preserves the installation's selected sandbox mode. Without OS isolation, apps share the bot account's files and processes; root-owned installed core files remain outside that account's editing authority.

## Setup and frontend commands

The distribution declares `botSetup` as an argv command in package.json. The core CLI and managed installer invoke that app for configuration and interactive choices. Provider catalogs, credentials prompts and feature presets are app policy.

Operator-owned `control/clients.json` maps frontend names to executable argv commands, relative to the installed distribution. The generic launcher supplies `BOT_CLIENT_URL`, a scoped `BOT_CLIENT_TOKEN`, `BOT_CLIENT_SESSION` and workspace/app paths, then applies the selected OS sandbox. It never supplies the operator token. Client commands can be replaced by the operator independently of the controller. Editable startup configuration cannot replace this trusted selection.

The default terminal app owns Pi TUI, ACP filtering and the editable chat adapter selection. Its protected transport rejects adapter requests for frontend files, terminals and permissions. The core's `/api/snapshot`, `/api/messages` and `/api/events` return generic journal records; apps own presentation. `/api/chat-snapshot` and the previous persistent chat token remain compatibility aliases for existing clients.
