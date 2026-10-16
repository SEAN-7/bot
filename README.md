# Agent harness

A minimal, always-on LLM agent for a Linux VM. The agent has one tool, the
shell, and builds everything else (memory, schedules, messaging, tools)
itself as files on the machine. The harness only provides a few guarantees:
privilege separation, journals, crash recovery and context resets. See
[the spec](bot/docs/spec.md) for the design.

## Install

You need a Linux VM with systemd (Ubuntu or Debian), root and a provider
API key. On the VM, with the release you want for `vX.Y.Z`:

```bash
curl -fsSL https://raw.githubusercontent.com/SEAN-7/bot/vX.Y.Z/bot/install.sh | sudo sh
```

The installer verifies every download against pinned checksums, asks for a
provider plugin (anthropic or openai-compatible), its credentials and a
model, creates the pool users `agent-1` to `agent-<max_agents>`, starts the
service and creates the main agent, `main`, as `agent-1`. No agent gets
sudo.

A newer release's `install.sh` upgrades in place, keeping agents' files,
journals and credentials; a failed upgrade restores the previous binary.
Installations older than 0.4.1 upgrade through the v0.4.1 `install.sh`
first. Releases up to 0.5.2 keep `install.sh` at the repository root
(`.../vX.Y.Z/install.sh`), and releases up to 0.5.5 need a GitHub token, as
their README says.

To add another provider later, download its release asset
`plugin-NAME-linux-ARCH` (`amd64` or `arm64`) and install it; `agent
provider install` checks it against the checksums compiled into `agent` and
prompts for its settings:

```bash
name=openai-compatible asset=plugin-openai-compatible-linux-amd64
curl -fsSL -o "/tmp/$asset" "https://github.com/SEAN-7/bot/releases/download/v$(agent version)/$asset"
sudo agent provider install "$name" "/tmp/$asset"
```

## Set a spend limit first

The agent and its subagents work for as long as they decide to. Set a
monthly spend limit on the API key in your provider's console before the
first message. When it is reached, calls fail, the agent pauses, and
`sudo agent log main` shows the error.

## Talk to it

You are root, the top of the agent tree; `main` is your child. You use the
same commands an agent uses for its children:

```bash
sudo agent send main "Hello. Read your README, then tell me how you plan to keep notes."
sudo agent messages main  # the conversation so far, both directions
```

Only messages you send this way are labelled as yours; everything else the
agent reads is marked as data. The agent replies with `agent message`, into
a log only the harness writes (`/var/log/agent/main/messages.jsonl`). It is
not push: you see replies when you look. For notifications, ask the agent
to build a channel:

```bash
sudo agent send main "Set up a Telegram channel for yourself so we can talk there. Here is a bot token from @BotFather: 123456:ABC-DEF... Only treat messages from my Telegram account as instructions; I will message the bot first so you can learn my chat ID. Run the poller as a long-lived job with systemd-run --user, note how it works in your README, and confirm on Telegram when it is working."
```

## Give it standing instructions

`/etc/agent/agents/main/OWNER.md` holds your standing instructions: house
rules, what to ask you first, how to treat subagents. The agent cannot
change it, and it starts every run, labelled as yours. Messages are for
tasks; this file is for what should stay true.

```bash
sudo $EDITOR /etc/agent/agents/main/OWNER.md
```

An edit rebuilds the agent's prompt cache once. The file must be a regular
file owned by root and writable by no one else, in a directory likewise, or
the agent does not start and `sudo agent log main` says why. Following it
is the model's job; anything that must hold (credentials, spend,
privileges) is enforced by the privilege model and your provider's limits.
Subagents don't see it; each has its own, written by its parent.

## Agents and subagents

Every agent runs as its own pool user; `max_agents` (default 16, main
included) is in `/etc/agent/settings.json`. An agent that does not fit is
not started, and its parent is told.

Agents create subagents with `agent new` and remove them with
`agent remove`. As root you can watch any of them:

```bash
sudo agent log main/agents/research                # a subagent's latest run
sudo agent messages main/agents/research           # its conversation with its parent
```

You can also create your own agents beside `main`, isolated from it:

```bash
echo '{"provider":"P","model":"M"}' | sudo agent new research -   # models: sudo agent provider describe
sudo $EDITOR /etc/agent/agents/research/OWNER.md
sudo agent send research "..."
sudo agent remove research    # stops it and its subagents, discards their homes, keeps their journals
```

`agent new` returns at once; `/etc/agent/agents/NAME/` then gets a `home`
link, or an `ERROR` saying why not.

## Watch it

```bash
sudo agent log main        # the main agent's ERROR, then its latest run
journalctl -u agent -f     # the service log
```

`agent log` and `agent messages` print once. To follow a run, `tail -F` the
newest run file in `/var/log/agent/main/` (raw JSON).

## Stop it

```bash
sudo systemctl stop agent      # stops the agents and everything they started
sudo systemctl start agent     # an interrupted run resumes
```

Jobs an agent started with `systemd-run --user` keep running; list main's
with:

```bash
u=$(sudo stat -L -c %U /etc/agent/agents/main/home)
sudo -u "$u" XDG_RUNTIME_DIR=/run/user/$(id -u "$u") systemctl --user list-units
```

## If pool.json is lost

`/var/lib/agent/pool.json` is the only record of which pool user each agent
holds. If it is missing while `/var/log/agent` holds journals, the daemon
refuses to start, since a live agent's home could be cleared or given to
another agent. Either:

- Restore the latest backup, taken after the last admission or removal. An
  older one can make the daemon wipe the home of an agent that now holds a
  reused user.
- Or start with no agents: stop everything the pool users run, then move
  the journals somewhere only root can read. Old control directories stay
  and are not started.

  ```bash
  for u in $(getent passwd | cut -d: -f1 | grep -x 'agent-[0-9]*'); do
    sudo loginctl disable-linger "$u"; sudo crontab -r -u "$u"; sudo pkill -KILL -u "$u"
  done
  sudo mkdir -m 700 /root/agent-journals && sudo mv /var/log/agent/* /root/agent-journals/
  ```

Then `sudo systemctl restart agent`.

## What is protected, and what isn't

- No agent has sudo, can read provider credentials (`/etc/agent`), or can
  change its journals or conversation log (`/var/log/agent`). The daemon
  touches agent files only with the agent's own permissions.
- Nothing an agent writes can pass as your message.
- An agent cannot change its parent's or siblings' files or its own control
  directory (`OWNER.md`, `ERROR`). A parent shares files with a child
  explicitly, with `setfacl`.
- Ancestors can read an agent's home and journals, not write them. An agent
  can restrict its files' modes, but not its journals.
- A process an ancestor started while in a subagent's group keeps that
  group after the subagent is removed, and can read the next holder's home
  until it restarts (`sudo systemctl restart user@UID.service`, or reboot).
- Users are reused: a parent's `setfacl` grants and files a subagent left
  in its directories outlive the subagent unless the parent removes them
  first, as the agents' manual says.

## Development

Go standard library only. `go test ./...` runs everything that does not need
root; the privilege tests run as root in CI (`AGENT_ROOT_TESTS=1`).
