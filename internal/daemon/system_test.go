package daemon

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

// exitStatus1 is the error pgrep's "no process matched" exit produces.
func exitStatus1(t *testing.T) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit 1").Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatal(err)
	}
	return err
}

func TestExecSystemCommands(t *testing.T) {
	var calls [][]string
	out := ""
	var fail error
	s := &execSystem{
		run: func(name string, args ...string) ([]byte, error) {
			calls = append(calls, append([]string{name}, args...))
			return []byte(out), fail
		},
		lookup: func(string) (*syscall.Credential, error) { return &syscall.Credential{Uid: 1003}, nil },
	}
	last := func() []string { return calls[len(calls)-1] }

	if err := s.SetMembers("agent-3", []string{"agent", "agent-1"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"gpasswd", "-M", "agent,agent-1", "agent-3"}; !reflect.DeepEqual(last(), want) {
		t.Fatal(last())
	}
	if err := s.SetMembers("agent-3", nil); err != nil {
		t.Fatal(err)
	}
	if want := []string{"gpasswd", "-M", "", "agent-3"}; !reflect.DeepEqual(last(), want) {
		t.Fatal(last())
	}
	if err := s.EnableLinger("agent-3"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"loginctl", "enable-linger", "agent-3"}; !reflect.DeepEqual(last(), want) {
		t.Fatal(last())
	}
	if err := s.DisableLinger("agent-3"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"loginctl", "disable-linger", "agent-3"}; !reflect.DeepEqual(last(), want) {
		t.Fatal(last())
	}
	if err := s.RemoveCrontab("agent-3"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"crontab", "-r", "-u", "agent-3"}; !reflect.DeepEqual(last(), want) {
		t.Fatal(last())
	}

	out, fail = "no crontab for agent-3\n", errors.New("exit status 1")
	if err := s.RemoveCrontab("agent-3"); err != nil {
		t.Fatalf("missing crontab should succeed: %v", err)
	}
	out = "permission denied"
	if err := s.RemoveCrontab("agent-3"); err == nil {
		t.Fatal("other crontab failures should be reported")
	}
}

func TestExecSystemStopProcesses(t *testing.T) {
	var calls [][]string
	pgreps := 0
	s := &execSystem{
		run: func(name string, args ...string) ([]byte, error) {
			calls = append(calls, append([]string{name}, args...))
			switch name {
			case "systemctl":
				return nil, errors.New("not loaded") // ignored
			case "pgrep":
				pgreps++
				if pgreps < 4 {
					return nil, nil // still running
				}
				return nil, exitStatus1(t)
			}
			return nil, nil
		},
		lookup: func(string) (*syscall.Credential, error) { return &syscall.Credential{Uid: 1003}, nil },
	}
	if err := s.StopProcesses("agent-3"); err != nil {
		t.Fatal(err)
	}
	// The stop is queued, not waited for: the kill loop ends everything,
	// and the poll goroutine must not wait for systemd's stop timeout.
	if want := []string{"systemctl", "stop", "--no-block", "user@1003.service"}; !reflect.DeepEqual(calls[0], want) {
		t.Fatal(calls[0])
	}
	// By effective and by real uid, so a process that changed either one
	// (a setuid program, run by or as the user) is found.
	want := [][]string{
		{"pkill", "-KILL", "-u", "agent-3"},
		{"pkill", "-KILL", "-U", "agent-3"},
		{"pgrep", "-u", "agent-3"},
	}
	if !reflect.DeepEqual(calls[1:4], want) {
		t.Fatal(calls[1:4])
	}
	// Rounds repeat until neither check finds a process: three rounds
	// still found one by effective uid; the fourth found none by either.
	if pgreps != 5 {
		t.Fatalf("pgrep ran %d times: %v", pgreps, calls)
	}
	if last := calls[len(calls)-1]; !reflect.DeepEqual(last, []string{"pgrep", "-U", "agent-3"}) {
		t.Fatal(last)
	}
}

func TestExecSystemStopProcessesGivesUp(t *testing.T) {
	defer func(d time.Duration) { killPause = d }(killPause)
	killPause = time.Millisecond
	s := &execSystem{
		run:    func(name string, args ...string) ([]byte, error) { return nil, nil },
		lookup: func(string) (*syscall.Credential, error) { return &syscall.Credential{Uid: 1003}, nil },
	}
	if err := s.StopProcesses("agent-3"); err == nil {
		t.Fatal("expected an error when processes survive")
	}
}

// fakeAccounts is a passwd and group database behind getent, usermod and
// groupmod, for the real System's commands.
type fakeAccounts struct {
	t      *testing.T
	homes  map[string]string // user → home
	groups map[string]bool
	calls  []string // the commands that change something
}

func (a *fakeAccounts) system() *execSystem {
	return &execSystem{run: a.run, lookup: func(name string) (*syscall.Credential, error) {
		if _, ok := a.homes[name]; ok {
			return &syscall.Credential{Uid: 1001}, nil
		}
		return nil, errors.New("unknown user " + name)
	}}
}

func (a *fakeAccounts) run(name string, args ...string) ([]byte, error) {
	notFound := func() ([]byte, error) {
		err := exec.Command("sh", "-c", "exit 2").Run()
		return nil, err
	}
	switch cmd := name + " " + strings.Join(args[:len(args)-1], " "); {
	case cmd == "getent passwd":
		home, ok := a.homes[args[1]]
		if !ok {
			return notFound()
		}
		return []byte(args[1] + ":x:1001:1001::" + home + ":/bin/bash\n"), nil
	case cmd == "getent group":
		if !a.groups[args[1]] {
			return notFound()
		}
		return []byte(args[1] + ":x:1001:\n"), nil
	case strings.HasPrefix(cmd, "usermod -l "):
		a.homes[args[1]] = a.homes[args[2]]
		delete(a.homes, args[2])
	case strings.HasPrefix(cmd, "groupmod -n "):
		a.groups[args[1]] = true
		delete(a.groups, args[2])
	case strings.HasPrefix(cmd, "usermod -d "):
		a.homes[args[2]] = args[1]
	default:
		a.t.Fatalf("unexpected command %s %q", name, args)
	}
	a.calls = append(a.calls, name+" "+strings.Join(args, " "))
	return nil, nil
}

func TestExecSystemRenameUser(t *testing.T) {
	a := &fakeAccounts{t: t, homes: map[string]string{"agent": "/home/agent"}, groups: map[string]bool{"agent": true}}
	s := a.system()
	if err := s.RenameUser("agent", "agent-16", "/home/agent-16"); err != nil {
		t.Fatal(err)
	}
	want := []string{"usermod -l agent-16 agent", "groupmod -n agent-16 agent", "usermod -d /home/agent-16 agent-16"}
	if !reflect.DeepEqual(a.calls, want) {
		t.Fatal(a.calls)
	}
	if a.homes["agent-16"] != "/home/agent-16" || !a.groups["agent-16"] || a.groups["agent"] {
		t.Fatal(a.homes, a.groups)
	}
	// Done: nothing runs again. usermod refuses to touch a user whose
	// processes run, as the renamed user's do once it lingers again.
	a.calls = nil
	if err := s.RenameUser("agent", "agent-16", "/home/agent-16"); err != nil || a.calls != nil {
		t.Fatal(err, a.calls)
	}
	// Stopped after renaming the user: the rest is finished.
	a = &fakeAccounts{t: t, homes: map[string]string{"agent-16": "/home/agent"}, groups: map[string]bool{"agent": true}}
	if err := a.system().RenameUser("agent", "agent-16", "/home/agent-16"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"groupmod -n agent-16 agent", "usermod -d /home/agent-16 agent-16"}; !reflect.DeepEqual(a.calls, want) {
		t.Fatal(a.calls)
	}
	// A user or group already named new: nothing is changed.
	for _, a := range []*fakeAccounts{
		{t: t, homes: map[string]string{"agent": "/home/agent", "agent-16": "/x"}, groups: map[string]bool{"agent": true}},
		{t: t, homes: map[string]string{"agent": "/home/agent"}, groups: map[string]bool{"agent": true, "agent-16": true}},
	} {
		if err := a.system().RenameUser("agent", "agent-16", "/home/agent-16"); err == nil || a.calls != nil {
			t.Fatal(err, a.calls)
		}
	}
	// Neither user exists.
	a = &fakeAccounts{t: t, homes: map[string]string{}, groups: map[string]bool{}}
	if err := a.system().RenameUser("agent", "agent-16", "/home/agent-16"); err == nil {
		t.Fatal("renamed a missing user")
	}
}

func TestExecSystemRenameCrontab(t *testing.T) {
	dir := t.TempDir()
	s := &execSystem{crontabs: dir}
	old, new := filepath.Join(dir, "agent"), filepath.Join(dir, "agent-16")
	if err := s.RenameCrontab("agent", "agent-16"); err != nil {
		t.Fatalf("no crontab: %v", err)
	}
	os.WriteFile(old, []byte("* * * * * true\n"), 0o600)
	if err := s.RenameCrontab("agent", "agent-16"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(new); err != nil || string(data) != "* * * * * true\n" {
		t.Fatal(string(data), err)
	}
	if err := s.RenameCrontab("agent", "agent-16"); err != nil {
		t.Fatalf("already renamed: %v", err)
	}
	// Both: neither is replaced.
	os.WriteFile(old, []byte("old\n"), 0o600)
	if err := s.RenameCrontab("agent", "agent-16"); err == nil {
		t.Fatal("replaced a crontab")
	}
	if data, _ := os.ReadFile(new); string(data) != "* * * * * true\n" {
		t.Fatal(string(data))
	}
}

func TestExecSystemStopUserManager(t *testing.T) {
	var calls [][]string
	var fail error
	s := &execSystem{
		run: func(name string, args ...string) ([]byte, error) {
			calls = append(calls, append([]string{name}, args...))
			return nil, fail
		},
		lookup: func(string) (*syscall.Credential, error) { return &syscall.Credential{Uid: 1001}, nil },
	}
	if err := s.StopUserManager("agent"); err != nil {
		t.Fatal(err)
	}
	// Waited for, so the user's services shut down cleanly, but bounded.
	if want := []string{"timeout", "180", "systemctl", "stop", "user@1001.service"}; !reflect.DeepEqual(calls[0], want) {
		t.Fatal(calls[0])
	}
	fail = exec.Command("sh", "-c", "exit 124").Run()
	if err := s.StopUserManager("agent"); err == nil || !strings.Contains(err.Error(), "did not stop within") {
		t.Fatalf("timeout: %v", err)
	}
}
