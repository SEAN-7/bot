# Linux installation

The Linux bundle includes the application, Node, and the terminal chat frontend.
You do not install a separate frontend runtime. The initial
target is Debian and Ubuntu with systemd, on x86-64 or ARM64. Installation needs
administrator access and package-repository access for process utilities. The running agent does not have sudo access.

Download `bot-linux-x64` for an x86-64 VM or `bot-linux-arm64` for an ARM64 VM.
These are self-extracting executables, not source archives. On the VM:

```sh
sudo sh ./bot-linux-x64 install
```

Installation creates the isolated account and service, but does not configure
or start the agent. On first `bot start`, the installer asks for your model provider, model ID,
credentials, and optional features.
The default demo uses no paid model calls. API keys are entered without echoing
and stored in a root-owned environment file. Completing the wizard starts the background service and enables it after reboot.
Cancelling the wizard leaves the service stopped; the next launch asks again.

Then open a terminal chat:

```sh
bot start
bot chat
```

The operator may be asked for their sudo password to attach as the dedicated
account. This does not give the agent sudo access. Closing chat leaves the
service running. The local chat API stays on loopback; no inbound network port
is required. SSH to the VM before running these commands. Bare `bot` shows help.
`bot chat` only attaches to a running service; if stopped, run `bot start` first.
Starting an already configured bot does not repeat setup or open chat.
Chat uses a full-screen conversation, a fixed multiline input box, and a single
status line. Tool activity, including failures, is hidden by default. Ctrl+T
toggles tool diagnostics; Ctrl+C cancels a submission still awaiting delivery
or exits after delivery; Ctrl+D exits at any time. Page Up/Down and the mouse wheel scroll the
conversation. Ctrl+J or Shift/Alt+Enter inserts a newline.
Messages are acknowledged after durable delivery, allowing another message while the bot works. The bot reads the conversation with `bot-app chat read` and sends replies with `bot-app chat send`; ordinary model output is private. Closing or updating the chat does not discard accepted work. Reopen an older chat client once to load asynchronous delivery support.

Fresh installations run workers as the unprivileged `bot` account without
Bubblewrap. Apps can access the bot account's data and processes, while the
installed core files remain root-owned and read-only to the bot. The service allows
standard setuid/setgid user helpers, including `crontab`, so the bot can manage its
own cron jobs. The account has no sudo access. To enable the protected worker sandbox during installation, use:

```sh
sudo sh ./bot-linux-x64 install --sandbox auto
```

This mode installs Bubblewrap. On Ubuntu with restricted user namespaces it adds
an AppArmor exception for `/usr/bin/bwrap`; the global restriction remains enabled.
The selected mode is saved in root-owned `/etc/bot/installation.json` and used by
both the service and terminal chat. Updates retain that mode. Older installations
without a saved mode retain their protected sandbox.

## Service control

```sh
bot start
bot status
bot chat
bot stop
```

`bot start` starts the background service, enables it after reboot, and checks
runtime health. `bot stop` stops the service and disables startup after reboot
until you run `bot start` again. Both commands can be repeated. Stop interrupts
active work; the durable conversation, schedules, memory, and credentials remain.
The supervisor records interrupted tools as having an unknown outcome rather
than automatically replaying them. Chat is an optional app: service control
works with chat disabled, and closing chat leaves the service running.

## Updates and removal

```sh
bot status
bot logs
bot update
bot uninstall
```

The repository is private. Automatic update checks need your administrator's
GitHub access through `GH_TOKEN` or an authenticated `gh` CLI. GitHub credentials
are not stored in the agent account. Alternatively, download the next bundle
and run it directly:

```sh
sudo sh ./bot-linux-x64 update
```

Updates preserve the conversation, memory, schedules, and edited apps. Older `pi-model` apps are renamed to `model-provider`, preserving provider settings, custom code and state namespaces; the exact working copies are backed up for rollback. The API 3 migration converts the API 2 behavior packages into executable apps, removes the plugin loader, preserves state namespaces and retains complete draft backups; edited legacy packages block automatic migration. Open terminal chats retain their scoped credential across service restarts and reconnect to the same durable conversation. Prompts sent during a brief update outage retry with the same event ID, preventing duplicate submission. The
new runtime is tested before activation and must pass startup and heartbeat
validation before the installer reports success. Core updates need no chat
message or paid model call. Ordinary app edits are immediately available on the next invocation, without
validation or activation. Controller replacements retain recovery through genuine
work. The transition updates unchanged bundled guides and behavior, preserves
bot-authored programs, and backs up the exact working files for rollback. A failed service update restores the prior
application release and, during the API migration, the original drafts. Recovery to a different application API requires the operator to restore its retained executable; the runtime refuses that incompatible rollback without changing conversation data.

Uninstall stops the service and removes the application and launcher. It retains
the agent's data and credential file. It does not delete the account or purge
your conversation.

## Managed paths

| Path | Owner and purpose |
| --- | --- |
| `/opt/bot/releases/` | Root-owned application bundles |
| `/opt/bot/current` | Selected application release |
| `/usr/local/bin/bot` | Root-owned operator launcher |
| `~bot/.bot` (`/var/lib/bot/.bot`) | Private persistent bot data |
| `/etc/bot/environment` | Root-readable model credentials |
| `/etc/bot/installation.json` | Root-owned installation and sandbox settings |
| `bot.service` | Systemd service running as the unprivileged `bot` user |

The installer refuses unrelated pre-existing accounts or managed paths. With `--sandbox auto`, the Linux worker uses Bubblewrap with a private PID
namespace and writable mounts only for its workspace and app drafts. This
keeps the supervisor outside the worker's process namespace while retaining
durable recovery.

## Building a bundle

Build on a Linux machine matching the target architecture:

```sh
python3 scripts/build-bundle.py dist
```

The build downloads pinned runtimes and dependencies, verifies the downloaded Node
distribution and npm dependency integrity, and embeds a checksum for the self-extracting payload.
The output is `dist/bot-linux-ARCH` and its `.sha256` file. Build machines need
Python with safe tar extraction support and normal Linux archive utilities.

The `Build Linux bundles` GitHub Actions workflow builds both architectures and
uploads artifacts. Building does not automatically publish a GitHub release.
For `bot update` to discover a version, a maintainer must publish the matching
bundle and checksum as GitHub release assets.

The installed runtime always loads its core from the root-owned application
release. Writable configuration cannot select a different runtime core. Core
updates go through the operator installer; apps remain editable in `~bot/.bot`.
Older state paths are retained as compatibility aliases when needed.
