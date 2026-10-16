package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/SEAN-7/bot/internal/journal"
	"github.com/SEAN-7/bot/internal/shell"
)

// removedMark prefixes the comment of a user whose agent is being removed.
// It is written before anything is destroyed, so a control directory made
// again under the same id never gets this user back: it waits for the
// deletion and then a fresh user.
const removedMark = "removed:"

// remove stops the agents whose control directories are gone and deletes
// their users, deepest first, so a child goes while its parent's user still
// exists and a parent's helper outlives the children that use it. Only the
// poll goroutine calls it.
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

// reclaim deletes user, agentID's user: it marks the user removed, stops
// everything it runs, removes its temporary files, hands agentID's journals
// to the parent's group and runs userdel, which takes the home and the
// group with it. A failure is logged once and the whole thing is retried
// at the next poll and on the next start, from the mark.
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
		{"mark", func() error { return d.markRemoved(user, agentID) }},
		{"linger", func() error { return sys.DisableLinger(user) }},
		// Before the kill loop, so no cron job starts after it.
		{"crontab", func() error { return sys.RemoveCrontab(user) }},
		{"stop processes", func() error { return sys.StopProcesses(user) }},
		{"temporary files", func() error { return d.sweepTmp(user) }},
		{"journal", func() error {
			gid, err := d.ancestorGid(agentID)
			if err != nil {
				return err
			}
			return journal.SecureTree(d.cfg.JournalDir(agentID), gid)
		}},
		{"userdel", func() error { return sys.DeleteUser(user) }},
	}
	for _, s := range steps {
		if err := s.do(); err != nil {
			return errors.New(s.name + ": " + err.Error())
		}
	}
	d.forgetUser(user)
	return nil
}

// markRemoved puts the removed mark in the user's comment, once.
func (d *daemon) markRemoved(user, agentID string) error {
	for i, u := range d.users {
		if u.Name != user {
			continue
		}
		if strings.HasPrefix(u.ID, removedMark) {
			return nil
		}
		if err := d.cfg.System.SetComment(user, removedMark+agentID); err != nil {
			return err
		}
		d.users[i].ID = removedMark + agentID
		return nil
	}
	return nil // not in the list: userdel decides
}

// forgetUser drops user from the daemon's list once it is deleted.
func (d *daemon) forgetUser(user string) {
	d.users = slices.DeleteFunc(d.users, func(u User) bool { return u.Name == user })
}

// sweepTmp removes the user's entries in the shared temporary directories,
// as the user and after its processes are dead, so no symlink can race it.
// The home goes with userdel.
func (d *daemon) sweepTmp(user string) error {
	if len(d.cfg.TmpDirs) == 0 {
		return nil
	}
	cred, err := d.cfg.LookupUser(user)
	if err != nil {
		return err
	}
	var b strings.Builder
	for _, dir := range d.cfg.TmpDirs {
		fmt.Fprintf(&b, "[ -d %[1]s ] && find %[1]s -mindepth 1 -maxdepth 1 -user \"$(id -un)\" -exec sh -c 'chmod -R u+rwX \"$@\" 2>/dev/null; rm -rf \"$@\"' sh {} +; ", shellQuote(dir))
	}
	home := d.cfg.Home(user)
	result, err := shell.Run(context.Background(), b.String()+"true", shell.Options{
		Dir: "/", Home: home, Env: shell.Env(home, user, credUID(cred)), User: cred, Timeout: 2 * time.Minute,
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("exit status %d: %s", result.ExitCode, strings.TrimSpace(result.Output))
	}
	return nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// parentID is the id of agentID's parent: "" (root) for a top-level agent.
func parentID(agentID string) string {
	i := strings.LastIndex(agentID, "/agents/")
	if i < 0 {
		return ""
	}
	return agentID[:i]
}

// ancestorGid is the group of agentID's nearest ancestor that still has a
// user: its parent's, unless the parent has been removed too, in which case
// its journals already belong to a higher ancestor's group, or root's when
// none has (0 in production, so only root reads them). -1 leaves groups
// alone (no credential: unprivileged tests).
func (d *daemon) ancestorGid(agentID string) (int, error) {
	user := d.cfg.RootUser
	for p := parentID(agentID); p != ""; p = parentID(p) {
		if u := d.userOf(p); u != "" {
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
// exists: removal follows only from that, never from a missing home link,
// which the parent may delete. A directory that is not the one admitted
// (want, when known) was removed and created again, so the admitted one is
// gone. Anything short of certainty keeps the agent: a failing helper or
// user lookup must not delete a live agent.
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

// sweep deletes the users left behind: ones marked removed by a removal that
// failed or a daemon that crashed, and ones whose agent is not running and
// whose control directory has gone, such as one removed while the daemon
// was stopped. Deepest first, like remove. Only the poll goroutine calls it.
func (d *daemon) sweep() {
	users := slices.Clone(d.users)
	sort.SliceStable(users, func(i, j int) bool {
		return strings.Count(users[i].ID, "/") > strings.Count(users[j].ID, "/")
	})
	for _, u := range users {
		if id, ok := strings.CutPrefix(u.ID, removedMark); ok {
			d.reclaim(u.Name, id)
		} else if u.ID != "" && d.orphaned(u.ID) {
			d.reclaim(u.Name, u.ID)
		}
	}
}

// orphaned reports whether the agent id, which holds a user, is not running
// and its control directory is gone. Its parent must be root or running to
// look, through the parent's helper; a parent that holds no user is gone,
// and its control directories with it.
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
		return d.userOf(pid) == ""
	}
	return controlDirGone(parent, filepath.Join(parent.home, "agents", filepath.Base(id)), fileID{})
}
