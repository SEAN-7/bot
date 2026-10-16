package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SEAN-7/bot/internal/layout"
)

// testEnv is an agent's environment (or root's, with euid 0) under temporary
// directories.
func testEnv(t *testing.T, euid int) (env, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	l := layout.Layout{Homes: filepath.Join(root, "home"), Etc: filepath.Join(root, "etc"), Lib: filepath.Join(root, "lib"), Log: filepath.Join(root, "log")}
	out := &bytes.Buffer{}
	return env{l: l, euid: euid, out: out}, out
}

// captureStderr runs f and returns what it wrote to os.Stderr.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	f()
	os.Stderr = old
	w.Close()
	data, _ := io.ReadAll(r)
	return string(data)
}

func TestRunDispatch(t *testing.T) {
	for _, c := range []struct {
		name string
		euid int
		args []string
		err  string
		out  string
	}{
		{"no arguments", 1000, nil, "", "Usage:"},
		{"help", 1000, []string{"help"}, "", "/var/lib/agent/manual/manual.md"},
		{"unknown command", 1000, []string{"bogus"}, `unknown command "bogus"`, ""},
		{"daemon as an agent", 1000, []string{"daemon"}, "must run as root", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, out := testEnv(t, c.euid)
			err := run(c.args, e)
			if c.err == "" && err != nil {
				t.Fatalf("got error %v", err)
			}
			if c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)) {
				t.Fatalf("got error %v, want one containing %q", err, c.err)
			}
			if !strings.Contains(out.String(), c.out) {
				t.Fatalf("stdout %q lacks %q", out.String(), c.out)
			}
		})
	}
}

// The commands files replaced in 0.7.0 are gone.
func TestReplacedCommandsGone(t *testing.T) {
	for _, args := range [][]string{{"new", "x"}, {"remove", "x"}, {"send", "x", "hi"}, {"message", "hi"}, {"messages"}, {"log"}, {"version"}, {"notes"}, {"provider", "describe"}} {
		e, _ := testEnv(t, 1000)
		err := run(args, e)
		if err == nil || !(strings.Contains(err.Error(), "unknown command") || strings.Contains(err.Error(), "usage: agent provider install")) {
			t.Errorf("%v: %v", args, err)
		}
	}
}

func TestProviderInstallUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"--help"}, {"replay"}, {"replay", "f", "extra"}} {
		e, out := testEnv(t, 0)
		var err error
		stderr := captureStderr(t, func() { err = run(append([]string{"provider", "install"}, args...), e) })
		if err == nil || err.Error() != "usage: agent provider install NAME FILE" || out.Len() != 0 || stderr != "" {
			t.Errorf("%q: err %v, stdout %q, stderr %q", args, err, out.String(), stderr)
		}
	}
	e, _ := testEnv(t, 1000)
	if err := run([]string{"provider", "install", "replay", "f"}, e); err == nil || !strings.Contains(err.Error(), "needs root") {
		t.Errorf("as an agent: %v", err)
	}
}
