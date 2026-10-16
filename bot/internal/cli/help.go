package cli

// Usage lists the commands. NAME is a direct child of the caller: an agent's
// ~/agents/NAME, or for root /etc/agent/agents/NAME.
const Usage = `Messages, subagents, journals and provider details are files, described in
/var/lib/agent/manual/manual.md. The commands marked * do the same file
operations; they still work in this release and are removed in 0.8.0.

Usage:
* agent new NAME [CONFIG|-]         create a subagent with the config.json in the file CONFIG,
                                    or on stdin for -; without CONFIG, a copy of your own
                                    ~/config.json (root must pass CONFIG)
* agent remove NAME                 remove a subagent and reclaim its home
* agent send NAME "text"|-          send a message to your subagent NAME; - reads stdin
* agent message "text"|-            (agents) send a message to your owner; - reads stdin
* agent messages [NAME]             print your conversation with your owner, or the one
                                    of subagent NAME (root must name an agent)
* agent log [NAME]                  print your journal, or subagent NAME's (root must name an agent)
  agent provider install NAME FILE  install provider plugin NAME from the downloaded FILE,
                                    verified against this release's checksums (root)
* agent provider describe           print providers, models and options as JSON
* agent version
  agent help                        this text
  agent daemon                      run the service (root only)

NAME is a direct child of yours. messages and log also accept a deeper
descendant, such as research/agents/scraper.

A config.json is {"provider": "P", "model": "M", "options": {...}}, checked
against agent provider describe before anything is created. agent new prints
the control directory it created and exits; the daemon admits the subagent
shortly. Its home appears at agents/NAME/home, and a reason it was not started
in agents/NAME/ERROR.
`
