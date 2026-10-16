package daemon

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// migratePool moves an installation from the pool of releases before 0.9.0
// to a user per agent, once: pool.json's active entries give their users
// the agent id as comment, and every other agent user is marked removed,
// so the sweep deletes it through the full sequence (its processes stopped
// first, retried until it succeeds). Only then does the file go. It writes
// comments only, so it is safe to repeat from any point; an error leaves
// the file for the next start, and the daemon does not run on a
// half-migrated passwd. It is for upgrades from 0.8.0 and earlier and
// leaves in 0.10.0.
func migratePool(cfg Config, sys System) error {
	path := filepath.Join(cfg.Lib, "pool.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	var entries []struct{ User, Agent, State string }
	if err := json.Unmarshal(data, &entries); err != nil {
		return err
	}
	held := map[string]string{}
	for _, e := range entries {
		if e.State == "active" {
			held[e.User] = e.Agent
		}
	}
	users, err := sys.Users(cfg.UserPrefix)
	if err != nil {
		return err
	}
	for _, u := range users {
		comment, ok := held[u.Name]
		if !ok {
			comment = removedMark + u.Name
			if e, found := entryOf(entries, u.Name); found {
				comment = removedMark + e
			}
		}
		if u.ID == comment {
			continue
		}
		if err := sys.SetComment(u.Name, comment); err != nil {
			return err
		}
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	cfg.Logger.Printf("migrated pool.json: %d agents keep their users", len(held))
	return nil
}

// entryOf is the agent id pool.json records for user, if any.
func entryOf(entries []struct{ User, Agent, State string }, user string) (string, bool) {
	for _, e := range entries {
		if e.User == user {
			return e.Agent, true
		}
	}
	return "", false
}
