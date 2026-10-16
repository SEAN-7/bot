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
// the agent id as comment, its reclaiming entries and the pool users no
// agent held are deleted (their homes are empty), and the file goes. It is
// for upgrades from 0.8.0 and earlier and leaves in 0.10.0.
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
	held := map[string]bool{}
	for _, e := range entries {
		if e.State == "active" {
			if err := sys.SetComment(e.User, e.Agent); err != nil {
				return err
			}
			held[e.User] = true
		}
	}
	users, err := sys.Users(cfg.UserPrefix)
	if err != nil {
		return err
	}
	for _, u := range users {
		if !held[u.Name] {
			if err := sys.DeleteUser(u.Name); err != nil {
				return err
			}
		}
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	cfg.Logger.Printf("migrated pool.json: %d agents keep their users", len(held))
	return nil
}
