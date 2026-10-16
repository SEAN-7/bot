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
	"path/filepath"
	"sort"
	"strings"
	"sync"
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

const replayHeader = `{"models":[{"id":"replay-1","context_tokens":1000,"image_types":["image/jpeg","image/png","image/gif"],"options":{"style":{"description":"s","values":["a","b"]}}},{"id":"replay-noimg","context_tokens":1000}]}`

type harness struct {
	t        *testing.T
	root     string
	cfg      Config
	mainID   string // "main", root's child
	mainCtrl string // root's control directory for main
	mainHome string // main's home: the first pool user's, as main is admitted first
	script   string
	reqLog   string
	logs     *syncBuffer
	cancel   context.CancelFunc
	done     chan error
}

func step(command string, totalTokens int) string {
	line, _ := json.Marshal(map[string]any{"command": command, "usage": map[string]int{"input_tokens": totalTokens}})
	return string(line)
}

// waitStep is a step whose sh call also gives a wait argument.
func waitStep(command string, wait any, totalTokens int) string {
	line, _ := json.Marshal(map[string]any{"command": command, "wait": wait, "usage": map[string]int{"input_tokens": totalTokens}})
	return string(line)
}

func matchStep(match, command string, totalTokens int) string {
	line, _ := json.Marshal(map[string]any{"match": match, "command": command, "usage": map[string]int{"input_tokens": totalTokens}})
	return string(line)
}

func done() string { return `{"text":"done","usage":{"input_tokens":10}}` }

const testConfig = `{"provider":"replay","model":"replay-1"}`

// newHarness lays out a temporary root with root's control directory for
// main; the daemon admits main from it, as any agent, once started.
func newHarness(t *testing.T, steps ...string) *harness {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l := layout.Layout{Homes: filepath.Join(root, "home"), Etc: filepath.Join(root, "etc", "agent"), Lib: filepath.Join(root, "lib"), Log: filepath.Join(root, "log")}
	tmp := filepath.Join(root, "tmp")
	for _, dir := range []string{l.Plugins(), l.Credentials(), l.Homes, l.Lib, l.Log, tmp} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bin, _ := os.ReadFile(replayBin)
	os.WriteFile(filepath.Join(l.Plugins(), "replay"), bin, 0o755)
	h := &harness{t: t, root: root, script: filepath.Join(root, "script.jsonl"), reqLog: filepath.Join(root, "requests.jsonl"), logs: &syncBuffer{}}
	h.mainID, h.mainHome = "main", l.Home("agent-1")
	h.setScript(steps...)
	os.WriteFile(filepath.Join(l.Credentials(), "replay.env"), plugin.FormatEnv(map[string]string{"REPLAY_SCRIPT": h.script, "REPLAY_LOG": h.reqLog}), 0o600)
	// providers.json as `agent provider install` writes it: the gate checks
	// config.json against it and never runs the plugin.
	os.WriteFile(l.ProvidersCache(), []byte(`{"providers":{"replay":`+replayHeader+`}}`), 0o644)
	self, _ := os.Executable()
	h.cfg = DefaultConfig(self)
	h.cfg.Layout = l
	h.mainCtrl = topLevel(t, h, "main")
	h.cfg.RootUser, h.cfg.ProviderUser = "", ""
	// Every agent user resolves to the current user, so all helpers run as us.
	h.cfg.LookupUser = func(string) (*syscall.Credential, error) { return nil, nil }
	h.cfg.System = &fakeSystem{}
	h.cfg.TmpDirs = []string{tmp}
	h.cfg.Poll = 10 * time.Millisecond
	h.cfg.CommandWait, h.cfg.MaxCommandWait = 2*time.Second, 3*time.Second
	h.cfg.RetryBase, h.cfg.RetryMax = 10*time.Millisecond, 50*time.Millisecond
	h.cfg.DrainTimeout = 2 * time.Second
	h.cfg.Logger = log.New(io.MultiWriter(h.logs, testWriter{t}), "", 0)
	t.Cleanup(h.stop)
	return h
}

// writeConfig creates the control directory ctrl holding config.json, as a
// parent would; config.json is what makes the daemon admit it.
func writeConfig(t *testing.T, ctrl string) string {
	t.Helper()
	if err := os.MkdirAll(ctrl, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctrl, "config.json"), []byte(testConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	return ctrl
}

// topLevel creates root's control directory for a top-level agent.
func topLevel(t *testing.T, h *harness, name string) string {
	t.Helper()
	return writeConfig(t, filepath.Join(h.cfg.RootHome(), "agents", name))
}

// detachedMain builds root and main as the daemon would, without running
// it, both with one helper running as us; d gets the root.
func detachedMain(t *testing.T, h *harness, d *daemon) *agent {
	t.Helper()
	fs, err := fsio.Start(h.cfg.Self, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fs.Close() })
	d.root = &agent{d: d, name: "root", home: h.cfg.RootHome(), user: h.cfg.RootUser, fs: fs, done: make(chan struct{})}
	inbox := h.cfg.Inbox("agent-1")
	os.MkdirAll(inbox, 0o770)
	os.MkdirAll(h.mainHome, 0o755)
	os.Symlink(inbox, filepath.Join(h.mainHome, "inbox"))
	return &agent{d: d, id: h.mainID, name: h.mainID, home: h.mainHome, inbox: inbox, ctrl: h.mainCtrl, user: "agent-1", parent: d.root, fs: fs, done: make(chan struct{})}
}

// syncBuffer collects the daemon's log; tests read it while the daemon writes.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func (h *harness) setScript(steps ...string) {
	os.WriteFile(h.script, []byte(strings.Join(append([]string{replayHeader}, steps...), "\n")+"\n"), 0o644)
}

// start runs the daemon and waits until main is admitted, so a test can
// write into its home: admission clears a newly assigned home.
func (h *harness) start() {
	h.t.Helper()
	h.run()
	h.childHome(h.mainCtrl)
}

// run starts the daemon without waiting for anything.
func (h *harness) run() {
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
// by the agent's id, its path from root: main, main/agents/c.
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

// sendOwner writes a message from root into main's inbox, as the README's
// send function does: staged in the home, then renamed in.
func (h *harness) sendOwner(text string) string {
	h.t.Helper()
	return h.sendTo(h.mainHome, text)
}

// sendTo writes a message into the inbox of the agent at home, staged as a
// dot-file in the home and renamed in, as any sender does.
func (h *harness) sendTo(home, text string) string {
	h.t.Helper()
	temp := filepath.Join(home, ".m")
	if err := os.WriteFile(temp, []byte(text), 0o644); err != nil {
		h.t.Fatal(err)
	}
	path := filepath.Join(home, "inbox", time.Now().UTC().Format("20060102T150405.000000000Z")+".md")
	if err := os.Rename(temp, path); err != nil {
		h.t.Fatal(err)
	}
	return path
}

// sawRecord reports whether any request message contains every fragment.
func (h *harness) sawRecord(fragments ...string) bool {
	for _, req := range h.requests() {
		for _, m := range req.Messages {
			all := true
			for _, f := range fragments {
				if !strings.Contains(m.Content, f) {
					all = false
					break
				}
			}
			if all {
				return true
			}
		}
	}
	return false
}

// sendMessageText is the shell command an agent runs to message its owner:
// the file staged in its home, then moved into the owner's inbox through
// ~/owner.
func sendMessageText(text string) string {
	return fmt.Sprintf("printf %%s %s > ~/.m && mv ~/.m ~/owner/$(date -u +%%Y%%m%%dT%%H%%M%%SZ).md", shellQuote(text))
}

// sendDownText is the shell command an agent runs to message its subagent
// name, once the subagent is admitted and its inbox exists.
func sendDownText(name, text string) string {
	return fmt.Sprintf("until [ -d ~/agents/%[1]s/home/inbox ]; do sleep 0.05; done; printf %%s %[2]s > ~/.m && mv ~/.m ~/agents/%[1]s/home/inbox/$(date -u +%%Y%%m%%dT%%H%%M%%SZ).md", name, shellQuote(text))
}

// Replay match strings that tell main's requests from a seeded subagent's:
// markParent gives main this README, and a subagent's README is the seed's.
const (
	parentMark = "parent-readme"
	childMark  = "This file is yours"
)

// markParent replaces main's seeded README; call it once main is admitted.
func (h *harness) markParent() {
	os.WriteFile(filepath.Join(h.mainHome, "README.md"), []byte(parentMark), 0o644)
}

func doneFor(mark string) string {
	return `{"match":"` + mark + `","text":"done","usage":{"input_tokens":10}}`
}

// fileText returns the contents of path.
func fileText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
