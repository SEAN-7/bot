package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/SEAN-7/bot/internal/osuser"
	"github.com/SEAN-7/bot/internal/shell"
)

// System is the daemon's root-only side effects, behind an interface so tests
// that cannot be root can substitute a fake.
type System interface {
	// AddUser creates the agent user name with its own group, home as its
	// home directory (not created) and the agent id as its GECOS comment.
	AddUser(name, id, home string) error
	// DeleteUser removes the user, its group and its home. A user that is
	// already gone is not an error.
	DeleteUser(name string) error
	// SetComment makes id the user's GECOS comment.
	SetComment(name, id string) error
	// Users lists the users named prefix followed by digits, with the agent
	// id each carries in its comment ("" for none), by name.
	Users(prefix string) ([]User, error)
	// PrepareHome creates home if missing and makes the user own it, setgid.
	PrepareHome(user, home string) error
	// ShareInbox grants (or revokes) uid write and search access to the
	// inbox in home, as user, who owns it: setfacl, run as the owner so
	// that a planted link reaches nothing root could.
	ShareInbox(user, home string, uid int, grant bool) error
	// SetMembers makes members the whole membership of group.
	SetMembers(group string, members []string) error
	EnableLinger(user string) error
	DisableLinger(user string) error
	// StopProcesses ends the user's systemd instance and every process it owns.
	StopProcesses(user string) error
	RemoveCrontab(user string) error
}

// User is an agent user and the agent id in its comment.
type User struct{ Name, ID string }

type execSystem struct {
	run    func(name string, args ...string) ([]byte, error)
	lookup func(string) (*syscall.Credential, error)
	passwd string // the passwd file Users reads
}

func newExecSystem() System {
	return &execSystem{
		run: func(name string, args ...string) ([]byte, error) {
			return exec.Command(name, args...).CombinedOutput()
		},
		lookup: osuser.Lookup,
		passwd: "/etc/passwd",
	}
}

func (s *execSystem) AddUser(name, id, home string) error {
	return s.do("useradd", "--no-create-home", "--home-dir", home, "--user-group", "--shell", "/bin/bash", "--comment", id, name)
}

// DeleteUser runs userdel -r; its exit status 6 means no such user.
func (s *execSystem) DeleteUser(name string) error {
	out, err := s.run("userdel", "-r", name)
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 6 {
		return nil
	}
	if err != nil {
		return fmt.Errorf("userdel -r %s: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s *execSystem) SetComment(name, id string) error {
	return s.do("usermod", "-c", id, name)
}

// Users reads the passwd file: agents are local users, and the file is the
// record. The comment is the GECOS field up to its first comma.
func (s *execSystem) Users(prefix string) ([]User, error) {
	data, err := os.ReadFile(s.passwd)
	if err != nil {
		return nil, err
	}
	var users []User
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Split(line, ":")
		if len(f) < 5 || !isAgentUser(f[0], prefix) {
			continue
		}
		comment, _, _ := strings.Cut(f[4], ",")
		users = append(users, User{f[0], comment})
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Name < users[j].Name })
	return users, nil
}

// isAgentUser reports whether name is prefix followed by one or more digits.
func isAgentUser(name, prefix string) bool {
	digits, ok := strings.CutPrefix(name, prefix)
	if !ok || digits == "" {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
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

func (s *execSystem) ShareInbox(user, home string, uid int, grant bool) error {
	cred, err := s.lookup(user)
	if err != nil {
		return err
	}
	command := fmt.Sprintf("setfacl -x u:%d ~/inbox", uid)
	if grant {
		command = fmt.Sprintf("setfacl -m u:%d:wx ~/inbox", uid)
	}
	result, err := shell.Run(context.Background(), command, shell.Options{
		Dir: home, Home: home, Env: shell.Env(home, user, credUID(cred)), User: cred, Timeout: 30 * time.Second,
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("%s: exit status %d: %s", command, result.ExitCode, strings.TrimSpace(result.Output))
	}
	return nil
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

func (s *execSystem) do(name string, args ...string) error {
	if out, err := s.run(name, args...); err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
