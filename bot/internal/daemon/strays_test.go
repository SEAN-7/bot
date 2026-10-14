package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const serviceCgroup = "0::/system.slice/agent.service\n"

// fakeProc is a /proc with only what findStrays reads.
type fakeProc struct {
	t    *testing.T
	root string
}

func newFakeProc(t *testing.T) *fakeProc {
	p := &fakeProc{t, t.TempDir()}
	p.write("self", "cgroup", serviceCgroup)
	os.Mkdir(filepath.Join(p.root, "net"), 0o755)
	return p
}

func (p *fakeProc) write(pid, name, content string) {
	p.t.Helper()
	dir := filepath.Join(p.root, pid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		p.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		p.t.Fatal(err)
	}
}

// add creates a process: its name, state letter, real and effective uid and
// cgroup file.
func (p *fakeProc) add(pid int, name, state string, ruid, euid int, cgroup string) {
	id := fmt.Sprint(pid)
	p.write(id, "comm", name+"\n")
	p.write(id, "status", fmt.Sprintf("Name:\t%s\nState:\t%s (x)\nTgid:\t%d\nUid:\t%d\t%d\t%d\t%d\nGid:\t5\t5\t5\t5\n", name, state, pid, ruid, euid, euid, euid))
	p.write(id, "cgroup", cgroup)
}

func TestFindStrays(t *testing.T) {
	p := newFakeProc(t)
	p.add(100, "sleep", "S", 1001, 1001, serviceCgroup)
	p.add(101, "su", "S", 1001, 0, serviceCgroup)                                                                      // setuid: real uid is the agent's
	p.add(102, "sleep", "S", 1002, 1002, serviceCgroup)                                                                // another agent
	p.add(103, "vite", "S", 1001, 1001, "0::/user.slice/user-1001.slice/user@1001.service/app.slice/run-u1.service\n") // systemd-run --user
	p.add(104, "agent", "S", 1001, 1001, serviceCgroup)                                                                // the agent's file helper
	p.add(105, "sh", "Z", 1001, 1001, serviceCgroup)                                                                   // exited, not yet reaped
	p.add(106, "bash", "S", 0, 0, serviceCgroup)                                                                       // the daemon's own
	p.write("107", "comm", "gone\n")                                                                                   // exited while being read
	got, err := findStrays(p.root, 1001, 104)
	if err != nil {
		t.Fatal(err)
	}
	want := []stray{{100, "sleep"}, {101, "su"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("strays %v, want %v", got, want)
	}
}

func TestFindStraysNeedsOwnCgroup(t *testing.T) {
	if _, err := findStrays(t.TempDir(), 1001, 0); err == nil {
		t.Fatal("no error without the daemon's own cgroup")
	}
}

func noHelper() int { return 0 }

func TestKillStraysUntilNoneLeft(t *testing.T) {
	defer func(d time.Duration) { killPause = d }(killPause)
	killPause = time.Millisecond
	p := newFakeProc(t)
	p.add(100, "bash", "S", 1001, 1001, serviceCgroup)
	p.add(101, "python3", "S", 1001, 1001, serviceCgroup)
	var killed []int
	defer func(f func(int) error) { killProcess = f }(killProcess)
	killProcess = func(pid int) error {
		killed = append(killed, pid)
		os.RemoveAll(filepath.Join(p.root, fmt.Sprint(pid)))
		if pid == 100 { // forked as it was killed
			p.add(102, "python3", "S", 1001, 1001, serviceCgroup)
		}
		return nil
	}
	names, err := killStrays(p.root, 1001, noHelper)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"bash", "python3", "python3"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("names %v, want %v", names, want)
	}
	if want := []int{100, 101, 102}; !reflect.DeepEqual(killed, want) {
		t.Fatalf("killed %v, want %v", killed, want)
	}
}

func TestKillStraysNoneIsQuiet(t *testing.T) {
	p := newFakeProc(t)
	defer func(f func(int) error) { killProcess = f }(killProcess)
	killProcess = func(pid int) error { t.Fatalf("killed %d", pid); return nil }
	if names, err := killStrays(p.root, 1001, noHelper); err != nil || names != nil {
		t.Fatalf("names %v err %v", names, err)
	}
}

func TestKillStraysGivesUp(t *testing.T) {
	defer func(d time.Duration) { killPause = d }(killPause)
	killPause = time.Millisecond
	p := newFakeProc(t)
	p.add(100, "sleep", "S", 1001, 1001, serviceCgroup)
	defer func(f func(int) error) { killProcess = f }(killProcess)
	killProcess = func(int) error { return nil } // survives
	names, err := killStrays(p.root, 1001, noHelper)
	if err == nil || !reflect.DeepEqual(names, []string{"sleep"}) {
		t.Fatalf("names %v err %v", names, err)
	}
}

func TestStrayNote(t *testing.T) {
	for _, c := range []struct {
		names []string
		want  string
	}{
		{nil, ""},
		{[]string{"python3"}, "Killed 1 process this command left running (python3)."},
		{[]string{"bash", "sleep", "sleep"}, "Killed 3 processes this command left running (bash, sleep ×2)."},
		{[]string{"a", "b", "c", "d", "e", "f", "a"}, "Killed 7 processes this command left running (a ×2, b, c, d, e, …)."},
	} {
		got := strayNote(c.names)
		if c.want == "" {
			if got != "" {
				t.Fatalf("%v: %q", c.names, got)
			}
			continue
		}
		if !strings.HasPrefix(got, c.want+" ") || !strings.Contains(got, "systemd-run --user") {
			t.Fatalf("%v: %q, want it to start %q and name systemd-run --user", c.names, got, c.want)
		}
	}
}
