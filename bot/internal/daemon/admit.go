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
	held := d.pool.userOf(id)
	ok, err := d.admission(parent, ctrl, held)
	if err != nil {
		d.admitFailed(ctrl, id, "admission", err)
		return nil
	}
	if !ok {
		return nil // it may still be being written
	}
	keep := held != ""
	limit := min(len(d.pool.users), d.settings.MaxAgents)
	user, err := d.pool.assign(id, limit)
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
	// already held keeps them.
	fresh, cleared := !keep, false
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
	if err := journal.Secure(d.cfg.JournalDir(id), c.gid()); err != nil {
		return fail("journal", err)
	}
	// Never creates the control directory: agent remove may have moved it
	// away since the scan, and the removed agent must not come back, so the
	// link then fails.
	if err := parent.fs.Symlink(c.home, filepath.Join(ctrl, "home")); err != nil {
		return fail("home link", err)
	}
	// The parent's spool for it, so messages need only a file written; a
	// failure leaves the parent to create it.
	spool := filepath.Join(parent.home, ".messages", "agents", filepath.Base(ctrl))
	if skipped, err := parent.fs.Private(spool); err != nil {
		c.logf("spool %s: %v", spool, err)
	} else if len(skipped) > 0 {
		c.logf("not closed to others: %s is a link, a file or another user's", spool)
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
// run, is admitted (false: it is not an agent yet). held is the user it holds
// in pool.json. An error means there is no answer yet (a file or helper
// failed): the caller retries at the next poll. It is admitted when:
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

// harnessDirs are the directories the harness keeps in a home, parents
// first.
var harnessDirs = []string{"inbox", "inbox/processed", "agents", ".messages", ".messages/agents"}

// seed creates the harness's directories and the seed files the home lacks,
// through the agent's own helper. The directories are created open to the
// home's group, the agent's ancestors, and closed to others: a child the
// agent lets through its home never reads its inbox, spools or its
// siblings' control directories. One left alone, as it is not a real
// directory the agent owns, may be open to others: the service log names it
// once per daemon run.
func (a *agent) seed() error {
	dirs := make([]string, len(harnessDirs))
	for i, dir := range harnessDirs {
		dirs[i] = filepath.Join(a.home, dir)
	}
	skipped, err := a.fs.Private(dirs...)
	for _, path := range skipped {
		if !a.d.unclosed[path] {
			if a.d.unclosed == nil {
				a.d.unclosed = map[string]bool{}
			}
			a.d.unclosed[path] = true
			a.logf("not closed to others: %s is a link, a file or another user's", path)
		}
	}
	if err != nil {
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
	d.notStarted(parent, ctrl, name, fmt.Sprintf(MsgLimitFmt, n), fmt.Sprintf(MsgLimitParentFmt, name, n))
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
	parent.fs.Remove(filepath.Join(ctrl, "ERROR"))
}
