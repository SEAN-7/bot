package daemon

import (
	"os"
	"strings"
	"sync"
)

// fakeSystem records the root-only operations instead of performing them.
// PrepareHome still creates the home, so the agent's helper can seed it.
type fakeSystem struct {
	mu    sync.Mutex
	calls []string
}

func (s *fakeSystem) record(call string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
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
	s.record("home " + user)
	return os.MkdirAll(home, 0o755)
}

func (s *fakeSystem) SetMembers(group string, members []string) error {
	s.record("members " + group + " " + strings.Join(members, ","))
	return nil
}

func (s *fakeSystem) EnableLinger(user string) error  { s.record("linger " + user); return nil }
func (s *fakeSystem) DisableLinger(user string) error { s.record("unlinger " + user); return nil }
func (s *fakeSystem) StopProcesses(user string) error { s.record("stop " + user); return nil }
func (s *fakeSystem) RemoveCrontab(user string) error { s.record("crontab " + user); return nil }
