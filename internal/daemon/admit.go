package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/SEAN-7/bot/internal/fsio"
	"github.com/SEAN-7/bot/internal/journal"
	"github.com/SEAN-7/bot/internal/seed"
)

const errLimitPrefix = "Agent limit reached"

// admit gives the subagent at ctrl a pool user and a home, and starts its
// loop. It returns nil when the subagent cannot start yet: no user is free,
// its user is still being reclaimed, or a step failed and is retried at the
// next poll. Only the poll goroutine calls it.
func (d *daemon) admit(parent *agent, ctrl string) *agent {
	if d.draining() {
		return nil
	}
	name := filepath.Base(ctrl)
	if parent != d.main {
		name = parent.name + "/agents/" + name
	}
	held := d.pool.userOf(ctrl)
	user, err := d.pool.assign(ctrl, min(len(d.pool.users), d.settings.MaxAgents-1))
	if err != nil {
		d.admitFailed(ctrl, name, "pool", err)
		return nil
	}
	if user == "" {
		if held == "" {
			d.holdBack(parent, ctrl, name)
		}
		return nil // held: its user is being reclaimed
	}
	c := &agent{d: d, id: ctrl, name: name, home: d.cfg.PoolHome(user), ctrl: ctrl, user: user, parent: parent, done: make(chan struct{})}
	// A user new to this agent may hold a previous agent's files; a user it
	// already held (a daemon restart) keeps them.
	fresh, cleared := held == "", false
	fail := func(step string, err error) *agent {
		d.admitFailed(ctrl, name, step, err)
		if c.fs != nil {
			c.fs.Close()
		}
		if fresh && !cleared {
			// Retry as a new assignment, so the home is cleared then.
			if err := d.pool.free(user); err != nil {
				d.cfg.Logger.Printf("%s: pool: %v", name, err)
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
	var ancestors []string // nearest first
	for p := parent; p != nil; p = p.parent {
		ancestors = append(ancestors, p.userName())
	}
	if err := d.cfg.System.SetMembers(user, ancestors); err != nil {
		return fail("group members", err)
	}
	if err := c.seed(); err != nil {
		return fail("seed home", err)
	}
	if err := d.cfg.System.EnableLinger(user); err != nil {
		return fail("linger", err)
	}
	if err := journal.Secure(d.cfg.JournalDir(ctrl), c.gid()); err != nil {
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

// admitFailed logs a failed admission once per distinct error.
func (d *daemon) admitFailed(ctrl, name, step string, err error) {
	msg := step + ": " + err.Error()
	if d.admitErrs[ctrl] != msg {
		d.admitErrs[ctrl] = msg
		d.cfg.Logger.Printf("%s: not started: %s", name, msg)
	}
}

// holdBack reports a subagent that max_agents keeps from starting: an ERROR
// in its control directory and one note to its parent, once.
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
	if err := d.writeNote(parent, "subagent-error", fmt.Sprintf(MsgLimitParentFmt, name, limit)); err != nil {
		d.cfg.Logger.Printf("%s: notify parent: %v", name, err)
	}
}

// clearLimited removes a limit ERROR from an admitted subagent's control
// directory, including one left by an earlier daemon.
func (d *daemon) clearLimited(parent *agent, ctrl string) {
	delete(d.limited, ctrl)
	path := filepath.Join(ctrl, "ERROR")
	if data, _, err := parent.fs.Read(path, 4096); err == nil && strings.HasPrefix(string(data), errLimitPrefix) {
		parent.fs.Remove(path)
	}
}
