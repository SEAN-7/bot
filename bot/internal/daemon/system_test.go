package daemon

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
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

// exitStatus is the error a process exiting with code produces.
func exitStatus(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit "+strconv.Itoa(code)).Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatal(err)
	}
	return err
}

// The daemon creates and deletes agent users itself; the agent id rides in
// the GECOS field so the passwd file is the only record.
func TestExecSystemUsers(t *testing.T) {
	var calls [][]string
	var fail error
	s := &execSystem{
		run: func(name string, args ...string) ([]byte, error) {
			calls = append(calls, append([]string{name}, args...))
			return nil, fail
		},
	}
	last := func() []string { return calls[len(calls)-1] }
	if err := s.AddUser("agent-1", "main/agents/r", "/home/agent-1"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"useradd", "--no-create-home", "--home-dir", "/home/agent-1", "--user-group", "--shell", "/bin/bash", "--comment", "main/agents/r", "agent-1"}; !reflect.DeepEqual(last(), want) {
		t.Fatal(last())
	}
	if err := s.SetComment("agent-1", "main"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"usermod", "-c", "main", "agent-1"}; !reflect.DeepEqual(last(), want) {
		t.Fatal(last())
	}
	if err := s.DeleteUser("agent-1"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"userdel", "-r", "agent-1"}; !reflect.DeepEqual(last(), want) {
		t.Fatal(last())
	}
	fail = exitStatus(t, 6) // no such user: already gone
	if err := s.DeleteUser("agent-1"); err != nil {
		t.Fatalf("missing user should succeed: %v", err)
	}
	fail = exitStatus(t, 8) // user logged in
	if err := s.DeleteUser("agent-1"); err == nil {
		t.Fatal("other userdel failures should be reported")
	}
}

func TestExecSystemUsersParsesPasswd(t *testing.T) {
	passwd := filepath.Join(t.TempDir(), "passwd")
	os.WriteFile(passwd, []byte(strings.Join([]string{
		"root:x:0:0:root:/root:/bin/bash",
		"agent-1:x:1001:1001:main:/home/agent-1:/bin/bash",
		"agent-provider:x:998:998::/:/usr/sbin/nologin",
		"agent-7:x:1007:1007:main/agents/r,extra:/home/agent-7:/bin/bash", // the whole field is the id
		"agent-x:x:1008:1008:not an agent:/home/agent-x:/bin/bash",
		"agent-3:x:1003:1003::/home/agent-3:/bin/bash",
		"",
	}, "\n")), 0o644)
	s := &execSystem{passwd: passwd}
	got, err := s.Users("agent-")
	if err != nil {
		t.Fatal(err)
	}
	want := []User{{"agent-1", "main"}, {"agent-3", ""}, {"agent-7", "main/agents/r,extra"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
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
