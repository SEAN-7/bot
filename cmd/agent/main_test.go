package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	return env{l: l, euid: euid, home: home, in: strings.NewReader(""), out: out}, out
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
		{"new without name", 1000, []string{"new"}, "usage: agent new NAME [CONFIG|-]", ""},
		{"new with a flag", 1000, []string{"new", "--help"}, "usage: agent new NAME [CONFIG|-]", ""},
		{"new with too many arguments", 1000, []string{"new", "x", "a.json", "b.json"}, "usage: agent new NAME [CONFIG|-]", ""},
		{"new as root without config", 0, []string{"new", "x"}, "root has no config.json to copy", ""},
		{"remove without name", 1000, []string{"remove"}, "usage: agent remove NAME", ""},
		{"messages with two names", 1000, []string{"messages", "a", "b"}, "usage: agent messages [NAME]", ""},
		{"log with two names", 1000, []string{"log", "a", "b"}, "usage: agent log [NAME]", ""},
		{"messages as root without name", 0, []string{"messages"}, "root has no owner", ""},
		{"help", 1000, []string{"help"}, "", "agent new NAME [CONFIG|-]"},
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

// writeConfig writes a config file under dir and returns its path.
func writeConfig(t *testing.T, dir, text string) string {
	t.Helper()
	path := filepath.Join(dir, "child.json")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// agent new takes its config from a file, from stdin for "-", or from the
// caller's own ~/config.json, and prints the control directory it made.
func TestNewConfigSources(t *testing.T) {
	const config = `{"provider":"p","model":"m","options":{"effort":"high"}}`
	for _, c := range []struct {
		name string
		euid int
		args func(dir string) []string
		in   string
	}{
		{"file as root", 0, func(dir string) []string { return []string{"new", "x", writeConfig(t, dir, config)} }, ""},
		{"file as agent", 1000, func(dir string) []string { return []string{"new", "x", writeConfig(t, dir, config)} }, ""},
		{"stdin as root", 0, func(string) []string { return []string{"new", "x", "-"} }, config},
		{"stdin as agent", 1000, func(string) []string { return []string{"new", "x", "-"} }, config},
		{"caller's own config", 1000, func(string) []string { return []string{"new", "x"} }, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, out := testEnv(t, c.euid)
			e.in = strings.NewReader(c.in)
			os.WriteFile(filepath.Join(e.home, "config.json"), []byte(config), 0o644)
			if err := run(c.args(t.TempDir()), e); err != nil {
				t.Fatal(err)
			}
			base := e.home
			if c.euid == 0 {
				base = e.l.RootHome()
			}
			ctrl := filepath.Join(base, "agents", "x")
			data, err := os.ReadFile(filepath.Join(ctrl, "config.json"))
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Provider, Model string
				Options         map[string]any
			}
			json.Unmarshal(data, &got)
			if got.Provider != "p" || got.Model != "m" || got.Options["effort"] != "high" {
				t.Fatalf("config %s", data)
			}
			if !strings.Contains(out.String(), "Created "+ctrl+"\n") {
				t.Fatalf("output %q", out.String())
			}
		})
	}
}

// A usage error or an invalid config creates nothing.
func TestNewErrorsCreateNothing(t *testing.T) {
	for _, args := range [][]string{{"new"}, {"new", "--model", "m", "x"}, {"new", "x", "-", "extra"}, {"new", "x", "/nonexistent.json"}} {
		e, out := testEnv(t, 0)
		if err := run(args, e); err == nil || out.Len() != 0 {
			t.Errorf("%v: err %v, stdout %q", args, err, out.String())
		}
		if _, statErr := os.Stat(filepath.Join(e.l.RootHome(), "agents")); statErr == nil {
			t.Errorf("%v created agents/", args)
		}
	}
	e, _ := testEnv(t, 0)
	e.in = strings.NewReader(`{"provider":"p","model":"nope"}`)
	if err := run([]string{"new", "x", "-"}, e); err == nil || !strings.Contains(err.Error(), `unknown model "nope"`) {
		t.Fatalf("got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(e.l.RootHome(), "agents")); statErr == nil {
		t.Error("an invalid config created agents/")
	}
}

// provider install takes exactly NAME and FILE, and prints nothing but the
// usage error otherwise.
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
