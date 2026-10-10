// Package daemon runs one loop per agent directory under the main agent.
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
	"github.com/SEAN-7/bot/internal/layout"
	"github.com/SEAN-7/bot/internal/osuser"
	"github.com/SEAN-7/bot/internal/plugin"
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
		OutputClip:     16 << 10,
		InboxClip:      64 << 10,
		Logger:         log.New(os.Stderr, "", log.LstdFlags),
	}
}

// errRemoved cancels an agent whose directory disappeared, as opposed to a
// daemon shutdown, which leaves the run unclean for crash recovery.
var errRemoved = errors.New("agent directory removed")

type daemon struct {
	cfg       Config
	fs        *fsio.Client
	runner    *plugin.Runner
	agentCred *syscall.Credential
	agentName string
	agentUID  int

	mu     sync.Mutex
	agents map[string]*agent
	wg     sync.WaitGroup
}

func Run(ctx context.Context, cfg Config) error {
	d := &daemon{cfg: cfg, agents: map[string]*agent{}}
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
	d.runner = &plugin.Runner{PluginDir: cfg.Plugins(), CredDir: cfg.Credentials(), User: providerCred, Timeout: 15 * time.Minute, Stderr: os.Stderr}
	d.recover()
	ticker := time.NewTicker(cfg.Poll)
	defer ticker.Stop()
	for {
		d.deliverOwner()
		d.reconcile(ctx)
		select {
		case <-ctx.Done():
			d.mu.Lock()
			for _, a := range d.agents {
				a.cancel(ctx.Err())
			}
			d.mu.Unlock()
			d.wg.Wait()
			return nil
		case <-ticker.C:
		}
	}
}

// reconcile starts loops for new agent directories and stops removed ones.
func (d *daemon) reconcile(ctx context.Context) {
	dirs, err := d.fs.Scan(d.cfg.Home)
	if err != nil {
		d.cfg.Logger.Printf("scan %s: %v", d.cfg.Home, err)
		return
	}
	present := map[string]bool{}
	for _, dir := range d.admit(dirs) {
		present[dir] = true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for dir, a := range d.agents {
		if !present[dir] {
			a.cancel(errRemoved)
			delete(d.agents, dir)
		}
	}
	for dir := range present {
		if _, ok := d.agents[dir]; !ok && ctx.Err() == nil {
			a := newAgent(d, dir)
			actx, cancel := context.WithCancelCause(ctx)
			a.cancel = cancel
			d.agents[dir] = a
			d.wg.Add(1)
			go func() {
				defer d.wg.Done()
				a.loop(actx)
			}()
		}
	}
}

// admit applies max_agents; extra agents are reported and not started.
func (d *daemon) admit(dirs []string) []string { return dirs }

func (d *daemon) recover() {}

func (d *daemon) deliverOwner() {}

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
