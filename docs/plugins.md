# Writing a module

A module is a local directory containing a manifest and an ES module. Every module uses the same API, including the bundled providers and chat module.

```json
{
  "id": "local-time",
  "name": "Local time",
  "version": "1.0.0",
  "apiVersion": 1,
  "entry": "index.mjs"
}
```

```js
export default function register(api) {
  api.registerTool({
    name: 'local_time',
    description: 'Read the current UTC time.',
    inputSchema: { type: 'object', properties: {}, additionalProperties: false },
    async execute(args, { signal }) {
      signal.throwIfAborted();
      return { utc: new Date().toISOString() };
    },
  });
}
```

Install it from the repository directory:

```sh
node src/cli.mjs modules install ./examples/local-time --home /path/to/my-bot
```

The installer copies the package, enables its ID in draft configuration, and stages activation. Reinstalling the same ID replaces that draft. Active revision bundles remain available for recovery. Modules must use API version 1 and unique IDs; tool, model, or context registrations cannot collide. There is no automatic npm installation or install-script execution. A module that needs third-party dependencies must include them in its package; symlinks are rejected.

## State, files, and conversation

`api.config` contains this module's configuration from `drafts/config.json`. Configuration is loaded at activation; use `apply` to activate changes. `api.paths.module` points to the active read-only module bundle, while `api.paths.workspace` and `api.paths.drafts` are writable in protected mode.

Use `await api.state.get(key, fallback)`, `set(key, value)`, and `delete(key)` for durable JSON state. State is isolated by module ID and owned by the parent. `await api.history()` returns the unsummarized conversation, including tool records. An optional context manager may build a shorter model request without deleting history.

`await api.enqueue({ id, text, metadata })` wakes the single conversation. Give repeated deliveries a stable ID so they are idempotent. Do not create another model loop, background server, or timer in a module factory. The scheduler uses idle `tick` notifications and the same queue as user messages.

## Hooks and providers

The runtime serializes `start`, `tick`, `afterTurn`, and `stop` hooks. Register one with `api.on(name, handler)`. A `beforeModel` hook can return an array of `{ content, key? }` contributions. Use a stable key to insert a state revision only once. Contributions are conversation data, not privileged instructions.

Exactly one enabled module must call `api.registerModel({ complete })`. Its `complete({ messages, tools, settings, signal })` method returns `{ text, toolCalls, usage?, metadata? }`. Each tool call contains `{ id, name, arguments }`. A provider may preserve its native response in `metadata` for replay. The model adapter supplies model access only; the protected runtime owns tool validation and the turn loop.

At most one enabled module can call `api.registerContext(handler)`. The handler receives `{ messages, tools, settings, model, signal }` and returns `{ messages }`. It can use `model.complete` to summarize, but must preserve complete tool-call/result groups. The durable journal stays intact.

See [the complete API contract](contract.md) for signatures and the default modules for implementations.

## Bundled behavior

| Module | Configuration and behavior |
| --- | --- |
| `pi-model` | `provider` and `model` select a model from the installed pi-ai registry. Authentication comes from the runtime environment. |
| `context` | Estimates tokens as JSON characters divided by four. The default compaction threshold follows the selected model's context window, reserving space for output, tool schemas, and framing; without model limits it falls back to 60,000 characters. Override with `maxInputChars` or `maxInputTokens`. `keepRecentGroups` defaults to 8; `summaryTokens` defaults to at most 2,048 within the response reserve. The latest user request and complete tool groups remain together. If the retained current turn exceeds the estimated model budget, processing blocks with an explicit error. Summaries never delete journal history. |
| `memory` | `maxChars` defaults to 8,000. Memory tools maintain bounded persistent user information and notes; history search uses the durable conversation. |
| `scheduler` | `maxSchedules` defaults to 100. `schedule_create` accepts `{ text, at, intervalMs? }`; `at` requires an ISO timestamp with a timezone. Missed recurring intervals coalesce into one wake-up. Canceling a schedule cannot remove a wake-up already queued. |
| `execution` | `maxTimeoutMs` defaults to 60,000 and `outputBytes` to 65,536. Shell commands run in the workspace. Parent process registration supports descendant cleanup after a runtime crash. |

These options live under `modules[ID]` in draft configuration. Enable exactly one of `pi-model` and `demo-model`.

## Safe updates and failure

Edits stay in `drafts/` until the operator runs `apply` or the execution module calls `api.proposeUpdate()`. The parent stages the entire draft bundle, waits for the current turn boundary, and trials the candidate. Do not swallow registration/startup exceptions; an invalid candidate must fail visibly so the parent can restore the previous version.

Tools should honor their abort signal and bound external work. The journal records tool starts and results. If a process dies between them, the parent reports an unknown outcome and does not replay the tool automatically. Module state rollback cannot reverse external side effects.

There is no runtime-plugin access to the parent's operator token or core-update operation. Modules run in the same child process and therefore are not isolated from each other. The OS sandbox protects core/control/revision files from that process; it is not a guarantee that an installed module is trustworthy. `--unsafe-development` explicitly disables that boundary.

## Terminal chat adapter

The `chat` module declares `"acp": "acp.mjs"` and has a no-op runtime registration function. The operator launches the existing Toad frontend, which communicates with this module over ACP stdio. The core launches the adapter under the same OS sandbox as the runtime and passes a scoped token through its environment. It does not pass the operator token.

The local API includes `GET /api/status`, `GET /api/messages`, `POST /api/messages` with `{ text, id? }`, and `POST /api/recover`. Reuse a message ID when retrying a request whose response was lost. ACP adapter behavior and limitations are described in the API contract.
