package daemon

import (
	"context"
	"time"

	"github.com/SEAN-7/bot/internal/journal"
)

type agent struct {
	d      *daemon
	dir    string
	cancel context.CancelCauseFunc

	failedConfig string // hash of a config.json already reported invalid
}

func newAgent(d *daemon, dir string) *agent { return &agent{d: d, dir: dir} }

// loop wakes whenever the inbox holds a deliverable file, one run at a time.
func (a *agent) loop(ctx context.Context) {
	a.recover()
	ticker := time.NewTicker(a.d.cfg.Poll)
	defer ticker.Stop()
	for {
		if names, err := a.d.pending(a.dir); err == nil && len(names) > 0 {
			a.run(ctx)
		}
		select {
		case <-ctx.Done():
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
