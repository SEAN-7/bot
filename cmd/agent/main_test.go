package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SEAN-7/bot/internal/cli"
	"github.com/SEAN-7/bot/internal/layout"
)

// testEnv is an agent's environment (or root's, with euid 0) under temporary
// directories, with one provider "p" whose model "m" takes an "effort" option.
func testEnv(t *testing.T, euid int) (env, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	l := layout.Layout{Homes: filepath.Join(root, "home"), Etc: filepath.Join(root, "etc"), Lib: filepath.Join(root, "lib"), Log: filepath.Join(root, "log")}
	home := filepath.Join(root, "me")
	os.MkdirAll(home, 0o755)
	os.MkdirAll(l.Lib, 0o755)
	cache := `{"providers":{"p":{"models":[{"id":"m","context_tokens":1000,"options":{"effort":{"values":["low","high"]}}},{"id":"m2","context_tokens":1000}]}}}`
	if err := os.WriteFile(l.ProvidersCache(), []byte(cache), 0o644); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	return env{l: l, euid: euid, home: home, out: out, wait: 5 * time.Millisecond, poll: time.Millisecond}, out
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
		err  string // substring of the error; "" for success
		out  string // substring of stdout
	}{
		{"no arguments", 1000, nil, "", "Usage:"},
		{"unknown command", 1000, []string{"bogus"}, `unknown command "bogus"`, ""},
		{"send without name as root", 0, []string{"send", "hi"}, "name an agent", ""},
		{"send without name as agent", 1000, []string{"send", "hi"}, "usage: agent send NAME", ""},
		{"send nothing as agent", 1000, []string{"send"}, "usage: agent send NAME", ""},
		{"message as root", 0, []string{"message", "hi"}, "root has no owner", ""},
		{"message without text", 1000, []string{"message"}, "usage: agent message", ""},
		{"new --help", 1000, []string{"new", "--help"}, "", "Usage: agent new NAME"},
		{"new -h", 0, []string{"new", "-h"}, "", "Usage: agent new NAME"},
		{"new x --help", 1000, []string{"new", "x", "--help"}, "", "Usage: agent new NAME"},
		{"new without name", 1000, []string{"new"}, "usage: agent new NAME", ""},
		{"remove without name", 1000, []string{"remove"}, "usage: agent remove NAME", ""},
		{"messages with two names", 1000, []string{"messages", "a", "b"}, "usage: agent messages [NAME]", ""},
		{"log with two names", 1000, []string{"log", "a", "b"}, "usage: agent log [NAME]", ""},
		{"messages as root without name", 0, []string{"messages"}, "root has no owner", ""},
		{"help", 1000, []string{"help"}, "", "Sharing a directory"},
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

func TestNewHelpPrintsNewUsage(t *testing.T) {
	e, out := testEnv(t, 1000)
	var err error
	stderr := captureStderr(t, func() { err = run([]string{"new", "--help"}, e) })
	if err != nil || out.String() != cli.NewUsage || stderr != "" {
		t.Fatalf("err %v, stdout %q, stderr %q", err, out.String(), stderr)
	}
}

func TestNewFlagErrorsPrintOnce(t *testing.T) {
	for _, args := range [][]string{{"new", "--bogus", "x"}, {"new", "x", "--bogus"}, {"new", "x", "--model"}} {
		e, out := testEnv(t, 0)
		var err error
		stderr := captureStderr(t, func() { err = run(args, e) })
		if err == nil || stderr != "" || out.Len() != 0 {
			t.Errorf("%v: err %v, stderr %q, stdout %q", args, err, stderr, out.String())
		}
		if _, statErr := os.Stat(filepath.Join(e.l.RootHome(), "agents")); statErr == nil {
			t.Errorf("%v created agents/", args)
		}
	}
	e, _ := testEnv(t, 0)
	err := run([]string{"new", "--bogus", "x"}, e)
	if err == nil || err.Error() != "flag provided but not defined: -bogus" {
		t.Fatalf("got %v", err)
	}
}

// Flags that take a value consume the next argument; the others do not.
func TestNewFlagPositions(t *testing.T) {
	for _, args := range [][]string{
		{"new", "x", "--provider", "p", "--model", "m"},
		{"new", "--model", "m", "x"},
		{"new", "--model=m", "x", "--provider=p"},
		{"new", "x", "--model", "m", "--option", "effort=high"},
		{"new", "x", "--model", "m", "--option=effort=high"},
	} {
		e, _ := testEnv(t, 0)
		if err := run(args, e); err != nil {
			t.Errorf("%v: %v", args, err)
			continue
		}
		data, err := os.ReadFile(filepath.Join(e.l.RootHome(), "agents", "x", "config.json"))
		if err != nil {
			t.Errorf("%v: %v", args, err)
			continue
		}
		var c struct {
			Provider, Model string
			Options         map[string]any
		}
		json.Unmarshal(data, &c)
		if c.Provider != "p" || c.Model != "m" {
			t.Errorf("%v: config %s", args, data)
		}
		if strings.Contains(strings.Join(args, " "), "effort") && c.Options["effort"] != "high" {
			t.Errorf("%v: config %s", args, data)
		}
	}
}

func TestReorder(t *testing.T) {
	for _, c := range []struct{ in, want []string }{
		{[]string{"x", "--provider", "p", "--model", "m"}, []string{"--provider", "p", "--model", "m", "x"}},
		{[]string{"--model", "m", "x"}, []string{"--model", "m", "x"}},
		{[]string{"x", "--option=a=b"}, []string{"--option=a=b", "x"}},
		{[]string{"x", "--bogus"}, []string{"--bogus", "x"}},
		{[]string{"--bogus", "x"}, []string{"--bogus", "x"}},
		{[]string{"x", "--help", "y"}, []string{"--help", "x", "y"}},
		{[]string{"x", "--", "y"}, []string{"--", "x", "y"}},
		{[]string{"--model", "m", "x", "--", "--weird", "-h"}, []string{"--model", "m", "--", "x", "--weird", "-h"}},
		{[]string{"x", "-sha256", "ab"}, []string{"-sha256", "ab", "x"}},
	} {
		if got := reorder(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("reorder(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestProviderInstallHelpAndFlagErrors(t *testing.T) {
	e, out := testEnv(t, 0)
	var err error
	stderr := captureStderr(t, func() { err = run([]string{"provider", "install", "--help"}, e) })
	if err != nil || !strings.HasPrefix(out.String(), "Usage: agent provider install") || stderr != "" {
		t.Fatalf("err %v, stdout %q, stderr %q", err, out.String(), stderr)
	}
	stderr = captureStderr(t, func() { err = run([]string{"provider", "install", "--bogus", "x"}, e) })
	if err == nil || stderr != "" {
		t.Fatalf("err %v, stderr %q", err, stderr)
	}
}

// A name after "--" is a positional argument, not a NAME in its place.
func TestNewDoubleDash(t *testing.T) {
	e, _ := testEnv(t, 0)
	err := run([]string{"new", "x", "--model", "m", "--", "--weird"}, e)
	if err == nil || !strings.Contains(err.Error(), "usage: agent new NAME") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.l.RootHome(), "agents")); err == nil {
		t.Fatal("a usage error created agents/")
	}
	if err := run([]string{"new", "--model", "m", "--", "x"}, e); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.l.RootHome(), "agents", "x", "config.json")); err != nil {
		t.Fatal(err)
	}
}
