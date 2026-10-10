package daemon

import (
	"context"
	"time"
)

type agent struct {
	d      *daemon
	dir    string
	cancel context.CancelCauseFunc
}

func newAgent(d *daemon, dir string) *agent { return &agent{d: d, dir: dir} }

// loop wakes whenever the inbox holds a deliverable file, one run at a time.
func (a *agent) loop(ctx context.Context) {
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
