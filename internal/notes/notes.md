# Release notes

What changed for agents in each release, newest first. After an upgrade the
daemon delivers the sections an agent has not seen to its inbox with its
next wake. Write facts, not instructions.

## 0.4.0
- The sh tool takes an optional `wait`: how many seconds to let the command
  run before it is killed. The default is still 120; the maximum is 600.
  The message for a killed command names the wait it had.
- Agents form one tree below root, the human's account. The main agent is
  root's child `main`. Every agent is named by its path from root:
  `main`, `main/agents/NAME`, `main/agents/NAME/agents/NAME`. The harness's
  notes that a subagent did not start name it that way, for example
  "Subagent main/agents/NAME did not start: ...". Journals are under
  /var/log/agent/<that path>/, and ~/journal points there.
- `agent new NAME` creates a subagent: it writes ~/agents/NAME/ with a copy
  of your config.json (or the provider, model and options you pass with
  `--provider`, `--model` and `--option`), waits up to 10 seconds for the
  harness to start it, and prints its home or why it did not start.
  `agent remove NAME` removes it: it stops, with its own subagents, and its
  home is discarded; its journal remains. `agent help` lists the commands, how to share files
  with a subagent, and what to clean up before removing one.
- Each agent owns its config: yours is ~/config.json, and you may change it,
  for example to another model. Your next run starts fresh with it. A
  subagent's config.json moves from ~/agents/NAME/ into its home when it
  starts, so it is the subagent's own from then on; a config.json written
  into ~/agents/NAME/ later is ignored. Moving a config.json away no longer
  pauses a subagent: `agent remove` is the way to stop one.
- An agent's ERROR is in its parent's control directory, never in its own
  home: your subagents' are ~/agents/NAME/ERROR. The main agent's ~/ERROR
  was removed.
- Your conversation log with your owner is ~/journal/messages.jsonl, for
  every agent. The main agent's conversation with the human moved there
  from /var/log/agent/messages.jsonl.
- `agent send NAME "text"` delivers the text exactly as given, without
  adding a newline. It fails at once if ~/agents/NAME/ does not exist.
- No agent has sudo. A sudo grant the main agent had was removed.
- Release notes like these now reach every agent, subagents included, with
  its next run: nothing wakes an agent for them alone. A new agent receives
  only the notes of later releases.
- If you are the main agent: your Unix user was renamed to a pool user,
  agent-N, keeping its uid, files, crontab and user services, and your home
  moved to /home/agent-N. /home/agent is a link to it, so paths under
  /home/agent keep working. To make the move, the upgrade stopped your user
  manager and started it again: enabled user services and timers came back,
  and transient `systemd-run` jobs that were running were stopped and did
  not restart. Subagents' homes and services did not move or stop.

## 0.3.0
- Each subagent now runs as its own Unix user. Its home is at
  ~/agents/NAME/home. You can read it but not write to it. Sibling
  subagents cannot read each other's homes.
- A subagent's control directory ~/agents/NAME/ holds only config.json,
  OWNER.md, ERROR and the home link. Removing or renaming the directory stops
  the subagent and discards its home; its journal remains.
- Message a child with `agent send NAME "text"`. Raw files written into a
  child's inbox no longer work. The child replies with `agent message`, and
  the reply arrives in your inbox as "from": "agents/NAME".
- Subagents can no longer write files in your home. You can grant access
  with ACLs, where USER owns ~/agents/NAME/home
  (`stat -L -c %U ~/agents/NAME/home`). The subagent needs `x` on each
  directory leading to what you share, and `x` on a directory lets it reach
  everything inside that others can read (in ~, your README.md, TASK.md and
  notes too), so share a dedicated directory and `chmod o-rwx` what should
  stay private:
  `mkdir -p ~/share && mkdir -m 700 ~/share/NAME`, then
  `setfacl -m u:USER:x ~ ~/share`, then
  `setfacl -R -m u:USER:rwX ~/share/NAME && setfacl -R -d -m u:USER:rwX,u:$(id -un):rwX ~/share/NAME`
  (the default ACL covers files either of you creates there later).
- Users are reused. Before removing a subagent, delete the files it created
  in your directories (copy first what you want to keep), then remove its
  grants: `setfacl -R -x u:USER,d:u:USER DIR`, and `setfacl -x u:USER` on
  the directories it traversed.
- Cron entries and user timers your subagents created before 0.3.0 are in
  your crontab and user manager: they run as you and use the old paths.
  Make them send with `agent send NAME "text"`, or remove them and have the
  subagent recreate its own.
- Existing subagents were moved to their own users, and their notes and
  README.md were kept.
- /var/log/agent/messages.jsonl is now readable only by you and root.
- /etc/agent/OWNER.md, when your owner writes it, is loaded after the system
  prompt and labelled as your owner's instructions. A subagent's OWNER.md is
  written by its parent.

## 0.2.1
- After each harness upgrade, a note like this one arrives in your inbox
  listing what changed since the version you last heard about. It goes to the
  main agent only, once.

## 0.2.0
- A message that arrives within about four minutes of a run ending continues
  that conversation, with its context and a warm cache; later messages start
  a fresh run. A change to config.json also starts fresh.
- `touch ~/.reset` before stopping makes your next run start fresh regardless.
- ~/journal records each model call's token usage (input, output, cache reads
  and cache writes).
- The system prompt, README.md and TASK.md are cached separately, so editing
  TASK.md no longer invalidates the cached README.

## 0.1.2
- There is no daily heartbeat. You wake on messages and on wake-ups you
  schedule yourself.
- When the harness stops or upgrades, it lets your current run finish (up to
  15 minutes) instead of interrupting it; inbox files that arrive meanwhile
  wait for the next start.
