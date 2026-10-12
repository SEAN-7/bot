package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/SEAN-7/bot/internal/fsio"
	"github.com/SEAN-7/bot/internal/journal"
	"github.com/SEAN-7/bot/internal/layout"
	"github.com/SEAN-7/bot/internal/plugin"
	"github.com/SEAN-7/bot/internal/proto"
)

var replayBin string

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "_fsio" {
		if err := fsio.Serve(os.Stdin, os.Stdout); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	// REPLAY_BIN lets a cross-compiled test binary run where Go is absent.
	if replayBin = os.Getenv("REPLAY_BIN"); replayBin != "" {
		os.Exit(m.Run())
	}
	dir, err := os.MkdirTemp("", "replay")
	if err != nil {
		panic(err)
	}
	os.Chmod(dir, 0o755)
	replayBin = filepath.Join(dir, "replay")
	if out, err := exec.Command("go", "build", "-o", replayBin, "github.com/SEAN-7/bot/plugins/replay").CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

const replayHeader = `{"models":[{"id":"replay-1","context_tokens":1000,"options":{"style":{"description":"s","values":["a","b"]}}}]}`

type harness struct {
	t      *testing.T
	root   string
	cfg    Config
	script string
	reqLog string
	logs   *strings.Builder
	cancel context.CancelFunc
	done   chan error
}

func step(command string, totalTokens int) string {
	line, _ := json.Marshal(map[string]any{"command": command, "usage": map[string]int{"input_tokens": totalTokens}})
	return string(line)
}

func matchStep(match, command string, totalTokens int) string {
	line, _ := json.Marshal(map[string]any{"match": match, "command": command, "usage": map[string]int{"input_tokens": totalTokens}})
	return string(line)
}

func done() string { return `{"text":"done","usage":{"input_tokens":10}}` }

func newHarness(t *testing.T, steps ...string) *harness {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l := layout.Layout{Home: filepath.Join(root, "home", "agent"), Homes: filepath.Join(root, "home"), Etc: filepath.Join(root, "etc", "agent"), Lib: filepath.Join(root, "lib"), Log: filepath.Join(root, "log")}
	tmp := filepath.Join(root, "tmp")
	for _, dir := range []string{l.Plugins(), l.Credentials(), l.OwnerSpool(), l.Log, filepath.Join(l.Home, "inbox"), tmp} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bin, _ := os.ReadFile(replayBin)
	os.WriteFile(filepath.Join(l.Plugins(), "replay"), bin, 0o755)
	h := &harness{t: t, root: root, script: filepath.Join(root, "script.jsonl"), reqLog: filepath.Join(root, "requests.jsonl"), logs: &strings.Builder{}}
	h.setScript(steps...)
	os.WriteFile(filepath.Join(l.Credentials(), "replay.env"), plugin.FormatEnv(map[string]string{"REPLAY_SCRIPT": h.script, "REPLAY_LOG": h.reqLog}), 0o600)
	os.WriteFile(filepath.Join(l.Home, "README.md"), []byte("readme"), 0o644)
	os.WriteFile(filepath.Join(l.Home, "TASK.md"), []byte("task"), 0o644)
	os.WriteFile(filepath.Join(l.Home, "config.json"), []byte(`{"provider":"replay","model":"replay-1"}`), 0o644)
	self, _ := os.Executable()
	h.cfg = DefaultConfig(self)
	h.cfg.Layout = l
	h.cfg.AgentUser, h.cfg.ProviderUser = "", ""
	// Every pool user resolves to the current user, so all helpers run as us.
	h.cfg.PoolUsers = []string{"agent-1", "agent-2", "agent-3"}
	h.cfg.LookupUser = func(string) (*syscall.Credential, error) { return nil, nil }
	h.cfg.System = &fakeSystem{}
	h.cfg.TmpDirs = []string{tmp}
	h.cfg.OwnerUID = os.Getuid()
	h.cfg.Poll = 10 * time.Millisecond
	h.cfg.CommandTimeout = 2 * time.Second
	h.cfg.RetryBase, h.cfg.RetryMax = 10*time.Millisecond, 50*time.Millisecond
	h.cfg.DrainTimeout = 2 * time.Second
	h.cfg.Logger = log.New(io.MultiWriter(h.logs, testWriter{t}), "", 0)
	t.Cleanup(h.stop)
	return h
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func (h *harness) setScript(steps ...string) {
	os.WriteFile(h.script, []byte(strings.Join(append([]string{replayHeader}, steps...), "\n")+"\n"), 0o644)
}

func (h *harness) start() {
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel, h.done = cancel, make(chan error, 1)
	go func() { h.done <- Run(ctx, h.cfg) }()
}

func (h *harness) stop() {
	if h.cancel == nil {
		return
	}
	h.cancel()
	select {
	case err := <-h.done:
		if err != nil {
			h.t.Errorf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		h.t.Error("daemon did not stop")
	}
	h.cancel = nil
}

// drop delivers a message atomically, as the seed README advises.
func (h *harness) drop(home, name, text string) {
	h.t.Helper()
	temp := filepath.Join(home, "inbox", "."+name)
	if err := os.WriteFile(temp, []byte(text), 0o644); err != nil {
		h.t.Fatal(err)
	}
	if err := os.Rename(temp, filepath.Join(home, "inbox", name)); err != nil {
		h.t.Fatal(err)
	}
}

func readRecords(path string) []map[string]any {
	data, _ := os.ReadFile(path)
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

// runs returns every run journal of the agent in order. Journals are keyed
// by the agent's id: the main home, or a subagent's control directory.
func (h *harness) runs(agentID string) []string {
	matches, _ := filepath.Glob(filepath.Join(h.cfg.JournalDir(agentID), "*.jsonl"))
	var runs []string
	for _, m := range matches {
		if journal.IsRun(filepath.Base(m)) {
			runs = append(runs, m)
		}
	}
	sort.Strings(runs)
	return runs
}

// childHome waits for the daemon to admit the subagent at ctrl and returns
// its home, the target of ctrl/home.
func (h *harness) childHome(ctrl string) string {
	h.t.Helper()
	var home string
	waitFor(h.t, "admission of "+ctrl, func() bool {
		home, _ = os.Readlink(filepath.Join(ctrl, "home"))
		return home != ""
	})
	return home
}

// mainUser is the main agent's user name as the daemon reports it.
func (h *harness) mainUser() string {
	if h.cfg.AgentUser != "" {
		return h.cfg.AgentUser
	}
	u, err := user.Current()
	if err != nil {
		h.t.Fatal(err)
	}
	return u.Username
}

func ended(path string) bool {
	records := readRecords(path)
	return len(records) > 0 && records[len(records)-1]["type"] == "end"
}

// waitEnds waits until n runs of the agent have ended and returns the records
// of the n-th run.
func (h *harness) waitEnds(agentID string, n int) []map[string]any {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		runs := h.runs(agentID)
		count := 0
		for _, run := range runs {
			if ended(run) {
				count++
			}
		}
		if count >= n {
			return readRecords(runs[n-1])
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %d ended runs of %s; logs:\n%s", n, agentID, h.logs)
	return nil
}

func lastEnd(records []map[string]any) string {
	if len(records) == 0 {
		return ""
	}
	reason, _ := records[len(records)-1]["reason"].(string)
	return reason
}

func (h *harness) requests() []proto.CompleteRequest {
	f, err := os.Open(h.reqLog)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []proto.CompleteRequest
	scanner := bufio.NewScanner(f)
	scanner.Buffer(nil, 64<<20)
	for scanner.Scan() {
		var entry struct {
			Request proto.CompleteRequest `json:"request"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) == nil {
			out = append(out, entry.Request)
		}
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func inboxNames(dir string) []string {
	entries, _ := os.ReadDir(filepath.Join(dir, "inbox"))
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() {
			names = append(names, e.Name())
		}
	}
	return names
}

func fileContains(path, text string) bool {
	data, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(data), text)
}

var _ = fmt.Sprint
