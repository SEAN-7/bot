package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SEAN-7/bot/internal/proto"
)

var replayBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "replay")
	if err != nil {
		panic(err)
	}
	replayBin = filepath.Join(dir, "replay")
	if out, err := exec.Command("go", "build", "-o", replayBin, "github.com/SEAN-7/bot/plugins/replay").CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func runner(t *testing.T, steps ...string) *Runner {
	t.Helper()
	root := t.TempDir()
	plugins, creds := filepath.Join(root, "plugins"), filepath.Join(root, "credentials")
	os.MkdirAll(plugins, 0o755)
	os.MkdirAll(creds, 0o700)
	data, _ := os.ReadFile(replayBin)
	os.WriteFile(filepath.Join(plugins, "replay"), data, 0o755)
	script := filepath.Join(root, "script.jsonl")
	header := `{"models":[{"id":"replay-1","context_tokens":1000,"options":{"style":{"description":"s","values":["a","b"]},"budget":{"description":"b","type":"integer","min":1,"max":10}}}]}`
	os.WriteFile(script, []byte(strings.Join(append([]string{header}, steps...), "\n")+"\n"), 0o644)
	env := FormatEnv(map[string]string{"REPLAY_SCRIPT": script, "REPLAY_LOG": filepath.Join(root, "log.jsonl")})
	os.WriteFile(filepath.Join(creds, "replay.env"), env, 0o600)
	return &Runner{PluginDir: plugins, CredDir: creds, Timeout: 10 * time.Second, Stderr: os.Stderr}
}

func TestDescribeReplay(t *testing.T) {
	d, err := runner(t).Describe(context.Background(), "replay")
	if err != nil {
		t.Fatal(err)
	}
	if d.Models[0].ID != "replay-1" || ContextTokens(d, "replay-1") != 1000 || ContextTokens(d, "x") != 0 {
		t.Fatalf("got %+v", d)
	}
}

func TestCompleteReplay(t *testing.T) {
	r := runner(t, `{"command":"echo hi"}`)
	resp, err := r.Complete(context.Background(), "replay", proto.CompleteRequest{Model: "replay-1"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Command == nil || *resp.Command != "echo hi" {
		t.Fatalf("got %+v", resp)
	}
}

func TestCompleteErrorIsGoError(t *testing.T) {
	_, err := runner(t, `{"error":"boom"}`).Complete(context.Background(), "replay", proto.CompleteRequest{Model: "replay-1"})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("got %v", err)
	}
}

func TestInstalledAndBadName(t *testing.T) {
	r := runner(t)
	names, err := r.Installed()
	if err != nil || fmt.Sprint(names) != "[replay]" {
		t.Fatalf("got %v %v", names, err)
	}
	if _, err := r.Describe(context.Background(), "../../bin/sh"); err == nil {
		t.Fatal("path traversal in provider name accepted")
	}
}

func TestValidate(t *testing.T) {
	d, err := runner(t).Describe(context.Background(), "replay")
	if err != nil {
		t.Fatal(err)
	}
	installed := []string{"anthropic", "replay"}
	cases := []struct {
		config string
		want   string
	}{
		{`{"provider":"replay","model":"replay-1","options":{"style":"a","budget":3}}`, ""},
		{`{"provider":"nope","model":"replay-1"}`, `unknown provider "nope" (installed: anthropic, replay)`},
		{`{"provider":"replay","model":"m"}`, `unknown model "m" for provider "replay" (available: replay-1)`},
		{`{"provider":"replay","model":"replay-1","options":{"color":"red"}}`, `unknown option "color" for model "replay-1"`},
		{`{"provider":"replay","model":"replay-1","options":{"style":"c"}}`, `option "style": value c not allowed (allowed: a, b)`},
		{`{"provider":"replay","model":"replay-1","options":{"budget":11}}`, `option "budget": value 11 out of range [1, 10]`},
		{`{"provider":"replay","model":"replay-1","options":{"budget":2.5}}`, `option "budget": value 2.5 out of range [1, 10]`},
	}
	for _, c := range cases {
		cfg, err := ParseConfig([]byte(c.config))
		if err != nil {
			t.Fatal(err)
		}
		err = Validate(cfg, installed, d)
		if c.want == "" && err != nil || c.want != "" && (err == nil || err.Error() != c.want) {
			t.Errorf("%s: got %v want %q", c.config, err, c.want)
		}
	}
}

func TestParseConfigInvalid(t *testing.T) {
	for _, bad := range []string{`{"provider":"replay"`, `{}`, `{"provider":"r","model":"m","max_steps":5}`, ``} {
		if _, err := ParseConfig([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	cfg, err := ParseConfig([]byte(`{"provider":"r","model":"m"}`))
	if err != nil || cfg.Provider != "r" || cfg.Model != "m" {
		t.Fatalf("got %+v %v", cfg, err)
	}
	data, _ := json.Marshal(cfg)
	if strings.Contains(string(data), "max_steps") {
		t.Fatal("unexpected field")
	}
}

func TestEnvRoundTrip(t *testing.T) {
	m := map[string]string{"A": "1", "B_KEY": "x=y z"}
	got, err := ParseEnv(FormatEnv(m))
	if err != nil || fmt.Sprint(got) != fmt.Sprint(m) {
		t.Fatalf("got %v %v", got, err)
	}
	got, err = ParseEnv([]byte("# comment\n\nC=3\n"))
	if err != nil || got["C"] != "3" || len(got) != 1 {
		t.Fatalf("got %v %v", got, err)
	}
	if _, err := ParseEnv([]byte("novalue\n")); err == nil {
		t.Fatal("accepted line without =")
	}
}

func TestAcceptsImages(t *testing.T) {
	d := proto.DescribeResponse{Models: []proto.Model{{ID: "a", Images: true}, {ID: "b"}}}
	if !AcceptsImages(d, "a") {
		t.Error("a: want true")
	}
	if AcceptsImages(d, "b") {
		t.Error("b: want false")
	}
	if AcceptsImages(d, "missing") {
		t.Error("missing: want false")
	}
}
