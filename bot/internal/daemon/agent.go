package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/SEAN-7/bot/internal/fsio"
	"github.com/SEAN-7/bot/internal/journal"
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

// errorFile is the agent's ERROR and the helper that reaches it: ERROR is in
// the control directory, which belongs to the parent, so the agent cannot
// hide its errors.
func (a *agent) errorFile() (*fsio.Client, string) {
	return a.parent.fs, filepath.Join(a.ctrl, "ERROR")
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
	if err := a.d.writeNote(a, "interrupted", fmt.Sprintf(MsgInterruptedFmt, path)); err != nil {
		a.logf("interrupted note: %v", err)
		return
	}
	if err := journal.MarkInterrupted(path); err != nil {
		a.logf("%v", err)
	}
	a.logf("resumed after interruption (%s)", path)
}
