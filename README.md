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
a model, then creates the pool users `agent-1` to `agent-<max_agents>`,
installs the `acl` package if `setfacl` is missing, installs the service,
starts it, and creates the main agent with `agent new main`. The main agent
runs as `agent-1`, with its home at `/home/agent-1`. The token is not stored
and is passed to curl on stdin, so it never shows up in process listings.
No agent gets sudo.

Running a newer release's `install.sh` upgrades in place, keeping the
agents' files, journals and credentials. The previous binary is kept as
`/usr/local/bin/agent.prev`, and a failed upgrade restores it and restarts
the service.

An installation older than 0.4.1 must upgrade through the v0.4.1
`install.sh` first; later installers refuse it and change nothing.

To add another provider plugin later, download its release asset
`plugin-NAME-linux-ARCH` (ARCH `amd64` or `arm64`) for the installed version
through the GitHub API, with `GITHUB_TOKEN` set as above and passed on curl's
stdin as the installer does, then install it. `agent provider install`
installs it only if it matches the checksums compiled into `agent`, and
prompts for its settings:

```bash
name=openai-compatible asset=plugin-openai-compatible-linux-amd64 repo=https://api.github.com/repos/SEAN-7/bot
api() { printf 'header = "Authorization: Bearer %s"\n' "$GITHUB_TOKEN" | curl -fsSL -K - "$@"; }
id=$(api "$repo/releases/tags/v$(agent version)" | tr -d '\n' |
  grep -o '"url": *"[^"]*/releases/assets/[0-9]*"[^{}]*"name": *"'"$asset"'"' | grep -o 'assets/[0-9]*' | cut -d/ -f2)
api -H "Accept: application/octet-stream" -o "/tmp/$asset" "$repo/releases/assets/$id"
sudo agent provider install "$name" "/tmp/$asset"
```

## Set a spend limit first

The agent works for as long as it decides is necessary, and its subagents too.
The real brake on cost is a spend limit on the API key: set a monthly limit
in your provider's console (Anthropic Console → Billing → Limits, or the
equivalent for your OpenAI-compatible provider) before sending the first
message. When the limit is reached, calls fail, the agent pauses, and
`sudo agent log main` shows the provider error at the top.

## Talk to it

You are root, the top of the agent tree, and the main agent is your child
`main`. You use the same commands an agent uses for its own children:

```bash
sudo agent send main "Hello. Read your README, then tell me how you plan to keep notes."
```

Only messages sent this way are labelled as yours; everything else the agent
reads (web pages, its own scripts, subagent reports) is marked as data.

The agent writes back with `agent message`. Read the conversation, both
directions, with:

```bash
sudo agent messages main  # prints the conversation so far; run it again for later messages
```

The conversation log (`/var/log/agent/main/messages.jsonl`) is written only
by the harness: the agent can add messages but cannot edit or delete them,
and the labels on who said what can't be forged. Only the agent and root can
read it. It is not a push channel, so you see messages when you look.
Subagents cannot message you; they report to the agent that created them.

For notifications on your phone, ask the agent to build a channel. For
example:

```bash
sudo agent send main "Set up a Telegram channel for yourself so we can talk there. Here is a bot token from @BotFather: 123456:ABC-DEF... Only treat messages from my Telegram account as instructions; I will message the bot first so you can learn my chat ID. Run the poller as a long-lived job with systemd-run --user, note how it works in your README, and confirm on Telegram when it is working."
```

## Give it standing instructions

`/etc/agent/agents/main/OWNER.md` holds your standing instructions for the
main agent: house rules, what to ask you before doing, how you want it to
treat its subagents. Unlike its README, the agent cannot change it, and it
is shown to the model at the start of every fresh run, labelled as yours.
Messages are for tasks; use this file for what should stay true.

```bash
sudo $EDITOR /etc/agent/agents/main/OWNER.md
```

Each edit changes the start of the main agent's prompt, so its prompt cache
is rebuilt once and its next run starts fresh. Upgrades never create, change
or remove the file. The file must be a regular file owned by root and not
writable by anyone else, in a directory likewise; otherwise the agent does
not start and `sudo agent log main` shows an `OWNER.md: ...` error until you
fix it.

That the model follows it is a convention, not a guarantee. Anything that
must hold (credentials, spend, privileges) is enforced by the privilege model
and your provider's limits, not by this file. Subagents do not see it: each
subagent has its own `OWNER.md`, written by its parent, and a parent passes
on what applies.

## Agents and subagents

Every agent runs as its own pool user, `agent-1` to `agent-<max_agents>`
(16 with the default `max_agents`, the main agent included). The limit is in
`/etc/agent/settings.json`. An agent that does not fit is not started, and
its parent is told. `/var/lib/agent/pool.json` records which user each
agent holds: if it is lost while `/var/log/agent` holds journals, the daemon
does not start (see [If pool.json is lost](#if-pooljson-is-lost)).

The main agent creates subagents itself with `agent new` and removes them
with `agent remove`. Each has its own home, its own `config.json` and its own
`OWNER.md`, written by its parent. As root you can watch any of them,
whatever their depth:

```bash
sudo agent log main/agents/research                # a subagent's latest run
sudo agent messages main/agents/research           # its conversation with its parent
sudo agent log main/agents/research/agents/scraper # a subagent of a subagent
```

You can also create agents of your own beside `main`. Each is your child,
isolated from `main` like a sibling, and talks only with you:

```bash
echo '{"provider":"P","model":"M"}' | sudo agent new research -   # or a file: agent new research config.json
                                             # providers and models: sudo agent provider describe
sudo $EDITOR /etc/agent/agents/research/OWNER.md
sudo agent send research "..."
sudo agent remove research                   # stops it and discards its home
```

Removing an agent stops it and its subagents, frees their users and
discards their homes; their journals remain under `/var/log/agent/`. You
can message only your own children; subagents hear from their parents.
`sudo agent remove main` would remove the main agent the same way.

## Watch it

```bash
sudo agent log main                     # the main agent's ERROR, then its latest run
sudo agent log main/agents/research     # the same for the main agent's subagent research
journalctl -u agent -f                  # the service log: startup, config and provider errors
```

`agent log` and `agent messages` print once and exit. To follow a run as it
happens, `tail -F` its newest run file (named by start time, not `messages.jsonl`)
in `/var/log/agent/main/`; the lines are raw JSON.

`agent new` also returns at once. The daemon admits the new agent shortly: its
control directory then holds a `home` link, or an `ERROR` saying why not
(`/etc/agent/agents/NAME/`).

The agents' journals are under `/var/log/agent/<agent>/` and linked from
`~/journal` in each agent's home.

## Stop it

```bash
sudo systemctl stop agent      # stop the agents and every command they started
sudo systemctl start agent     # start again; an interrupted run is resumed
```

Jobs an agent started with `systemd-run --user` keep running while the
service is stopped; list the main agent's with

```bash
u=$(sudo stat -L -c %U /etc/agent/agents/main/home)
sudo -u "$u" XDG_RUNTIME_DIR=/run/user/$(id -u "$u") systemctl --user list-units
```

## If pool.json is lost

`/var/lib/agent/pool.json` is the only record of which pool user each agent
holds. If it is missing while `/var/log/agent` holds journals, the daemon
does not start: every agent would look new and its user free, so a live
agent's home could be cleared or given to another agent. There are two ways
to start again:

- Restore the latest backup of `pool.json`, one taken after the last time an
  agent was admitted or removed. An older one can map a user to an agent
  that has since been removed; the daemon then reclaims that user and wipes
  the home of the agent that now holds it.
- Start with no agents, by moving the journals somewhere only root can read.
  The daemon does not stop what the pool users were running, and a user it
  gives to a new agent would share it, so first stop every pool user's
  processes, crontab and lingering:

  ```bash
  for u in $(getent passwd | cut -d: -f1 | grep -x 'agent-[0-9]*'); do
    sudo loginctl disable-linger "$u"; sudo crontab -r -u "$u"; sudo pkill -KILL -u "$u"
  done
  sudo mkdir -m 700 /root/agent-journals && sudo mv /var/log/agent/* /root/agent-journals/
  ```

  The old control directories stay and are not started.

Then start the service again with `sudo systemctl restart agent`.

## What is protected, and what isn't

- No agent has sudo. No agent can read provider credentials (`/etc/agent`)
  or change its journals or conversation log (`/var/log/agent`), and the
  daemon reads and writes agent files only with the agent's own permissions.
- Owner messages are verified; nothing an agent writes can pass as yours.
- Each agent runs as its own user. It cannot change its parent's files, its
  siblings' files or its own control directory (so not its `OWNER.md` or
  `ERROR`), or read them unless its parent grants access, and its journals
  are written only by the harness. A parent gives a subagent access to a
  file explicitly, with `setfacl`.
- Ancestors can read an agent's home and journals by default (they are
  members of its group) but cannot write there. An agent can restrict its
  own files' modes, but not its journals.
- Group membership is read when a process starts. A process an ancestor
  started while it was in a subagent's group (its user manager, started at
  boot, for example) keeps that group after the subagent is removed and its
  user given to a new subagent, so it can read the new holder's home until
  it restarts (`sudo systemctl restart user@UID.service` for the ancestor's
  uid, or a reboot).
- Users are reused. Grants a parent made with `setfacl`, and files a
  subagent created in its parent's directories, outlive the subagent unless
  the parent removes them first; the seed README tells agents how.

## Development

Go standard library only. `go test ./...` runs everything that does not need
root; the privilege tests run as root in CI (`AGENT_ROOT_TESTS=1`).
