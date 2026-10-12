package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/SEAN-7/bot/internal/fsio"
	"github.com/SEAN-7/bot/internal/journal"
	"github.com/SEAN-7/bot/internal/seed"
)

const errLimitPrefix = "Agent limit reached"

// admit gives the agent at ctrl a pool user and a home, and starts its
// loop. It returns nil when the agent cannot start yet: no user is free,
// its user is still being reclaimed, or a step failed and is retried at the
// next poll. Only the poll goroutine calls it.
func (d *daemon) admit(parent *agent, ctrl string) *agent {
	if d.draining() {
		return nil
	}
	id := d.childID(parent, ctrl)
	dir, err := parent.fs.Stat(ctrl)
	if err != nil {
		d.admitFailed(ctrl, id, "control directory", err)
		return nil
	}
	held := d.pool.userOf(id)
	user, err := d.pool.assign(id, min(len(d.pool.users), d.settings.MaxAgents))
	if err != nil {
		d.admitFailed(ctrl, id, "pool", err)
		return nil
	}
	if user == "" {
		if held == "" {
			d.holdBack(parent, ctrl, id)
		}
		return nil // held: its user is being reclaimed
	}
	c := &agent{d: d, id: id, name: id, home: d.cfg.PoolHome(user), ctrl: ctrl, ctrlID: idOf(dir), user: user, parent: parent, done: make(chan struct{})}
	// A user new to this agent may hold a previous agent's files; a user it
	// already held (a daemon restart) keeps them.
	fresh, cleared := held == "", false
	fail := func(step string, err error) *agent {
		d.admitFailed(ctrl, id, step, err)
		if c.fs != nil {
			c.fs.Close()
		}
		if fresh && !cleared {
			// Retry as a new assignment, so the home is cleared then.
			if err := d.pool.free(user); err != nil {
				d.cfg.Logger.Printf("%s: pool: %v", id, err)
			}
		}
		return nil
	}
	if err := d.cfg.System.PrepareHome(user, c.home); err != nil {
		return fail("prepare home", err)
	}
	cred, err := c.cred()
	if err == nil {
		c.fs, err = fsio.Start(d.cfg.Self, cred)
	}
	if err != nil {
		return fail("file helper", err)
	}
	if fresh && c.ownsHome() {
		// pool.json lost this assignment, but the home's journal link
		// shows it is this agent's own: keep it.
		c.logf("keeping %s: its journal link is this agent's", c.home)
		fresh = false
	}
	if fresh {
		if err := c.fs.Clear(c.home); err != nil {
			return fail("clear home", err)
		}
		cleared = true
	}
	var ancestors []string // nearest first; root needs no group membership
	for p := parent; p != d.root; p = p.parent {
		ancestors = append(ancestors, p.userName())
	}
	if err := d.cfg.System.SetMembers(user, ancestors); err != nil {
		return fail("group members", err)
	}
	if err := c.seed(); err != nil {
		return fail("seed home", err)
	}
	if err := c.adoptConfig(); err != nil {
		return fail("config", err)
	}
	if err := d.cfg.System.EnableLinger(user); err != nil {
		return fail("linger", err)
	}
	if err := d.secureJournal(id, c.gid()); err != nil {
		return fail("journal", err)
	}
	if err := parent.fs.Symlink(c.home, filepath.Join(ctrl, "home")); err != nil {
		return fail("home link", err)
	}
	delete(d.admitErrs, ctrl)
	d.clearLimited(parent, ctrl)
	d.mu.Lock()
	d.start(c)
	d.mu.Unlock()
	c.logf("admitted as %s", user)
	return c
}

// secureJournal creates id's journal directory and gives it to group gid, as
// admission does. The group remembered on start is then out of date, so
// ownsHome reads the group from the directory from here on.
func (d *daemon) secureJournal(id string, gid int) error {
	dir := d.cfg.JournalDir(id)
	if err := journal.Secure(dir, gid); err != nil {
		return err
	}
	delete(d.journalGids, dir)
	return nil
}

// ownsHome reports whether a's home already holds a journal link to a's own
// journal directory, which only a run of this agent creates.
//
// The link lives in a home the previous holder of this user controlled, so
// it is trusted only if the journal directory also carries this user's
// group, which only the daemon sets: the group it had on start, if
// secureUnheldJournals has changed it since and nothing has set it again.
func (a *agent) ownsHome() bool {
	e, err := a.fs.Stat(filepath.Join(a.home, "journal"))
	dir := a.d.cfg.JournalDir(a.id)
	if err != nil || e.Type != "symlink" || e.Target != dir {
		return false
	}
	gid := a.gid()
	if prior, ok := a.d.journalGids[dir]; ok {
		return gid < 0 || prior == gid
	}
	return journalOwnedBy(dir, gid)
}

// journalOwnedBy reports whether dir is a real directory with group gid. It
// runs as root on the root-owned journal tree. Without a credential (gid < 0:
// every agent runs as the one current user) there is no group to check.
func journalOwnedBy(dir string, gid int) bool {
	if gid < 0 {
		return true
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Gid) == gid
}

// repairHomeLink puts back the home link in an agent's control directory
// if its parent removed it, through the parent's helper. A failure is
// logged once and retried at the next poll.
func (d *daemon) repairHomeLink(c *agent) {
	link := filepath.Join(c.ctrl, "home")
	_, err := c.parent.fs.Stat(link)
	if errors.Is(err, fs.ErrNotExist) {
		if err = c.parent.fs.Symlink(c.home, link); err == nil {
			c.logf("home link restored")
		}
	}
	if err == nil {
		c.linkErr = ""
	} else if msg := err.Error(); msg != c.linkErr {
		c.linkErr = msg
		c.logf("home link: %v", err)
	}
}

// seed creates the harness's directories and the seed files the home lacks,
// through the agent's own helper.
func (a *agent) seed() error {
	for _, dir := range []string{"inbox/processed", "agents"} {
		if err := a.fs.Mkdir(filepath.Join(a.home, dir)); err != nil {
			return err
		}
	}
	for _, file := range []struct{ name, text string }{{"README.md", seed.README}, {"TASK.md", seed.TASK}} {
		path := filepath.Join(a.home, file.name)
		_, err := a.fs.Stat(path)
		if errors.Is(err, fs.ErrNotExist) {
			err = a.fs.Write(path, []byte(file.text))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// adoptConfig moves the starting config.json from the control directory into
// the home, which the agent owns from then on: written through the agent's
// helper, read and removed through its parent's. A home that already holds
// one (a daemon restart) keeps it, and a config.json the parent wrote since
// is dropped.
func (c *agent) adoptConfig() error {
	from, to := filepath.Join(c.ctrl, "config.json"), filepath.Join(c.home, "config.json")
	_, err := c.fs.Stat(to)
	if errors.Is(err, fs.ErrNotExist) {
		var data []byte
		var size int64
		if data, size, err = c.parent.fs.Read(from, configLimit); err == nil && size > int64(len(data)) {
			err = fmt.Errorf("%s is larger than %d bytes", from, configLimit)
		}
		if err == nil {
			err = c.fs.Write(to, data)
		}
	}
	if err != nil {
		return err
	}
	return c.parent.fs.Remove(from)
}

// admitFailed logs a failed admission once per distinct error.
func (d *daemon) admitFailed(ctrl, name, step string, err error) {
	msg := step + ": " + err.Error()
	if d.admitErrs[ctrl] != msg {
		d.admitErrs[ctrl] = msg
		d.cfg.Logger.Printf("%s: not started: %s", name, msg)
	}
}

// holdBack reports an agent that max_agents keeps from starting: an ERROR
// in its control directory and one note to its parent, once. Root has no
// inbox: for its children the service log is the report.
func (d *daemon) holdBack(parent *agent, ctrl, name string) {
	if d.limited[ctrl] {
		return
	}
	d.limited[ctrl] = true
	limit := d.settings.MaxAgents
	d.cfg.Logger.Printf("%s: not started: agent limit reached (max_agents=%d)", name, limit)
	if err := parent.fs.Write(filepath.Join(ctrl, "ERROR"), []byte(fmt.Sprintf(MsgLimitFmt, limit)+"\n")); err != nil {
		d.cfg.Logger.Printf("%s: write ERROR: %v", name, err)
	}
	if parent == d.root {
		return
	}
	if err := d.writeNote(parent, "subagent-error", fmt.Sprintf(MsgLimitParentFmt, name, limit)); err != nil {
		d.cfg.Logger.Printf("%s: notify parent: %v", name, err)
	}
}

// clearLimited removes a limit ERROR from an admitted agent's control
// directory, including one left by an earlier daemon.
func (d *daemon) clearLimited(parent *agent, ctrl string) {
	delete(d.limited, ctrl)
	path := filepath.Join(ctrl, "ERROR")
	if data, _, err := parent.fs.Read(path, 4096); err == nil && strings.HasPrefix(string(data), errLimitPrefix) {
		parent.fs.Remove(path)
	}
}
