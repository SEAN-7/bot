package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
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
	// RenameUser renames user old and its group to new, keeping their ids,
	// and makes home its home directory. A rerun finishes a rename that
	// stopped part way.
	RenameUser(old, new, home string) error
	// RenameCrontab gives old's crontab, if it has one, to new.
	RenameCrontab(old, new string) error
}

type execSystem struct {
	run      func(name string, args ...string) ([]byte, error)
	lookup   func(string) (*syscall.Credential, error)
	crontabs string // cron's spool of user crontabs
}

func newExecSystem() System {
	return &execSystem{
		run: func(name string, args ...string) ([]byte, error) {
			return exec.Command(name, args...).CombinedOutput()
		},
		lookup:   osuser.Lookup,
		crontabs: "/var/spool/cron/crontabs",
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
		// By effective and by real uid: a setuid program keeps one of them
		// as the user's, whether the user ran it or it runs as the user.
		s.run("pkill", "-KILL", "-u", user)
		s.run("pkill", "-KILL", "-U", user)
		if s.noProcess("-u", user) && s.noProcess("-U", user) {
			return nil
		}
		time.Sleep(killPause)
	}
	return fmt.Errorf("processes of %s survived SIGKILL", user)
}

// noProcess reports whether pgrep finds no process of user by the given
// uid flag; any other outcome counts as a process still running.
func (s *execSystem) noProcess(flag, user string) bool {
	_, err := s.run("pgrep", flag, user)
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == 1
}

func (s *execSystem) RemoveCrontab(user string) error {
	out, err := s.run("crontab", "-r", "-u", user)
	if err != nil && !strings.Contains(string(out), "no crontab") {
		return fmt.Errorf("crontab -r -u %s: %v: %s", user, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RenameUser skips each part already done: usermod refuses to change a user
// whose processes run, as the renamed user's do once it lingers again. A
// user or group already named new stops it before anything changes.
func (s *execSystem) RenameUser(old, new, home string) error {
	if _, err := s.lookup(old); err == nil {
		if _, err := s.lookup(new); err == nil {
			return fmt.Errorf("users %s and %s both exist", old, new)
		}
		group, err := s.getent("group", new)
		if err != nil {
			return err
		}
		if group != "" {
			return fmt.Errorf("group %s already exists", new)
		}
		if err := s.do("usermod", "-l", new, old); err != nil {
			return err
		}
	}
	group, err := s.getent("group", old)
	if err != nil {
		return err
	}
	if group != "" {
		if err := s.do("groupmod", "-n", new, old); err != nil {
			return err
		}
	}
	entry, err := s.getent("passwd", new)
	if err != nil {
		return err
	}
	if entry == "" {
		return fmt.Errorf("no such user %q", new)
	}
	if fields := strings.Split(entry, ":"); len(fields) > 5 && fields[5] == home {
		return nil
	}
	return s.do("usermod", "-d", home, new)
}

// getent returns key's entry in database, or "" when it has none.
func (s *execSystem) getent(database, key string) (string, error) {
	out, err := s.run("getent", database, key)
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 2 {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("getent %s %s: %v: %s", database, key, err, strings.TrimSpace(string(out)))
	}
	line, _, _ := strings.Cut(string(out), "\n")
	return line, nil
}

// RenameCrontab renames the crontab file, which cron reads by user name;
// its owner is the uid, which the rename keeps. It never replaces one.
func (s *execSystem) RenameCrontab(old, new string) error {
	from, to := filepath.Join(s.crontabs, old), filepath.Join(s.crontabs, new)
	if _, err := os.Lstat(from); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err := os.Lstat(to); err == nil {
		return fmt.Errorf("%s and %s both exist", from, to)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Rename(from, to)
}

func (s *execSystem) do(name string, args ...string) error {
	if out, err := s.run(name, args...); err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
