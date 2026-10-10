// Package shell runs one agent command with a timeout, in its own process
// group, as the agent user.
package shell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// captureLimit bounds the output kept in memory and saved to ~/.outputs.
const captureLimit = 8 << 20

type Options struct {
	Dir     string
	Home    string
	Env     []string
	User    *syscall.Credential // nil runs as the current user
	Timeout time.Duration
}

type Result struct {
	Output   string
	ExitCode int
	TimedOut bool
}

// Env is the environment for agent commands. XDG_RUNTIME_DIR and the bus
// address let `systemd-run --user` reach the agent's lingering user manager.
func Env(home, user string, uid int) []string {
	run := "/run/user/" + strconv.Itoa(uid)
	return []string{
		"HOME=" + home,
		"USER=" + user,
		"LOGNAME=" + user,
		"PATH=" + home + "/bin:/usr/local/bin:/usr/bin:/bin",
		"XDG_RUNTIME_DIR=" + run,
		"DBUS_SESSION_BUS_ADDRESS=unix:path=" + run + "/bus",
		"TERM=dumb",
	}
}

type capped struct {
	data    []byte
	dropped int
}

func (c *capped) Write(p []byte) (int, error) {
	room := captureLimit - len(c.data)
	if room >= len(p) {
		c.data = append(c.data, p...)
	} else {
		if room > 0 {
			c.data = append(c.data, p[:room]...)
		}
		c.dropped += len(p) - max(room, 0)
	}
	return len(p), nil
}

func shellPath() string {
	if _, err := os.Stat("/bin/bash"); err == nil {
		return "/bin/bash"
	}
	return "/bin/sh"
}

// Run executes command and waits for the shell to exit. Background children
// may keep running; they cannot hold Run open, because pipes are abandoned
// one second after the shell exits. On timeout or cancellation the whole
// process group is killed.
func Run(ctx context.Context, command string, o Options) (Result, error) {
	cmd := exec.Command(shellPath(), "-c", command)
	cmd.Dir = o.Dir
	cmd.Env = o.Env
	out := &capped{}
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Credential: o.User}
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return Result{}, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(o.Timeout)
	defer timer.Stop()
	var result Result
	var err error
	select {
	case err = <-done:
	case <-timer.C:
		result.TimedOut = true
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		err = <-done
	case <-ctx.Done():
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		err = <-done
	}
	var exit *exec.ExitError
	switch {
	case err == nil || errors.Is(err, exec.ErrWaitDelay):
	case errors.As(err, &exit):
	default:
		return Result{}, err
	}
	result.ExitCode = cmd.ProcessState.ExitCode()
	result.Output = strings.ToValidUTF8(string(out.data), "�")
	if out.dropped > 0 {
		result.Output += fmt.Sprintf("\n[output truncated: %d further bytes not captured]\n", out.dropped)
	}
	return result, nil
}

// Clip keeps the first and last limit/2 bytes of s, cut on rune boundaries.
func Clip(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	head := limit / 2
	for head > 0 && !utf8.RuneStart(s[head]) {
		head--
	}
	tail := len(s) - limit/2
	for tail < len(s) && !utf8.RuneStart(s[tail]) {
		tail++
	}
	return fmt.Sprintf("%s\n[... %d bytes clipped ...]\n%s", s[:head], tail-head, s[tail:]), true
}
