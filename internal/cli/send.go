// Package cli implements the agent command's subcommands.
package cli

import (
	"errors"
	"os"
	"time"

	"github.com/SEAN-7/bot/internal/layout"
)

// Send queues an owner message from root to main through root's spool for
// it. Only root can write that spool, which is what lets the daemon label
// these messages as coming from the owner, and only root can read it.
func Send(l layout.Layout, text string, now time.Time) error {
	if os.Geteuid() != 0 {
		return errors.New("agent send needs root (sudo)")
	}
	return send(l, text, now)
}

func send(l layout.Layout, text string, now time.Time) error {
	return sendChild(l.RootHome(), "main", text, now, true)
}
