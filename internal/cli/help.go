package cli

// Usage lists the commands. NAME is a direct child of the caller: an agent's
// ~/agents/NAME, or for root /etc/agent/agents/NAME.
const Usage = `Usage:
  agent new NAME [--provider P] [--model M] [--option k=v]...
                                    create a subagent; waits up to 10 seconds for the
                                    daemon to admit it (see agent new --help)
  agent remove NAME                 remove a subagent and reclaim its home
  agent send NAME "text"|-          send a message to your subagent NAME; - reads stdin
  agent message "text"|-            (agents) send a message to your owner; - reads stdin
  agent messages [NAME]             follow your conversation with your owner, or the one
                                    of subagent NAME (root must name an agent)
  agent log [NAME]                  follow your journal, or subagent NAME's (root must name an agent)
  agent provider install NAME|URL   install a provider plugin (root; URL needs --sha256)
  agent provider describe           show providers, models and options
  agent version
  agent help                        this text and how to share a directory with a subagent
  agent daemon                      run the service (root only)

NAME is a direct child of yours. messages and log also accept a deeper
descendant, such as research/agents/scraper.

If the daemon has not admitted a new subagent within 10 seconds, agent new
says so and exits 0; agent log NAME shows why. Nothing is removed then.
`

// SharingHelp is the reference for what the harness leaves to the agent: a
// subagent starts with no access to its parent's files.
const SharingHelp = `
Sharing a directory with a subagent

A subagent runs as its own Unix user with its own home, ~/agents/NAME/home.
You can read its home but not write to it. It cannot write in your home, or
even reach it, unless you grant access with ACLs. The harness grants nothing
beyond what the ACLs say.

USER below is the user that owns the subagent's home:

  stat -L -c %U ~/agents/NAME/home      (-L because home is a symlink)

The subagent needs x on every directory leading to the shared directory, and
x on a directory lets USER reach everything inside it that others can read
(new files are other-readable by default; in ~ that includes your README.md,
TASK.md and notes). The harness keeps
~/inbox, ~/agents, ~/.messages and ~/.outputs closed to others (0750), so x
on ~ exposes only other-readable files you create yourself. So share a
dedicated directory, and chmod o-rwx whatever on the way should stay private.
For example:

  mkdir -p ~/share && mkdir -m 700 ~/share/NAME
  setfacl -m u:USER:x ~ ~/share
  setfacl -R -m u:USER:rwX ~/share/NAME
  setfacl -R -d -m u:USER:rwX,u:$(id -un):rwX ~/share/NAME

The last command sets a default ACL, so files either of you creates there
later are shared too and stay readable by you.

Cleaning up before agent remove

Users are reused: once a subagent is removed, its user goes to the next one.
Before running agent remove NAME:

  1. Delete the files it created in your directories:
       find ~/share/NAME -user USER
     Copy first what you want to keep; the copy is yours.
  2. Remove its grants:
       setfacl -R -x u:USER,d:u:USER ~/share/NAME
       setfacl -x u:USER ~ ~/share
`

// NewUsage is the text of agent new --help.
const NewUsage = `Usage: agent new NAME [--provider P] [--model M] [--option k=v]...

Creates the subagent NAME. An agent's default is a copy of its own
~/config.json; root must pass --model. --provider or --model starts from no
options, so only --option applies then. --option KEY=VALUE may be given once
per KEY; an integer option takes a number, and an option with a list of
allowed values takes one of them. --provider is needed when more than one is
installed and the config does not name one.

agent new checks the config, creates the subagent's control directory and
waits up to 10 seconds for the daemon to admit it. It then prints the new
home, or fails with the reason the daemon gave. If the daemon has not
admitted it in time, it prints that the daemon has not admitted it yet and
exits 0; agent log NAME shows why. If anything fails after the directory was
created, the directory is removed.
`
