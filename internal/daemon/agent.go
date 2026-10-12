package daemon

import (
	"context"
	"path/filepath"
	"reflect"
	"time"

	"github.com/SEAN-7/bot/internal/plugin"
	"github.com/SEAN-7/bot/internal/proto"

	"github.com/SEAN-7/bot/internal/journal"
)

type agent struct {
	d      *daemon
	dir    string
	cancel context.CancelCauseFunc

	failedConfig string // hash of a config.json already reported invalid

	conv *conversation // the last run's conversation, if it ended normally
}

// conversation is what a follow-up needs to continue a run: the frozen system
// sections and messages, so the provider's cached prefix stays identical.
type conversation struct {
	cfg      plugin.Config
	system   []proto.Segment
	messages []proto.Message
	calls    int // call ids used so far
	warned   bool
	ended    time.Time
	journal  string
}

// takeConversation returns the previous conversation if this run may
// continue it: it ended normally within ContinueWindow, the configuration is
// unchanged, and the agent has not asked for a fresh start with ~/.reset.
func (a *agent) takeConversation(cfg plugin.Config) *conversation {
	c := a.conv
	a.conv = nil
	reset := filepath.Join(a.dir, ".reset")
	if _, err := a.d.fs.Stat(reset); err == nil {
		if err := a.d.fs.Remove(reset); err != nil {
			a.logf("remove ~/.reset: %v", err)
		}
		return nil
	}
	if c == nil || time.Since(c.ended) > a.d.cfg.ContinueWindow || !reflect.DeepEqual(c.cfg, cfg) {
		return nil
	}
	return c
}

func newAgent(d *daemon, dir string) *agent { return &agent{d: d, dir: dir} }

// loop wakes whenever the inbox holds a deliverable file, one run at a time.
func (a *agent) loop(ctx context.Context) {
	a.recover()
	ticker := time.NewTicker(a.d.cfg.Poll)
	defer ticker.Stop()
	for {
		if a.d.draining() {
			return
		}
		if names, err := a.d.pending(a.dir); err == nil && len(names) > 0 {
			a.run(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-a.d.stop.Done():
			return
		case <-ticker.C:
		}
	}
}

// recover tells an agent whose newest run never ended (daemon crash or stop,
// or held back by max_agents) that it was interrupted, then marks that run
// ended so the note is sent once.
func (a *agent) recover() {
	path, clean, err := journal.Latest(a.d.cfg.JournalDir(a.dir))
	if err != nil || path == "" || clean {
		return
	}
	if err := a.d.writeNote(a.dir, "interrupted", MsgInterrupted); err != nil {
		a.logf("interrupted note: %v", err)
		return
	}
	if err := journal.MarkInterrupted(path); err != nil {
		a.logf("%v", err)
	}
	a.logf("resumed after interruption (%s)", path)
}
