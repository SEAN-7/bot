package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/SEAN-7/bot/internal/fsio"
	"github.com/SEAN-7/bot/internal/journal"
	"github.com/SEAN-7/bot/internal/seed"
)

// admit gives the agent at ctrl a pool user and a home, and starts its
// loop. It returns nil when ctrl is not an agent (yet), or the agent cannot
// start yet: no user is free, its user is still being reclaimed, or a step
// failed and is retried at the next poll. Only the poll goroutine calls it.
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
	user := d.userOf(id)
	ok, err := d.admission(parent, ctrl, user)
	if err != nil {
		d.admitFailed(ctrl, id, "admission", err)
		return nil
	}
	if !ok {
		return nil // it may still be being written
	}
	fresh := user == ""
	if fresh {
		if d.agentCount() >= d.settings.MaxAgents {
			d.holdBack(parent, ctrl, id)
			return nil
		}
		// A user of its own, named for the lowest free number. A name freed
		// by userdel comes back, but as a new user with an empty home.
		user = d.newUserName()
		if err := d.cfg.System.AddUser(user, id, d.cfg.Home(user)); err != nil {
			d.admitFailed(ctrl, id, "user", err)
			return nil
		}
		d.users = append(d.users, User{user, id})
	}
	c := &agent{d: d, id: id, name: id, home: d.cfg.Home(user), inbox: d.cfg.Inbox(user), ctrl: ctrl, ctrlID: idOf(dir), user: user, parent: parent, done: make(chan struct{})}
	fail := func(step string, err error) *agent {
		d.admitFailed(ctrl, id, step, err)
		if c.fs != nil {
			c.fs.Close()
		}
		return nil
	}
	if err := d.cfg.System.PrepareHome(user, c.home); err != nil {
		return fail("prepare home", err)
	}
	if err := d.cfg.System.PrepareInbox(user, c.inbox); err != nil {
		return fail("prepare inbox", err)
	}
	cred, err := c.cred()
	if err == nil {
		c.fs, err = fsio.Start(d.cfg.Self, cred)
	}
	if err != nil {
		return fail("file helper", err)
	}
	if fresh {
		// A home directory left under this name (a userdel that could not
		// remove it) must not reach the new user.
		if err := d.clearHome(user); err != nil {
			return fail("clear home", err)
		}
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
	// Its messages are files it writes into its owner's inbox: the owner,
	// who owns the inbox, grants it write access. Root has no inbox.
	if parent != d.root {
		if err := d.cfg.System.ShareInbox(parent.user, parent.inbox, c.uid(), true); err != nil {
			return fail("share inbox", err)
		}
	}
	if err := c.adoptConfig(); err != nil {
		return fail("config", err)
	}
	if err := d.cfg.System.EnableLinger(user); err != nil {
		return fail("linger", err)
	}
	if err := journal.Secure(d.cfg.JournalDir(id), c.gid()); err != nil {
		return fail("journal", err)
	}
	// Never creates the control directory: agent remove may have moved it
	// away since the scan, and the removed agent must not come back, so the
	// link then fails.
	if err := parent.fs.Symlink(c.home, filepath.Join(ctrl, "home")); err != nil {
		return fail("home link", err)
	}
	delete(d.admitErrs, ctrl)
	d.clearHeldBack(parent, ctrl)
	d.mu.Lock()
	d.start(c)
	d.mu.Unlock()
	c.logf("admitted as %s", user)
	return c
}

// admission decides whether the agent at ctrl, which the daemon does not
// run, is admitted (false: it is not an agent yet). held is the user it
// holds. An error means there is no answer yet (a file or helper failed):
// the caller retries at the next poll. It is admitted when:
//  1. it holds a user (a daemon restart): that user, keeping the home;
//  2. it holds a regular config.json: a fresh user.
func (d *daemon) admission(parent *agent, ctrl, held string) (bool, error) {
	if held != "" {
		return true, nil
	}
	e, err := parent.fs.Stat(filepath.Join(ctrl, "config.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return e.Type == "file", nil
}

// seed creates the harness's directories and the seed files the agent
// lacks, through the agent's own helper: inbox/processed in the inbox
// (closed to others, so a subagent with write access to the inbox never
// reads what was collected) and agents/ in the home (closed to others, so
// a child the agent lets through its home never reads its siblings'
// control directories). A directory that exists is left as it is; anything
// else of that name is an error, and the agent does not start. An inbox
// from a release before 0.9.0, a real directory in the home, is moved
// aside once, its pending files into the new inbox.
func (a *agent) seed() error {
	if err := a.fs.Mkdir(filepath.Join(a.inbox, "processed"), 0o750); err != nil {
		return err
	}
	if err := a.fs.Mkdir(filepath.Join(a.home, "agents"), 0o750); err != nil {
		return err
	}
	if err := a.moveLegacyInbox(); err != nil {
		return err
	}
	if err := a.fs.Symlink(a.inbox, filepath.Join(a.home, "inbox")); err != nil {
		return err
	}
	for _, file := range []struct{ name, text string }{{"README.md", seed.README}} {
		path := filepath.Join(a.home, file.name)
		_, err := a.fs.Stat(path)
		if errors.Is(err, fs.ErrNotExist) {
			err = a.fs.Write(path, []byte(file.text), 0o644)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// moveLegacyInbox handles a ~/inbox that is a real directory, left by a
// release before 0.9.0: it is renamed to ~/inbox.0.8.0, keeping its
// processed/ history, and its regular files, the pending ones, are moved
// into the inbox, which shares the filesystem. Nothing happens otherwise.
func (a *agent) moveLegacyInbox() error {
	old := filepath.Join(a.home, "inbox")
	e, err := a.fs.Stat(old)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && e.Type != "dir") {
		return nil
	} else if err != nil {
		return err
	}
	kept := old + ".0.8.0"
	if err := a.fs.Rename(old, kept); err != nil {
		return err
	}
	entries, err := a.fs.List(kept)
	if err != nil {
		return err
	}
	for _, f := range entries {
		if f.Type != "file" {
			continue
		}
		if err := a.fs.Rename(filepath.Join(kept, f.Name), filepath.Join(a.inbox, f.Name)); err != nil {
			return err
		}
	}
	a.logf("moved the pre-0.9.0 inbox aside as %s", kept)
	return nil
}

// adoptConfig moves the starting config.json from the control directory into
// the home, which the agent owns from then on: written through the agent's
// helper, read and removed through its parent's. Only a regular file is a
// starting config. A home that already holds one (a daemon restart) keeps
// it, and a config.json the parent wrote since stays where it is, unread.
// With neither, admission goes on: the first run reports the missing config.
func (c *agent) adoptConfig() error {
	from, to := filepath.Join(c.ctrl, "config.json"), filepath.Join(c.home, "config.json")
	e, err := c.parent.fs.Stat(from)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if e.Type != "file" {
		return nil
	}
	_, err = c.fs.Stat(to)
	if err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	data, size, err := c.parent.fs.Read(from, configLimit)
	if err == nil && size > int64(len(data)) {
		err = errors.New("config.json is larger than 1 MiB; shrink it in place")
	}
	if err == nil {
		err = c.fs.Write(to, data, 0o644)
	}
	if err != nil {
		return err
	}
	if err := c.parent.fs.Unlink(from); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// admitFailed logs a failed admission once per distinct error.
func (d *daemon) admitFailed(ctrl, name, step string, err error) {
	msg := step + ": " + err.Error()
	if d.admitErrs[ctrl] != msg {
		d.admitErrs[ctrl] = msg
		d.cfg.Logger.Printf("%s: not started: %s", name, msg)
	}
}

// holdBack reports an agent no pool user is given to: as many entries as
// max_agents, or as the pool users there are, already exist.
func (d *daemon) holdBack(parent *agent, ctrl, name string) {
	n := d.settings.MaxAgents
	d.notStarted(parent, ctrl, name, fmt.Sprintf(MsgLimitFmt, n), fmt.Sprintf(MsgLimitParentFmt, filepath.Base(ctrl), n))
}

// notStarted reports an agent that is not admitted for a reason its parent
// can act on: text in the ERROR of its control directory and the service
// log, and note in its parent's inbox, once while the reason stays the
// same. Root has no inbox: for its children the service log is the report.
func (d *daemon) notStarted(parent *agent, ctrl, name, text, note string) {
	if d.heldBack[ctrl] == text {
		return
	}
	d.heldBack[ctrl] = text
	d.cfg.Logger.Printf("%s: not started: %s", name, text)
	if err := parent.fs.Write(filepath.Join(ctrl, "ERROR"), []byte(text+"\n"), 0o644); err != nil {
		d.cfg.Logger.Printf("%s: write ERROR: %v", name, err)
	}
	if parent == d.root {
		return
	}
	if err := d.writeNote(parent, "subagent-error", note); err != nil {
		d.cfg.Logger.Printf("%s: notify parent: %v", name, err)
	}
}

// clearHeldBack removes an admitted agent's ERROR, which said why it was not
// admitted; the agent's first run writes it again if something is still
// wrong.
func (d *daemon) clearHeldBack(parent *agent, ctrl string) {
	delete(d.heldBack, ctrl)
	parent.fs.Unlink(filepath.Join(ctrl, "ERROR"))
}
