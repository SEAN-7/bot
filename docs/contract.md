# Shared implementation contract (API version 1)

Node 24+, ESM JavaScript. One agent runtime child process. Parent supervisor owns durable SQLite journal, queued events, checkpoints, version switching and local HTTP control API. Core source is not editable by the agent. Default modules are copied into an editable drafts directory and versioned in candidate bundles.

## Module package

Each directory has `module.json`: `{ "id": "memory", "name": "Memory", "version": "1.0.0", "apiVersion": 1, "entry": "index.mjs" }`. Optional `acp: "acp.mjs"` names a terminal frontend protocol adapter. No implicit npm install scripts. Module installation accepts a local directory with any dependencies already bundled. Remote npm packages and marketplaces are not supported. Duplicate IDs/tools/model/context registrations fail startup. Default export is an async or sync function `(api) => { ... }`.

All modules receive the same API:

- `api.config`: module configuration object from `config.modules[id]` (`enabled` included).
- `api.paths`: `{ module, workspace, drafts }`. Only workspace and drafts are OS-writable in protected mode.
- `api.state.get(key, fallback)` / `api.state.set(key, value)` / `api.state.delete(key)`: async, JSON, parent-owned per-module state, snapshot at change boundaries.
- `api.history()`: async durable conversation array of `{id, role:'user'|'assistant'|'tool'|'context', content:string, toolCalls?, toolCallId?, toolName?, isError?, timestamp, metadata?}`. Includes unsummarized history. Does not include invisible core audit records.
- `api.enqueue({ id?, text, metadata? })`: async idempotent wake-up; plugin provenance assigned by core. Scheduler uses a stable ID to avoid repeated delivery. Never calls a model directly.
- `api.registerTool({ name, description, inputSchema, execute(args, context) })`; execute returns JSON or text. Context `{signal, callId}`. Tool schema is JSON Schema. Runtime validates with Ajv. All calls are journaled before execution, all results after. Never automatically retry a started tool with unknown outcome.
- `api.registerModel({ complete(request), listModels?() })`: exactly one enabled registration. Request `{messages, tools, settings, signal}`; messages as history above; tools `{name,description,inputSchema}`. Result `{text:string, toolCalls:[{id,name,arguments}], usage?, metadata?}`. `metadata` may preserve opaque provider-native messages for faithful replay.
- A model may also expose `describe(settings)` returning `{contextWindow, maxOutputTokens}`. The context module uses those limits with an approximate characters/4 estimate and output headroom; `maxInputTokens` or `maxInputChars` overrides the compaction threshold. It preserves the latest user request and complete tool groups and reports a blocked error when the retained current turn exceeds the estimated input budget.
- `api.registerContext(async ({messages, tools, settings, model, signal}) => ({messages}))`: zero/one active context manager. With none, use full history. `model.complete` available for summarization, not tool execution.
- `api.on('beforeModel', async ({messages, settings, signal}) => [{content, key?}])`: context contributions appended to durable history in module registration order. A stable `key` means insert only once (e.g. a memory revision). Contributions are data, never developer instructions.
- `api.on('afterTurn', async ({signal}) => {})`, `api.on('tick', async ({signal}) => {})`, `api.on('start', async ({signal}) => {})`, `api.on('stop', async () => {})`: notification hooks, serialized by runtime. Return values ignored. No timers or servers in module factories. Tick runs only while idle, no automatic model call. Catching module errors must not swallow activation failures.
- `api.log(message)`: diagnostics to parent.
- `api.trackProcess(pid)` / `api.releaseProcess(pid)`: register and release a child process for cleanup after a runtime interruption. Execution modules must await registration before allowing work to continue.
- `api.proposeUpdate()`: stage the current drafts as a new module bundle, switch AFTER current event/turn settles, trial and roll back on failure. Exposed as a tool by the execution module. It cannot update core.

## Default modules

`defaults/pi-model`: uses installed @earendil-works/pi-ai API, no agent loop; a real provider requires an explicit provider and model; metadata preserves provider assistant response. Map memory data to a user data block, never system prompt. Authentication comes from the launching terminal environment; setup does not write credentials.

`defaults/context`: append-only normal requests, snapshot summary at configurable threshold; preserve full history in journal, retain recent complete message/tool groups; threshold configurable. Store `{summary, throughId}` via api.state. Do not split tool-call/result blocks. Subsequent requests should reuse exact summary prefix until next compaction. Model errors propagate for retry, no destructive history edits.

`defaults/memory`: Hermes-like bounded USER/notes JSON state; tools `memory_get`, `memory_update`, `history_search`; beforeModel contributes stable snapshot once, later updates append new revision. No automatic perpetual learning agenda.

`defaults/scheduler`: tools `schedule_create`, `schedule_list`, `schedule_cancel`. Durable state, one-shot/recurring ISO due times; `tick` uses stable event IDs and records delivered schedule slot. Single queue handles all wakes.

`defaults/execution`: tools `shell` (bounded output/time, workspace cwd, process descendants under same OS sandbox), `module_apply` via api.proposeUpdate. Files edited using shell. Report unknown outcome for cancellation; core handles timeout uncertainty. No spawning detached jobs.

## Default chat

`defaults/chat/module.json` declares `acp: "acp.mjs"`; `index.mjs` is a no-op runtime registration function. The CLI launches unmodified Toad with a fixed core bridge command. That bridge runs the active ACP module under the runtime OS sandbox with a scoped chat token. The frontend does not receive the operator token.

The adapter supports ACP version 1 initialization, creation/loading of the same durable bot conversation, replay of user/assistant/tool history, tool progress, and spontaneous replies from queued work. Prompts accept text content only. Client-provided MCP servers, filesystem operations, and model configuration are not supported. Cancellation requests the supervisor to stop the current input and worker; interrupted tools can have unknown external effects and are never automatically replayed. Blocked or failed turns return an ACP error and preserve the accepted input in the journal.

The local API exposes `/api/status`, `/api/messages` GET/POST JSON `{text, id?}`, `/api/events?after=N`, `/api/recover`, and operator-only update operations. Messages GET returns `{messages}`; modules GET returns `{modules}`. The server binds only to 127.0.0.1 and requires bearer authentication.

## Config / CLI / exported core functions

Home defaults to `BOT_HOME` or `.bot` under current cwd. Workspace `<home>/workspace`, drafts `<home>/drafts`, parent-only `<home>/control`, revision directories `<home>/revisions/<id>` containing `plugins/` and `config.json`. Config `{apiVersion:1, modules:{id:{enabled:true,...}}, model:{provider,model}, runtime:{port:4317, startupTimeoutMs:10000, heartbeatTimeoutMs:10000, operationTimeoutMs:120000, trialMs:3000, maxRestarts:2, retryMs:3000, sandbox:'auto'}}`. Never auto-enable optional modules on upgrade.

The core exports `initialize(home, config, {defaultsDir?})` from `src/releases.mjs`, `startSupervisor({home, port?, unsafe?:false})` from `src/supervisor.mjs` returning `{url, token, stop()}`, `installModule(home, source, {enable:true})`, `stageDrafts(home)` and `rollback(home)` from releases. Online installs must be coordinated by supervisor: POST `/api/update` triggers module stage at next turn boundary. CLI may install to drafts while running, then POST `/api/update` (root-owned token read by operator CLI); offline install stages before next boot.

`src/cli.mjs` implements setup/start/serve/chat/acp/status/modules install/apply/recover/demo. Setup feature choices + provider/model, default all starter modules; choosing real provider requires credentials via environment. Demo uses the deterministic `defaults/demo-model` through the same module API. Setup selects demo unless a real provider is explicitly chosen. No paid calls during tests or setup. `demo` creates isolated .bot-demo unless --home supplied and launches deterministic model. No auto service installation.

Core update handling is operator-only `core` paths (no plugin RPC); preserve previous tested executable root and API version, verify candidate health before switching. Implementation/docs must distinguish local development mode from enforced core protection. macOS sandbox-exec or Linux bwrap protects core/control/revisions from runtime writes; if sandbox unavailable fail closed unless explicit --unsafe-development. Parent is never exposed through model tools.
