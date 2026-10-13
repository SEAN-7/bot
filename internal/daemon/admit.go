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

// heldBackPrefixes begin the ERRORs that say why an agent was not admitted
// (see notStarted), which admission clears, including those an earlier
// daemon wrote.
var heldBackPrefixes = []string{"Agent limit reached", "No free agent user", MsgConfigPrefix + MsgConfigTooLarge}

// errConfigTooLarge: a starting config.json over configLimit.
var errConfigTooLarge = errors.New(MsgConfigTooLarge)

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
	usable, keep, err := d.admission(parent, ctrl, id, held)
	if err != nil {
		d.admitFailed(ctrl, id, "admission", err)
		return nil
	}
	if usable == nil {
		return nil // it may still be being written
	}
	if !keep {
		// A new agent's config.json is adopted: one too large to adopt
		// takes no user.
		if e, err := parent.fs.Stat(filepath.Join(ctrl, "config.json")); err == nil && e.Size > configLimit {
			d.configTooLarge(parent, ctrl, id)
			return nil
		}
	}
	limit := min(len(d.pool.users), d.settings.MaxAgents)
	var checkErr error // a free home that could not be checked
	user, err := d.pool.assign(id, limit, func(u string) bool {
		ok, err := usable(u)
		if err != nil {
			checkErr = err
		}
		return ok
	})
	if err != nil {
		d.admitFailed(ctrl, id, "pool", err)
		return nil
	}
	if user == "" {
		if checkErr != nil {
			// Not "no free user": that home may be free to use.
			d.admitFailed(ctrl, id, "admission", checkErr)
		} else if held == "" {
			d.holdBack(parent, ctrl, id, limit)
		}
		return nil // held: its user is being reclaimed
	}
	c := &agent{d: d, id: id, name: id, home: d.cfg.PoolHome(user), ctrl: ctrl, ctrlID: idOf(dir), user: user, parent: parent, done: make(chan struct{})}
	// A user new to this agent may hold a previous agent's files; a user it
	// already held, or recovered by its evidence, keeps them.
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
	if fresh && c.ownsHome() {
		// pool.json lost this assignment, but the home's journal link
		// shows it is this agent's own: keep it.
		c.logf("keeping %s: its journal link is this agent's", c.home)
		fresh = false
	}
	if e, _ := d.pool.entryOf(id); e.Notes == "" {
		if _, ok := parseVersion(d.cfg.Version); ok {
			// A new agent's seed README describes this version; an entry
			// without notes (recovered, or from before they existed) does
			// not know what it saw. Nothing to catch up on, either way.
			if err := d.pool.setNotes(user, d.cfg.Version); err != nil {
				return fail("notes", err)
			}
		}
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
		if errors.Is(err, errConfigTooLarge) {
			d.configTooLarge(parent, ctrl, id)
		}
		return fail("config", err)
	}
	if err := d.cfg.System.EnableLinger(user); err != nil {
		return fail("linger", err)
	}
	if err := d.secureJournal(id, c.gid()); err != nil {
		return fail("journal", err)
	}
	// Closed to others like the parent's other directories: one an earlier
	// release created is closed now. Root's are 0700 and stay so.
	if err := c.private(parent.fs, ctrl); err != nil {
		return fail("control directory", err)
	}
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

// admission decides how the agent id at ctrl, which the daemon does not run,
// is admitted: which free users it may be given (usable, which fails when a
// home cannot be checked) and whether its home is kept. A nil usable means
// ctrl is not an agent yet. An error means there is no answer yet (a file
// or helper failed): the caller retries at the next poll. In order:
//  1. it holds a user in pool.json (a daemon restart): that user, kept;
//  2. its home link names a free user whose home holds this agent's
//     evidence (recovery after a lost pool.json): that user, kept;
//  3. it holds a regular config.json: a fresh user, never one whose home
//     holds another agent's evidence, so a lost pool.json cannot wipe a
//     live home.
func (d *daemon) admission(parent *agent, ctrl, id, held string) (usable func(string) (bool, error), keep bool, err error) {
	own := d.cfg.JournalDir(id)
	if gen := d.pool.generation(); d.homes == nil || gen != d.homesGen {
		d.homes, d.homesGen = map[string]string{}, gen
	}
	if held != "" {
		return func(string) (bool, error) { return false, nil }, true, nil // assign returns the held user
	}
	u, err := d.linkedUser(parent, ctrl)
	if err != nil {
		return nil, false, err
	}
	if u != "" && d.pool.isFree(u) {
		dir, err := d.homeEvidence(u)
		if err != nil {
			return nil, false, err
		}
		if dir == own {
			return func(c string) (bool, error) { return c == u, nil }, true, nil
		}
	}
	e, err := parent.fs.Stat(filepath.Join(ctrl, "config.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, err
	}
	if e.Type != "file" {
		return nil, false, nil
	}
	return func(u string) (bool, error) {
		dir, err := d.homeEvidence(u)
		return err == nil && (dir == "" || dir == own), err
	}, false, nil
}

// linkedUser is the pool user whose home the home link in ctrl names, or "".
func (d *daemon) linkedUser(parent *agent, ctrl string) (string, error) {
	e, err := parent.fs.Stat(filepath.Join(ctrl, "home"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	if e.Type != "symlink" {
		return "", nil
	}
	for _, u := range d.pool.users {
		if d.cfg.PoolHome(u) == e.Target {
			return u, nil
		}
	}
	return "", nil
}

// homeEvidence returns the journal directory that user's home is evidence
// for (see evidence), looking through a helper running as user: the daemon
// never opens a home as root. A result is kept until the pool or a journal
// directory's group changes (see daemon.homes): a free user's home was
// cleared before it was freed, or is a lost pool.json's unrecovered agent's,
// and the daemon writes into neither. An error is not kept. It never locks
// the pool, so assign's usable can call it.
func (d *daemon) homeEvidence(user string) (string, error) {
	if dir, ok := d.homes[user]; ok {
		return dir, nil
	}
	dir, err := d.checkHome(user)
	if err != nil {
		return "", fmt.Errorf("check %s's home: %w", user, err)
	}
	if d.homes != nil {
		d.homes[user] = dir
	}
	return dir, nil
}

func (d *daemon) checkHome(user string) (string, error) {
	cred, err := d.cfg.LookupUser(user)
	if err != nil {
		return "", err
	}
	helper, err := fsio.Start(d.cfg.Self, cred)
	if err != nil {
		return "", err
	}
	defer helper.Close()
	gid := -1
	if cred != nil {
		gid = int(cred.Gid)
	}
	return d.evidence(helper, d.cfg.PoolHome(user), gid)
}

// evidence returns the journal directory home's journal link points at, if
// that directory carries group gid, the home user's: only a run of the agent
// whose journals are there creates the link, and only the daemon sets the
// group, so the home is that agent's. The link lives in a home a previous
// holder controlled, so the group is what counts: the one the directory had
// on start, if secureUnheldJournals has changed it since and nothing has set
// it again. helper runs as the home's user. "" when there is no evidence.
func (d *daemon) evidence(helper *fsio.Client, home string, gid int) (string, error) {
	e, err := helper.Stat(filepath.Join(home, "journal"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	dir := e.Target
	if e.Type != "symlink" || filepath.Clean(dir) != dir || !strings.HasPrefix(dir, filepath.Clean(d.cfg.Log)+"/") {
		return "", nil
	}
	if prior, ok := d.journalGids[dir]; ok {
		if gid < 0 || prior == gid {
			return dir, nil
		}
		return "", nil
	}
	if journalOwnedBy(dir, gid) {
		return dir, nil
	}
	return "", nil
}

// secureJournal creates id's journal directory and gives it to group gid, as
// admission does. The group remembered on start is then out of date, so
// ownsHome reads the group from the directory from here on.
func (d *daemon) secureJournal(id string, gid int) error {
	dir := d.cfg.JournalDir(id)
	// Before Secure, which may change the group and then fail.
	delete(d.journalGids, dir)
	d.homes = nil
	return journal.Secure(dir, gid)
}

// ownsHome reports whether a's home holds evidence (see evidence) that it is
// a's own.
func (a *agent) ownsHome() bool {
	dir, err := a.d.evidence(a.fs, a.home, a.gid())
	return err == nil && dir == a.d.cfg.JournalDir(a.id)
}

// journalOwnedBy reports whether dir is a real directory with group gid. It
// runs as root on the root-owned journal tree. Without a credential (gid < 0:
// every agent runs as the one current user) there is no group to check.
func journalOwnedBy(dir string, gid int) bool {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && (gid < 0 || int(st.Gid) == gid)
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

// harnessDirs are the directories the harness keeps in a home, parents
// first.
var harnessDirs = []string{"inbox", "inbox/processed", "agents", ".messages", ".messages/agents", ".outputs"}

// seed creates the harness's directories and the seed files the home lacks,
// through the agent's own helper. The directories are open to the home's
// group, the agent's ancestors, and closed to others, also when an earlier
// release left them open: a child the agent lets through its home never
// reads its inbox, outputs, spools or its siblings' control directories.
func (a *agent) seed() error {
	dirs := make([]string, len(harnessDirs))
	for i, dir := range harnessDirs {
		dirs[i] = filepath.Join(a.home, dir)
	}
	if err := a.private(a.fs, dirs...); err != nil {
		return err
	}
	for _, file := range []struct{ name, text string }{{"README.md", seed.README}, {"TASK.md", seed.TASK}} {
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

// private closes paths of a's to others through helper (see
// fsio.Client.Private). A path left alone, as it is not a real directory
// helper's user owns, may be open to others: the service log names it once
// per daemon run.
func (a *agent) private(helper *fsio.Client, paths ...string) error {
	skipped, err := helper.Private(paths...)
	for _, path := range skipped {
		if !a.d.unclosed[path] {
			if a.d.unclosed == nil {
				a.d.unclosed = map[string]bool{}
			}
			a.d.unclosed[path] = true
			a.logf("not closed to others: %s is a link, a file or another user's", path)
		}
	}
	return err
}

// adoptConfig moves the starting config.json from the control directory into
// the home, which the agent owns from then on: written through the agent's
// helper, read and removed through its parent's. Only a regular file is a
// starting config. A home that already holds one (a daemon restart) keeps
// it, and a config.json the parent wrote since is renamed config.json.ignored
// in the control directory, replacing an older one, so the parent loses
// nothing. With neither, admission goes on: the first run reports the
// missing config.
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
		ignored := from + ".ignored"
		if err := c.parent.fs.Remove(ignored); err != nil {
			return err
		}
		if err := c.parent.fs.Rename(from, ignored); err != nil {
			return err
		}
		c.logf("kept its own config.json; the one in its control directory is now %s", ignored)
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	data, size, err := c.parent.fs.Read(from, configLimit)
	if err == nil && size > int64(len(data)) {
		err = errConfigTooLarge
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

// holdBack reports an agent no pool user is given to: limit entries
// already exist (max_agents, or the pool users there are), or every free
// user's home holds another agent's evidence.
func (d *daemon) holdBack(parent *agent, ctrl, name string, limit int) {
	if len(d.pool.entries()) >= limit {
		n := d.settings.MaxAgents
		d.notStarted(parent, ctrl, name, fmt.Sprintf(MsgLimitFmt, n), fmt.Sprintf(MsgLimitParentFmt, name, n))
		return
	}
	d.notStarted(parent, ctrl, name, MsgNoFreeUser, fmt.Sprintf(MsgNoFreeUserParentFmt, name))
}

// configTooLarge reports a starting config.json over configLimit.
func (d *daemon) configTooLarge(parent *agent, ctrl, name string) {
	text := MsgConfigPrefix + MsgConfigTooLarge
	d.notStarted(parent, ctrl, name, text, fmt.Sprintf(MsgSubagentFmt, name, text))
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

// clearHeldBack removes from an admitted agent's control directory the
// ERROR that said why it was not admitted, including one an earlier daemon
// wrote. (An oversized ~/config.json's ERROR, which reads the same, is
// written again by the next run that finds it.)
func (d *daemon) clearHeldBack(parent *agent, ctrl string) {
	delete(d.heldBack, ctrl)
	path := filepath.Join(ctrl, "ERROR")
	data, _, err := parent.fs.Read(path, 4096)
	if err != nil {
		return
	}
	for _, prefix := range heldBackPrefixes {
		if strings.HasPrefix(string(data), prefix) {
			parent.fs.Remove(path)
			return
		}
	}
}
