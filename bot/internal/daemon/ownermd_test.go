package daemon

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/SEAN-7/bot/internal/fsio"
)

// mainOwner is main's OWNER.md, in root's control directory.
func (h *harness) mainOwner() string { return filepath.Join(h.mainCtrl, "OWNER.md") }

func TestOwnerSectionForMain(t *testing.T) {
	h := newHarness(t, done())
	os.WriteFile(h.mainOwner(), []byte("Be brief."), 0o644)
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	h.waitEnds(h.mainID, 1)
	want := SystemPrompt + "\n\n" + OwnerHeader + "\n\nBe brief."
	if got := h.requests()[0].System[0].Text; got != want {
		t.Fatalf("first segment %q, want %q", got, want)
	}
}

func TestOwnerHeaderVerbatim(t *testing.T) {
	want := "# Owner's instructions\n\nYour owner wrote the following. You cannot change it, and agents you\ncreate do not see it. It takes precedence over everything else in your\ncontext."
	if OwnerHeader != want {
		t.Fatalf("OwnerHeader %q", OwnerHeader)
	}
	if got := ownerSegment(" \n\t"); got != "" {
		t.Fatalf("whitespace-only owner segment %q", got)
	}
	if got := ownerSegment("x"); got != "\n\n"+want+"\n\nx" {
		t.Fatalf("owner segment %q", got)
	}
}

func TestNoOwnerFileKeepsPrompt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content *string
	}{{"missing", nil}, {"whitespace", ptr(" \n\t\n")}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, done())
			if tc.content != nil {
				os.WriteFile(h.mainOwner(), []byte(*tc.content), 0o644)
			}
			h.start()
			h.drop(h.mainHome, "go.md", "go")
			h.waitEnds(h.mainID, 1)
			if got := h.requests()[0].System[0].Text; got != SystemPrompt {
				t.Fatalf("first segment %q, want the system prompt alone", got)
			}
		})
	}
}

func ptr(s string) *string { return &s }

// runChild admits main's child c, gives both it and main a message, and
// waits for both runs.
func runChild(t *testing.T, h *harness, ctrl string) {
	t.Helper()
	h.drop(h.mainHome, "go.md", "go")
	h.drop(h.childHome(ctrl), "task.md", "work")
	h.waitEnds(h.mainID, 1)
	h.waitEnds(childID("c"), 1)
}

// requestsOf splits the logged requests into main's and the seeded child's.
func requestsOf(h *harness) (mainSystem, childSystem []string, child []string) {
	for _, req := range h.requests() {
		if strings.Contains(req.System[1].Text, childMark) {
			childSystem = append(childSystem, req.System[0].Text)
			var all []string
			for _, s := range req.System {
				all = append(all, s.Text)
			}
			for _, m := range req.Messages {
				all = append(all, m.Content, m.Text)
			}
			child = append(child, strings.Join(all, "\n"))
		} else {
			mainSystem = append(mainSystem, req.System[0].Text)
		}
	}
	return
}

func TestOwnerSectionForChild(t *testing.T) {
	h := newHarness(t)
	os.WriteFile(h.mainOwner(), []byte("Main rules."), 0o644)
	h.start()
	h.markParent()
	ctrl := filepath.Join(h.mainHome, "agents", "c")
	os.MkdirAll(ctrl, 0o755)
	os.WriteFile(filepath.Join(ctrl, "OWNER.md"), []byte("Child rules."), 0o644)
	writeConfig(t, ctrl)
	runChild(t, h, ctrl)
	mainSystem, childSystem, _ := requestsOf(h)
	if len(mainSystem) != 1 || len(childSystem) != 1 {
		t.Fatalf("%d main and %d child requests", len(mainSystem), len(childSystem))
	}
	if want := SystemPrompt + "\n\n" + OwnerHeader + "\n\nChild rules."; childSystem[0] != want {
		t.Fatalf("child's first segment %q, want %q", childSystem[0], want)
	}
	if want := SystemPrompt + "\n\n" + OwnerHeader + "\n\nMain rules."; mainSystem[0] != want {
		t.Fatalf("main's first segment %q, want %q", mainSystem[0], want)
	}
}

func TestParentOwnerNotInherited(t *testing.T) {
	h := newHarness(t)
	os.WriteFile(h.mainOwner(), []byte("MAIN-ONLY"), 0o644)
	h.start()
	h.markParent()
	ctrl := makeChild(t, h, "c", true)
	runChild(t, h, ctrl)
	_, childSystem, child := requestsOf(h)
	if len(child) == 0 {
		t.Fatal("no child request")
	}
	for _, req := range child {
		if strings.Contains(req, "MAIN-ONLY") {
			t.Fatalf("child saw its parent's OWNER.md: %q", req)
		}
	}
	if childSystem[0] != SystemPrompt {
		t.Fatalf("child without OWNER.md has first segment %q", childSystem[0])
	}
}

func TestUnsafeOwnerStopsRun(t *testing.T) {
	cases := []struct {
		name   string
		child  bool
		reason string
		spoil  func(t *testing.T, owner string)
		fix    func(t *testing.T, owner string)
	}{
		{"symlink", false, "is a symlink",
			func(t *testing.T, owner string) {
				target := filepath.Join(filepath.Dir(owner), "elsewhere.md")
				os.WriteFile(target, []byte("Be brief."), 0o644)
				if err := os.Symlink(target, owner); err != nil {
					t.Fatal(err)
				}
			},
			func(t *testing.T, owner string) {
				// Replaced in one step: a poll between removing the link
				// and writing the file would run without an OWNER.md.
				temp := owner + ".new"
				os.WriteFile(temp, []byte("Be brief."), 0o644)
				if err := os.Rename(temp, owner); err != nil {
					t.Fatal(err)
				}
			}},
		{"group-writable file", false, "is writable by group or others",
			func(t *testing.T, owner string) {
				os.WriteFile(owner, []byte("Be brief."), 0o644)
				os.Chmod(owner, 0o664)
			},
			func(t *testing.T, owner string) { os.Chmod(owner, 0o644) }},
		{"world-writable control directory", true, "is writable by group or others",
			func(t *testing.T, owner string) {
				os.WriteFile(owner, []byte("Be brief."), 0o644)
				os.Chmod(filepath.Dir(owner), 0o777)
			},
			func(t *testing.T, owner string) { os.Chmod(filepath.Dir(owner), 0o755) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			home, dir, id := h.mainHome, h.mainCtrl, h.mainID
			owner := h.mainOwner()
			if !tc.child {
				tc.spoil(t, owner)
			}
			h.start()
			if tc.child {
				dir = filepath.Join(h.mainHome, "agents", "c")
				id, owner = childID("c"), filepath.Join(dir, "OWNER.md")
				os.MkdirAll(dir, 0o755)
				tc.spoil(t, owner)
				writeConfig(t, dir)
				home = h.childHome(dir)
			}
			h.drop(home, "go.md", "go")
			errorFile := filepath.Join(dir, "ERROR")
			waitFor(t, "OWNER.md ERROR", func() bool { return fileContains(errorFile, tc.reason) })
			if data, _ := os.ReadFile(errorFile); !strings.HasPrefix(string(data), MsgOwnerPrefix) {
				t.Fatalf("ERROR %q does not start with %q", data, MsgOwnerPrefix)
			}
			time.Sleep(200 * time.Millisecond) // several polls
			if n := len(h.requests()); n != 0 {
				t.Fatalf("%d requests with an unsafe OWNER.md", n)
			}
			if names := inboxNames(home); len(names) != 1 || names[0] != "go.md" {
				t.Fatalf("inbox touched: %v", names)
			}
			if n := len(h.runs(id)); n != 0 {
				t.Fatalf("%d runs started", n)
			}
			if n := strings.Count(h.logs.String(), tc.reason); n != 1 {
				t.Fatalf("OWNER.md error logged %d times:\n%s", n, h.logs)
			}
			tc.fix(t, owner)
			h.waitEnds(id, 1)
			if _, err := os.Stat(errorFile); !os.IsNotExist(err) {
				t.Fatal("OWNER.md ERROR not cleared")
			}
			if got := h.requests()[0].System[0].Text; got != SystemPrompt+ownerSegment("Be brief.") {
				t.Fatalf("first segment after the fix %q", got)
			}
		})
	}
}

func TestOwnerEditTakesEffectNextRun(t *testing.T) {
	h := newHarness(t, textStep("one", 100), textStep("two", 100))
	os.WriteFile(h.mainOwner(), []byte("First rules."), 0o644)
	h.start()
	h.drop(h.mainHome, "a.md", "first")
	h.waitEnds(h.mainID, 1)
	if want := SystemPrompt + ownerSegment("First rules."); h.requests()[0].System[0].Text != want {
		t.Fatalf("first segment %q, want %q", h.requests()[0].System[0].Text, want)
	}
	os.WriteFile(h.mainOwner(), []byte("Second rules."), 0o644)
	h.drop(h.mainHome, "b.md", "second")
	h.waitEnds(h.mainID, 2)
	req := h.requests()[1]
	if len(req.Messages) != 1 || !strings.Contains(req.Messages[0].Content, "second") {
		t.Fatalf("second run is not fresh: %+v", req.Messages)
	}
	if want := SystemPrompt + ownerSegment("Second rules."); req.System[0].Text != want {
		t.Fatalf("first segment %q, want %q", req.System[0].Text, want)
	}
}

// ownerAgents builds main and its child c without running the daemon, so
// readOwner can be checked against users the harness cannot make.
func ownerAgents(t *testing.T, h *harness) (*agent, *agent) {
	t.Helper()
	d := &daemon{cfg: h.cfg}
	main := detachedMain(t, h, d)
	ctrl := writeConfig(t, filepath.Join(h.mainHome, "agents", "c"))
	child := &agent{d: d, id: childID("c"), name: childID("c"), ctrl: ctrl, parent: main, user: "agent-2", done: make(chan struct{})}
	return main, child
}

// The harness runs as one user, so a directory and file with different
// owners need root (TestPrivilegeChildOwnerMd). Without root, a wrong
// expected owner fails at the directory, which is checked first.
func TestChildOwnerDirectoryOfOtherUserRejected(t *testing.T) {
	h := newHarness(t)
	other := uint32(os.Getuid() + 1)
	h.cfg.LookupUser = func(string) (*syscall.Credential, error) { return &syscall.Credential{Uid: other, Gid: other}, nil }
	_, child := ownerAgents(t, h)
	os.WriteFile(filepath.Join(child.ctrl, "OWNER.md"), []byte("x"), 0o644)
	if _, err := child.readOwner(); err == nil || err.Error() != child.ctrl+" is not owned by its owner" {
		t.Fatalf("child OWNER.md in a directory the parent's user does not own: %v", err)
	}
}

// fakeParent is a parent's helper whose Stat reports chosen owners.
type fakeParent map[string]fsio.Entry

func (f fakeParent) Stat(path string) (fsio.Entry, error) {
	e, ok := f[path]
	if !ok {
		return fsio.Entry{}, fs.ErrNotExist
	}
	return e, nil
}

func (f fakeParent) Read(path string, limit int64) ([]byte, int64, error) {
	return []byte("rules"), 5, nil
}

func TestChildOwnerFileOfOtherUserRejected(t *testing.T) {
	ctrl := "/home/agent-1/agents/c"
	path := ctrl + "/OWNER.md"
	parent := fakeParent{ctrl: {Type: "dir", UID: 1000, Mode: 0o755}, path: {Type: "file", UID: 1000, Mode: 0o644}}
	if data, err := readChildOwner(parent, 1000, path); err != nil || string(data) != "rules" {
		t.Fatalf("safe OWNER.md: %q, %v", data, err)
	}
	parent[path] = fsio.Entry{Type: "file", UID: 1001, Mode: 0o644}
	if _, err := readChildOwner(parent, 1000, path); err == nil || err.Error() != path+" is not owned by its owner" {
		t.Fatalf("OWNER.md of another user in the parent's directory: %v", err)
	}
}

func TestOwnerDirectoryWritableRejected(t *testing.T) {
	h := newHarness(t)
	main, _ := ownerAgents(t, h)
	os.WriteFile(h.mainOwner(), []byte("x"), 0o644)
	os.Chmod(h.mainCtrl, 0o775)
	defer os.Chmod(h.mainCtrl, 0o755)
	if _, err := main.readOwner(); err == nil || !strings.Contains(err.Error(), h.mainCtrl+" is writable by group or others") {
		t.Fatalf("group-writable %s: %v", h.mainCtrl, err)
	}
	os.Remove(h.mainOwner())
	if text, err := main.readOwner(); text != "" || err != nil {
		t.Fatalf("missing OWNER.md in an unsafe directory: %q, %v", text, err)
	}
}

func TestChildOwnerParentLookupFails(t *testing.T) {
	h := newHarness(t)
	h.cfg.LookupUser = func(string) (*syscall.Credential, error) { return nil, errors.New("no such user") }
	_, child := ownerAgents(t, h)
	os.WriteFile(filepath.Join(child.ctrl, "OWNER.md"), []byte("x"), 0o644)
	if _, err := child.readOwner(); err == nil {
		t.Fatal("OWNER.md accepted although the parent's user could not be looked up")
	}
}

func TestOwnerNotRegularFileRejected(t *testing.T) {
	h := newHarness(t)
	main, child := ownerAgents(t, h)
	os.Mkdir(h.mainOwner(), 0o755)
	os.Mkdir(filepath.Join(child.ctrl, "OWNER.md"), 0o755)
	for _, a := range []*agent{main, child} {
		if _, err := a.readOwner(); err == nil || !strings.Contains(err.Error(), "is not a regular file") {
			t.Fatalf("%s: OWNER.md directory: %v", a.name, err)
		}
	}
}

func TestOwnerErrorFromEarlierDaemonCleared(t *testing.T) {
	h := newHarness(t, done())
	errorFile := filepath.Join(h.mainCtrl, "ERROR")
	os.WriteFile(errorFile, []byte(MsgOwnerPrefix+"left by an earlier daemon\n"), 0o644)
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	h.waitEnds(h.mainID, 1)
	if _, err := os.Stat(errorFile); !os.IsNotExist(err) {
		t.Fatal("OWNER.md ERROR from before a restart not cleared")
	}
}

func TestOwnerSuccessKeepsOtherErrors(t *testing.T) {
	h := newHarness(t, done())
	errorFile := filepath.Join(h.mainCtrl, "ERROR")
	os.WriteFile(errorFile, []byte("something else\n"), 0o644)
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	h.waitEnds(h.mainID, 1)
	if !fileContains(errorFile, "something else") {
		t.Fatal("an ERROR that is not an OWNER.md error was cleared")
	}
}

// unsafeOwner starts the daemon with a group-writable OWNER.md and a message
// waiting, and returns the ERROR path once the problem is reported.
func unsafeOwner(t *testing.T, h *harness) string {
	t.Helper()
	os.WriteFile(h.mainOwner(), []byte("Be brief."), 0o644)
	os.Chmod(h.mainOwner(), 0o664)
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	errorFile := filepath.Join(h.mainCtrl, "ERROR")
	waitFor(t, "OWNER.md ERROR", func() bool { return fileContains(errorFile, MsgOwnerPrefix) })
	return errorFile
}

func TestOwnerErrorSurvivesConfigError(t *testing.T) {
	h := newHarness(t)
	errorFile := unsafeOwner(t, h)
	want, _ := os.ReadFile(errorFile)
	config := filepath.Join(h.mainHome, "config.json")
	os.WriteFile(config, []byte(`{"provider":"replay","model":"nope"}`), 0o644)
	waitFor(t, "config ERROR", func() bool { return fileContains(errorFile, MsgConfigPrefix) })
	os.WriteFile(config, []byte(testConfig), 0o644)
	waitFor(t, "OWNER.md ERROR again", func() bool {
		got, _ := os.ReadFile(errorFile)
		return string(got) == string(want)
	})
	if n := len(h.requests()); n != 0 {
		t.Fatalf("%d requests with an unsafe OWNER.md", n)
	}
	if n := strings.Count(h.logs.String(), "is writable by group or others"); n != 1 {
		t.Fatalf("OWNER.md error logged %d times:\n%s", n, h.logs)
	}
}

func TestOwnerErrorRewrittenWhenDeleted(t *testing.T) {
	h := newHarness(t)
	errorFile := unsafeOwner(t, h)
	os.Remove(errorFile)
	waitFor(t, "OWNER.md ERROR rewritten", func() bool { return fileContains(errorFile, MsgOwnerPrefix) })
	if n := len(h.requests()); n != 0 {
		t.Fatalf("%d requests with an unsafe OWNER.md", n)
	}
}
