// Package daemon runs one loop per agent directory under the main agent.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/user"
	"path/filepath"
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
	AgentUser    string // runs commands and file access; "" = current user
	ProviderUser string // runs plugins; "" = current user
	Self         string // this executable, re-run as the file helper

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

// errRemoved cancels an agent whose directory disappeared, as opposed to a
// daemon shutdown, which leaves the run unclean for crash recovery.
var (
	errRemoved = errors.New("agent directory removed")
	errLimited = errors.New("agent limit reached")
	errDrained = errors.New("drain deadline reached")
)

type daemon struct {
	cfg       Config
	fs        *fsio.Client
	runner    *plugin.Runner
	agentCred *syscall.Credential
	agentName string
	agentUID  int

	mu sync.Mutex
	// stop is Run's context. Once it is cancelled the daemon drains: no new
	// runs start, and active runs finish before it exits, up to DrainTimeout.
	stop        context.Context
	agents      map[string]*agent
	delivered   *delivered
	settings    layout.Settings // last good settings.json
	settingsErr string
	limited     map[string]bool // agents held back by max_agents, already reported
	wg          sync.WaitGroup
}

func Run(ctx context.Context, cfg Config) error {
	d := &daemon{cfg: cfg, agents: map[string]*agent{}, limited: map[string]bool{}, stop: ctx}
	d.settings, _ = layout.LoadSettings("")
	var err error
	if d.agentCred, err = osuser.Lookup(cfg.AgentUser); err != nil {
		return err
	}
	providerCred, err := osuser.Lookup(cfg.ProviderUser)
	if err != nil {
		return err
	}
	d.agentName, d.agentUID = cfg.AgentUser, os.Getuid()
	if d.agentCred != nil {
		d.agentUID = int(d.agentCred.Uid)
	} else if u, err := user.Current(); err == nil {
		d.agentName = u.Username
	}
	if d.fs, err = fsio.Start(cfg.Self, d.agentCred); err != nil {
		return err
	}
	defer d.fs.Close()
	if d.delivered, err = loadDelivered(cfg.DeliveredFile()); err != nil {
		return err
	}
	d.deliverReleaseNotes()
	d.runner = &plugin.Runner{PluginDir: cfg.Plugins(), CredDir: cfg.Credentials(), User: providerCred, Timeout: 15 * time.Minute, Stderr: os.Stderr}
	ticker := time.NewTicker(cfg.Poll)
	defer ticker.Stop()
	for {
		d.deliverOwner()
		d.collectMessages(d.reconcile(ctx))
		select {
		case <-ctx.Done():
			d.shutdown()
			return nil
		case <-ticker.C:
		}
	}
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

// draining is true from the moment a stop is requested, with no poll in between.
func (d *daemon) draining() bool { return d.stop.Err() != nil }

// reconcile starts loops for new agent directories and stops removed ones,
// returning the admitted agents.
func (d *daemon) reconcile(ctx context.Context) []string {
	dirs, err := d.fs.Scan(d.cfg.Home)
	if err != nil {
		d.cfg.Logger.Printf("scan %s: %v", d.cfg.Home, err)
		return nil
	}
	var valid []string
	for _, dir := range dirs {
		if validAgentDir(d.cfg.Home, dir) {
			valid = append(valid, dir)
		} else {
			d.cfg.Logger.Printf("ignoring unexpected agent path %q", dir)
		}
	}
	exists, present := map[string]bool{}, map[string]bool{}
	for _, dir := range valid {
		exists[dir] = true
	}
	admitted := d.admit(valid)
	for _, dir := range admitted {
		present[dir] = true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for dir, a := range d.agents {
		if !present[dir] {
			if exists[dir] {
				a.cancel(errLimited) // left unclean: resumed when admitted again
			} else {
				a.cancel(errRemoved)
			}
			delete(d.agents, dir)
		}
	}
	for dir := range present {
		if _, ok := d.agents[dir]; !ok && ctx.Err() == nil {
			a := newAgent(d, dir)
			actx, cancel := context.WithCancelCause(context.Background())
			a.cancel = cancel
			d.agents[dir] = a
			d.wg.Add(1)
			go func() {
				defer d.wg.Done()
				a.loop(actx)
			}()
		}
	}
	return admitted
}

// validAgentDir accepts home and home/agents/NAME[/agents/NAME...] with clean
// components. Scan results come from a process the agent user could tamper
// with, and journal paths are derived from them.
func validAgentDir(home, dir string) bool {
	if dir == home {
		return true
	}
	if filepath.Clean(dir) != dir || !strings.HasPrefix(dir, home+"/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(dir, home+"/"), "/")
	if len(parts)%2 != 0 {
		return false
	}
	for i, part := range parts {
		if i%2 == 0 && part != "agents" || part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// admit applies max_agents in scan order. Each excess agent gets an ERROR
// and its parent one note; both clear when the agent is admitted later.
func (d *daemon) admit(dirs []string) []string {
	settings, err := layout.LoadSettings(d.cfg.SettingsFile())
	if err != nil {
		// Keep the last good settings: a half-written file must not stop agents.
		if msg := err.Error(); msg != d.settingsErr {
			d.settingsErr = msg
			d.cfg.Logger.Printf("settings.json: %v; keeping max_agents=%d", err, d.settings.MaxAgents)
		}
		settings = d.settings
	} else {
		d.settings, d.settingsErr = settings, ""
	}
	if len(dirs) <= settings.MaxAgents {
		d.clearLimited(dirs)
		return dirs
	}
	admitted, excess := dirs[:settings.MaxAgents], dirs[settings.MaxAgents:]
	d.clearLimited(admitted)
	for _, dir := range excess {
		if d.limited[dir] {
			continue
		}
		d.limited[dir] = true
		d.cfg.Logger.Printf("%s: not started: agent limit reached (max_agents=%d)", d.rel(dir), settings.MaxAgents)
		if err := d.fs.Write(filepath.Join(dir, "ERROR"), []byte(fmt.Sprintf(MsgLimitFmt, settings.MaxAgents)+"\n")); err != nil {
			d.cfg.Logger.Printf("%s: write ERROR: %v", d.rel(dir), err)
		}
		if parent := d.parent(dir); parent != "" {
			d.writeNote(parent, "subagent-error", fmt.Sprintf(MsgLimitParentFmt, d.rel(dir), settings.MaxAgents))
		}
	}
	return admitted
}

func (d *daemon) clearLimited(dirs []string) {
	for _, dir := range dirs {
		if d.limited[dir] {
			delete(d.limited, dir)
			path := filepath.Join(dir, "ERROR")
			if data, _, err := d.fs.Read(path, 4096); err == nil && strings.HasPrefix(string(data), "Agent limit reached") {
				d.fs.Remove(path)
			}
		}
	}
}

// rel names an agent for logs and notes, relative to the main agent.
func (d *daemon) rel(dir string) string {
	if dir == d.cfg.Home {
		return "main"
	}
	return strings.TrimPrefix(dir, filepath.Join(d.cfg.Home, "agents")+"/")
}

// parent returns the agent that owns dir's agents/ folder, or "" for main.
func (d *daemon) parent(dir string) string {
	if dir == d.cfg.Home {
		return ""
	}
	return filepath.Dir(filepath.Dir(dir))
}
