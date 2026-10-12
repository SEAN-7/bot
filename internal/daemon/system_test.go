package daemon

import (
	"errors"
	"os/exec"
	"reflect"
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
				if pgreps < 3 {
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
	// By real uid, so a process that changed only its effective uid is found.
	if want := []string{"pkill", "-KILL", "-U", "agent-3"}; !reflect.DeepEqual(calls[1], want) {
		t.Fatal(calls[1])
	}
	if want := []string{"pgrep", "-U", "agent-3"}; !reflect.DeepEqual(calls[2], want) {
		t.Fatal(calls[2])
	}
	if pgreps != 3 {
		t.Fatalf("pgrep ran %d times", pgreps)
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
