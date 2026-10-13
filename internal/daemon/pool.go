package daemon

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Pool states. A user with no entry is free.
const (
	poolActive     = "active"
	poolReclaiming = "reclaiming"
)

type poolEntry struct {
	User  string `json:"user"`
	Agent string `json:"agent"`
	State string `json:"state"`
	Notes string `json:"notes,omitempty"` // newest release-notes version the agent has seen
}

// pool records which pool user each agent holds. Every change is persisted
// before it is reported, so a restart sees the same assignments.
type pool struct {
	mu     sync.Mutex
	path   string
	users  []string // assignment order
	byUser map[string]poolEntry
}

func loadPool(path string, users []string) (*pool, error) {
	p := &pool{path: path, users: users, byUser: map[string]poolEntry{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	var list []poolEntry
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	for _, e := range list {
		p.byUser[e.User] = e
	}
	return p, nil
}

func (p *pool) userOf(agentID string) string {
	e, _ := p.entryOf(agentID)
	return e.User
}

// entryOf returns agentID's entry, if it holds a user.
func (p *pool) entryOf(agentID string) (poolEntry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.byUser {
		if e.Agent == agentID {
			return e, true
		}
	}
	return poolEntry{}, false
}

// hasDescendants reports whether any agent below agentID still holds a user.
func (p *pool) hasDescendants(agentID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.byUser {
		if strings.HasPrefix(e.Agent, agentID+"/agents/") {
			return true
		}
	}
	return false
}

// assign returns agentID's user, giving it the first free one if it has
// none. It returns "" when limit entries already exist, or the agent's user
// is being reclaimed: a reclaiming user is never handed out.
func (p *pool) assign(agentID string, limit int) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.byUser {
		if e.Agent == agentID {
			if e.State == poolReclaiming {
				return "", nil
			}
			return e.User, nil
		}
	}
	if len(p.byUser) >= limit {
		return "", nil
	}
	for _, u := range p.users {
		if _, taken := p.byUser[u]; !taken {
			next := p.with(u, poolEntry{User: u, Agent: agentID, State: poolActive})
			if err := p.commit(next); err != nil {
				return "", err
			}
			return u, nil
		}
	}
	return "", nil
}

func (p *pool) mark(user, state string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.byUser[user]
	if !ok {
		return nil
	}
	e.State = state
	return p.commit(p.with(user, e))
}

// setNotes records the release-notes version user's agent has seen.
func (p *pool) setNotes(user, version string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.byUser[user]
	if !ok {
		return nil
	}
	e.Notes = version
	return p.commit(p.with(user, e))
}

func (p *pool) free(user string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.byUser[user]; !ok {
		return nil
	}
	next := p.with(user, poolEntry{})
	delete(next, user)
	return p.commit(next)
}

// with returns a copy of the pool's entries with e stored for user.
func (p *pool) with(user string, e poolEntry) map[string]poolEntry {
	next := make(map[string]poolEntry, len(p.byUser)+1)
	for k, v := range p.byUser {
		next[k] = v
	}
	next[user] = e
	return next
}

func (p *pool) entries() []poolEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sorted()
}

func (p *pool) sorted() []poolEntry { return sortedEntries(p.byUser) }

func sortedEntries(m map[string]poolEntry) []poolEntry {
	list := make([]poolEntry, 0, len(m))
	for _, e := range m {
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].User < list[j].User })
	return list
}

// commit persists next and only then makes it the pool's state, so a failed
// write leaves memory matching the file.
func (p *pool) commit(next map[string]poolEntry) error {
	if err := p.save(next); err != nil {
		return err
	}
	p.byUser = next
	return nil
}

// save writes the entries with replaceFile, so a crash never leaves a torn
// or lost file.
func (p *pool) save(m map[string]poolEntry) error {
	data, err := json.Marshal(sortedEntries(m))
	if err != nil {
		return err
	}
	return replaceFile(p.path, data, 0o600)
}

// replaceFile writes data to path through a temp file in its directory,
// synced before the rename and followed by a directory sync, so a crash
// leaves the old file or the new one, never a torn or lost one.
func replaceFile(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, werr := tmp.Write(data)
	if werr == nil {
		werr = tmp.Chmod(mode)
	}
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
