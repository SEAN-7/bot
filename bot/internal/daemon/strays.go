package daemon

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// A command's processes end with it. Anything it leaves running would stay
// in the daemon's service and die unannounced at the next restart, so it is
// killed as soon as the command finishes, and the agent is told. Work meant
// to keep running belongs in the agent's own systemd (systemd-run --user),
// whose processes are in another cgroup and never touched here.

type stray struct {
	pid  int
	name string
}

// killProcess sends SIGKILL; tests replace it.
var killProcess = func(pid int) error { return syscall.Kill(pid, syscall.SIGKILL) }

// findStrays lists the processes under proc (normally /proc) that are in the
// daemon's own cgroup and whose real or effective uid is uid, other than
// keep (the agent's file helper). Exited processes are skipped: they cannot
// be killed, only reaped.
func findStrays(proc string, uid, keep int) ([]stray, error) {
	self, err := os.ReadFile(filepath.Join(proc, "self", "cgroup"))
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(proc)
	if err != nil {
		return nil, err
	}
	var found []stray
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == keep {
			continue
		}
		// A process can exit while it is read; it is skipped then.
		dir := filepath.Join(proc, e.Name())
		cgroup, err := os.ReadFile(filepath.Join(dir, "cgroup"))
		if err != nil || !bytes.Equal(cgroup, self) {
			continue
		}
		status, err := os.ReadFile(filepath.Join(dir, "status"))
		if err != nil || !runningAs(status, uid) {
			continue
		}
		name, _ := os.ReadFile(filepath.Join(dir, "comm"))
		found = append(found, stray{pid, strings.TrimSpace(string(name))})
	}
	return found, nil
}

// runningAs reports whether a /proc status file describes a live process
// whose real or effective uid is uid.
func runningAs(status []byte, uid int) bool {
	want := strconv.Itoa(uid)
	var live, mine bool
	for _, line := range strings.Split(string(status), "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) >= 2 && f[0] == "State:":
			live = f[1] != "Z" && f[1] != "X"
		case len(f) >= 3 && f[0] == "Uid:":
			mine = f[1] == want || f[2] == want
		}
	}
	return live && mine
}

// killStrays kills what findStrays reports, looking again until nothing is
// left, since a process can fork as it is killed. keep gives the helper's
// pid on each pass, as the helper may be restarted meanwhile. It returns the
// names of every process it found, in the order found.
func killStrays(proc string, uid int, keep func() int) ([]string, error) {
	var names []string
	seen := map[int]bool{}
	for i := 0; i < killTries; i++ {
		found, err := findStrays(proc, uid, keep())
		if err != nil || len(found) == 0 {
			return names, err
		}
		for _, s := range found {
			if !seen[s.pid] {
				seen[s.pid] = true
				names = append(names, s.name)
			}
			killProcess(s.pid)
		}
		time.Sleep(killPause)
	}
	return names, fmt.Errorf("processes of uid %d survived SIGKILL", uid)
}

// strayNote tells the agent what was killed: the count, and up to five
// names with how often each occurred.
func strayNote(names []string) string {
	if len(names) == 0 {
		return ""
	}
	var order []string
	count := map[string]int{}
	for _, n := range names {
		if count[n] == 0 {
			order = append(order, n)
		}
		count[n]++
	}
	var parts []string
	for i, n := range order {
		if i == 5 {
			parts = append(parts, "…")
			break
		}
		if count[n] > 1 {
			n += fmt.Sprintf(" ×%d", count[n])
		}
		parts = append(parts, n)
	}
	noun := "processes"
	if len(names) == 1 {
		noun = "process"
	}
	return fmt.Sprintf(MsgStraysFmt, len(names), noun, strings.Join(parts, ", "))
}
