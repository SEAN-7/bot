package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
	"github.com/SEAN-7/bot/internal/release"
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

func serveAsset(t *testing.T, name string, body []byte) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/releases/tags/v1.0.0":
			fmt.Fprintf(w, `{"assets":[{"id":7,"name":%q}]}`, name)
		case "/repos/o/r/releases/assets/7":
			w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func installOptions(t *testing.T, srv *httptest.Server, sum string, prompt func(proto.Setting) (string, error)) (InstallOptions, string) {
	l := tempLayout(t)
	return InstallOptions{
		Release: &release.Client{API: srv.URL, Token: "tok"},
		Repo:    "o/r", Version: "1.0.0", Arch: "amd64",
		Index:  &plugins.Index{Version: "1.0.0", Plugins: map[string]map[string]string{"replay": {"linux-amd64": sum}}},
		Prompt: prompt,
		Runner: &plugin.Runner{PluginDir: l.Plugins(), CredDir: l.Credentials(), Timeout: 10 * time.Second, Stderr: os.Stderr},
		Layout: l,
	}, l.Plugins()
}

func TestInstallVerifiesChecksum(t *testing.T) {
	srv := serveAsset(t, "plugin-replay-linux-amd64", []byte("tampered"))
	opts, dir := installOptions(t, srv, strings.Repeat("0", 64), nil)
	err := Install(context.Background(), "replay", "", opts)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch for replay") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "replay")); !os.IsNotExist(err) {
		t.Fatal("plugin written despite mismatch")
	}
}

func TestInstallWritesPluginAndCreds(t *testing.T) {
	bin := buildReplay(t)
	sum := sha256.Sum256(bin)
	srv := serveAsset(t, "plugin-replay-linux-amd64", bin)
	script := filepath.Join(t.TempDir(), "script.jsonl")
	os.WriteFile(script, []byte(`{"models":[{"id":"replay-1","context_tokens":1000}]}`+"\n"), 0o644)
	var asked []string
	opts, dir := installOptions(t, srv, hex.EncodeToString(sum[:]), func(s proto.Setting) (string, error) {
		asked = append(asked, s.Name)
		if s.Name == "REPLAY_SCRIPT" {
			return script, nil
		}
		return filepath.Join(filepath.Dir(script), "log.jsonl"), nil
	})
	if err := Install(context.Background(), "replay", "", opts); err != nil {
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
	if err := Install(context.Background(), "replay", "", opts); err != nil || len(asked) != 0 {
		t.Fatalf("reinstall asked %v: %v", asked, err)
	}
}

func TestInstallURLRequiresSha(t *testing.T) {
	srv := serveAsset(t, "x", nil)
	opts, _ := installOptions(t, srv, "", nil)
	if err := Install(context.Background(), "https://example.com/plugin-x-linux-amd64", "", opts); err == nil || !strings.Contains(err.Error(), "--sha256") {
		t.Fatalf("got %v", err)
	}
}

func fileContains(t *testing.T, path, text string) bool {
	data, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(data), text)
}
