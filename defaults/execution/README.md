# Editing modules

The runtime supplies `api.paths.workspace` for work and `api.paths.drafts` for editable module packages. The workspace's `BOT-API.md` contains the full API contract. This guide is environment reference; it does not request any changes.

Each module lives in `<drafts>/<id>/`. Its `module.json` names its entry point:

```json
{"id":"example","name":"Example","version":"1.0.0","apiVersion":1,"entry":"index.mjs"}
```

`index.mjs` exports a factory that registers its features:

```js
export default function example(api) {
  api.registerTool({
    name: 'example_read',
    description: 'Read this module\'s saved value.',
    inputSchema: { type: 'object', properties: {}, additionalProperties: false },
    execute: () => api.state.get('value', null),
  });
}
```

For requested changes, inspect the existing module and API reference, edit its draft, and check syntax and behavior before applying. To add a package, place its source and bundled dependencies in a new draft directory. Update `modules.<id>` in `<drafts>/config.json`, including `enabled: true` when it should run. Keep exactly one model registration and at most one context manager; tool names and module IDs must be unique. Do not modify operator-owned runtime or core settings.

Use the `shell` tool for files and checks, then `module_apply` to request activation. The supervisor stages a snapshot and switches after the current turn settles; a startup trial can roll back failures. A successful stage does not itself confirm activation. Module startup trials cannot prove that every later tool path works, so run focused checks before staging.

Available API methods include `registerTool`, `registerModel`, `registerContext`, `on`, `state.get/set/delete`, `history`, `enqueue`, `log`, and `proposeUpdate`. Hooks are serialized: `start`, `beforeModel`, `afterTurn`, `tick`, and `stop`. Use a stable key for `beforeModel` reference contributions and a stable event ID for idempotent `enqueue`. Tick hooks run while idle; they do not invoke the model automatically. Factories must not launch timers or servers.

Shell commands start in the workspace and are bounded by time and output. The execution module registers each process group with the supervisor before allowing it to start work. Commands must not launch detached background services. A timeout or cancellation can leave completed side effects even after the process stops; inspect uncertain outcomes before retrying.
