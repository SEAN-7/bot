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

func TestOwnerSectionForMain(t *testing.T) {
	h := newHarness(t, done())
	os.WriteFile(h.cfg.OwnerFile(), []byte("Be brief."), 0o644)
	h.drop(h.cfg.Home, "go.md", "go")
	h.start()
	h.waitEnds(h.cfg.Home, 1)
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
				os.WriteFile(h.cfg.OwnerFile(), []byte(*tc.content), 0o644)
			}
			h.drop(h.cfg.Home, "go.md", "go")
			h.start()
			h.waitEnds(h.cfg.Home, 1)
			if got := h.requests()[0].System[0].Text; got != SystemPrompt {
				t.Fatalf("first segment %q, want the system prompt alone", got)
			}
		})
	}
}

func ptr(s string) *string { return &s }

// runChild admits the child at ctrl, gives both it and main a message, and
// waits for both runs.
func runChild(t *testing.T, h *harness, ctrl string) {
	t.Helper()
	h.drop(h.cfg.Home, "go.md", "go")
	h.start()
	h.drop(h.childHome(ctrl), "task.md", "work")
	h.waitEnds(h.cfg.Home, 1)
	h.waitEnds(ctrl, 1)
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
	h.markParent()
	os.WriteFile(h.cfg.OwnerFile(), []byte("Main rules."), 0o644)
	ctrl := makeChild(t, h, "c", true)
	os.WriteFile(filepath.Join(ctrl, "OWNER.md"), []byte("Child rules."), 0o644)
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
	h.markParent()
	os.WriteFile(h.cfg.OwnerFile(), []byte("MAIN-ONLY"), 0o644)
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
			home, dir, id := h.cfg.Home, h.cfg.Home, h.cfg.Home
			owner := h.cfg.OwnerFile()
			if tc.child {
				dir = makeChild(t, h, "c", true)
				id, owner = dir, filepath.Join(dir, "OWNER.md")
			}
			tc.spoil(t, owner)
			h.start()
			if tc.child {
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

func TestOwnerEditStartsFresh(t *testing.T) {
	h := newHarness(t, textStep("one", 100), textStep("two", 100), textStep("three", 100))
	os.WriteFile(h.cfg.OwnerFile(), []byte("First rules."), 0o644)
	h.drop(h.cfg.Home, "a.md", "first")
	h.start()
	h.waitEnds(h.cfg.Home, 1)
	h.drop(h.cfg.Home, "b.md", "second")
	h.waitEnds(h.cfg.Home, 2)
	if n := len(h.requests()[1].Messages); n != 3 {
		t.Fatalf("unchanged OWNER.md did not continue: %d messages", n)
	}
	os.WriteFile(h.cfg.OwnerFile(), []byte("Second rules."), 0o644)
	h.drop(h.cfg.Home, "c.md", "third")
	h.waitEnds(h.cfg.Home, 3)
	req := h.requests()[2]
	if len(req.Messages) != 1 || !strings.Contains(req.Messages[0].Content, "third") {
		t.Fatalf("edited OWNER.md continued the conversation: %+v", req.Messages)
	}
	if want := SystemPrompt + ownerSegment("Second rules."); req.System[0].Text != want {
		t.Fatalf("first segment %q, want %q", req.System[0].Text, want)
	}
}

// ownerAgents builds a main agent and its child c without running the
// daemon, so readOwner can be checked against users the harness cannot make.
func ownerAgents(t *testing.T, h *harness) (*agent, *agent) {
	t.Helper()
	fs, err := fsio.Start(h.cfg.Self, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fs.Close() })
	d := &daemon{cfg: h.cfg}
	main := newMain(d)
	main.fs = fs
	ctrl := makeChild(t, h, "c", true)
	child := &agent{d: d, id: ctrl, name: "c", ctrl: ctrl, parent: main, user: "agent-1", done: make(chan struct{})}
	return main, child
}

// The harness runs as one user, so a directory and file with different
// owners need root (TestPrivilegeOwnerFileOfOtherUser). Without root, a
// wrong expected owner fails at the directory, which is checked first.
func TestOwnerDirectoryOfOtherUserRejected(t *testing.T) {
	h := newHarness(t)
	h.cfg.OwnerUID = os.Getuid() + 1
	main, _ := ownerAgents(t, h)
	os.WriteFile(h.cfg.OwnerFile(), []byte("x"), 0o644)
	if _, err := main.readOwner(); err == nil || err.Error() != h.cfg.Etc+" is not owned by its owner" {
		t.Fatalf("main OWNER.md in another user's directory: %v", err)
	}
	os.Remove(h.cfg.OwnerFile())
	if text, err := main.readOwner(); text != "" || err != nil {
		t.Fatalf("missing OWNER.md: %q, %v", text, err)
	}
}

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

// fakeInfo is a stat result with a chosen owner.
type fakeInfo struct {
	mode fs.FileMode
	uid  uint32
}

func (f fakeInfo) Name() string       { return "" }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeInfo) Sys() any           { return &syscall.Stat_t{Uid: f.uid} }

func TestMainOwnerFileOfOtherUserRejected(t *testing.T) {
	dir, path := "/etc/agent", "/etc/agent/OWNER.md"
	safeDir := fakeInfo{fs.ModeDir | 0o711, 0}
	if err := checkMainOwner(dir, safeDir, path, fakeInfo{0o644, 0}, 0); err != nil {
		t.Fatalf("safe OWNER.md rejected: %v", err)
	}
	if err := checkMainOwner(dir, safeDir, path, fakeInfo{0o644, 1000}, 0); err == nil || err.Error() != path+" is not owned by its owner" {
		t.Fatalf("OWNER.md of another user in a safe directory: %v", err)
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
	ctrl := "/home/agent/agents/c"
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
	os.WriteFile(h.cfg.OwnerFile(), []byte("x"), 0o644)
	os.Chmod(h.cfg.Etc, 0o775)
	defer os.Chmod(h.cfg.Etc, 0o755)
	if _, err := main.readOwner(); err == nil || !strings.Contains(err.Error(), h.cfg.Etc+" is writable by group or others") {
		t.Fatalf("group-writable %s: %v", h.cfg.Etc, err)
	}
	os.Remove(h.cfg.OwnerFile())
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
	os.Mkdir(h.cfg.OwnerFile(), 0o755)
	os.Mkdir(filepath.Join(child.ctrl, "OWNER.md"), 0o755)
	for _, a := range []*agent{main, child} {
		if _, err := a.readOwner(); err == nil || !strings.Contains(err.Error(), "is not a regular file") {
			t.Fatalf("%s: OWNER.md directory: %v", a.name, err)
		}
	}
}

func TestOwnerErrorFromEarlierDaemonCleared(t *testing.T) {
	h := newHarness(t, done())
	errorFile := filepath.Join(h.cfg.Home, "ERROR")
	os.WriteFile(errorFile, []byte(MsgOwnerPrefix+"left by an earlier daemon\n"), 0o644)
	h.drop(h.cfg.Home, "go.md", "go")
	h.start()
	h.waitEnds(h.cfg.Home, 1)
	if _, err := os.Stat(errorFile); !os.IsNotExist(err) {
		t.Fatal("OWNER.md ERROR from before a restart not cleared")
	}
}

func TestOwnerSuccessKeepsOtherErrors(t *testing.T) {
	h := newHarness(t, done())
	errorFile := filepath.Join(h.cfg.Home, "ERROR")
	os.WriteFile(errorFile, []byte("something else\n"), 0o644)
	h.drop(h.cfg.Home, "go.md", "go")
	h.start()
	h.waitEnds(h.cfg.Home, 1)
	if !fileContains(errorFile, "something else") {
		t.Fatal("an ERROR that is not an OWNER.md error was cleared")
	}
}

// unsafeOwner starts the daemon with a group-writable OWNER.md and a message
// waiting, and returns the ERROR path once the problem is reported.
func unsafeOwner(t *testing.T, h *harness) string {
	t.Helper()
	os.WriteFile(h.cfg.OwnerFile(), []byte("Be brief."), 0o644)
	os.Chmod(h.cfg.OwnerFile(), 0o664)
	h.drop(h.cfg.Home, "go.md", "go")
	h.start()
	errorFile := filepath.Join(h.cfg.Home, "ERROR")
	waitFor(t, "OWNER.md ERROR", func() bool { return fileContains(errorFile, MsgOwnerPrefix) })
	return errorFile
}

func TestOwnerErrorSurvivesConfigError(t *testing.T) {
	h := newHarness(t)
	errorFile := unsafeOwner(t, h)
	want, _ := os.ReadFile(errorFile)
	config := filepath.Join(h.cfg.Home, "config.json")
	os.WriteFile(config, []byte(`{"provider":"replay","model":"nope"}`), 0o644)
	waitFor(t, "config ERROR", func() bool { return fileContains(errorFile, MsgConfigPrefix) })
	os.WriteFile(config, []byte(`{"provider":"replay","model":"replay-1"}`), 0o644)
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
