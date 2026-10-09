# Bot

A small bot with one durable working conversation and separate communication apps, including a full-screen terminal interface built with Pi TUI. The parent process protects the journal, controls the queue, and restores working versions. The protected runtime exposes one supervised shell tool. A replaceable controller and separate process apps provide model access, context, wake-ups and ACP chat; ordinary apps paired with skills provide capabilities.

We are building a bot that can develop its own capabilities while retaining a small, protected foundation. See [the project philosophy](docs/philosophy.md) for the principles that guide design and implementation.

This is a local prototype. The bundled demo is deterministic: it echoes messages, uses no AI, uses shell apps to echo chat messages and makes no paid calls.

The bundled web-search app uses free DuckDuckGo search with no API key. Run it through shell with `bot-app web-search query '{"query":"some query"}'` after reading its skill guide.

## Install on a Linux VM

Download the self-contained Linux bundle, then run:

```sh
sudo sh ./bot-linux-x64 install
bot start
bot chat
```

Use `bot-linux-arm64` on ARM64. The installer provides Node and the chat frontend,
creates an isolated `bot` account, and leaves the service stopped. The first
`bot start` asks for your model, credentials and optional features, then starts
the background service. `bot chat` attaches the chat app. The agent runs without sudo. Closing chat leaves
the service running. Linux bundles use the dedicated account without Bubblewrap
by default. The core is loaded from root-owned `/opt/bot`; editable data lives
in the bot account's `~/.bot`. Apps can access that account's files and processes. Use
`install --sandbox auto` to opt into OS isolation.

`bot` shows help. `bot start` starts the service and enables startup after reboot;
`bot stop` stops the service and disables startup after reboot, retaining the
conversation and credentials. Chat never starts or stops the service.

`bot update` updates the application; `bot uninstall` removes it while retaining
the conversation and credentials. Updates from this private repository require
administrator GitHub access, or you can run a downloaded bundle with `update`.
See [Linux installation](docs/linux-install.md) for supported systems, retained
data, and bundle builds.

## Run from source

Use Node.js 24 or newer, then run these commands from this directory:

```sh
npm install
npm run demo
```

Full-screen chat opens in this terminal. Type ordinary messages in its prompt. The demo uses `.bot-demo` and resumes the same bot conversation on later runs. Exiting chat stops the supervisor started by this command. An interactive terminal is required.

The frontend uses `@earendil-works/pi-tui`, installed with the other npm dependencies.
It shows a scrollable conversation, a fixed multiline editor, and one status line.
Use Enter to send, Ctrl+J (or Shift/Alt+Enter) for a newline, Page Up/Down or the
mouse wheel to scroll. Tool activity, including failures, is hidden by default;
Ctrl+T shows or hides it for diagnostics. Messages return after durable delivery, so you can send another while the bot works. The bot reads and sends chat through `bot-app chat read` and `bot-app chat send`; ordinary model output stays private. Ctrl+C cancels a submission still awaiting delivery; otherwise it exits chat.
Ctrl+D exits at any time. Closing an attached chat leaves accepted work running.
The frontend uses the same Node runtime as the bot; no separate Python runtime
or frontend installation is needed. Launch never downloads software automatically.

The default runtime requires an OS sandbox: `sandbox-exec` on macOS or Bubblewrap (`bwrap`) on Linux. If the sandbox is missing or cannot run, startup fails closed. For local development only, you can explicitly run without protection:

```sh
npm run demo -- --unsafe-development
```

That flag removes the filesystem protection boundary. The supervisor reports this mode. Do not describe an unprotected run as a protected bot.

## Connect a model

Interactive setup asks for a provider, exact model ID, and which features to enable:

```sh
node src/cli.mjs setup
node src/cli.mjs serve
```

Setup defaults to the demo. A real provider must be selected explicitly. Provider credentials come from the environment of the terminal that runs setup and serve; keys are never written by setup. For OpenAI, set `OPENAI_API_KEY`; for Anthropic, set `ANTHROPIC_API_KEY`; for Google, set `GEMINI_API_KEY`. Other providers follow the authentication requirements of the installed `@earendil-works/pi-ai` package. The model ID must exist in that package's model registry.

List the installed registry's provider and model IDs without making a model request:

```sh
node --input-type=module -e 'import { builtinModels } from "@earendil-works/pi-ai/providers/all"; for (const m of builtinModels().getModels()) console.log(m.provider, m.id)'
```

For a noninteractive setup, replace `YOUR_MODEL_ID` with the exact available model ID, with credentials already exported in your shell:

```sh
node src/cli.mjs setup --provider openai --model YOUR_MODEL_ID --disable scheduler --yes
node src/cli.mjs serve
```

Setup creates a new installation and refuses to overwrite a nonempty home. It never calls a paid model. Once a real provider is running, user messages and scheduled work may incur provider charges. To switch an existing demo to a provider, edit `drafts/config.json`: set `apps.agent.modelApp` to `pi-model`, enable `apps.pi-model`, disable `apps.demo-model`, and set `provider`/`model` inside `apps.pi-model` and the top-level `model` object; then run `apply`.

`--disable` and `--enable` accept comma-separated `chat,memory,context,scheduler,skills,web-search`. Skills and web search start enabled. The bot always has one supervised shell tool. Capabilities run as ordinary app commands paired with skills; there is no plugin loader or registration API. Disabling scheduler stops future wake-up delivery.

The skills app publishes a compact index of app guides and workspace skills. Read a matching SKILL.md through shell; `bot-app --help` lists apps and `bot-app skills list '{}'` lists guides. Add workflow guides in workspace/skills/<name>/SKILL.md. See [Skills](docs/skills.md).

## Installation location

Every command accepts `--home DIR`. `BOT_HOME` is the second choice; otherwise the default is `.bot` in the current directory (`.bot-demo` for `demo`). Use a consistent absolute path when starting from different directories:

```sh
node src/cli.mjs setup --home /path/to/my-bot --yes
node src/cli.mjs serve --home /path/to/my-bot
node src/cli.mjs status --home /path/to/my-bot
```

The home contains:

| Path | Purpose |
| --- | --- |
| `workspace/` | Files the bot works on |
| `drafts/` | Editable behavior apps, apps/ packages and config.json |
| `revisions/` | Versioned app and behavior app bundles |
| `cores/` | Protected runtime-engine snapshots used by revisions |
| `control/` | Parent-owned journal, release pointers, state, and local control credentials |

The protected runtime can write only to its workspace and drafts. Its state API writes through the parent. Keep `control/` private. Chat launches a protected ACP adapter with a scoped chat token; the adapter cannot install apps or update the core. The separate operator token remains in `control/`. The local control server binds to `127.0.0.1`.

For a supervisor that continues running independently of the frontend:

```sh
node src/cli.mjs serve --home /path/to/my-bot
# In another interactive terminal:
node src/cli.mjs chat --home /path/to/my-bot
```

Exiting an attached chat leaves `serve` running. Stop the supervisor with Ctrl+C in its own terminal. From source, `start` is a headless alias for `serve` and remains in the foreground; the managed Linux launcher provides background `start` and `stop`.

Chat reconnects to its own durable app history. Terminal chat and Telegram are separate conversations; incoming messages wake the bot with reference notifications, and the bot uses app commands to read and reply. The adapter accepts text messages, acknowledges durable delivery immediately, and receives replies independently. The bot uses `bot-app chat read` and `bot-app chat send` to communicate, with one durable queue for accepted work. Chat cancellation stops the current input through the supervisor; interrupted tools may have uncertain side effects. The adapter does not support client-provided MCP servers, filesystem operations, or changing model configuration through chat.

Clean service restarts stay quiet in chat. Genuine recovery events appear as System notices, including interrupted tool actions whose outcomes need checking. Routine restart notices from older versions are hidden from chat while remaining in the journal.

The fixed core bridge filters ACP traffic and rejects adapter requests for frontend filesystem, terminal, and permission operations. Editable adapter code receives only scoped chat access.

## Telegram

The Telegram app runs a receiver alongside the controller and wakes the bot when your messages arrive. It keeps its own inbox and replies directly in Telegram, independently of terminal chat. See [Telegram setup](docs/telegram.md) for VM credentials and private-account pairing.

## Add capabilities

Create ordinary programs paired with skills, or copy the local-time app example into `<home>/drafts/apps/local-time`. Add `apps.local-time.enabled: true` to drafts/config.json for core-bridge access, then run it. App edits and upgrades take effect on the next invocation; they require no mandatory validation, activation, probation or bot restart. Test when useful. Existing running programs use their loaded code until restarted.

Apps have app.json with an executable argv command, SKILL.md, and their own program files. They execute as ordinary shell child processes, validate their CLI arguments and print JSON. Apps do not register model tools. Existing system commands and workspace scripts also work directly through shell.

The replaceable controller, model, context, memory, scheduler, skills and chat all run in separate processes. The core imports no app code and has no plugin registration API. List or install packages with `node src/cli.mjs apps` and `node src/cli.mjs apps install DIR`. Read [the API contract](docs/contract.md).

## Recovery and updates

The parent owns the journal and single event queue. It saves incoming work notifications before processing them and records tool starts before execution. Communication apps own their message history and delivery state. A started tool whose outcome is unknown after interruption is not automatically executed again. Recovery keeps the conversation; controller rollback can restore app state from a controller change boundary while preserving independently edited ordinary app files. It cannot undo external side effects such as a file already written or a request already sent.

Use `bot-app updates apply '{}'` only for replacing the running controller or its startup configuration. After the current turn finishes, controller replacement runs its declared preflight with scratch state. Unrelated apps are not validated. A passing controller starts in probation. It becomes the working revision only after a real conversation turn completes successfully and `runtime.trialMs` has elapsed (3 seconds by default). Startup errors or crashes during probation restore the previous working revision. If attention is needed, the supervisor reports the error. The operator can also use:

```sh
node src/cli.mjs recover
```

Recovery requires a previous tested revision. On first-start errors, correct the provider/model configuration or credentials, or fix the app program and retry. Apply and restart only for controller changes. If chat cannot connect, start the supervisor and attach with `chat` again.

The operator can trial an installed runtime-core checkout with a compatible `botCoreApi: 3` package marker:

```sh
node src/cli.mjs core install /path/to/installed-bot-core
```

Setup copies runtime source into `cores/` and links its dependencies to the installed application's protected dependencies. Core installation snapshots the candidate source and dependencies before a trial, preserving the previous engine for rollback. This switches the child runtime engine and retains the previous engine for rollback. It does **not** replace the already running parent guardian or CLI. Guardian upgrades require an operator to install the new application and restart it. The agent cannot update protected core files, choose a different sandbox policy, or call the operator's core-update endpoint through its application API.

## Check the implementation

```sh
npm test
```

The test suite uses deterministic fixtures and does not require provider credentials or paid model calls. No launch agent, daemon, cloud resource, or startup service is installed automatically.

The tests use deterministic providers; real model integration can be exercised
after setup with your chosen provider. Installer health checks do not send a
synthetic prompt. Operator core updates complete startup and heartbeat validation without model traffic; bot-managed controller trials settle through genuine work.
