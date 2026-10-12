package daemon

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
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
}

// pool records which pool user each subagent holds. Every change is persisted
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
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.byUser {
		if e.Agent == agentID {
			return e.User
		}
	}
	return ""
}

// assign returns agentID's user, giving it the first free one if it has none.
// It returns "" when every user is taken or limit entries already exist.
func (p *pool) assign(agentID string, limit int) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.byUser {
		if e.Agent == agentID {
			return e.User, nil
		}
	}
	if len(p.byUser) >= limit {
		return "", nil
	}
	for _, u := range p.users {
		if _, taken := p.byUser[u]; !taken {
			p.byUser[u] = poolEntry{User: u, Agent: agentID, State: poolActive}
			return u, p.save()
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
	p.byUser[user] = e
	return p.save()
}

func (p *pool) free(user string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.byUser[user]; !ok {
		return nil
	}
	delete(p.byUser, user)
	return p.save()
}

func (p *pool) entries() []poolEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sorted()
}

func (p *pool) sorted() []poolEntry {
	list := make([]poolEntry, 0, len(p.byUser))
	for _, e := range p.byUser {
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].User < list[j].User })
	return list
}

// save writes the pool through a temp file so a crash never leaves it torn.
func (p *pool) save() error {
	data, err := json.Marshal(p.sorted())
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p.path), ".pool-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, werr := tmp.Write(data)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p.path)
}
