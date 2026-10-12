package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/SEAN-7/bot/internal/fsio"
	"github.com/SEAN-7/bot/internal/journal"
	"github.com/SEAN-7/bot/internal/layout"
)

// Migrate turns a 0.3 installation into the agent tree. The main agent ran
// as Config.LegacyUser (agent) with its home at /home/agent; it becomes root's
// child main on the first pool user that does not exist yet (agent-16 by
// default), renamed in place, so its uid, files, crontab and user units are
// kept, and /home/agent stays as a link to its new home. Its subagents'
// ids, journals, delivery records and the owner's spool move to the tree's
// places. It runs once, as root, from install.sh with the daemon stopped,
// and can be removed when no installation predates 0.4.0.
//
// Every check is made before the first change, and every step skips what an
// earlier, interrupted run did, so a rerun finishes the migration with the
// user recorded in MigratingUserFile. A failure before the user is renamed
// leaves the installation as the previous release expects it, the user
// lingering again; from then on, or whenever an earlier run may have renamed
// it, the error is a *PartialError. The daemon never writes inside a home as
// root, and neither does this: changes there go through a helper running as
// the home's user.
func Migrate(cfg Config) (err error) {
	m := &migrator{cfg: cfg, oldMain: filepath.Join(cfg.Homes, cfg.LegacyUser), helpers: map[string]*fsio.Client{}}
	defer m.closeHelpers()
	defer func() {
		if err != nil && m.partial {
			err = &PartialError{Err: err}
		}
	}()
	// Runs before the wrapping above: a failure before the rename leaves the
	// previous release to run, with its services. Lingering alone does not
	// restart a manager that was stopped while the user already lingered.
	// Best effort: the error stays the plain one.
	defer func() {
		if err != nil && !m.partial && m.stopping {
			if lerr := cfg.System.EnableLinger(cfg.LegacyUser); lerr != nil {
				cfg.Logger.Printf("re-enabling lingering for %s: %v", cfg.LegacyUser, lerr)
			}
			if serr := cfg.System.StartUserManager(cfg.LegacyUser); serr != nil {
				cfg.Logger.Printf("restarting the user manager of %s: %v", cfg.LegacyUser, serr)
			}
		}
	}()
	legacy, lerr := m.resolves(cfg.LegacyUser)
	target, rerr := m.recordedTarget()
	if lerr != nil || rerr != nil {
		// A record, or one that cannot be read, means an earlier run may
		// have renamed the user, unless it is certainly still there.
		m.partial = (rerr != nil || target != "") && (lerr != nil || !legacy)
		return errors.Join(lerr, rerr)
	}
	m.target = target
	if !legacy && m.target == "" {
		return m.checkNothingLeft()
	}
	if legacy && m.target != "" {
		// Nothing is renamed yet, so the record binds nothing; a user
		// created since under its name only means choosing again.
		if taken, err := m.resolves(m.target); err != nil {
			return err
		} else if taken {
			cfg.Logger.Printf("%s exists now; choosing another user for %s", m.target, cfg.LegacyUser)
			m.target = ""
		}
	}
	// Once the user is renamed, an earlier run has changed things.
	m.partial = !legacy
	recorded := m.target != ""
	if !recorded {
		if m.target, err = m.firstFreeUser(); err != nil {
			return err
		}
	}
	if err := m.check(legacy); err != nil {
		return err
	}
	if !recorded {
		if err := m.change("record target"); err != nil {
			return err
		}
		if err := replaceFile(cfg.MigratingUserFile(), []byte(m.target+"\n"), 0o600); err != nil {
			return err
		}
	}
	if legacy {
		cfg.Logger.Printf("migrating %s to %s", cfg.LegacyUser, m.target)
		if err := m.stopLegacyUser(); err != nil {
			return err
		}
	}
	m.partial = true
	m.legacyResolves = legacy
	for _, step := range []func() error{m.renameUser, m.rootControlDir, m.rewritePool, m.moveJournals, m.rewriteDelivered, m.moveOwnerSpool, m.removeSudoers} {
		if err := step(); err != nil {
			return err
		}
	}
	if err := m.change("remove target record"); err != nil {
		return err
	}
	if err := os.Remove(cfg.MigratingUserFile()); err != nil {
		return err
	}
	cfg.Logger.Printf("migrated %s to %s", cfg.LegacyUser, m.target)
	return nil
}

// PartialError is Migrate's error once the installation is part way
// migrated, by this run or an earlier one. The previous release must not run
// on it; rerunning Migrate finishes it.
type PartialError struct{ Err error }

func (e *PartialError) Error() string { return e.Err.Error() }
func (e *PartialError) Unwrap() error { return e.Err }

// migrateHook, when set, runs before each change Migrate makes, named; an
// error stops the migration there. Tests interrupt it at every change.
var migrateHook func(change string) error

type migrator struct {
	cfg     Config
	target  string // the pool user the legacy user becomes
	oldMain string // the main agent's id and home before: /home/agent
	partial bool   // see PartialError
	// stopping: this run began stopping the legacy user, which must linger
	// again if the run fails before renaming it.
	stopping bool
	// legacyResolves: the legacy user existed when this run renamed it.
	legacyResolves bool
	// homeMoved and homeLinked: the legacy home is at the target's home
	// already, and its old path links there.
	homeMoved, homeLinked bool
	helpers               map[string]*fsio.Client // by user
}

func (m *migrator) change(name string) error {
	if migrateHook != nil {
		return migrateHook(name)
	}
	return nil
}

// resolves reports whether name is a user. Only a definite "no such user"
// is false: taking a failing lookup for a missing legacy user would skip
// the migration and start the daemon on a 0.3 installation.
func (m *migrator) resolves(name string) (bool, error) {
	_, err := m.cfg.LookupUser(name)
	var unknown user.UnknownUserError
	if errors.As(err, &unknown) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("looking up %s: %w", name, err)
	}
	return true, nil
}

// recordedTarget is the target an interrupted run recorded, or "".
func (m *migrator) recordedTarget() (string, error) {
	data, err := os.ReadFile(m.cfg.MigratingUserFile())
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(data))
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/ \t\n") {
		return "", fmt.Errorf("%s does not name a user: %q", m.cfg.MigratingUserFile(), data)
	}
	return name, nil
}

// checkNothingLeft makes sure an installation with no legacy user to migrate
// has no 0.3 ids in pool.json either: the daemon would take their agents
// for removed and wipe their homes.
func (m *migrator) checkNothingLeft() error {
	p, err := loadPool(m.cfg.PoolFile(), nil)
	if err != nil {
		return err
	}
	for _, e := range p.entries() {
		if filepath.IsAbs(e.Agent) {
			return fmt.Errorf("%s holds the 0.3 agent %s, but user %s does not exist to migrate; restore the user or the previous release", m.cfg.PoolFile(), e.Agent, m.cfg.LegacyUser)
		}
	}
	return nil
}

// firstFreeUser is the first pool user that does not exist yet.
func (m *migrator) firstFreeUser() (string, error) {
	users := m.cfg.PoolUsers
	if users == nil {
		settings, err := layout.LoadSettings(m.cfg.SettingsFile())
		if err != nil {
			return "", fmt.Errorf("settings.json: %w", err)
		}
		users = layout.PoolUsers(settings.MaxAgents)
	}
	for _, u := range users {
		exists, err := m.resolves(u)
		if err != nil {
			return "", err
		}
		if !exists {
			return u, nil
		}
	}
	return "", fmt.Errorf("every pool user exists, so none is left for the main agent; raise max_agents in %s", m.cfg.SettingsFile())
}

// check finds, before anything changes, whatever would stop a step that is
// not done yet, given what an earlier run did.
func (m *migrator) check(legacy bool) error {
	cfg := m.cfg
	exists, err := m.resolves(m.target)
	if err != nil {
		return err
	}
	if legacy && exists {
		return fmt.Errorf("users %s and %s both exist", cfg.LegacyUser, m.target)
	}
	if legacy {
		lookupGroup := cfg.LookupGroup
		if lookupGroup == nil {
			lookupGroup = groupExists
		}
		taken, err := lookupGroup(m.target)
		if err != nil {
			return fmt.Errorf("looking up group %s: %w", m.target, err)
		}
		if taken {
			return fmt.Errorf("group %s exists, so user %s cannot be renamed to it", m.target, cfg.LegacyUser)
		}
	}
	if !legacy && !exists {
		return fmt.Errorf("neither user %s nor %s exists", cfg.LegacyUser, m.target)
	}
	if err := m.checkHome(legacy); err != nil {
		return err
	}
	for _, dir := range []string{filepath.Join(cfg.RootHome(), "agents"), m.mainCtrl()} {
		if err := realDirOrNothing(dir); err != nil {
			return err
		}
	}
	if err := checkMove(filepath.Join(cfg.Etc, "OWNER.md"), filepath.Join(m.mainCtrl(), "OWNER.md")); err != nil {
		return err
	}
	if _, err := m.loadPool(); err != nil {
		return err
	}
	oldLog, mainLog := m.oldLog(), cfg.JournalDir("main")
	if err := realDirOrNothing(oldLog); err != nil {
		return err
	}
	if err := checkMove(oldLog, mainLog); err != nil {
		return err
	}
	if exists, _ := present(oldLog); exists {
		mainLog = oldLog // where main's journals are until they move
	}
	if err := checkMove(filepath.Join(cfg.Log, "messages.jsonl"), filepath.Join(mainLog, "messages.jsonl")); err != nil {
		return err
	}
	spooled, err := os.ReadDir(m.oldSpool())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, e := range spooled {
		if exists, err := present(filepath.Join(m.mainSpool(), e.Name())); err != nil || exists {
			return errors.Join(err, fmt.Errorf("%s exists; not moving %s over it", filepath.Join(m.mainSpool(), e.Name()), filepath.Join(m.oldSpool(), e.Name())))
		}
	}
	return nil
}

// checkHome finds where the legacy home is: still at its old path, a real
// directory on the same filesystem as the target's, which does not exist;
// or moved, with its old path missing or already linking to it.
func (m *migrator) checkHome(legacy bool) error {
	old, home := m.oldMain, m.cfg.PoolHome(m.target)
	info, err := os.Lstat(old)
	switch {
	case err == nil && info.IsDir():
		if exists, err := present(home); err != nil || exists {
			return errors.Join(err, fmt.Errorf("%s exists; not moving %s over it", home, old))
		}
		homes, err := os.Stat(m.cfg.Homes)
		if err != nil {
			return err
		}
		if dev(info) != dev(homes) {
			return fmt.Errorf("%s is not on the filesystem of %s; it cannot be moved by renaming", old, m.cfg.Homes)
		}
		return nil
	case err == nil && info.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(old)
		if err != nil {
			return err
		}
		if target != home {
			return fmt.Errorf("%s links to %s, not %s", old, target, home)
		}
		m.homeLinked = true
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	default:
		return fmt.Errorf("%s is not a directory", old)
	}
	if legacy {
		return fmt.Errorf("%s has moved but user %s is not renamed yet; not migrating", old, m.cfg.LegacyUser)
	}
	if info, err := os.Lstat(home); err != nil || !info.IsDir() {
		return errors.Join(err, fmt.Errorf("%s is neither at %s nor at %s", old, old, home))
	}
	m.homeMoved = true
	return nil
}

// stopLegacyUser stops everything the legacy user runs, so it can be
// renamed and its home moved. Nothing is renamed yet: a failure leaves the
// previous release able to run.
func (m *migrator) stopLegacyUser() error {
	sys, u := m.cfg.System, m.cfg.LegacyUser
	m.stopping = true
	// Its services first, waiting for them to shut down cleanly; then
	// whatever is left.
	if err := m.change("stop user manager"); err != nil {
		return err
	}
	if err := sys.StopUserManager(u); err != nil {
		return err
	}
	if err := m.change("disable linger"); err != nil {
		return err
	}
	if err := sys.DisableLinger(u); err != nil {
		return err
	}
	if err := m.change("stop processes"); err != nil {
		return err
	}
	return sys.StopProcesses(u)
}

// renameUser renames the legacy user, moves its home, renames its crontab,
// leaves a link at the old home's path for the absolute paths in its
// crontab, units and programs, and lets its units run again.
func (m *migrator) renameUser() error {
	cfg, sys := m.cfg, m.cfg.System
	old, home := m.oldMain, cfg.PoolHome(m.target)
	if err := m.change("rename user"); err != nil {
		return err
	}
	if err := sys.RenameUser(cfg.LegacyUser, m.target, home); err != nil {
		// Renaming the user is RenameUser's first change: while the legacy
		// user is still there, nothing is renamed and the previous release
		// can run (a busy user, say, from a cron job started since).
		if m.legacyResolves {
			if still, lerr := m.resolves(cfg.LegacyUser); lerr == nil && still {
				m.partial = false
			}
		}
		return err
	}
	if !m.homeMoved {
		if err := m.change("move home"); err != nil {
			return err
		}
		if err := os.Rename(old, home); err != nil {
			if errors.Is(err, syscall.EXDEV) {
				return fmt.Errorf("%s is not on the filesystem of %s; it cannot be moved by renaming", old, home)
			}
			return err
		}
		cfg.Logger.Printf("moved %s to %s", old, home)
	}
	if err := m.change("chmod home"); err != nil {
		return err
	}
	if err := chmodDir(home, 0o750|fs.ModeSetgid); err != nil {
		return err
	}
	if err := m.change("rename crontab"); err != nil {
		return err
	}
	if err := sys.RenameCrontab(cfg.LegacyUser, m.target); err != nil {
		return err
	}
	if !m.homeLinked {
		if err := m.change("link old home"); err != nil {
			return err
		}
		if err := os.Symlink(home, old); err != nil {
			return err
		}
	}
	if err := m.change("enable linger"); err != nil {
		return err
	}
	return sys.EnableLinger(m.target)
}

// rootControlDir makes root's control directory for main, moves the owner's
// OWNER.md into it, and removes the ERROR 0.3 kept in the home: an agent's
// ERROR is in its parent's control directory now.
func (m *migrator) rootControlDir() error {
	for _, dir := range []string{filepath.Join(m.cfg.RootHome(), "agents"), m.mainCtrl()} {
		if exists, err := present(dir); err != nil || exists {
			if err != nil {
				return err
			}
			continue
		}
		if err := m.change("mkdir " + filepath.Base(dir)); err != nil {
			return err
		}
		if err := os.Mkdir(dir, 0o700); err != nil {
			return err
		}
	}
	from := filepath.Join(m.cfg.Etc, "OWNER.md")
	if exists, err := present(from); err != nil {
		return err
	} else if exists {
		if err := m.change("move OWNER.md"); err != nil {
			return err
		}
		if err := os.Rename(from, filepath.Join(m.mainCtrl(), "OWNER.md")); err != nil {
			return err
		}
	}
	helper, err := m.helper(m.target)
	if err != nil {
		return err
	}
	path := filepath.Join(m.cfg.PoolHome(m.target), "ERROR")
	e, err := helper.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if e.Type == "dir" {
		m.cfg.Logger.Printf("%s is a directory, not the daemon's; leaving it", path)
		return nil
	}
	if err := m.change("remove ERROR"); err != nil {
		return err
	}
	return helper.Unlink(path)
}

// rewritePool gives every assignment its id in the tree and main its user.
// Every agent gets the release notes since the version upgraded from.
func (m *migrator) rewritePool() error {
	p, err := m.loadPool()
	if err != nil {
		return err
	}
	seen := m.seenVersion()
	next, changed := map[string]poolEntry{}, false
	for _, e := range p.entries() {
		if id := m.newID(e.Agent); id != e.Agent {
			e.Agent, e.Notes, changed = id, seen, true
		}
		next[e.User] = e
	}
	if _, ok := next[m.target]; !ok {
		next[m.target] = poolEntry{User: m.target, Agent: "main", State: poolActive, Notes: seen}
		changed = true
	}
	if !changed {
		return nil
	}
	if err := m.change("rewrite pool"); err != nil {
		return err
	}
	return p.commit(next)
}

// loadPool loads pool.json, refusing an entry the migration cannot give an
// id in the tree, or that holds the target or main for another: the daemon
// would take such an agent for removed and wipe its home.
func (m *migrator) loadPool() (*pool, error) {
	p, err := loadPool(m.cfg.PoolFile(), nil)
	if err != nil {
		return nil, err
	}
	for _, e := range p.entries() {
		id := m.newID(e.Agent)
		if id != "main" && !strings.HasPrefix(id, "main/agents/") {
			return nil, fmt.Errorf("%s: %s holds %s, which is not the main agent's subagent; not migrating", m.cfg.PoolFile(), e.User, e.Agent)
		}
		if (e.User == m.target) != (id == "main") {
			return nil, fmt.Errorf("%s: %s holds %s, but %s is the main agent's new user; not migrating", m.cfg.PoolFile(), e.User, e.Agent, m.target)
		}
	}
	return p, nil
}

// newID is a 0.3 id's id in the tree: /home/agent is main, and its
// subagents' ids follow. Any other id is returned as it is.
func (m *migrator) newID(id string) string {
	if id == m.oldMain {
		return "main"
	}
	if rest, ok := strings.CutPrefix(id, m.oldMain+"/agents/"); ok {
		return "main/agents/" + rest
	}
	return id
}

// seenVersion is the version upgraded from, whose release notes the agents
// had: install.sh recorded it. 0.3.0 is the oldest that migrates.
func (m *migrator) seenVersion() string {
	data, err := os.ReadFile(m.cfg.VersionFile())
	v := strings.TrimSpace(string(data))
	if _, ok := parseVersion(v); err != nil || !ok {
		m.cfg.Logger.Printf("%s does not hold a version (%v); release notes since 0.3.0 follow", m.cfg.VersionFile(), err)
		return "0.3.0"
	}
	return v
}

// moveJournals moves main's journal directory, with its subagents', to its
// id, and the conversation log into it. Both keep their group, which the
// rename kept. Then each agent's journal link leads to its journals.
func (m *migrator) moveJournals() error {
	cfg := m.cfg
	oldLog, mainLog := m.oldLog(), cfg.JournalDir("main")
	if exists, err := present(oldLog); err != nil {
		return err
	} else if exists {
		if err := m.change("move journals"); err != nil {
			return err
		}
		if err := os.Rename(oldLog, mainLog); err != nil {
			return err
		}
	}
	homes := filepath.Dir(oldLog)
	if entries, err := os.ReadDir(homes); err == nil && len(entries) == 0 {
		if err := m.change("remove log/home"); err != nil {
			return err
		}
		if err := os.Remove(homes); err != nil {
			return err
		}
	}
	conversation := filepath.Join(cfg.Log, "messages.jsonl")
	if exists, err := present(conversation); err != nil {
		return err
	} else if exists {
		if exists, err := present(mainLog); err != nil {
			return err
		} else if !exists {
			// Main never ran under 0.3: the journal directory admission
			// would make.
			cred, err := cfg.LookupUser(m.target)
			if err != nil {
				return err
			}
			gid := -1
			if cred != nil {
				gid = int(cred.Gid)
			}
			if err := m.change("make journal directory"); err != nil {
				return err
			}
			if err := journal.Secure(mainLog, gid); err != nil {
				return err
			}
		}
		if err := m.change("move conversation"); err != nil {
			return err
		}
		if err := os.Rename(conversation, cfg.ConversationFile("main")); err != nil {
			return err
		}
	}
	p, err := m.loadPool()
	if err != nil {
		return err
	}
	for _, e := range p.entries() {
		if err := m.repointJournal(e.User, e.Agent); err != nil {
			return fmt.Errorf("%s: journal link: %w", e.Agent, err)
		}
	}
	return nil
}

// repointJournal points the journal link in user's home at the journals of
// id, through a helper running as user. A home without a link gets one on
// its agent's next run; anything but a link is the agent's own.
func (m *migrator) repointJournal(user, id string) error {
	helper, err := m.helper(user)
	if err != nil {
		return err
	}
	link, want := filepath.Join(m.cfg.PoolHome(user), "journal"), m.cfg.JournalDir(id)
	e, err := helper.Stat(link)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if e.Type != "symlink" || e.Target == want {
		return nil
	}
	if err := m.change("journal link " + user); err != nil {
		return err
	}
	return helper.Symlink(want, link)
}

// rewriteDelivered rewrites the agent ids of the delivery records, so the
// inbox files the 0.3 daemon delivered keep their senders' labels. Lines
// that need no change stay as they are.
func (m *migrator) rewriteDelivered() error {
	path := m.cfg.DeliveredFile()
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines, changed := bytes.SplitAfter(data, []byte("\n")), false
	for i, line := range lines {
		var r deliveryRecord
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		if id := m.newID(r.Agent); id != r.Agent {
			r.Agent = id
			out, err := json.Marshal(r)
			if err != nil {
				return err
			}
			if bytes.HasSuffix(line, []byte("\n")) {
				out = append(out, '\n')
			}
			lines[i], changed = out, true
		}
	}
	if !changed {
		return nil
	}
	if err := m.change("rewrite delivered"); err != nil {
		return err
	}
	return replaceFile(path, bytes.Join(lines, nil), info.Mode().Perm())
}

// moveOwnerSpool moves the owner's messages 0.3 had not delivered yet into
// root's spool for main, which is root's alone as agent send makes it.
func (m *migrator) moveOwnerSpool() error {
	old, spool := m.oldSpool(), m.mainSpool()
	entries, err := os.ReadDir(old)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if len(entries) > 0 {
		if exists, err := present(spool); err != nil {
			return err
		} else if !exists {
			if err := m.change("make spool"); err != nil {
				return err
			}
			if err := os.MkdirAll(spool, 0o700); err != nil {
				return err
			}
		}
	}
	for _, e := range entries {
		to := filepath.Join(spool, e.Name())
		if exists, err := present(to); err != nil || exists {
			return errors.Join(err, fmt.Errorf("%s exists; not moving %s over it", to, filepath.Join(old, e.Name())))
		}
		if err := m.change("move spooled " + e.Name()); err != nil {
			return err
		}
		if err := os.Rename(filepath.Join(old, e.Name()), to); err != nil {
			return err
		}
	}
	if err := m.change("remove owner spool"); err != nil {
		return err
	}
	return os.Remove(old)
}

// removeSudoers removes the sudo grant 0.3 could give the main agent; no
// agent has sudo now.
func (m *migrator) removeSudoers() error {
	path := filepath.Join(m.cfg.SudoersDir, m.cfg.LegacyUser)
	if exists, err := present(path); err != nil || !exists {
		return err
	}
	if err := m.change("remove sudoers"); err != nil {
		return err
	}
	return os.Remove(path)
}

func (m *migrator) mainCtrl() string { return filepath.Join(m.cfg.RootHome(), "agents", "main") }
func (m *migrator) mainSpool() string {
	return filepath.Join(m.cfg.RootHome(), ".messages", "agents", "main")
}
func (m *migrator) oldSpool() string { return filepath.Join(m.cfg.Lib, "owner") }

// oldLog is the main agent's 0.3 journal directory, which mirrored its home.
func (m *migrator) oldLog() string {
	return filepath.Join(m.cfg.Log, "home", m.cfg.LegacyUser)
}

// helper returns a file helper running as user, started on first use.
func (m *migrator) helper(user string) (*fsio.Client, error) {
	if c := m.helpers[user]; c != nil {
		return c, nil
	}
	cred, err := m.cfg.LookupUser(user)
	if err != nil {
		return nil, err
	}
	c, err := fsio.Start(m.cfg.Self, cred)
	if err != nil {
		return nil, err
	}
	m.helpers[user] = c
	return c, nil
}

func (m *migrator) closeHelpers() {
	for _, c := range m.helpers {
		c.Close()
	}
}

// present reports whether path exists, without following a link.
func present(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// checkMove fails when both from and to exist: moving from to to
// would replace to.
func checkMove(from, to string) error {
	exists, err := present(from)
	if err != nil || !exists {
		return err
	}
	if exists, err := present(to); err != nil || exists {
		return errors.Join(err, fmt.Errorf("%s exists; not moving %s over it", to, from))
	}
	return nil
}

// realDirOrNothing fails unless path is missing or a real directory.
func realDirOrNothing(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	return nil
}

// chmodDir sets the mode of the directory dir, never following a link.
func chmodDir(dir string, mode fs.FileMode) error {
	f, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Chmod(mode)
}

func dev(info fs.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev)
	}
	return 0
}

// replaceFile writes data to path through a temp file in its directory,
// synced before the rename and followed by a directory sync, so a crash
// leaves the old file or the new one, never a torn or lost one.
func replaceFile(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, werr := tmp.Write(data)
	if werr == nil {
		werr = tmp.Chmod(mode)
	}
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
