# Agent harness

A minimal, always-on LLM agent for a Linux VM. The agent has one tool, the
shell, and builds everything else (memory, schedules, messaging, tools)
itself as files on the machine. The harness only provides a few guarantees:
privilege separation, journals, crash recovery and context resets. See
[the spec](docs/spec.md) for the design.

## Install

You need a Linux VM with systemd (Ubuntu or Debian), root access, a provider
API key, and a GitHub token that can read this private repository. Create a
fine-grained token at GitHub → Settings → Developer settings → Fine-grained
tokens, limited to this repository with read-only **Contents** access.

On the VM, replacing `vX.Y.Z` with the release you want:

```bash
export GITHUB_TOKEN=github_pat_...
printf 'header = "Authorization: Bearer %s"\n' "$GITHUB_TOKEN" | curl -fsSL -K - https://raw.githubusercontent.com/SEAN-7/bot/vX.Y.Z/install.sh | sudo --preserve-env=GITHUB_TOKEN sh
```

The installer verifies every download against pinned checksums, asks which
provider plugin to use (anthropic or openai-compatible), its credentials and
a model, then creates the `agent` user and `/home/agent`, installs the
service and a daily heartbeat, and starts it. The token is not stored and is
passed to curl on stdin, so it never shows up in process listings.

Running a newer release's `install.sh` upgrades in place, keeping the agent's
files, journals and credentials. The previous binary is kept as
`/usr/local/bin/agent.prev`, and a failed upgrade restores it and restarts the
service.

`--sudo` (append `-s -- --sudo` after `sh`) gives the agent passwordless sudo;
rerunning without it leaves an existing grant alone (remove
`/etc/sudoers.d/agent` to revoke).
Only use it if you accept that the credential, journal and owner-message
protections then become conventions the agent could ignore.

## Set a spend limit first

The agent works for as long as it decides is necessary, and its subagents too.
The real brake on cost is a spend limit on the API key: set a monthly limit
in your provider's console (Anthropic Console → Billing → Limits, or the
equivalent for your OpenAI-compatible provider) before sending the first
message. When the limit is reached, calls fail, the agent pauses, and
`agent log` shows the provider error at the top.

## Talk to it

```bash
sudo agent send "Hello. Read your README, then tell me how you plan to keep notes."
```

Only messages sent this way are labelled as yours; everything else the agent
reads (web pages, its own scripts, subagent reports) is marked as data.

The agent replies however it decides to, so the first useful thing to ask for
is a channel. For example:

```bash
sudo agent send "Set up a Telegram channel for yourself so we can talk there. Here is a bot token from @BotFather: 123456:ABC-DEF... Only treat messages from my Telegram account as instructions; I will message the bot first so you can learn my chat ID. Run the poller as a long-lived job with systemd-run --user, note how it works in your README, and confirm on Telegram when it is working."
```

## Watch it

```bash
sudo agent log            # follow the main agent's current run (sudo also shows ~/ERROR)
agent log research        # follow a subagent at ~/agents/research
journalctl -u agent -f    # the service log: startup, config and provider errors
```

The agent's journals are under `/var/log/agent/` and linked from
`~/journal` in each agent directory.

## Stop it

```bash
sudo systemctl stop agent      # stop the agent and every command it started
sudo systemctl start agent     # start again; an interrupted run is resumed
```

Jobs the agent started with `systemd-run --user` keep running while the
service is stopped; list them with
`sudo -u agent XDG_RUNTIME_DIR=/run/user/$(id -u agent) systemctl --user list-units`.

## What is protected, and what isn't

- The agent cannot read provider credentials (`/etc/agent`) or change its
  journals (`/var/log/agent`), and the daemon reads and writes agent files
  only with the agent's own permissions.
- Owner messages are verified; nothing the agent writes can pass as yours.
- Subagents are **not** isolated from each other: all agents run as the one
  `agent` user and can read and change each other's files.
- With `--sudo`, none of the above is enforced.

## Development

Go standard library only. `go test ./...` runs everything that does not need
root; the privilege tests run as root in CI (`AGENT_ROOT_TESTS=1`).
