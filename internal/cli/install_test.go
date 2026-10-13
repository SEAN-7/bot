package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SEAN-7/bot/internal/plugin"
	"github.com/SEAN-7/bot/internal/proto"
	"github.com/SEAN-7/bot/plugins"
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

// pluginFile writes body where install.sh would have downloaded it.
func pluginFile(t *testing.T, body []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plugin-replay-linux-amd64")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func installOptions(t *testing.T, sum string, prompt func(proto.Setting) (string, error)) (InstallOptions, string) {
	l := tempLayout(t)
	return InstallOptions{
		Arch:   "amd64",
		Index:  &plugins.Index{Version: "1.0.0", Plugins: map[string]map[string]string{"replay": {"linux-amd64": sum}}},
		Prompt: prompt,
		Runner: &plugin.Runner{PluginDir: l.Plugins(), CredDir: l.Credentials(), Timeout: 10 * time.Second, Stderr: os.Stderr},
		Layout: l,
	}, l.Plugins()
}

func TestInstallVerifiesChecksum(t *testing.T) {
	opts, dir := installOptions(t, strings.Repeat("0", 64), nil)
	err := Install(context.Background(), "replay", pluginFile(t, []byte("tampered")), opts)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch for replay") || !strings.Contains(err.Error(), "nothing installed") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "replay")); !os.IsNotExist(err) {
		t.Fatal("plugin written despite mismatch")
	}
}

func TestInstallWritesPluginAndCreds(t *testing.T) {
	bin := buildReplay(t)
	sum := sha256.Sum256(bin)
	file := pluginFile(t, bin)
	script := filepath.Join(t.TempDir(), "script.jsonl")
	os.WriteFile(script, []byte(`{"models":[{"id":"replay-1","context_tokens":1000}]}`+"\n"), 0o644)
	var asked []string
	opts, dir := installOptions(t, hex.EncodeToString(sum[:]), func(s proto.Setting) (string, error) {
		asked = append(asked, s.Name)
		if s.Name == "REPLAY_SCRIPT" {
			return script, nil
		}
		return filepath.Join(filepath.Dir(script), "log.jsonl"), nil
	})
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

// A plugin the index does not list for this architecture, or an invalid
// name, is refused before the file is read.
func TestInstallRefusesUnlistedPlugin(t *testing.T) {
	file := pluginFile(t, []byte("x"))
	for name, want := range map[string]string{"other": `no plugin "other" for linux-amd64`, "../replay": "invalid plugin name"} {
		opts, dir := installOptions(t, strings.Repeat("0", 64), nil)
		if err := Install(context.Background(), name, file, opts); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v", name, err)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("%s: wrote %v", name, entries)
		}
	}
	opts, _ := installOptions(t, strings.Repeat("0", 64), nil)
	opts.Arch = "arm64"
	if err := Install(context.Background(), "replay", file, opts); err == nil || !strings.Contains(err.Error(), "linux-arm64") {
		t.Errorf("arm64: got %v", err)
	}
}

func fileContains(t *testing.T, path, text string) bool {
	data, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(data), text)
}
