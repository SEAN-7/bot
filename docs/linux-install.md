# Linux installation

The Linux bundle includes the application, Node, Python, and the existing Toad
terminal frontend. You do not install those runtimes separately. The initial
target is Debian and Ubuntu with systemd, on x86-64 or ARM64. Installation needs
administrator access and package-repository access for process utilities. The running agent does not have sudo access.

Download `bot-linux-x64` for an x86-64 VM or `bot-linux-arm64` for an ARM64 VM.
These are self-extracting executables, not source archives. On the VM:

```sh
sudo sh ./bot-linux-x64 install
```

Installation creates the isolated account and service, but does not configure
or start the agent. On first launch, `bot` asks for your model provider, model ID,
credentials, and optional features.
The default demo uses no paid model calls. API keys are entered without echoing
and stored in a root-owned environment file. Completing the wizard starts the background service and enables it after reboot.
Cancelling the wizard leaves the service stopped; the next launch asks again.

Then open a terminal chat:

```sh
bot
```

The operator may be asked for their sudo password to attach as the dedicated
account. This does not give the agent sudo access. Closing chat leaves the
service running. The local chat API stays on loopback; no inbound network port
is required. SSH to the VM before running `bot`. `bot chat` is an alias.
Subsequent launches attach to your configured agent without repeating setup.
Chat defaults to a conversation and compact input, with sidebar, session tabs,
shortcut footer and status bars hidden. Toad settings remain customizable.
Fresh installations run workers as the unprivileged `bot` account without
Bubblewrap. Plugins can access the bot account's data and processes, while the
installed core files remain root-owned and read-only to the bot. To enable the protected worker sandbox during installation, use:

```sh
sudo sh ./bot-linux-x64 install --sandbox auto
```

This mode installs Bubblewrap. On Ubuntu with restricted user namespaces it adds
an AppArmor exception for `/usr/bin/bwrap`; the global restriction remains enabled.
The selected mode is saved in root-owned `/etc/bot/installation.json` and used by
both the service and terminal chat. Updates retain that mode. Older installations
without a saved mode retain their protected sandbox.

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

Updates preserve the conversation, memory, schedules, and edited plugins. The
new runtime is tested before activation and remains on probation until a real
conversation turn succeeds. An update does not invent a prompt or make a paid
model call just to promote itself. A failed service update restores the prior
application release.

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
namespace and writable mounts only for its workspace and plugin drafts. This
keeps the supervisor outside the worker's process namespace while retaining
durable recovery.

## Building a bundle

Build on a Linux machine matching the target architecture:

```sh
python3 scripts/build-bundle.py dist
```

The build downloads pinned runtimes and dependencies, verifies downloaded Node
and uv distributions, and embeds a checksum for the self-extracting payload.
The output is `dist/bot-linux-ARCH` and its `.sha256` file. Build machines need
Python with safe tar extraction support and normal Linux archive utilities.

The `Build Linux bundles` GitHub Actions workflow builds both architectures and
uploads artifacts. Building does not automatically publish a GitHub release.
For `bot update` to discover a version, a maintainer must publish the matching
bundle and checksum as GitHub release assets.

The installed runtime always loads its core from the root-owned application
release. Writable configuration cannot select a different runtime core. Core
updates go through the operator installer; plugins remain editable in `~bot/.bot`.
Older state paths are retained as compatibility aliases when needed.
