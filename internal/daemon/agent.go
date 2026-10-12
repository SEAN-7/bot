package daemon

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"time"

	"github.com/SEAN-7/bot/internal/fsio"
	"github.com/SEAN-7/bot/internal/journal"
	"github.com/SEAN-7/bot/internal/plugin"
	"github.com/SEAN-7/bot/internal/proto"
)

// agent is one node of the tree: the main agent, or a subagent admitted
// from a control directory under its parent's home.
type agent struct {
	d      *daemon
	id     string // main home or control directory: keys journals, pool and deliveries
	name   string // "main", or the path under main's agents/ (research/agents/scraper)
	home   string
	ctrl   string // control directory; "" for main
	user   string // cfg.AgentUser for main, a pool user for subagents
	parent *agent
	fs     *fsio.Client // runs as user: every access to home goes through it
	cancel context.CancelCauseFunc
	done   chan struct{} // closed when loop returns

	failedConfig string // hash of a config.json already reported invalid
	failedOwner  string // an OWNER.md problem already reported

	conv *conversation // the last run's conversation, if it ended normally
}

func newMain(d *daemon) *agent {
	return &agent{d: d, id: d.cfg.Home, name: "main", home: d.cfg.Home, user: d.cfg.AgentUser, done: make(chan struct{})}
}

// cred looks the user up on every call, so a command started after a group
// membership change runs with the new groups.
func (a *agent) cred() (*syscall.Credential, error) { return a.d.cfg.LookupUser(a.user) }

// uid is the agent's uid: its user's, or the daemon's own when it runs
// without changing identity. -1 when the user cannot be looked up.
func (a *agent) uid() int {
	cred, err := a.cred()
	if err != nil {
		return -1
	}
	return credUID(cred)
}

// gid is the agent's group, or -1 (leave groups alone) without a credential.
func (a *agent) gid() int {
	cred, err := a.cred()
	if err != nil || cred == nil {
		return -1
	}
	return int(cred.Gid)
}

func credUID(cred *syscall.Credential) int {
	if cred == nil {
		return os.Getuid()
	}
	return int(cred.Uid)
}

// userName is the agent's user name, the daemon's own for a main agent
// configured to run without changing identity.
func (a *agent) userName() string {
	if a.user != "" {
		return a.user
	}
	return a.d.selfUser
}

// ctl is where the agent's config.json and ERROR live, and the helper that
// reaches them: a subagent's control directory belongs to its parent.
func (a *agent) ctl() (*fsio.Client, string) {
	if a.parent != nil {
		return a.parent.fs, a.ctrl
	}
	return a.fs, a.home
}

// conversation is what a follow-up needs to continue a run: the frozen system
// sections and messages, so the provider's cached prefix stays identical.
type conversation struct {
	cfg      plugin.Config
	system   []proto.Segment
	messages []proto.Message
	calls    int // call ids used so far
	warned   bool
	ended    time.Time
	journal  string
}

// takeConversation returns the previous conversation if this run may
// continue it: it ended normally within ContinueWindow, the configuration is
// unchanged, and the agent has not asked for a fresh start with ~/.reset.
func (a *agent) takeConversation(cfg plugin.Config) *conversation {
	c := a.conv
	a.conv = nil
	reset := filepath.Join(a.home, ".reset")
	if _, err := a.fs.Stat(reset); err == nil {
		if err := a.fs.Remove(reset); err != nil {
			a.logf("remove ~/.reset: %v", err)
		}
		return nil
	}
	if c == nil || time.Since(c.ended) > a.d.cfg.ContinueWindow || !reflect.DeepEqual(c.cfg, cfg) {
		return nil
	}
	return c
}

// loop wakes whenever the inbox holds a deliverable file, one run at a time.
func (a *agent) loop(ctx context.Context) {
	a.recover()
	ticker := time.NewTicker(a.d.cfg.Poll)
	defer ticker.Stop()
	for {
		if a.d.draining() {
			return
		}
		if names, err := a.pending(); err == nil && len(names) > 0 {
			a.run(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-a.d.stop.Done():
			return
		case <-ticker.C:
		}
	}
}

// recover tells an agent whose newest run never ended (daemon crash or stop,
// or held back by max_agents) that it was interrupted, then marks that run
// ended so the note is sent once.
func (a *agent) recover() {
	path, clean, err := journal.Latest(a.d.cfg.JournalDir(a.id))
	if err != nil || path == "" || clean {
		return
	}
	if err := a.d.writeNote(a, "interrupted", MsgInterrupted); err != nil {
		a.logf("interrupted note: %v", err)
		return
	}
	if err := journal.MarkInterrupted(path); err != nil {
		a.logf("%v", err)
	}
	a.logf("resumed after interruption (%s)", path)
}
