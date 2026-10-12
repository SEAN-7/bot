package daemon

import (
	"fmt"
	"os"
	"os/user"
	"strings"
	"sync"
	"syscall"
)

// fakeSystem records the root-only operations instead of performing them.
// PrepareHome still creates the home, so the agent's helper can seed it.
// RenameUser renames a user in users, which lookup resolves.
type fakeSystem struct {
	mu    sync.Mutex
	calls []string
	fail  map[string]error // calls that fail, still recorded
	users map[string]bool
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

func (s *fakeSystem) SetMembers(group string, members []string) error {
	return s.record("members " + group + " " + strings.Join(members, ","))
}

func (s *fakeSystem) EnableLinger(user string) error  { return s.record("linger " + user) }
func (s *fakeSystem) DisableLinger(user string) error { return s.record("unlinger " + user) }
func (s *fakeSystem) StopProcesses(user string) error { return s.record("stop " + user) }
func (s *fakeSystem) RemoveCrontab(user string) error { return s.record("crontab " + user) }

func (s *fakeSystem) RenameUser(old, new, home string) error {
	if err := s.record("rename " + old + " " + new); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.users[old] {
		delete(s.users, old)
		s.users[new] = true
	} else if !s.users[new] {
		return fmt.Errorf("no such user %q", old)
	}
	return nil
}

func (s *fakeSystem) RenameCrontab(old, new string) error {
	return s.record("rename crontab " + old + " " + new)
}

// lookup resolves the names in users, as the current user (nil), and fails
// for any other name as user.Lookup does.
func (s *fakeSystem) lookup(name string) (*syscall.Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.users[name] {
		return nil, nil
	}
	return nil, user.UnknownUserError(name)
}

// setUser makes name resolve, or not.
func (s *fakeSystem) setUser(name string, exists bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if exists {
		s.users[name] = true
	} else {
		delete(s.users, name)
	}
}

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
