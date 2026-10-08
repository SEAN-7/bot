# Bot

A small bot with one durable conversation and a terminal messaging interface powered by the existing Toad application. The parent process protects the journal, controls the queue, and restores working versions. Replaceable modules provide the model, memory, scheduling, context management, execution, and ACP chat adapter.

This is a local prototype. The bundled demo is deterministic: it echoes messages, uses no AI, performs no tool actions, and makes no paid calls.

## Install on a Linux VM

Download the self-contained Linux bundle, then run:

```sh
sudo sh ./bot-linux-x64 install
bot chat
```

Use `bot-linux-arm64` on ARM64. The installer provides Node, Python and Toad,
creates an isolated `bot` account, asks for your model and optional features,
and starts a systemd service. The agent runs without sudo. Closing chat leaves
the service running.

`bot update` updates the application; `bot uninstall` removes it while retaining
the conversation and credentials. Updates from this private repository require
administrator GitHub access, or you can run a downloaded bundle with `update`.
See [Linux installation](docs/linux-install.md) for supported systems, retained
data, and bundle builds.

## Run from source

Use Node.js 24 or newer, then run these commands from this directory:

```sh
npm install
uv venv --python 3.14 .tui
uv pip install --python .tui/bin/python batrachian-toad==0.6.20
npm run demo
```

Toad opens in this terminal. Type ordinary messages in its prompt. The demo uses `.bot-demo` and resumes the same bot conversation on later runs. Exiting Toad stops the supervisor started by this command. An interactive terminal is required.

The frontend is unmodified Toad, installed separately into the project-local `.tui` environment. Use a current `uv` release to select stable Python 3.14; old releases may select a prerelease. If pip must build native dependencies, a current Rust toolchain may be required. Launch never downloads frontend software automatically. Set `BOT_TOAD_BIN` to an existing Toad executable to use another installation.

The default runtime requires an OS sandbox: `sandbox-exec` on macOS or Bubblewrap (`bwrap`) on Linux. If the sandbox is missing or cannot run, startup fails closed. For local development only, you can explicitly run without protection:

```sh
npm run demo -- --unsafe-development
```

That flag removes the filesystem protection boundary. The supervisor reports this mode. Do not describe an unprotected run as a protected bot.

## Connect a model

Interactive setup asks for a provider, exact model ID, and which features to enable:

```sh
node src/cli.mjs setup
node src/cli.mjs start
```

Setup defaults to the demo. A real provider must be selected explicitly. Provider credentials come from the environment of the terminal that runs setup and start; keys are never written by setup. For OpenAI, set `OPENAI_API_KEY`; for Anthropic, set `ANTHROPIC_API_KEY`; for Google, set `GEMINI_API_KEY`. Other providers follow the authentication requirements of the installed `@earendil-works/pi-ai` package. The model ID must exist in that package's model registry.

List the installed registry's provider and model IDs without making a model request:

```sh
node --input-type=module -e 'import { builtinModels } from "@earendil-works/pi-ai/providers/all"; for (const m of builtinModels().getModels()) console.log(m.provider, m.id)'
```

For a noninteractive setup, replace `YOUR_MODEL_ID` with the exact available model ID, with credentials already exported in your shell:

```sh
node src/cli.mjs setup --provider openai --model YOUR_MODEL_ID --disable scheduler --yes
node src/cli.mjs start
```

Setup creates a new installation and refuses to overwrite a nonempty home. It never calls a paid model. Once a real provider is running, user messages and scheduled work may incur provider charges. To switch an existing demo to a provider, edit `drafts/config.json`: enable `pi-model`, disable `demo-model`, and set `provider`/`model` inside the `pi-model` configuration and the top-level `model` object; then run `apply`.

`--disable` and `--enable` accept comma-separated `chat,memory,context,scheduler,execution`. All five start enabled. Optional modules remain disabled until you enable them. Disabling `execution` removes shell and module-update tools; disabling `scheduler` removes scheduled wake-ups.

## Installation location

Every command accepts `--home DIR`. `BOT_HOME` is the second choice; otherwise the default is `.bot` in the current directory (`.bot-demo` for `demo`). Use a consistent absolute path when starting from different directories:

```sh
node src/cli.mjs setup --home /path/to/my-bot --yes
node src/cli.mjs start --home /path/to/my-bot
node src/cli.mjs status --home /path/to/my-bot
```

The home contains:

| Path | Purpose |
| --- | --- |
| `workspace/` | Files the bot works on |
| `drafts/` | Editable module source and `config.json` |
| `revisions/` | Versioned, read-only module bundles |
| `cores/` | Protected runtime-engine snapshots used by revisions |
| `control/` | Parent-owned journal, release pointers, state, and local control credentials |

The protected runtime can write only to its workspace and drafts. Its state API writes through the parent. Keep `control/` private. Toad launches a protected ACP adapter with a scoped chat token; the adapter cannot install modules or update the core. The separate operator token remains in `control/`. The local control server binds to `127.0.0.1`.

For a supervisor that continues running independently of the frontend:

```sh
node src/cli.mjs serve --home /path/to/my-bot
# In another interactive terminal:
node src/cli.mjs chat --home /path/to/my-bot
```

Exiting an attached chat leaves `serve` running. Stop the supervisor with Ctrl+C in its own terminal.

Toad connects to the same durable conversation on each attachment and replays its history. The adapter accepts text prompts and reports tool progress and queued replies. Toad cancellation stops the current input through the supervisor; interrupted tools may have uncertain side effects. The adapter does not support client-provided MCP servers, filesystem operations, or changing model configuration through Toad.

The fixed core bridge filters ACP traffic and rejects adapter requests for frontend filesystem, terminal, and permission operations. Editable adapter code receives only scoped chat access.

## Install or change a module

Install the included example with one command:

```sh
node src/cli.mjs modules install ./examples/local-time
```

This copies the local module into drafts and stages a revision. If the supervisor is running, it activates at a turn boundary. Otherwise, the next startup tries it. There is no package registry or automatic dependency installation in this prototype; bundle any module dependencies before installing. Only install module code you trust.

List modules, or activate edits to module code/configuration:

```sh
node src/cli.mjs modules
node src/cli.mjs apply
```

Modules share one registration API. There is one active model and at most one context manager. Tool names must be unique. See [the module guide](docs/plugins.md) and [the API contract](docs/contract.md).

## Recovery and updates

The parent owns the journal and single event queue. It saves incoming messages before processing them and records tool starts before execution. A started tool whose outcome is unknown after interruption is not automatically executed again. Recovery keeps the conversation; it can restore module state from a change boundary. It cannot undo external side effects such as a file already written or a request already sent.

After the current turn finishes, module updates first run a separate preflight with scratch module state and external actions disabled. A passing candidate starts in probation. It becomes the working revision only after a real conversation turn completes successfully and `runtime.trialMs` has elapsed (3 seconds by default). Startup errors or crashes during probation restore the previous working revision. If attention is needed, the supervisor reports the error. The operator can also use:

```sh
node src/cli.mjs recover
```

Recovery requires a previous tested revision. On first-start errors, correct the provider/model configuration or credentials, or fix the module draft, then apply and restart as appropriate. If chat cannot connect, start the supervisor and attach with `chat` again.

The operator can trial an installed runtime-core checkout with a compatible `botCoreApi: 1` package marker:

```sh
node src/cli.mjs core install /path/to/installed-bot-core
```

Setup copies runtime source into `cores/` and links its dependencies to the installed application's protected dependencies. Core installation snapshots the candidate source and dependencies before a trial, preserving the previous engine for rollback. This switches the child runtime engine and retains the previous engine for rollback. It does **not** replace the already running parent guardian or CLI. Guardian upgrades require an operator to install the new application and restart it. The agent cannot update protected core files, choose a different sandbox policy, or call the operator's core-update endpoint through its module API.

## Check the implementation

```sh
npm test
```

The test suite uses deterministic fixtures and does not require provider credentials or paid model calls. No launch agent, daemon, cloud resource, or startup service is installed automatically.

The tests use deterministic providers; real model integration can be exercised
after setup with your chosen provider. Installer health checks do not send a
synthetic prompt or promote a runtime without a successful conversation turn.
