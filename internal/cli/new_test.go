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

// writeConfigFile writes a config for agent new to read and returns its path.
func writeConfigFile(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "child.json")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNewWritesValidatedConfig(t *testing.T) {
	l := tempLayout(t)
	installProviders(t, l, "p")
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"provider":"p","model":"m2"}`), 0o644)
	cfg := writeConfigFile(t, `{"provider":"p","model":"m","options":{"effort":"low","budget":5}}`)
	var out bytes.Buffer
	if err := New(l, false, home, "x", cfg, nil, &out); err != nil {
		t.Fatal(err)
	}
	ctrl := filepath.Join(home, "agents", "x")
	// It reports the directory it made and where the daemon's answer will be,
	// and does not wait for it.
	for _, want := range []string{"Created " + ctrl + "\n", filepath.Join(ctrl, "home"), filepath.Join(ctrl, "ERROR")} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	// Open to the caller's ancestors (its home's group) but not to its
	// children, whom a sharing grant may let through its home.
	if info, err := os.Stat(ctrl); err != nil || info.Mode().Perm() != 0o750 {
		t.Fatalf("control directory %v %v", info, err)
	}
	// The given file is used, not the caller's own.
	c := readConfig(t, filepath.Join(ctrl, "config.json"))
	if c.Provider != "p" || c.Model != "m" || c.Options["effort"] != "low" || c.Options["budget"] != float64(5) {
		t.Fatalf("config %+v", c)
	}
	if info, _ := os.Stat(filepath.Join(ctrl, "config.json")); info.Mode().Perm() != 0o640 {
		t.Fatalf("config mode %v", info.Mode())
	}
	if entries, _ := os.ReadDir(ctrl); len(entries) != 1 {
		t.Fatalf("control directory %v", entries)
	}
}

func TestNewCopiesCallerConfig(t *testing.T) {
	l := tempLayout(t)
	installProviders(t, l, "p")
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"provider":"p","model":"m","options":{"effort":"low"}}`), 0o644)
	if err := New(l, false, home, "x", "", nil, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if c := readConfig(t, filepath.Join(home, "agents", "x", "config.json")); c.Model != "m" || c.Options["effort"] != "low" {
		t.Fatalf("config %+v", c)
	}
}

func TestNewReadsStdin(t *testing.T) {
	l := tempLayout(t)
	installProviders(t, l, "p")
	home := newHome(t)
	if err := New(l, false, home, "x", "-", strings.NewReader(`{"provider":"p","model":"m2"}`), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if c := readConfig(t, filepath.Join(home, "agents", "x", "config.json")); c.Model != "m2" {
		t.Fatalf("config %+v", c)
	}
	// The same checks apply to what arrives on stdin.
	err := New(l, false, home, "y", "-", strings.NewReader(`{"provider":"p","model":"nope"}`), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), `unknown model "nope"`) {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "agents", "y")); err == nil {
		t.Fatal("an invalid config left a control directory")
	}
}

// Root has no config.json of its own to copy.
func TestNewRootNeedsConfig(t *testing.T) {
	l := tempLayout(t)
	installProviders(t, l, "p")
	err := New(l, true, "", "x", "", nil, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "root has no config.json to copy") || !strings.Contains(err.Error(), "agent new NAME CONFIG") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(filepath.Join(l.RootHome(), "agents")); err == nil {
		t.Fatal("a rejected agent left a directory")
	}
}

func TestNewRootWritesPrivateDirectory(t *testing.T) {
	l := tempLayout(t)
	installProviders(t, l, "p")
	cfg := writeConfigFile(t, `{"provider":"p","model":"m"}`)
	if err := New(l, true, "", "x", cfg, nil, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	ctrl := filepath.Join(l.RootHome(), "agents", "x")
	if c := readConfig(t, filepath.Join(ctrl, "config.json")); c.Provider != "p" || c.Model != "m" {
		t.Fatalf("config %+v", c)
	}
	if info, err := os.Stat(ctrl); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("control directory %v %v", info, err)
	}
}

func TestNewWithoutCallerConfig(t *testing.T) {
	l := tempLayout(t)
	installProviders(t, l, "p")
	home := t.TempDir()
	err := New(l, false, home, "x", "", nil, &bytes.Buffer{})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "agents", "x")); err == nil {
		t.Fatal("a rejected agent left a directory")
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	l := tempLayout(t)
	installProviders(t, l, "p")
	home := newHome(t)
	for _, c := range []struct{ config, want string }{
		{`{"provider":"p","model":"nope"}`, `unknown model "nope"`},
		{`{"provider":"q","model":"m"}`, `unknown provider "q"`},
		{`{"provider":"p","model":"m","options":{"nope":1}}`, `unknown option "nope"`},
		{`{"provider":"p","model":"m","options":{"effort":"medium"}}`, "not allowed"},
		{`{"provider":"p","model":"m","options":{"budget":"lots"}}`, "out of range"},
		{`{"provider":"p","model":"m","extra":1}`, "unknown field"},
		{`{"provider":"p"}`, `"provider" and "model" are required`},
		{`not json`, "invalid JSON"},
		{``, "invalid JSON"},
	} {
		cfg := writeConfigFile(t, c.config)
		err := New(l, false, home, "x", cfg, nil, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), cfg) {
			t.Errorf("%s: got %v, want %q naming %s", c.config, err, c.want, cfg)
		}
		if _, err := os.Stat(filepath.Join(home, "agents", "x")); err == nil {
			t.Fatalf("%s left a control directory", c.config)
		}
	}
}

// A config over the daemon's limit is refused, from a file or stdin; one
// exactly at the limit is accepted.
func TestNewLimitsConfigSize(t *testing.T) {
	l := tempLayout(t)
	installProviders(t, l, "p")
	home := newHome(t)
	const config = `{"provider":"p","model":"m"}`
	atLimit := config + strings.Repeat(" ", plugin.ConfigLimit-len(config))
	if err := New(l, false, home, "ok", "-", strings.NewReader(atLimit), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for name, run := range map[string]func() error{
		"file":  func() error { return New(l, false, home, "big", writeConfigFile(t, atLimit+" "), nil, &bytes.Buffer{}) },
		"stdin": func() error { return New(l, false, home, "big", "-", strings.NewReader(atLimit+" "), &bytes.Buffer{}) },
	} {
		if err := run(); err == nil || !strings.Contains(err.Error(), "larger than 1 MiB") {
			t.Errorf("%s: got %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(home, "agents", "big")); err == nil {
			t.Fatalf("%s: an oversized config left a control directory", name)
		}
	}
}

func TestNewRejectsExistingName(t *testing.T) {
	l := tempLayout(t)
	installProviders(t, l, "p")
	home := newHome(t)
	agentWithConfig(t, home, "x")
	err := New(l, false, home, "x", "", nil, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("got %v", err)
	}
	for _, name := range []string{"", "..", "a/b"} {
		if err := New(l, false, home, name, "", nil, &bytes.Buffer{}); err == nil {
			t.Errorf("name %q accepted", name)
		}
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
		if err := New(l, false, home, name, "", nil, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "not a direct subagent name") {
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
	err := New(l, false, home, "x", "", nil, &bytes.Buffer{})
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
	if err := New(l, false, home, "y", "", nil, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "no rename") {
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
		cfg := writeConfigFile(t, `{"provider":"p","model":"m"}`)
		old := syscall.Umask(umask)
		err := New(l, false, home, "x", cfg, nil, &bytes.Buffer{})
		rootErr := New(l, true, "", "r", cfg, nil, &bytes.Buffer{})
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
