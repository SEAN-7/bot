package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/SEAN-7/bot/internal/layout"
	"github.com/SEAN-7/bot/internal/plugin"
	"github.com/SEAN-7/bot/internal/proto"
)

// installProviders writes the describe cache the agent validates against: one
// provider "p" with models "m" and "m2"; m takes an integer option budget.
func installProviders(t *testing.T, l layout.Layout, providers ...string) {
	t.Helper()
	cache := providersCache{Providers: map[string]proto.DescribeResponse{}}
	for _, name := range providers {
		cache.Providers[name] = proto.DescribeResponse{Models: []proto.Model{
			{ID: "m", ContextTokens: 1000, Options: map[string]proto.Option{
				"budget": {Type: "integer"},
				"effort": {Values: []any{"low", "high"}},
				"level":  {Values: []any{float64(1), float64(2)}},
				"fast":   {Values: []any{true, false}},
			}},
			{ID: "m2", ContextTokens: 1000},
		}}
	}
	data, _ := json.Marshal(cache)
	os.MkdirAll(l.Lib, 0o755)
	if err := os.WriteFile(l.ProvidersCache(), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func fastNew(wait time.Duration) NewOptions {
	return NewOptions{Wait: wait, Poll: 5 * time.Millisecond}
}

func readConfig(t *testing.T, path string) plugin.Config {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := plugin.ParseConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewWritesValidatedConfig(t *testing.T) {
	l := tempLayout(t)
	installProviders(t, l, "p")
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"provider":"p","model":"m2"}`), 0o644)
	o := fastNew(20 * time.Millisecond)
	o.Model = "m"
	o.Options = []string{"effort=low"}
	var out bytes.Buffer
	if err := New(l, false, home, "x", o, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Created x; the daemon has not admitted it yet (agent log x shows why).") {
		t.Fatalf("output %q", out.String())
	}
	ctrl := filepath.Join(home, "agents", "x")
	// Open to the caller's ancestors (its home's group) but not to its
	// children, whom a sharing grant may let through its home.
	if info, err := os.Stat(ctrl); err != nil || info.Mode().Perm() != 0o750 {
		t.Fatalf("control directory %v %v", info, err)
	}
	c := readConfig(t, filepath.Join(ctrl, "config.json"))
	if c.Provider != "p" || c.Model != "m" || c.Options["effort"] != "low" {
		t.Fatalf("config %+v", c)
	}
	if info, _ := os.Stat(filepath.Join(ctrl, "config.json")); info.Mode().Perm() != 0o640 {
		t.Fatalf("config mode %v", info.Mode())
	}
	if entries, _ := os.ReadDir(ctrl); len(entries) != 1 {
		t.Fatalf("control directory %v", entries)
	}
}

// TestNewInheritedOptions: the caller's options go with its model, so they are
// dropped when --provider or --model is given and kept otherwise.
func TestNewInheritedOptions(t *testing.T) {
	l := tempLayout(t)
	installProviders(t, l, "p")
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"provider":"p","model":"m","options":{"effort":"low"}}`), 0o644)
	o := fastNew(20 * time.Millisecond)
	o.Model = "m2"
	if err := New(l, false, home, "cheap", o, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if c := readConfig(t, filepath.Join(home, "agents", "cheap", "config.json")); c.Model != "m2" || len(c.Options) != 0 {
		t.Fatalf("config %+v", c)
	}
	o.Model, o.Options = "m", []string{"budget=3"}
	if err := New(l, false, home, "same", o, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if c := readConfig(t, filepath.Join(home, "agents", "same", "config.json")); c.Options["effort"] != nil || c.Options["budget"] != float64(3) {
		t.Fatalf("config %+v", c)
	}
	if err := New(l, false, home, "plain", fastNew(20*time.Millisecond), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if c := readConfig(t, filepath.Join(home, "agents", "plain", "config.json")); c.Options["effort"] != "low" {
		t.Fatalf("config %+v", c)
	}
}

func TestNewWithoutCallerConfig(t *testing.T) {
	l := tempLayout(t)
	installProviders(t, l, "p")
	home := t.TempDir()
	o := fastNew(20 * time.Millisecond)
	o.Provider, o.Model = "p", "m"
	if err := New(l, false, home, "x", o, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if c := readConfig(t, filepath.Join(home, "agents", "x", "config.json")); c.Provider != "p" || c.Model != "m" {
		t.Fatalf("config %+v", c)
	}
	for _, o := range []NewOptions{{Model: "m"}, {Provider: "p"}, {}} {
		o.Wait, o.Poll = time.Millisecond, time.Millisecond
		err := New(l, false, home, "y", o, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "pass --provider and --model") {
			t.Fatalf("%+v: got %v", o, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "agents", "y")); err == nil {
		t.Fatal("a rejected agent left a directory")
	}
}

func TestNewRootRequiresModel(t *testing.T) {
	l := tempLayout(t)
	installProviders(t, l, "p")
	err := New(l, true, "", "x", fastNew(time.Millisecond), &bytes.Buffer{})
	if err == nil || err.Error() != "root must choose a model: agent new NAME --model M" {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(filepath.Join(l.RootHome(), "agents")); err == nil {
		t.Fatal("a rejected agent left a directory")
	}
}

func TestNewRootDefaultsProviderAndIsPrivate(t *testing.T) {
	l := tempLayout(t)
	installProviders(t, l, "p")
	o := fastNew(20 * time.Millisecond)
	o.Model = "m"
	if err := New(l, true, "", "x", o, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	ctrl := filepath.Join(l.RootHome(), "agents", "x")
	if c := readConfig(t, filepath.Join(ctrl, "config.json")); c.Provider != "p" || c.Model != "m" {
		t.Fatalf("config %+v", c)
	}
	if info, err := os.Stat(ctrl); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("control directory %v %v", info, err)
	}
	// With two providers installed, root has to choose.
	installProviders(t, l, "p", "q")
	err := New(l, true, "", "y", o, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--provider") {
		t.Fatalf("got %v", err)
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	l := tempLayout(t)
	installProviders(t, l, "p")
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"provider":"p","model":"m"}`), 0o644)
	o := fastNew(time.Millisecond)
	o.Model = "nope"
	err := New(l, false, home, "x", o, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), `unknown model "nope"`) {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "agents", "x")); err == nil {
		t.Fatal("an invalid config left a control directory")
	}
	o.Model = "m"
	o.Options = []string{"budget"}
	if err := New(l, false, home, "x", o, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "k=v") {
		t.Fatalf("got %v", err)
	}
}

func TestNewRejectsExistingName(t *testing.T) {
	l := tempLayout(t)
	installProviders(t, l, "p")
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"provider":"p","model":"m"}`), 0o644)
	agentWithConfig(t, home, "x")
	err := New(l, false, home, "x", fastNew(time.Millisecond), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("got %v", err)
	}
	for _, name := range []string{"", "..", "a/b"} {
		if err := New(l, false, home, name, fastNew(time.Millisecond), &bytes.Buffer{}); err == nil {
			t.Errorf("name %q accepted", name)
		}
	}
}

func TestNewReportsError(t *testing.T) {
	l := tempLayout(t)
	installProviders(t, l, "p")
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"provider":"p","model":"m"}`), 0o644)
	go func() {
		ctrl := filepath.Join(home, "agents", "x")
		for {
			if _, err := os.Stat(filepath.Join(ctrl, "config.json")); err == nil {
				os.WriteFile(filepath.Join(ctrl, "ERROR"), []byte("Agent limit reached (max_agents=2); this agent was not started.\n"), 0o644)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	err := New(l, false, home, "x", fastNew(5*time.Second), &bytes.Buffer{})
	want := "x was not started: Agent limit reached (max_agents=2); this agent was not started."
	if err == nil || err.Error() != want {
		t.Fatalf("got %v", err)
	}
}

func TestNewReportsHome(t *testing.T) {
	l := tempLayout(t)
	installProviders(t, l, "p")
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"provider":"p","model":"m"}`), 0o644)
	target := t.TempDir()
	go func() {
		ctrl := filepath.Join(home, "agents", "x")
		for {
			if _, err := os.Stat(filepath.Join(ctrl, "config.json")); err == nil {
				os.Symlink(target, filepath.Join(ctrl, "home"))
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	var out bytes.Buffer
	if err := New(l, false, home, "x", fastNew(5*time.Second), &out); err != nil {
		t.Fatal(err)
	}
	if want := "Created x: home " + target + "\n"; out.String() != want {
		t.Fatalf("output %q, want %q", out.String(), want)
	}
}

func TestNewParsesIntegerOption(t *testing.T) {
	l := tempLayout(t)
	installProviders(t, l, "p")
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"provider":"p","model":"m"}`), 0o644)
	o := fastNew(20 * time.Millisecond)
	o.Options = []string{"budget=5", "effort=high"}
	if err := New(l, false, home, "x", o, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(home, "agents", "x", "config.json"))
	var raw struct{ Options map[string]any }
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if n, ok := raw.Options["budget"].(float64); !ok || n != 5 || raw.Options["effort"] != "high" {
		t.Fatalf("options %v in %s", raw.Options, data)
	}
	// A non-integer value for an integer option is rejected before anything is written.
	o.Options = []string{"budget=lots"}
	if err := New(l, false, home, "y", o, &bytes.Buffer{}); err == nil {
		t.Fatal("budget=lots accepted")
	}
	if _, err := os.Stat(filepath.Join(home, "agents", "y")); err == nil {
		t.Fatal("a rejected option left a control directory")
	}
}

func TestRemoveDeletesControlDir(t *testing.T) {
	l := tempLayout(t)
	home := t.TempDir()
	target := t.TempDir()
	os.WriteFile(filepath.Join(target, "notes.txt"), []byte("keep"), 0o644)
	ctrl := filepath.Join(home, "agents", "x")
	os.MkdirAll(ctrl, 0o755)
	os.WriteFile(filepath.Join(ctrl, "OWNER.md"), []byte("me"), 0o644)
	mustLink(t, target, filepath.Join(ctrl, "home"))
	if err := Remove(l, false, home, "x"); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(home); len(entries) != 1 || entries[0].Name() != "agents" {
		t.Fatalf("home holds %v", entries)
	}
	if _, err := os.Stat(ctrl); !os.IsNotExist(err) {
		t.Fatal("control directory remains")
	}
	// The daemon, not Remove, reclaims the home.
	if _, err := os.Stat(filepath.Join(target, "notes.txt")); err != nil {
		t.Fatal(err)
	}
	if err := Remove(l, false, home, "x"); err == nil || !strings.Contains(err.Error(), `no subagent "x"`) {
		t.Fatalf("got %v", err)
	}
	// Only a real directory is removed, and only one level.
	mustLink(t, target, filepath.Join(home, "agents", "link"))
	for _, name := range []string{"link", "", "..", "a/b"} {
		if err := Remove(l, false, home, name); err == nil {
			t.Errorf("name %q accepted", name)
		}
	}
	if _, err := os.Stat(filepath.Join(target, "notes.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveRootUsesRootHome(t *testing.T) {
	l := tempLayout(t)
	agentWithConfig(t, l.RootHome(), "x")
	if err := Remove(l, true, "", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(l.RootHome(), "agents", "x")); !os.IsNotExist(err) {
		t.Fatal("control directory remains")
	}
}

func TestBase(t *testing.T) {
	l := tempLayout(t)
	if b, err := Base(l, true, ""); err != nil || b != l.RootHome() {
		t.Fatal(b, err)
	}
	if b, err := Base(l, false, "/home/a"); err != nil || b != "/home/a" {
		t.Fatal(b, err)
	}
	if _, err := Base(l, false, ""); err == nil {
		t.Fatal("empty HOME accepted")
	}
}

func TestLoadProviders(t *testing.T) {
	l := tempLayout(t)
	if _, err := LoadProviders(l); err == nil {
		t.Fatal("missing cache accepted")
	}
	installProviders(t, l, "p")
	got, err := LoadProviders(l)
	if err != nil || len(got["p"].Models) != 2 {
		t.Fatal(got, err)
	}
}

// newHome is a caller's home with a valid config.json.
func newHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"provider":"p","model":"m"}`), 0o644)
	return home
}

func TestNewRejectsBadNames(t *testing.T) {
	l := tempLayout(t)
	installProviders(t, l, "p")
	home := newHome(t)
	for _, name := range []string{".hidden", ".", "..", "a\nb", "a\tb", "a\x7fb", "a\x1bb", "a\x00b", "a/b", ""} {
		if cleanName(name) {
			t.Errorf("cleanName(%q) accepted", name)
		}
		if err := New(l, false, home, name, fastNew(time.Millisecond), &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "not a direct subagent name") {
			t.Errorf("New %q: %v", name, err)
		}
		if err := Remove(l, false, home, name); err == nil || !strings.Contains(err.Error(), "not a direct subagent name") {
			t.Errorf("Remove %q: %v", name, err)
		}
		if err := SendChild(home, name, "hi", time.Now()); err == nil || !strings.Contains(err.Error(), "not a direct subagent name") {
			t.Errorf("SendChild %q: %v", name, err)
		}
		if _, _, _, err := Target(l, true, "", name); err == nil {
			t.Errorf("Target %q accepted", name)
		}
	}
	if !cleanName("scraper-2") || !cleanName("a.b") {
		t.Error("plain names rejected")
	}
}

// A failed write leaves no control directory behind, and does not remove one
// that was already there.
func TestNewLeavesNothingOnFailure(t *testing.T) {
	l := tempLayout(t)
	installProviders(t, l, "p")
	home := newHome(t)
	defer func(f func(string, []byte, os.FileMode) error) { writeFile = f }(writeFile)
	writeFile = func(string, []byte, os.FileMode) error { return errors.New("disk full") }
	err := New(l, false, home, "x", fastNew(time.Millisecond), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Lstat(filepath.Join(home, "agents", "x")); !os.IsNotExist(err) {
		t.Fatalf("control directory remains: %v", err)
	}
	// Failing to rename the finished config away cleans up too.
	writeFile = os.WriteFile
	defer func(f func(string, string) error) { rename = f }(rename)
	rename = func(string, string) error { return errors.New("no rename") }
	if err := New(l, false, home, "y", fastNew(time.Millisecond), &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "no rename") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Lstat(filepath.Join(home, "agents", "y")); !os.IsNotExist(err) {
		t.Fatalf("control directory remains: %v", err)
	}
}

func TestNewModesIgnoreUmask(t *testing.T) {
	for _, umask := range []int{0o077, 0o027, 0o000} {
		l := tempLayout(t)
		installProviders(t, l, "p")
		home := newHome(t)
		o := fastNew(5 * time.Millisecond)
		o.Model = "m"
		old := syscall.Umask(umask)
		err := New(l, false, home, "x", o, &bytes.Buffer{})
		rootErr := New(l, true, "", "r", o, &bytes.Buffer{})
		syscall.Umask(old)
		if err != nil || rootErr != nil {
			t.Fatal(err, rootErr)
		}
		for _, c := range []struct {
			ctrl      string
			dir, file os.FileMode
		}{
			{filepath.Join(home, "agents", "x"), 0o750, 0o640},
			{filepath.Join(l.RootHome(), "agents", "r"), 0o700, 0o640},
		} {
			di, _ := os.Stat(c.ctrl)
			fi, _ := os.Stat(filepath.Join(c.ctrl, "config.json"))
			if di.Mode().Perm() != c.dir || fi.Mode().Perm() != c.file {
				t.Errorf("umask %04o: %s is %v, config.json %v; want %v and %v", umask, c.ctrl, di.Mode().Perm(), fi.Mode().Perm(), c.dir, c.file)
			}
		}
	}
}

func TestNewRejectsRepeatedOption(t *testing.T) {
	l := tempLayout(t)
	installProviders(t, l, "p")
	home := newHome(t)
	o := fastNew(time.Millisecond)
	o.Options = []string{"effort=low", "effort=high"}
	err := New(l, false, home, "x", o, &bytes.Buffer{})
	if err == nil || err.Error() != "option effort given twice" {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "agents", "x")); err == nil {
		t.Fatal("a rejected option left a control directory")
	}
}

func TestNewOptionValueTypes(t *testing.T) {
	l := tempLayout(t)
	installProviders(t, l, "p")
	home := newHome(t)
	o := fastNew(5 * time.Millisecond)
	o.Options = []string{"level=2", "fast=true", "effort=low", "budget=7"}
	if err := New(l, false, home, "x", o, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	c := readConfig(t, filepath.Join(home, "agents", "x", "config.json"))
	if c.Options["level"] != float64(2) || c.Options["fast"] != true || c.Options["effort"] != "low" || c.Options["budget"] != float64(7) {
		t.Fatalf("options %#v", c.Options)
	}
	// A value outside the allowed list stays a string and Validate rejects it.
	for _, bad := range []string{"level=3", "level=two", "fast=yes", "effort=medium"} {
		o.Options = []string{bad}
		err := New(l, false, home, "y", o, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Errorf("%s: got %v", bad, err)
		}
	}
}

func TestNewAdviceMatchesError(t *testing.T) {
	l := tempLayout(t)
	installProviders(t, l, "p")
	home := t.TempDir()
	cfg := filepath.Join(home, "config.json")
	if err := os.WriteFile(cfg, []byte(`{"provider":"p","model":"m"}`), 0o000); err != nil {
		t.Fatal(err)
	}
	if f, err := os.Open(cfg); err == nil {
		f.Close()
		t.Skip("running as a user that can read mode 000 files")
	}
	err := New(l, false, home, "x", fastNew(time.Millisecond), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "permission denied") || strings.Contains(err.Error(), "pass --provider") {
		t.Fatalf("got %v", err)
	}
}
