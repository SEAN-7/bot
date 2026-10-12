// Package daemon runs one loop per agent: every agent admitted from a
// control directory in the tree below root, each with its own pool user.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/SEAN-7/bot/internal/fsio"
	"github.com/SEAN-7/bot/internal/journal"
	"github.com/SEAN-7/bot/internal/layout"
	"github.com/SEAN-7/bot/internal/notes"
	"github.com/SEAN-7/bot/internal/osuser"
	"github.com/SEAN-7/bot/internal/plugin"
	"github.com/SEAN-7/bot/internal/version"
)

type Config struct {
	layout.Layout
	RootUser     string // root's file access in RootHome; "" = current user
	ProviderUser string // runs plugins; "" = current user
	Self         string // this executable, re-run as the file helper

	System     System                                    // root-only side effects; the real one by default
	PoolUsers  []string                                  // agent users; nil = the max_agents pool users that exist
	LookupUser func(string) (*syscall.Credential, error) // resolves user names; osuser.Lookup by default
	// LookupGroup reports whether a group exists; groupExists by default.
	LookupGroup func(string) (bool, error)
	TmpDirs     []string // shared directories swept when a user is reclaimed
	LegacyUser  string   // the main agent's user before 0.4.0, which Migrate renames
	SudoersDir  string   // where 0.3 granted LegacyUser sudo

	Poll           time.Duration
	CommandWait    time.Duration // how long a command runs when the model gives no wait
	MaxCommandWait time.Duration // the longest wait the model can ask for
	RetryBase      time.Duration
	RetryMax       time.Duration
	ErrorAfter     int
	DrainTimeout   time.Duration // how long a stop waits for active runs
	// ContinueWindow: a message arriving this soon after a run ended normally
	// continues that conversation. Shorter than the provider's 5-minute cache,
	// so a continued conversation always finds it warm.
	ContinueWindow time.Duration
	Version        string // this harness version; "dev" builds deliver no release notes
	ReleaseNotes   string // agent-facing notes, "## X.Y.Z" sections
	OutputClip     int
	InboxClip      int
	Logger         *log.Logger
}

func DefaultConfig(self string) Config {
	return Config{
		Layout:         layout.Default(),
		RootUser:       "root",
		ProviderUser:   "agent-provider",
		Self:           self,
		System:         newExecSystem(),
		LookupUser:     osuser.Lookup,
		LookupGroup:    groupExists,
		TmpDirs:        []string{"/tmp", "/var/tmp", "/dev/shm"},
		LegacyUser:     "agent",
		SudoersDir:     "/etc/sudoers.d",
		Poll:           time.Second,
		CommandWait:    120 * time.Second,
		MaxCommandWait: 600 * time.Second,
		RetryBase:      2 * time.Second,
		RetryMax:       10 * time.Minute,
		ErrorAfter:     3,
		DrainTimeout:   15 * time.Minute,
		ContinueWindow: 4 * time.Minute,
		Version:        version.Version,
		ReleaseNotes:   notes.Text,
		OutputClip:     16 << 10,
		InboxClip:      64 << 10,
		Logger:         log.New(os.Stderr, "", log.LstdFlags),
	}
}

// errRemoved cancels an agent whose control directory disappeared, as
// opposed to a daemon shutdown, which leaves the run unclean for crash recovery.
var (
	errRemoved = errors.New("agent directory removed")
	errDrained = errors.New("drain deadline reached")
)

type daemon struct {
	cfg      Config
	runner   *plugin.Runner
	root     *agent // the top of the tree: no loop, never in agents
	pool     *pool
	selfUser string // the daemon's own user name, for an agent run without changing identity

	mu sync.Mutex
	// stop is Run's context. Once it is cancelled the daemon drains: no new
	// runs start, and active runs finish before it exits, up to DrainTimeout.
	stop        context.Context
	agents      map[string]*agent // by id; root is not among them
	delivered   *delivered
	settings    layout.Settings // last good settings.json
	settingsErr string
	limited     map[string]bool   // control directories held back by max_agents, already reported
	admitErrs   map[string]string // the last admission error logged per control directory
	reclaimErrs map[string]string // the last reclaim error logged per pool user
	// spoolIgnored holds the spool entries already logged as not an agent's
	// message; only the poll goroutine touches it.
	spoolIgnored map[string]bool
	// unremoved holds spool files delivered but not yet removed, so they are
	// not delivered again while they stay; only the poll goroutine touches it.
	unremoved map[spoolKey]bool
	// journalGids holds the group each journal directory had before
	// secureUnheldJournals changed it, by path: admission's evidence that a
	// home is its agent's own, which a lost pool.json would otherwise erase.
	journalGids map[string]int
	// retries holds spool files whose delivery failed: the inbox name they
	// keep for every retry, so one delivery record serves them all, and the
	// error already logged; only the poll goroutine touches it.
	retries map[spoolKey]*retry
	wg      sync.WaitGroup
}

func Run(ctx context.Context, cfg Config) error {
	if err := checkMigrated(cfg); err != nil {
		return err
	}
	syscall.Umask(0o022)
	d := &daemon{cfg: cfg, agents: map[string]*agent{}, limited: map[string]bool{}, admitErrs: map[string]string{}, reclaimErrs: map[string]string{}, unremoved: map[spoolKey]bool{}, retries: map[spoolKey]*retry{}, stop: ctx}
	d.settings, _ = layout.LoadSettings(cfg.SettingsFile())
	if u, err := user.Current(); err == nil {
		d.selfUser = u.Username
	}
	d.root = &agent{d: d, name: "root", home: cfg.RootHome(), user: cfg.RootUser, done: make(chan struct{})}
	rootCred, err := d.root.cred()
	if err != nil {
		return err
	}
	providerCred, err := cfg.LookupUser(cfg.ProviderUser)
	if err != nil {
		return err
	}
	if d.pool, err = loadPool(cfg.PoolFile(), poolUsers(cfg, d.settings.MaxAgents)); err != nil {
		return err
	}
	for _, e := range d.pool.entries() {
		if strings.HasPrefix(e.Agent, "/") {
			return fmt.Errorf("not starting: %s holds the 0.3 agent id %s, so this installation is not migrated; run install.sh (agent _migrate) to migrate it", cfg.PoolFile(), e.Agent)
		}
	}
	if d.root.fs, err = fsio.Start(cfg.Self, rootCred); err != nil {
		return err
	}
	defer d.closeHelpers()
	d.secureUnheldJournals(d.root.id)
	if d.delivered, err = loadDelivered(cfg.DeliveredFile()); err != nil {
		return err
	}
	d.runner = &plugin.Runner{PluginDir: cfg.Plugins(), CredDir: cfg.Credentials(), User: providerCred, Timeout: 15 * time.Minute, Stderr: os.Stderr}
	ticker := time.NewTicker(cfg.Poll)
	defer ticker.Stop()
	for {
		// Before reconcile, so a reclaim a crash interrupted is finished
		// before the first admission, and one that failed is retried.
		d.sweep()
		d.reconcile()
		d.collectMessages()
		select {
		case <-ctx.Done():
			d.shutdown()
			return nil
		case <-ticker.C:
		}
	}
}

// checkMigrated keeps the daemon from starting on a tree an upgrade has not
// migrated, or migrated part way: there it would take agents for removed and
// reclaim their users, wiping their homes. install.sh migrates the tree and
// finishes an interrupted migration. pool.json is checked once it is loaded.
func checkMigrated(cfg Config) error {
	const fix = "; run install.sh (agent _migrate) to migrate it"
	for _, f := range []string{cfg.MigratingFile(), cfg.MigratingUserFile()} {
		if _, err := os.Lstat(f); err == nil {
			return fmt.Errorf("not starting: %s marks an unfinished migration; rerun install.sh to finish it", f)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("not starting: checking for an unfinished migration: %v; rerun install.sh to finish it", err)
		}
	}
	if cfg.LegacyUser == "" {
		return nil
	}
	_, err := cfg.LookupUser(cfg.LegacyUser)
	var unknown user.UnknownUserError
	switch {
	case err == nil:
		return fmt.Errorf("not starting: user %s, the 0.3 main agent's, exists, so this installation is not migrated%s", cfg.LegacyUser, fix)
	case !errors.As(err, &unknown):
		return fmt.Errorf("not starting: looking up user %s: %v; a 0.3 installation must be migrated first%s", cfg.LegacyUser, err, fix)
	}
	return nil
}

// secureUnheldJournals tightens, before any agent runs, the journals of the
// children of id ("" for root) that hold no user: removed, perhaps under an
// earlier release that left them readable by every user. They go to the
// group of their nearest ancestor that holds one, root's for a top-level
// agent. Root's children's journal directories are the log root's; a held
// agent's journals are set when it is admitted or reclaimed. These paths are
// outside every agent home, so root opens them.
func (d *daemon) secureUnheldJournals(id string) {
	agents := d.cfg.Log
	if id != "" {
		agents = filepath.Join(d.cfg.JournalDir(id), "agents")
	}
	items, err := os.ReadDir(agents)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			d.cfg.Logger.Printf("journal permissions: %v", err)
		}
		return
	}
	for _, item := range items {
		if !item.IsDir() {
			continue // not a journal directory; links are never followed
		}
		child := joinID(id, item.Name())
		if _, held := d.pool.entryOf(child); held {
			d.secureUnheldJournals(child)
			continue
		}
		gid, err := d.ancestorGid(child)
		if err == nil {
			d.rememberGroups(d.cfg.JournalDir(child))
			err = journal.SecureTree(d.cfg.JournalDir(child), gid)
		}
		if err != nil {
			d.cfg.Logger.Printf("%s: journal permissions: %v", child, err)
		}
	}
}

// rememberGroups records the group of every directory under dir that has
// none recorded yet, before secureUnheldJournals hands dir to another group.
// After a lost pool.json every agent looks unheld, and ownsHome still needs
// the group the daemon gave its journal directory, until admission or
// reclaim sets it again (secureJournal, forgetGroups).
func (d *daemon) rememberGroups(dir string) {
	if d.journalGids == nil {
		d.journalGids = map[string]int{}
	}
	filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil || !e.IsDir() {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return nil
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			if _, seen := d.journalGids[path]; !seen {
				d.journalGids[path] = int(st.Gid)
			}
		}
		return nil
	})
}

// forgetGroups drops the groups remembered on start for dir and everything
// under it, once the daemon gives that tree to another group.
func (d *daemon) forgetGroups(dir string) {
	for path := range d.journalGids {
		if path == dir || strings.HasPrefix(path, dir+"/") {
			delete(d.journalGids, path)
		}
	}
}

// groupExists reports whether the group database has name.
func groupExists(name string) (bool, error) {
	_, err := user.LookupGroup(name)
	var unknown user.UnknownGroupError
	if errors.As(err, &unknown) {
		return false, nil
	}
	return err == nil, err
}

// poolUsers returns the configured pool users, or the max_agents pool users,
// that exist, logging the ones that do not: agents can only use the users
// that resolve.
func poolUsers(cfg Config, maxAgents int) []string {
	configured := cfg.PoolUsers
	if configured == nil {
		configured = layout.PoolUsers(maxAgents)
	}
	var users, missing []string
	for _, u := range configured {
		if _, err := cfg.LookupUser(u); err == nil {
			users = append(users, u)
		} else {
			missing = append(missing, u)
		}
	}
	if len(missing) > 0 {
		cfg.Logger.Printf("pool users not found: %s; agents are limited to %d", strings.Join(missing, ", "), len(users))
	}
	return users
}

// shutdown lets active runs finish so a restart loses no context, then
// interrupts whatever is still running at the deadline. Interrupted runs stay
// unclean, so the next daemon resumes them.
func (d *daemon) shutdown() {
	finished := make(chan struct{})
	go func() { d.wg.Wait(); close(finished) }()
	select {
	case <-finished:
		return
	case <-time.After(d.cfg.DrainTimeout):
	}
	d.cfg.Logger.Printf("stop: active runs still going after %s; interrupting them", d.cfg.DrainTimeout)
	d.mu.Lock()
	for _, a := range d.agents {
		a.cancel(errDrained)
	}
	d.mu.Unlock()
	<-finished
}

// closeHelpers stops root's helper and those of every agent still in the tree.
func (d *daemon) closeHelpers() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.root.fs.Close()
	for _, a := range d.agents {
		a.fs.Close()
	}
}

// draining is true from the moment a stop is requested, with no poll in between.
func (d *daemon) draining() bool { return d.stop.Err() != nil }

// start runs a's loop until it is cancelled or the daemon stops. The caller
// holds d.mu.
func (d *daemon) start(a *agent) {
	ctx, cancel := context.WithCancelCause(context.Background())
	a.cancel = cancel
	d.agents[a.id] = a
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer close(a.done)
		a.loop(ctx)
	}()
}

// reconcile walks the tree breadth-first from root, scanning each node's
// home (RootHome for root) through its own helper. Known agents are kept,
// new control directories are admitted, and agents no longer reached are
// removed.
func (d *daemon) reconcile() {
	d.loadSettings()
	d.mu.Lock()
	known := make(map[string]*agent, len(d.agents))
	for id, a := range d.agents {
		known[id] = a
	}
	d.mu.Unlock()
	present := map[string]*agent{}
	seen, unscanned := map[string]bool{}, map[string]bool{}
	tree := []*agent{d.root}
	for i := 0; i < len(tree); i++ {
		a := tree[i]
		ctrls, err := a.fs.Scan(a.home)
		if err != nil {
			// Keep what is running rather than treat a failing helper as removal.
			a.logf("scan: %v", err)
			unscanned[a.home] = true
			for _, c := range known {
				if c.parent == a {
					present[c.id] = c
					tree = append(tree, c)
				}
			}
			continue
		}
		for _, ctrl := range ctrls {
			if !validControlDir(a.home, ctrl) {
				a.logf("ignoring unexpected agent path %q", ctrl)
				continue
			}
			seen[ctrl] = true
			c := known[d.childID(a, ctrl)]
			if c == nil {
				if c = d.admit(a, ctrl); c == nil {
					continue
				}
			} else if controlDirGone(a, ctrl, c.ctrlID) {
				// Created again since admission: the old agent is
				// removed below, and the new one admitted once its user
				// is reclaimed.
				continue
			}
			present[c.id] = c
			tree = append(tree, c)
		}
		// A known child the scan missed stays unless its control directory
		// is certainly gone, since removal wipes its home.
		for _, c := range known {
			if c.parent == a && present[c.id] == nil && !controlDirGone(a, c.ctrl, c.ctrlID) {
				seen[c.ctrl] = true
				present[c.id] = c
				tree = append(tree, c)
			}
		}
	}
	for _, c := range tree[1:] {
		if !unscanned[c.parent.home] {
			d.repairHomeLink(c)
		}
	}
	// Forget what was reported for directories that are gone, so they are
	// reported again if they come back.
	vanished := func(ctrl string) bool { return !seen[ctrl] && !unscanned[filepath.Dir(filepath.Dir(ctrl))] }
	for ctrl := range d.limited {
		if vanished(ctrl) {
			delete(d.limited, ctrl)
		}
	}
	for ctrl := range d.admitErrs {
		if vanished(ctrl) {
			delete(d.admitErrs, ctrl)
		}
	}
	d.mu.Lock()
	var gone []*agent
	for id, a := range d.agents {
		if present[id] == nil {
			gone = append(gone, a)
		}
	}
	d.mu.Unlock()
	d.remove(gone)
}

// childID is the identity of the agent at ctrl, a control directory of
// parent: its path from root, main or main/agents/research. It keys
// journals, the pool and deliveries, so a child's journals sit inside its
// parent's journal directory whichever pool user holds the parent.
func (d *daemon) childID(parent *agent, ctrl string) string {
	return joinID(parent.id, filepath.Base(ctrl))
}

// joinID is the id of the child name of the agent with id parent: root's id
// is "", so its children's ids are their names.
func joinID(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "/agents/" + name
}

// validControlDir accepts only home/agents/NAME with one clean component.
// Scan results come from a process the agent user could tamper with, and
// journal paths are derived from them.
func validControlDir(home, dir string) bool {
	base := filepath.Base(dir)
	return base != "." && base != ".." && base != "/" && dir == filepath.Join(home, "agents", base)
}

// loadSettings rereads settings.json, keeping the last good settings when it
// is malformed: a half-written file must not stop agents.
func (d *daemon) loadSettings() {
	settings, err := layout.LoadSettings(d.cfg.SettingsFile())
	if err != nil {
		if msg := err.Error(); msg != d.settingsErr {
			d.settingsErr = msg
			d.cfg.Logger.Printf("settings.json: %v; keeping max_agents=%d", err, d.settings.MaxAgents)
		}
		return
	}
	d.settings, d.settingsErr = settings, ""
}
