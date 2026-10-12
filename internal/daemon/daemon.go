// Package daemon runs one loop per agent: the main agent and every subagent
// admitted from a control directory below it, each with its own user.
package daemon

import (
	"context"
	"errors"
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
	AgentUser    string // runs commands and file access; "" = current user
	ProviderUser string // runs plugins; "" = current user
	Self         string // this executable, re-run as the file helper

	System     System                                    // root-only side effects; the real one by default
	PoolUsers  []string                                  // subagent users; nil = the max_agents pool users that exist
	LookupUser func(string) (*syscall.Credential, error) // resolves user names; osuser.Lookup by default
	TmpDirs    []string                                  // shared directories swept when a user is reclaimed
	OwnerUID   int                                       // owns the owner spool and OWNER.md

	Poll           time.Duration
	CommandTimeout time.Duration
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
		AgentUser:      "agent",
		ProviderUser:   "agent-provider",
		Self:           self,
		System:         newExecSystem(),
		LookupUser:     osuser.Lookup,
		TmpDirs:        []string{"/tmp", "/var/tmp", "/dev/shm"},
		OwnerUID:       0,
		Poll:           time.Second,
		CommandTimeout: 120 * time.Second,
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
	main     *agent
	pool     *pool
	selfUser string // the daemon's own user name, for a main agent run without changing identity

	mu sync.Mutex
	// stop is Run's context. Once it is cancelled the daemon drains: no new
	// runs start, and active runs finish before it exits, up to DrainTimeout.
	stop        context.Context
	agents      map[string]*agent // by id, main included
	delivered   *delivered
	settings    layout.Settings // last good settings.json
	settingsErr string
	limited     map[string]bool   // control directories held back by max_agents, already reported
	admitErrs   map[string]string // the last admission error logged per control directory
	reclaimErrs map[string]string // the last reclaim error logged per pool user
	// spoolIgnored holds the spool entries already logged as not an agent's
	// message; only the poll goroutine touches it.
	spoolIgnored map[string]bool
	wg           sync.WaitGroup
}

func Run(ctx context.Context, cfg Config) error {
	syscall.Umask(0o022)
	d := &daemon{cfg: cfg, agents: map[string]*agent{}, limited: map[string]bool{}, admitErrs: map[string]string{}, reclaimErrs: map[string]string{}, stop: ctx}
	d.settings, _ = layout.LoadSettings(cfg.SettingsFile())
	if u, err := user.Current(); err == nil {
		d.selfUser = u.Username
	}
	d.main = newMain(d)
	mainCred, err := d.main.cred()
	if err != nil {
		return err
	}
	providerCred, err := cfg.LookupUser(cfg.ProviderUser)
	if err != nil {
		return err
	}
	if d.pool, err = loadPool(cfg.PoolFile(), d.poolUsers()); err != nil {
		return err
	}
	if d.main.fs, err = fsio.Start(cfg.Self, mainCred); err != nil {
		return err
	}
	defer d.closeHelpers()
	if err := journal.Secure(cfg.JournalDir(d.main.id), d.main.gid()); err != nil {
		cfg.Logger.Printf("main: journal permissions: %v", err)
	}
	if d.delivered, err = loadDelivered(cfg.DeliveredFile()); err != nil {
		return err
	}
	d.deliverReleaseNotes()
	d.runner = &plugin.Runner{PluginDir: cfg.Plugins(), CredDir: cfg.Credentials(), User: providerCred, Timeout: 15 * time.Minute, Stderr: os.Stderr}
	d.mu.Lock()
	d.start(d.main)
	d.mu.Unlock()
	ticker := time.NewTicker(cfg.Poll)
	defer ticker.Stop()
	for {
		// Before reconcile, so a reclaim a crash interrupted is finished
		// before the first admission, and one that failed is retried.
		d.sweep()
		d.deliverOwner()
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

// poolUsers returns the configured pool users that exist, logging the ones
// that do not: subagents can only use the users that resolve.
func (d *daemon) poolUsers() []string {
	configured := d.cfg.PoolUsers
	if configured == nil {
		configured = layout.PoolUsers(d.settings.MaxAgents)
	}
	var users, missing []string
	for _, u := range configured {
		if _, err := d.cfg.LookupUser(u); err == nil {
			users = append(users, u)
		} else {
			missing = append(missing, u)
		}
	}
	if len(missing) > 0 {
		d.cfg.Logger.Printf("pool users not found: %s; subagents are limited to %d", strings.Join(missing, ", "), len(users))
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

// closeHelpers stops the helpers of every agent still in the tree.
func (d *daemon) closeHelpers() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.agents[d.main.id]; !ok {
		d.main.fs.Close() // its loop never started
	}
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

// reconcile walks the tree breadth-first from the main agent, scanning each
// agent's home through its own helper. Known agents are kept, new control
// directories are admitted, and agents no longer reached are removed.
func (d *daemon) reconcile() {
	d.loadSettings()
	d.mu.Lock()
	known := make(map[string]*agent, len(d.agents))
	for id, a := range d.agents {
		known[id] = a
	}
	d.mu.Unlock()
	present := map[string]*agent{d.main.id: d.main}
	seen, unscanned := map[string]bool{}, map[string]bool{}
	tree := []*agent{d.main}
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
			}
			present[c.id] = c
			tree = append(tree, c)
		}
		// A known child the scan skipped stays while its control directory
		// exists: a missing or renamed config.json only pauses it, since
		// removal wipes its home.
		for _, c := range known {
			if c.parent == a && present[c.id] == nil && !controlDirGone(a, c.ctrl) {
				seen[c.ctrl] = true
				present[c.id] = c
				tree = append(tree, c)
			}
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

// childName names the subagent at ctrl, a control directory of parent, by
// its path under main's agents/: research, research/agents/scraper.
func childName(parent *agent, ctrl string) string {
	if parent.parent == nil {
		return filepath.Base(ctrl)
	}
	return parent.name + "/agents/" + filepath.Base(ctrl)
}

// childID is a subagent's identity: its control directory's path as seen
// from the main agent. It keys journals, the pool and deliveries, so a
// grandchild's journals sit inside its parent's journal directory whichever
// pool user holds the parent. For a direct child it is the control directory.
func (d *daemon) childID(parent *agent, ctrl string) string {
	return filepath.Join(d.cfg.Home, "agents", childName(parent, ctrl))
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
