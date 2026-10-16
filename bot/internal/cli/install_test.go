package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SEAN-7/bot/internal/plugin"
	"github.com/SEAN-7/bot/internal/proto"
)

func buildReplay(t *testing.T) []byte {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "replay")
	if out, err := exec.Command("go", "build", "-o", bin, "github.com/SEAN-7/bot/plugins/replay").CombinedOutput(); err != nil {
		t.Fatal(string(out))
	}
	data, _ := os.ReadFile(bin)
	return data
}

// pluginFile writes body where a download would have put it.
func pluginFile(t *testing.T, body []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plugin-replay-linux-amd64")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func installOptions(t *testing.T, prompt func(proto.Setting) (string, error)) (InstallOptions, string) {
	l := tempLayout(t)
	return InstallOptions{
		Prompt: prompt,
		Runner: &plugin.Runner{PluginDir: l.Plugins(), CredDir: l.Credentials(), Timeout: 10 * time.Second, Stderr: os.Stderr},
		Layout: l,
	}, l.Plugins()
}

// replayPrompt answers the replay plugin's two settings.
func replayPrompt(t *testing.T, asked *[]string) (func(proto.Setting) (string, error), string) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "script.jsonl")
	os.WriteFile(script, []byte(`{"models":[{"id":"replay-1","context_tokens":1000}]}`+"\n"), 0o644)
	return func(s proto.Setting) (string, error) {
		if asked != nil {
			*asked = append(*asked, s.Name)
		}
		if s.Name == "REPLAY_SCRIPT" {
			return script, nil
		}
		return filepath.Join(filepath.Dir(script), "log.jsonl"), nil
	}, script
}

func TestInstallWritesPluginAndCreds(t *testing.T) {
	file := pluginFile(t, buildReplay(t))
	var asked []string
	prompt, script := replayPrompt(t, &asked)
	opts, dir := installOptions(t, prompt)
	if err := Install(context.Background(), "replay", file, opts); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(dir, "replay")); err != nil || info.Mode().Perm() != 0o750 {
		t.Fatalf("plugin %v %v", info, err)
	}
	env, _ := os.ReadFile(filepath.Join(opts.Layout.Credentials(), "replay.env"))
	if !strings.Contains(string(env), "REPLAY_SCRIPT="+script) || len(asked) != 2 {
		t.Fatalf("creds %q asked %v", env, asked)
	}
	if info, _ := os.Stat(filepath.Join(opts.Layout.Credentials(), "replay.env")); info.Mode().Perm() != 0o600 {
		t.Fatalf("creds mode %v", info.Mode())
	}
	if !fileContains(t, opts.Layout.ProvidersCache(), "replay-1") {
		t.Fatal("cache not refreshed")
	}
	// Reinstalling keeps existing settings without asking again.
	asked = nil
	if err := Install(context.Background(), "replay", file, opts); err != nil || len(asked) != 0 {
		t.Fatalf("reinstall asked %v: %v", asked, err)
	}
}

// An invalid name is refused before anything is read or written.
func TestInstallRefusesBadName(t *testing.T) {
	opts, dir := installOptions(t, nil)
	if err := Install(context.Background(), "../replay", pluginFile(t, []byte("x")), opts); err == nil || !strings.Contains(err.Error(), "invalid plugin name") {
		t.Fatalf("got %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("wrote %v", entries)
	}
}

// A plugin installs from an http(s) URL as from a file: the binary is
// fetched, installed for the provider group, described, and its settings
// asked for. A failed fetch installs nothing.
func TestInstallFromURL(t *testing.T) {
	bin := buildReplay(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/plugin-replay-linux-amd64" {
			http.NotFound(w, r)
			return
		}
		w.Write(bin)
	}))
	defer srv.Close()
	prompt, script := replayPrompt(t, nil)
	opts, dir := installOptions(t, prompt)
	if err := Install(context.Background(), "replay", srv.URL+"/plugin-replay-linux-amd64", opts); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(dir, "replay")); err != nil || info.Mode().Perm() != 0o750 {
		t.Fatalf("plugin %v %v", info, err)
	}
	if !fileContains(t, filepath.Join(opts.Layout.Credentials(), "replay.env"), "REPLAY_SCRIPT="+script) {
		t.Fatal("settings not written")
	}
	if !fileContains(t, opts.Layout.ProvidersCache(), "replay-1") {
		t.Fatal("cache not refreshed")
	}
	opts, dir = installOptions(t, prompt)
	err := Install(context.Background(), "replay", srv.URL+"/missing", opts)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("missing URL: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("installed despite the failed fetch: %v", entries)
	}
}

func fileContains(t *testing.T, path, text string) bool {
	data, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(data), text)
}
