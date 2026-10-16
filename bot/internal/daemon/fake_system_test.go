package daemon

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// fakeSystem records the root-only operations instead of performing them.
// PrepareHome still creates the home, so the agent's helper can seed it, and
// users live in a map so admission and removal see what they created.
type fakeSystem struct {
	mu    sync.Mutex
	calls []string
	fail  map[string]error  // calls that fail, still recorded
	users map[string]string // user name → agent id
	homes map[string]string // user name → home, removed with the user
}

// seed makes a user exist before the daemon starts, as one a previous
// daemon created would.
func (s *fakeSystem) seed(name, id, home string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.users == nil {
		s.users, s.homes = map[string]string{}, map[string]string{}
	}
	s.users[name], s.homes[name] = id, home
}

func (s *fakeSystem) AddUser(name, id, home string) error {
	if err := s.record("useradd " + name + " " + id); err != nil {
		return err
	}
	s.seed(name, id, home)
	return nil
}

// DeleteUser removes the user and, as userdel -r does with root's powers,
// its home whatever the modes inside.
func (s *fakeSystem) DeleteUser(name string) error {
	if err := s.record("userdel " + name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if home := s.homes[name]; home != "" {
		filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				os.Chmod(path, 0o700)
			}
			return nil
		})
		if err := os.RemoveAll(home); err != nil {
			return err
		}
	}
	delete(s.users, name)
	delete(s.homes, name)
	return nil
}

func (s *fakeSystem) SetComment(name, id string) error {
	if err := s.record("comment " + name + " " + id); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[name]; ok {
		s.users[name] = id
	}
	return nil
}

func (s *fakeSystem) Users(prefix string) ([]User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var users []User
	for name, id := range s.users {
		if isAgentUser(name, prefix) {
			users = append(users, User{name, id})
		}
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Name < users[j].Name })
	return users, nil
}

func (s *fakeSystem) record(call string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
	return s.fail[call]
}

// setFail makes call fail with err from now on; nil clears it.
func (s *fakeSystem) setFail(call string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail == nil {
		s.fail = map[string]error{}
	}
	if err == nil {
		delete(s.fail, call)
	} else {
		s.fail[call] = err
	}
}

// called reports whether exactly this call was made.
func (s *fakeSystem) called(call string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.calls {
		if c == call {
			return true
		}
	}
	return false
}

func (s *fakeSystem) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.calls, "; ")
}

func (s *fakeSystem) PrepareHome(user, home string) error {
	if err := s.record("home " + user); err != nil {
		return err
	}
	return os.MkdirAll(home, 0o755)
}

func (s *fakeSystem) ShareInbox(user, home string, uid int, grant bool) error {
	verb := "revoke"
	if grant {
		verb = "grant"
	}
	return s.record(verb + " " + user + " " + strconv.Itoa(uid))
}

func (s *fakeSystem) SetMembers(group string, members []string) error {
	return s.record("members " + group + " " + strings.Join(members, ","))
}

func (s *fakeSystem) EnableLinger(user string) error  { return s.record("linger " + user) }
func (s *fakeSystem) DisableLinger(user string) error { return s.record("unlinger " + user) }
func (s *fakeSystem) StopProcesses(user string) error { return s.record("stop " + user) }
func (s *fakeSystem) RemoveCrontab(user string) error { return s.record("crontab " + user) }

// inOrder reports whether calls were all made, in this order, possibly with
// other calls between them.
func (s *fakeSystem) inOrder(calls ...string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := 0
	for _, c := range s.calls {
		if i < len(calls) && c == calls[i] {
			i++
		}
	}
	return i == len(calls)
}
