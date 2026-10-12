package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/SEAN-7/bot/internal/osuser"
)

// System is the daemon's root-only side effects, behind an interface so tests
// that cannot be root can substitute a fake.
type System interface {
	// PrepareHome creates home if missing and makes the user own it, setgid.
	PrepareHome(user, home string) error
	// SetMembers makes members the whole membership of group.
	SetMembers(group string, members []string) error
	EnableLinger(user string) error
	DisableLinger(user string) error
	// StopProcesses ends the user's systemd instance and every process it owns.
	StopProcesses(user string) error
	RemoveCrontab(user string) error
}

type execSystem struct {
	run    func(name string, args ...string) ([]byte, error)
	lookup func(string) (*syscall.Credential, error)
}

func newExecSystem() System {
	return &execSystem{
		run: func(name string, args ...string) ([]byte, error) {
			return exec.Command(name, args...).CombinedOutput()
		},
		lookup: osuser.Lookup,
	}
}

// killPause is the wait between kill attempts; tests shorten it.
var killPause = 100 * time.Millisecond

const killTries = 50

func (s *execSystem) PrepareHome(user, home string) error {
	cred, err := s.lookup(user)
	if err != nil {
		return err
	}
	if cred == nil {
		return fmt.Errorf("no such user %q", user)
	}
	if err := os.Mkdir(home, 0o750); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	if err := os.Lchown(home, int(cred.Uid), int(cred.Gid)); err != nil {
		return err
	}
	// The parent is root-owned, so home cannot have been swapped for a link
	// to somewhere else; check anyway before following it with chmod.
	info, err := os.Lstat(home)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", home)
	}
	return os.Chmod(home, 0o750|fs.ModeSetgid)
}

func (s *execSystem) SetMembers(group string, members []string) error {
	return s.do("gpasswd", "-M", strings.Join(members, ","), group)
}

func (s *execSystem) EnableLinger(user string) error {
	return s.do("loginctl", "enable-linger", user)
}

func (s *execSystem) DisableLinger(user string) error {
	return s.do("loginctl", "disable-linger", user)
}

func (s *execSystem) StopProcesses(user string) error {
	if cred, err := s.lookup(user); err == nil && cred != nil {
		// Queued, not waited for: the poll goroutine must not wait for
		// systemd's stop timeout, and the kill loop ends everything anyway.
		s.run("systemctl", "stop", "--no-block", fmt.Sprintf("user@%d.service", cred.Uid)) // may not be running
	}
	for i := 0; i < killTries; i++ {
		// By real uid: a process that changed only its effective uid (a
		// setuid program) is still the user's.
		s.run("pkill", "-KILL", "-U", user)
		_, err := s.run("pgrep", "-U", user)
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return nil // no process left
		}
		time.Sleep(killPause)
	}
	return fmt.Errorf("processes of %s survived SIGKILL", user)
}

func (s *execSystem) RemoveCrontab(user string) error {
	out, err := s.run("crontab", "-r", "-u", user)
	if err != nil && !strings.Contains(string(out), "no crontab") {
		return fmt.Errorf("crontab -r -u %s: %v: %s", user, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s *execSystem) do(name string, args ...string) error {
	if out, err := s.run(name, args...); err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
