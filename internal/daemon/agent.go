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

// agent is one node of the tree: root, or an agent admitted from a control
// directory in its parent's home (RootHome for root's children).
type agent struct {
	d      *daemon
	id     string       // the path from root (main, main/agents/research), "" for root: keys journals, pool and deliveries
	name   string       // the id, or "root", in the service log
	home   string       // RootHome for root
	ctrl   string       // the real control directory, in the parent's home; "" for root
	ctrlID fileID       // ctrl as admitted: a directory created again there is another agent
	user   string       // a pool user, or cfg.RootUser for root
	parent *agent       // nil for root
	fs     *fsio.Client // runs as user: every access to home goes through it
	cancel context.CancelCauseFunc
	done   chan struct{} // closed when loop returns; root has no loop

	failedConfig string // hash of a config.json already reported invalid
	failedOwner  string // the OWNER.md error already logged
	linkErr      string // the home link error already logged; poll goroutine only

	conv *conversation // the last run's conversation, if it ended normally
}

// fileID identifies a file by device and inode.
type fileID struct{ dev, ino uint64 }

func idOf(e fsio.Entry) fileID { return fileID{e.Dev, e.Ino} }

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

// userName is the agent's user name, the daemon's own for an agent
// configured to run without changing identity.
func (a *agent) userName() string {
	if a.user != "" {
		return a.user
	}
	return a.d.selfUser
}

// ctl is where the agent's config.json and ERROR live, and the helper that
// reaches them: its control directory belongs to its parent.
func (a *agent) ctl() (*fsio.Client, string) { return a.parent.fs, a.ctrl }

// conversation is what a follow-up needs to continue a run: the frozen system
// sections and messages, so the provider's cached prefix stays identical.
type conversation struct {
	cfg      plugin.Config
	owner    string // the OWNER.md content the system sections were built with
	system   []proto.Segment
	messages []proto.Message
	calls    int // call ids used so far
	warned   bool
	ended    time.Time
	journal  string
}

// takeConversation returns the previous conversation if this run may
// continue it: it ended normally within ContinueWindow, the configuration and
// OWNER.md are unchanged, and the agent has not asked for a fresh start with
// ~/.reset.
func (a *agent) takeConversation(cfg plugin.Config, owner string) *conversation {
	c := a.conv
	a.conv = nil
	reset := filepath.Join(a.home, ".reset")
	if _, err := a.fs.Stat(reset); err == nil {
		if err := a.fs.Remove(reset); err != nil {
			a.logf("remove ~/.reset: %v", err)
		}
		return nil
	}
	if c == nil || time.Since(c.ended) > a.d.cfg.ContinueWindow || !reflect.DeepEqual(c.cfg, cfg) || c.owner != owner {
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
