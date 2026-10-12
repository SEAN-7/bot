package daemon

import (
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/SEAN-7/bot/internal/fsio"
	"github.com/SEAN-7/bot/internal/journal"
)

// remove stops the agents whose control directories are gone and reclaims
// their users, deepest first, so a child is reclaimed while its parent's
// user still exists and a parent's helper outlives the children that use it.
// Only the poll goroutine calls it.
func (d *daemon) remove(gone []*agent) {
	sort.SliceStable(gone, func(i, j int) bool { return depth(gone[i]) > depth(gone[j]) })
	for _, a := range gone {
		a.cancel(errRemoved)
	}
	for _, a := range gone {
		<-a.done
		d.mu.Lock()
		delete(d.agents, a.id)
		d.mu.Unlock()
		a.fs.Close() // its children have stopped, so nothing uses it
		a.logf("removed")
		d.reclaim(a.user, a.id)
	}
}

func depth(a *agent) int {
	n := 0
	for p := a.parent; p != nil; p = p.parent {
		n++
	}
	return n
}

// reclaim returns user to the pool: it marks the user reclaiming, stops
// everything it runs, wipes its home and temporary files, empties its group
// and hands agentID's journals to the parent's group, and only then frees
// it. A failure leaves the user reclaiming, so it is never assigned, and the
// whole reclaim is retried at the next poll and on the next start. Its group
// is emptied and it is freed only after its descendants' reclaims: their
// groups still list this user, so its next holder must not get it while their
// homes may hold files. What it runs is stopped at once all the same.
func (d *daemon) reclaim(user, agentID string) error {
	err := d.reclaimSteps(user, agentID)
	if err != nil {
		if msg := err.Error(); d.reclaimErrs[user] != msg {
			d.reclaimErrs[user] = msg
			d.cfg.Logger.Printf("%s: reclaim: %v", user, err)
		}
		return err
	}
	delete(d.reclaimErrs, user)
	d.cfg.Logger.Printf("%s: reclaimed", user)
	return nil
}

func (d *daemon) reclaimSteps(user, agentID string) error {
	sys := d.cfg.System
	steps := []struct {
		name string
		do   func() error
	}{
		// Persisted before anything is destroyed, so a crash at any later
		// step is finished on the next start instead of handing out the user.
		{"pool", func() error { return d.pool.mark(user, poolReclaiming) }},
		{"linger", func() error { return sys.DisableLinger(user) }},
		// Before the kill loop, so no cron job starts after it.
		{"crontab", func() error { return sys.RemoveCrontab(user) }},
		{"stop processes", func() error { return sys.StopProcesses(user) }},
		{"wipe files", func() error { return d.wipe(user) }},
		// Its descendants' groups still list this user, so it is not
		// handed on while their homes may hold files; everything it runs
		// is stopped above regardless.
		{"subagents", func() error {
			if d.pool.hasDescendants(agentID) {
				return errors.New("waiting for its subagents' users to be reclaimed")
			}
			return nil
		}},
		{"group members", func() error { return sys.SetMembers(user, nil) }},
		{"journal", func() error {
			gid, err := d.ancestorGid(agentID)
			if err != nil {
				return err
			}
			d.forgetGroups(d.cfg.JournalDir(agentID))
			return journal.SecureTree(d.cfg.JournalDir(agentID), gid)
		}},
		{"pool", func() error { return d.pool.free(user) }},
	}
	for _, s := range steps {
		if err := s.do(); err != nil {
			return errors.New(s.name + ": " + err.Error())
		}
	}
	return nil
}

// wipe empties user's home and removes its files in the shared temporary
// directories, through a fresh helper running as user. Its processes are
// stopped first, so nothing recreates them; /home is root-owned, so the
// home cannot have been swapped for a link.
func (d *daemon) wipe(user string) error {
	cred, err := d.cfg.LookupUser(user)
	if err != nil {
		return err
	}
	helper, err := fsio.Start(d.cfg.Self, cred)
	if err != nil {
		return err
	}
	defer helper.Close()
	if err := helper.Clear(d.cfg.PoolHome(user)); err != nil {
		return err
	}
	return helper.PurgeOwned(d.cfg.TmpDirs)
}

// parentID is the id of agentID's parent: "" (root) for a top-level agent.
func parentID(agentID string) string {
	i := strings.LastIndex(agentID, "/agents/")
	if i < 0 {
		return ""
	}
	return agentID[:i]
}

// ancestorGid is the group of agentID's nearest ancestor that still has a
// user: its parent's, unless the parent has been reclaimed too, in which
// case its journals already belong to a higher ancestor's group, or root's
// when none has (0 in production, so only root reads them). -1 leaves groups
// alone (no credential: unprivileged tests).
func (d *daemon) ancestorGid(agentID string) (int, error) {
	user := d.cfg.RootUser
	for p := parentID(agentID); p != ""; p = parentID(p) {
		if u := d.pool.userOf(p); u != "" {
			user = u
			break
		}
	}
	cred, err := d.cfg.LookupUser(user)
	if err != nil {
		return -1, err
	}
	if cred == nil {
		return -1, nil
	}
	return int(cred.Gid), nil
}

// controlDirGone reports whether parent's control directory ctrl no longer
// exists: removal and its reclaim follow only from that, never from a
// missing or renamed config.json, which an agent may move about. A directory
// that is not the one admitted (want, when known) was removed and created
// again, so the admitted one is gone. Anything short of certainty keeps the
// agent: a failing helper or user lookup must not wipe a live agent.
func controlDirGone(parent *agent, ctrl string, want fileID) bool {
	e, err := parent.fs.Stat(ctrl)
	if err != nil {
		return errors.Is(err, fs.ErrNotExist)
	}
	if e.Type != "dir" {
		return true // replaced by a link or a file
	}
	if want != (fileID{}) && idOf(e) != want {
		return true // another directory now
	}
	uid := parent.uid()
	return uid >= 0 && e.UID != uid // someone else's directory
}

// sweep reclaims the users left behind: entries still reclaiming after a
// failure or a crash, and active entries whose agent is not running and
// whose control directory has gone, such as one removed while the daemon was
// stopped. Deepest first, like remove. Only the poll goroutine calls it.
func (d *daemon) sweep() {
	entries := d.pool.entries()
	sort.SliceStable(entries, func(i, j int) bool {
		return strings.Count(entries[i].Agent, "/") > strings.Count(entries[j].Agent, "/")
	})
	for _, e := range entries {
		if e.State == poolReclaiming || d.orphaned(e.Agent) {
			d.reclaim(e.User, e.Agent)
		}
	}
}

// orphaned reports whether the agent id, which holds an active pool entry,
// is not running and its control directory is gone. Its parent must be root
// or running to look, through the parent's helper; a parent that holds no
// user, or whose user is being reclaimed, is gone, and its control
// directories with it.
func (d *daemon) orphaned(id string) bool {
	pid := parentID(id)
	d.mu.Lock()
	_, running := d.agents[id]
	parent := d.agents[pid]
	d.mu.Unlock()
	if running {
		return false
	}
	if pid == "" {
		parent = d.root
	}
	if parent == nil {
		e, held := d.pool.entryOf(pid)
		return !held || e.State == poolReclaiming
	}
	return controlDirGone(parent, filepath.Join(parent.home, "agents", filepath.Base(id)), fileID{})
}
