package shell

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

func opts(t *testing.T, timeout time.Duration) Options {
	dir := t.TempDir()
	return Options{Dir: dir, Home: dir, Env: Env(dir, "agent", os.Getuid()), Timeout: timeout}
}

func TestRunCapturesOutputAndExit(t *testing.T) {
	r, err := Run(context.Background(), "echo out; echo err >&2; exit 3", opts(t, 5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if r.ExitCode != 3 || !strings.Contains(r.Output, "out") || !strings.Contains(r.Output, "err") || r.TimedOut {
		t.Fatalf("got %+v", r)
	}
}

func TestRunUsesHomeAndDir(t *testing.T) {
	o := opts(t, 5*time.Second)
	r, err := Run(context.Background(), "pwd -P; echo $HOME", o)
	if err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(o.Dir)
	lines := strings.Split(strings.TrimSpace(r.Output), "\n")
	if len(lines) != 2 || lines[0] != real || lines[1] != o.Home {
		t.Fatalf("got %q", r.Output)
	}
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func TestTimeoutKillsProcessGroup(t *testing.T) {
	o := opts(t, 300*time.Millisecond)
	start := time.Now()
	r, err := Run(context.Background(), "sleep 30 & echo $! > bg.pid; sleep 30; echo never", o)
	if err != nil {
		t.Fatal(err)
	}
	if !r.TimedOut || strings.Contains(r.Output, "never") || time.Since(start) > 2*time.Second {
		t.Fatalf("got %+v after %s", r, time.Since(start))
	}
	data, err := os.ReadFile(filepath.Join(o.Dir, "bg.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	deadline := time.Now().Add(time.Second)
	for alive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if alive(pid) {
		t.Fatalf("background process %d survived the timeout", pid)
	}
}

func TestBackgroundChildDoesNotBlock(t *testing.T) {
	start := time.Now()
	r, err := Run(context.Background(), "sleep 30 & echo started", opts(t, 10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 3*time.Second || !strings.Contains(r.Output, "started") || r.TimedOut || r.ExitCode != 0 {
		t.Fatalf("got %+v after %s", r, time.Since(start))
	}
}

func TestCancelKillsCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	Run(ctx, "sleep 30", opts(t, 10*time.Second))
	if time.Since(start) > 2*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
}

func TestBinaryOutput(t *testing.T) {
	r, err := Run(context.Background(), `printf '\377\376ok'`, opts(t, 5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(r.Output) || !strings.Contains(r.Output, "ok") {
		t.Fatalf("got %q", r.Output)
	}
}

func TestEnv(t *testing.T) {
	env := strings.Join(Env("/home/agent", "agent", 1001), "\n")
	for _, want := range []string{"HOME=/home/agent", "USER=agent", "LOGNAME=agent", "PATH=/home/agent/bin:/usr/local/bin:/usr/bin:/bin", "XDG_RUNTIME_DIR=/run/user/1001", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1001/bus", "TERM=dumb"} {
		if !strings.Contains(env, want) {
			t.Errorf("missing %s", want)
		}
	}
}

func TestClipUnderLimit(t *testing.T) {
	if out, clipped := Clip("abc", 10); out != "abc" || clipped {
		t.Fatalf("got %q %v", out, clipped)
	}
}

func TestClipKeepsHeadAndTail(t *testing.T) {
	out, clipped := Clip(strings.Repeat("a", 20000)+"END", 16384)
	if !clipped || !strings.HasSuffix(out, "END") || !strings.HasPrefix(out, "aaa") || !strings.Contains(out, "bytes clipped") || len(out) > 16500 {
		t.Fatalf("clipped=%v len=%d", clipped, len(out))
	}
}

func TestClipKeepsValidUTF8(t *testing.T) {
	out, clipped := Clip(strings.Repeat("é", 9000), 16384)
	if !clipped || !utf8.ValidString(out) {
		t.Fatalf("clipped=%v valid=%v", clipped, utf8.ValidString(out))
	}
}
