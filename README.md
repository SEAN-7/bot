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
a model, then creates the `agent` user and `/home/agent`, the subagent users
`agent-1` to `agent-<max_agents-1>`, installs the `acl` package if `setfacl`
is missing, installs the service, and starts it. The token is not stored and is
passed to curl on stdin, so it never shows up in process listings.

Running a newer release's `install.sh` upgrades in place, keeping the agent's
files, journals and credentials. The previous binary is kept as
`/usr/local/bin/agent.prev`, and a failed upgrade restores it and restarts the
service. An upgrade from a release before 0.3.0 first backs up
`/home/agent/agents` to `/var/backups/agent-subagents-<UTC time>.tgz` (root
only), then moves each subagent's files to its own user's home, leaving its
`config.json` in place. If that fails before moving anything, the upgrade
is rolled back; once files have moved, the agent stays stopped on the new
version and rerunning `install.sh` finishes the job. The backup is kept
either way.

`--sudo` (append `-s -- --sudo` after `sh`) gives the agent passwordless sudo;
rerunning without it leaves an existing grant alone (remove
`/etc/sudoers.d/agent` to revoke).
Only the main agent gets sudo; subagents never do. Only use it if you accept
that the credential, journal, owner-message and `OWNER.md` protections then
become conventions the agent could ignore.

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

The agent writes back with `agent message`. Read the conversation, both
directions, with:

```bash
agent messages            # shows the conversation and follows new messages
```

The conversation log (`/var/log/agent/messages.jsonl`) is written only by the
harness: the agent can add messages but cannot edit or delete them, and the
labels on who said what can't be forged. Only the main agent and root can
read it. It is not a push channel, so you see messages when you look.
Subagents cannot message you; they report to the agent that created them.

For notifications on your phone, ask the agent to build a channel. For
example:

```bash
sudo agent send "Set up a Telegram channel for yourself so we can talk there. Here is a bot token from @BotFather: 123456:ABC-DEF... Only treat messages from my Telegram account as instructions; I will message the bot first so you can learn my chat ID. Run the poller as a long-lived job with systemd-run --user, note how it works in your README, and confirm on Telegram when it is working."
```

## Give it standing instructions

`/etc/agent/OWNER.md` holds your standing instructions for the main agent:
house rules, what to ask you before doing, how you want it to treat its
subagents. Unlike its README, the agent cannot change it, and it is shown to
the model at the start of every fresh run, labelled as yours. Messages are
for tasks; use this file for what should stay true.

```bash
sudo $EDITOR /etc/agent/OWNER.md
```

Each edit changes the start of the main agent's prompt, so its prompt cache
is rebuilt once and its next run starts fresh. Upgrades never create, change or remove the file. The file must be a
regular file owned by root and not writable by anyone else, in a directory
likewise; otherwise the agent does not start and `sudo agent log` shows an
`OWNER.md: ...` error until you fix it.

That the model follows it is a convention, not a guarantee. Anything that
must hold (credentials, spend, privileges) is enforced by the privilege model
and your provider's limits, not by this file. Subagents do not see it: each
subagent has its own `OWNER.md`, written by its parent, and a parent passes
on what applies.

## Subagents

The agent creates subagents itself, by writing `~/agents/NAME/config.json`.
Each runs as its own pool user (`agent-1` to `agent-<max_agents-1>`; with the
default `max_agents` of 16, that is 15 subagents). The limit is in
`/etc/agent/settings.json`. A subagent that does not fit is not started, and
its parent is told.

As root you can watch any of them, whatever their depth:

```bash
sudo agent log research                # follow a subagent's run
sudo agent messages research           # its conversation with its parent
sudo agent log research/agents/scraper # a subagent of a subagent
```

Removing or renaming a subagent's directory stops it and frees its user, and
its home is discarded; its journal remains under `/var/log/agent/`. You
cannot send messages to a subagent; only agents message their own children.

## Watch it

```bash
sudo agent log            # follow the main agent's current run (sudo also shows ~/ERROR)
sudo agent log research   # follow a subagent at ~/agents/research
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
- Each subagent runs as its own user. It cannot read or change its parent's
  files, its siblings' files or its own control directory (so not its
  `OWNER.md`), and its journals are written only by the harness. A parent
  gives a subagent access to a file explicitly, with `setfacl`.
- Ancestors can read a subagent's home and journals by default (they are
  members of its group) but cannot write there. A subagent can restrict its
  own files' modes, but not its journals.
- With `--sudo`, none of the above is enforced for the main agent: it can
  read `/etc/agent`, change journals and rewrite `/etc/agent/OWNER.md`.
  Subagents never get sudo, so one cannot use it to reach beyond its own user.

## Development

Go standard library only. `go test ./...` runs everything that does not need
root; the privilege tests run as root in CI (`AGENT_ROOT_TESTS=1`).
