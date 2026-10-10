// Package cli implements the agent command's subcommands.
package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SEAN-7/bot/internal/layout"
)

// Send queues an owner message. Only root can write the spool, which is what
// lets the daemon label these messages as coming from the owner.
func Send(l layout.Layout, text string, now time.Time) error {
	if os.Geteuid() != 0 {
		return errors.New("agent send needs root (sudo)")
	}
	return send(l, text, now)
}

func send(l layout.Layout, text string, now time.Time) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("message is empty")
	}
	if err := os.MkdirAll(l.OwnerSpool(), 0o700); err != nil {
		return err
	}
	name := now.UTC().Format("20060102T150405.000000000Z") + ".md"
	temp := filepath.Join(l.OwnerSpool(), "."+name)
	if err := os.WriteFile(temp, []byte(strings.TrimRight(text, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(temp, filepath.Join(l.OwnerSpool(), name))
}
