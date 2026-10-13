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
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/SEAN-7/bot/internal/fsio"
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
	TmpDirs    []string                                  // shared directories swept when a user is reclaimed

	Poll           time.Duration
	CommandWait    time.Duration // how long a command runs when the model gives no wait
	MaxCommandWait time.Duration // the longest wait the model can ask for
	RetryBase      time.Duration
	RetryMax       time.Duration
	ErrorAfter     int
	DrainTimeout   time.Duration // how long a stop waits for active runs
	Version        string        // this harness version; "dev" builds deliver no release notes
	ReleaseNotes   string        // agent-facing notes, "## X.Y.Z" sections
	OutputClip     int
	InboxClip      int
	ImageBudget    int // bytes of image data one run may show
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
		TmpDirs:        []string{"/tmp", "/var/tmp", "/dev/shm"},
		Poll:           time.Second,
		CommandWait:    120 * time.Second,
		MaxCommandWait: 600 * time.Second,
		RetryBase:      2 * time.Second,
		RetryMax:       10 * time.Minute,
		ErrorAfter:     3,
		DrainTimeout:   15 * time.Minute,
		Version:        version.Version,
		ReleaseNotes:   notes.Text,
		OutputClip:     16 << 10,
		InboxClip:      64 << 10,
		ImageBudget:    20 << 20,
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
	heldBack    map[string]string // control directories not admitted, with the reason already reported
	admitErrs   map[string]string // the last admission error logged per control directory
	reclaimErrs map[string]string // the last reclaim error logged per pool user
	// unclosed holds the harness directories already logged as left alone
	// by seed; only the poll goroutine touches it.
	unclosed map[string]bool
	// spoolIgnored holds the spool entries already logged as not an agent's
	// message; only the poll goroutine touches it.
	spoolIgnored map[string]bool
	// unremoved holds spool files delivered but not yet removed, so they are
	// not delivered again while they stay; only the poll goroutine touches it.
	unremoved map[spoolKey]bool
	// retries holds spool files whose delivery failed: the inbox name they
	// keep for every retry, so one delivery record serves them all, and the
	// error already logged; only the poll goroutine touches it.
	retries map[spoolKey]*retry
	wg      sync.WaitGroup
}

func Run(ctx context.Context, cfg Config) error {
	syscall.Umask(0o022)
	d := &daemon{cfg: cfg, agents: map[string]*agent{}, heldBack: map[string]string{}, admitErrs: map[string]string{}, reclaimErrs: map[string]string{}, unremoved: map[spoolKey]bool{}, retries: map[spoolKey]*retry{}, stop: ctx}
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
	if err := poolLost(cfg); err != nil {
		return err
	}
	if d.pool, err = loadPool(cfg.PoolFile(), poolUsers(cfg, d.settings.MaxAgents)); err != nil {
		return err
	}
	if d.root.fs, err = fsio.Start(cfg.Self, rootCred); err != nil {
		return err
	}
	defer d.closeHelpers()
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

// poolLost refuses to start without pool.json once the journal root holds
// anything but regular files (an agent's journal directory, or a link that
// may lead to one): agents have been admitted. pool.json is the only record
// of the user each holds; without it every agent would look new and its user
// free, so a live agent's home could be cleared or handed to another.
func poolLost(cfg Config) error {
	if _, err := os.Stat(cfg.PoolFile()); !errors.Is(err, fs.ErrNotExist) {
		return nil // loadPool reports any other error
	}
	entries, err := os.ReadDir(cfg.Log)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			return fmt.Errorf("not starting: %s is missing but %s holds agent journals; restore it from a backup, or move the journals somewhere only root can read", cfg.PoolFile(), cfg.Log)
		}
	}
	return nil
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
		ctrls, err := controlDirs(a)
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
	for ctrl := range d.heldBack {
		if vanished(ctrl) {
			delete(d.heldBack, ctrl)
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

// controlDirs lists a's control directories, home/agents/NAME, sorted: the
// real directories (never links) in a real agents/ directory, all owned by
// a's user. The daemon decides from what each holds whether it is an agent.
// It looks one level deep only; deeper agents belong to their own users. An
// agents/ that is missing, a link, another user's or unreachable holds none.
func controlDirs(a *agent) ([]string, error) {
	cred, err := a.cred()
	if err != nil {
		return nil, err
	}
	uid := credUID(cred)
	dir := filepath.Join(a.home, "agents")
	e, err := a.fs.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if e.Type != "dir" || e.UID != uid {
		return nil, nil
	}
	entries, err := a.fs.List(dir)
	if err != nil {
		return nil, err
	}
	var ctrls []string
	for _, e := range entries {
		if e.Type == "dir" && e.UID == uid {
			ctrls = append(ctrls, filepath.Join(dir, e.Name))
		}
	}
	sort.Strings(ctrls)
	return ctrls, nil
}

// validControlDir accepts only home/agents/NAME with one clean component.
// controlDirs's results come from a process the agent user could tamper
// with, and journal paths are derived from them.
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
