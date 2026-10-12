package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/SEAN-7/bot/internal/layout"
)

// Migrate converts the subagents of earlier releases, which ran as the main
// agent's user in a directory under agents/ holding their whole state, to
// pool users. It runs once, as root, from install.sh with the daemon
// stopped; it can be removed when no installation predates it.
//
// A legacy subagent is a real directory agents/NAME holding a real inbox/
// directory and no home link, found from the main home down, parents before
// children. It and its config.json must belong to the main agent's user. Each gets a pool user, recorded under its id (its path as seen
// from the main agent, so its journals keep their place); everything but
// config.json moves into the pool user's empty home by rename; and what the
// main agent's user owned there passes to the pool user. config.json stays in
// the control directory, which belongs to the parent.
//
// Every check and assignment is made before anything moves. Files move
// first, parents before children, and each inbox/ last, deepest first, so a
// run that fails part way leaves every unfinished subagent (and the top-level
// one above it) with its inbox/: the next run, which install.sh starts while
// any top-level inbox/ remains, finds them and finishes. Nothing is
// overwritten. All paths are opened beneath the homes directory, and every
// component is checked not to be a symbolic link before a rename. Once files
// have changed, by this run or an earlier one, the error is a *PartialError.
func Migrate(cfg Config) (err error) {
	settings, serr := layout.LoadSettings(cfg.SettingsFile())
	if serr != nil {
		cfg.Logger.Printf("settings.json: %v; using max_agents=%d", serr, settings.MaxAgents)
	}
	users := poolUsers(cfg, settings.MaxAgents)
	p, err := loadPool(cfg.PoolFile(), users)
	if err != nil {
		return err
	}
	main, err := filepath.Rel(cfg.Homes, cfg.Home)
	if err != nil || main == "." || strings.HasPrefix(main, "..") {
		return fmt.Errorf("%s is not inside %s", cfg.Home, cfg.Homes)
	}
	root, err := os.OpenRoot(cfg.Homes)
	if err != nil {
		return err
	}
	defer root.Close()
	m := &migrator{cfg: cfg, root: root, fsys: root.FS(), pool: p}
	defer func() {
		if err != nil && m.partial {
			err = &PartialError{Err: err}
		}
	}()
	if m.mainUID, _, err = m.ids(cfg.AgentUser); err != nil {
		return err
	}
	if err := m.plan(filepath.ToSlash(main)); err != nil {
		return err
	}
	if len(m.jobs) == 0 {
		return nil
	}
	if err := m.assign(min(len(users), settings.MaxAgents-1), settings.MaxAgents); err != nil {
		return err
	}
	for _, j := range m.jobs {
		if err := m.moveFiles(j); err != nil {
			return fmt.Errorf("%s: %w", j.name, err)
		}
	}
	for i := len(m.jobs) - 1; i >= 0; i-- {
		j := m.jobs[i]
		if err := m.moveInbox(j); err != nil {
			return fmt.Errorf("%s: %w", j.name, err)
		}
		cfg.Logger.Printf("migrated %s to %s", j.name, j.user)
	}
	return nil
}

// PartialError is Migrate's error when the tree is part way migrated: this
// run moved or chowned files, or an earlier run had. The previous release
// must not run on such a tree; rerunning Migrate finishes it.
type PartialError struct{ Err error }

func (e *PartialError) Error() string { return e.Err.Error() }
func (e *PartialError) Unwrap() error { return e.Err }

// migration is one legacy subagent. Paths are relative to the homes root.
type migration struct {
	id     string // the subagent's id: main home + /agents/NAME...
	name   string // id relative to the main home, for messages
	parent int    // index of the parent's migration, or -1
	src    string // its directory once its parent's files have moved
	user   string
	home   string // the pool user's home
	uid    int
	gid    int
	resume bool // it held its user already: an earlier run was interrupted
}

type migrator struct {
	cfg     Config
	root    *os.Root
	fsys    fs.FS
	pool    *pool
	mainUID int
	jobs    []*migration
	srcs    map[string]bool // every job's src
	partial bool            // files have changed: see PartialError
}

// node is an agent whose agents/ directory is searched: the dirs that may
// hold it now, and home, where it ends up.
type node struct {
	id   string
	dirs []searchDir
	home string
	job  int
}

// searchDir is a directory that may hold an agents/ directory. owner is the
// pool user's uid when it is a pool home, whose user an interrupted run may
// have given the control directories in it; -1 otherwise.
type searchDir struct {
	rel   string
	owner int
}

// plan finds the legacy subagents breadth-first, in the places they are now:
// a legacy subagent's own subagents are in its directory, or in its pool
// home if an interrupted run moved them. Subagents already migrated are
// searched too, for legacy subagents an interrupted run left below them.
func (m *migrator) plan(main string) error {
	if err := m.realDirs(main); err != nil {
		return err
	}
	queue := []node{{id: m.cfg.Home, dirs: []searchDir{{main, -1}}, home: main, job: -1}}
	for i := 0; i < len(queue); i++ {
		n := queue[i]
		found := map[string]string{}
		for _, dir := range n.dirs {
			agents := path.Join(dir.rel, "agents")
			info, err := m.root.Lstat(agents)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			if info.Mode()&fs.ModeSymlink != 0 {
				if i > 0 && n.job < 0 {
					continue // a migrated subagent's own business
				}
				return fmt.Errorf("%s is a symbolic link; not migrating", m.abs(agents))
			}
			if !info.IsDir() {
				continue
			}
			entries, err := fs.ReadDir(m.fsys, agents)
			if err != nil {
				return err
			}
			for _, e := range entries {
				name, ctrl := e.Name(), path.Join(agents, e.Name())
				if e.Type() != fs.ModeDir {
					continue // only real directories are subagents
				}
				if other, ok := found[name]; ok {
					return fmt.Errorf("%s and %s both exist; not migrating", m.abs(other), m.abs(ctrl))
				}
				found[name] = ctrl
				id := n.id + "/agents/" + name
				held := m.pool.userOf(id)
				heldUID := -1
				if held != "" {
					if heldUID, _, err = m.ids(held); err != nil {
						return err
					}
				}
				legacy, err := m.legacy(ctrl)
				if err != nil {
					return err
				}
				if !legacy {
					if held != "" {
						home := m.rel(held)
						queue = append(queue, node{id: id, dirs: []searchDir{{home, heldUID}}, home: home, job: -1})
					}
					continue
				}
				j := &migration{id: id, name: strings.TrimPrefix(id, m.cfg.Home+"/"), parent: n.job}
				if err := m.checkOwned(j, ctrl, dir.owner); err != nil {
					return err
				}
				if dir.owner >= 0 {
					m.partial = true // its parent's files have moved
				}
				if held != "" {
					if err := m.checkStarted(ctrl, m.rel(held)); err != nil {
						return err
					}
				}
				if n.job < 0 {
					j.src = path.Join(n.home, "agents", name)
				}
				m.jobs = append(m.jobs, j)
				child := node{id: id, dirs: []searchDir{{ctrl, -1}}, job: len(m.jobs) - 1}
				if held != "" {
					child.dirs = append(child.dirs, searchDir{m.rel(held), heldUID})
				}
				queue = append(queue, child)
			}
		}
	}
	return nil
}

// checkOwned refuses a legacy control directory, or its config.json, that is
// not a real directory or file of the main agent's user, or of owner, the
// pool user whose home holds it after an interrupted run. The daemon takes a
// control directory its parent's user does not own for gone, and would
// reclaim the subagent's home.
func (m *migrator) checkOwned(j *migration, ctrl string, owner int) error {
	for _, p := range []string{ctrl, path.Join(ctrl, "config.json")} {
		info, err := m.root.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%s: %s is missing; add it or remove %s, then rerun", j.name, m.abs(p), m.abs(path.Join(ctrl, "inbox")))
		}
		if err != nil {
			return err
		}
		if p == ctrl && !info.IsDir() {
			return fmt.Errorf("%s: %s is not a directory; not migrating", j.name, m.abs(p))
		}
		if p != ctrl && !info.Mode().IsRegular() {
			return fmt.Errorf("%s: %s is not a regular file; not migrating", j.name, m.abs(p))
		}
		uid := -2
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			uid = int(st.Uid)
		}
		if uid != m.mainUID && uid != owner {
			return fmt.Errorf("%s: %s is owned by uid %d, not the main agent's user; not migrating", j.name, m.abs(p), uid)
		}
	}
	return nil
}

// checkStarted marks the run partial when an earlier run already changed
// files of the legacy subagent at ctrl, which holds a user: its pool home
// has files, or its inbox/ was given to the pool user.
func (m *migrator) checkStarted(ctrl, home string) error {
	entries, err := fs.ReadDir(m.fsys, home)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if len(entries) > 0 {
		m.partial = true
	}
	info, err := m.root.Lstat(path.Join(ctrl, "inbox"))
	if err != nil {
		return err
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != m.mainUID {
		m.partial = true
	}
	return nil
}

// legacy reports whether the real directory ctrl is a legacy subagent: it
// holds a real inbox/ directory, and no home link from an admission (a
// home that is not a link is the subagent's own file).
func (m *migrator) legacy(ctrl string) (bool, error) {
	info, err := m.root.Lstat(path.Join(ctrl, "inbox"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, nil
	}
	info, err = m.root.Lstat(path.Join(ctrl, "home"))
	if err == nil {
		return info.Mode()&fs.ModeSymlink == 0, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	return true, nil
}

// assign gives every job a user, parents first, and checks its home: the
// pool home must be empty unless an interrupted run already gave the job
// that user. Nothing is assigned when there are too few users.
func (m *migrator) assign(limit, maxAgents int) error {
	need, free := 0, 0
	for _, j := range m.jobs {
		if m.pool.userOf(j.id) == "" {
			need++
		}
	}
	held := map[string]bool{}
	for _, e := range m.pool.entries() {
		held[e.User] = true
	}
	for _, u := range m.pool.users {
		if !held[u] {
			free++
		}
	}
	free = max(0, min(free, limit-len(held)))
	if need > free {
		return fmt.Errorf("%d subagents need a pool user and %d are free (max_agents=%d, %d pool users exist); raise max_agents in %s or create the missing pool users",
			need, free, maxAgents, len(m.pool.users), m.cfg.SettingsFile())
	}
	m.srcs = map[string]bool{}
	for _, j := range m.jobs {
		previous := m.pool.userOf(j.id)
		user, err := m.pool.assign(j.id, limit)
		if err != nil {
			return err
		}
		if user == "" {
			return fmt.Errorf("%s: no pool user available (its user may be being reclaimed)", j.name)
		}
		j.user, j.home, j.resume = user, m.rel(user), previous != ""
		if j.parent >= 0 {
			j.src = path.Join(m.jobs[j.parent].home, "agents", path.Base(j.id))
		}
		m.srcs[j.src] = true
		if err := m.prepare(j); err != nil {
			if !j.resume {
				if ferr := m.pool.free(user); ferr != nil {
					err = errors.Join(err, ferr)
				}
			}
			return fmt.Errorf("%s: %w", j.name, err)
		}
	}
	return nil
}

func (m *migrator) prepare(j *migration) error {
	var err error
	if j.uid, j.gid, err = m.ids(j.user); err != nil {
		return err
	}
	if err := m.cfg.System.PrepareHome(j.user, m.abs(j.home)); err != nil {
		return err
	}
	if err := m.realDirs(j.home); err != nil {
		return err
	}
	if j.resume {
		return nil
	}
	entries, err := fs.ReadDir(m.fsys, j.home)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("pool home %s is not empty", m.abs(j.home))
	}
	return nil
}

// moveFiles moves everything but config.json and inbox/ into the pool home,
// then gives the pool user what the main agent's user owned there. The
// control directories of the job's own legacy subagents, and their
// config.json, pass to the pool user, the parent; the rest of theirs is
// left for their own turn.
func (m *migrator) moveFiles(j *migration) error {
	if err := m.realDirs(j.src); err != nil {
		return err
	}
	entries, err := fs.ReadDir(m.fsys, j.src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == "config.json" || e.Name() == "inbox" {
			continue
		}
		if err := m.move(path.Join(j.src, e.Name()), path.Join(j.home, e.Name())); err != nil {
			return err
		}
	}
	return m.chown(j.home, j)
}

// moveInbox gives the pool user the inbox and then moves it, completing the
// job: a legacy directory loses its inbox/ only once all else is done.
func (m *migrator) moveInbox(j *migration) error {
	if err := m.realDirs(path.Join(j.src, "inbox")); err != nil {
		return err
	}
	if err := m.chown(path.Join(j.src, "inbox"), j); err != nil {
		return err
	}
	return m.move(path.Join(j.src, "inbox"), path.Join(j.home, "inbox"))
}

// move renames from to to, never replacing anything. Its directory has been
// checked; rename itself does not follow a link in its last component.
func (m *migrator) move(from, to string) error {
	if _, err := m.root.Lstat(to); err == nil {
		return fmt.Errorf("%s already exists; not moving %s over it", m.abs(to), m.abs(from))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := m.root.Rename(from, to); err != nil {
		return err
	}
	m.partial = true
	return nil
}

// chown walks dir without following links and gives j's user what the main
// agent's user owns.
func (m *migrator) chown(dir string, j *migration) error {
	return fs.WalkDir(m.fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := m.lchown(p, j); err != nil {
			return err
		}
		if m.srcs[p] {
			// A legacy subagent's control directory: the parent's.
			if err := m.lchown(path.Join(p, "config.json"), j); err != nil {
				return err
			}
			return fs.SkipDir
		}
		return nil
	})
}

func (m *migrator) lchown(p string, j *migration) error {
	info, err := m.root.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != m.mainUID {
		return nil
	}
	if err := m.root.Lchown(p, j.uid, j.gid); err != nil {
		return err
	}
	m.partial = true
	return nil
}

// realDirs fails unless every component of rel is a directory, not a link.
func (m *migrator) realDirs(rel string) error {
	parts := strings.Split(rel, "/")
	for i := range parts {
		p := path.Join(parts[:i+1]...)
		info, err := m.root.Lstat(p)
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link; not migrating", m.abs(p))
		}
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory", m.abs(p))
		}
	}
	return nil
}

// ids resolves a user to its uid and gid; nil credentials mean this process.
func (m *migrator) ids(user string) (int, int, error) {
	cred, err := m.cfg.LookupUser(user)
	if err != nil {
		return 0, 0, err
	}
	if cred == nil {
		return os.Getuid(), os.Getgid(), nil
	}
	return int(cred.Uid), int(cred.Gid), nil
}

// rel is a pool user's home relative to the homes root.
func (m *migrator) rel(user string) string {
	r, _ := filepath.Rel(m.cfg.Homes, m.cfg.PoolHome(user))
	return filepath.ToSlash(r)
}

func (m *migrator) abs(rel string) string { return filepath.Join(m.cfg.Homes, filepath.FromSlash(rel)) }
