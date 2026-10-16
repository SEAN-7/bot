package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

// OWNER.md has to be a regular file, not a link: the helper never reads
// through links. The control directory is the parent's, so its modes and
// owners are not checked.
func TestOwnerNotRegularFileIsError(t *testing.T) {
	h := newHarness(t)
	main, child := ownerAgents(t, h)
	os.Mkdir(h.mainOwner(), 0o755)
	os.Mkdir(filepath.Join(child.ctrl, "OWNER.md"), 0o755)
	for _, a := range []*agent{main, child} {
		if _, err := a.readOwner(); err == nil || !strings.Contains(err.Error(), "is not a regular file") {
			t.Fatalf("%s: OWNER.md directory: %v", a.name, err)
		}
	}
	os.Remove(h.mainOwner())
	target := filepath.Join(h.mainCtrl, "elsewhere.md")
	os.WriteFile(target, []byte("hi"), 0o644)
	if err := os.Symlink(target, h.mainOwner()); err != nil {
		t.Fatal(err)
	}
	if text, err := main.readOwner(); err == nil || !strings.Contains(err.Error(), "is not a regular file") {
		t.Fatalf("OWNER.md link: %q, %v", text, err)
	}
	os.Remove(h.mainOwner())
	os.Rename(target, h.mainOwner())
	os.Chmod(h.mainCtrl, 0o775)
	if text, err := main.readOwner(); err != nil || text != "hi" {
		t.Fatalf("OWNER.md in a group-writable directory: %q, %v", text, err)
	}
}

// ERROR holds the most recent cause, whatever kind it is, and goes when a
// run starts.
func TestErrorHoldsLatestCause(t *testing.T) {
	h := newHarness(t, done())
	os.WriteFile(filepath.Join(h.mainCtrl, "config.json"), []byte(`{"provider":"replay","model":"nope"}`), 0o644)
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	errorFile := filepath.Join(h.mainCtrl, "ERROR")
	startsWith := func(prefix string) bool {
		data, err := os.ReadFile(errorFile)
		return err == nil && strings.HasPrefix(string(data), prefix)
	}
	waitFor(t, "config ERROR", func() bool { return startsWith(MsgConfigPrefix) })
	os.Mkdir(h.mainOwner(), 0o755)
	os.WriteFile(filepath.Join(h.mainHome, "config.json"), []byte(testConfig), 0o644)
	waitFor(t, "OWNER.md ERROR", func() bool { return startsWith(MsgOwnerPrefix) })
	if fileContains(errorFile, MsgConfigPrefix) {
		t.Fatalf("ERROR keeps a cause that is gone: %q", fileText(t, errorFile))
	}
	os.Remove(h.mainOwner())
	h.waitEnds(h.mainID, 1)
	if _, err := os.Stat(errorFile); !os.IsNotExist(err) {
		t.Fatalf("ERROR after a run started: %v", err)
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

// A run starting removes ERROR whatever it holds, so one left by an earlier
// release's daemon does not outlive its cause.
func TestRunStartClearsAnyError(t *testing.T) {
	h := newHarness(t, done())
	errorFile := filepath.Join(h.mainCtrl, "ERROR")
	os.WriteFile(errorFile, []byte("something else\n"), 0o644)
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	h.waitEnds(h.mainID, 1)
	if _, err := os.Stat(errorFile); !os.IsNotExist(err) {
		t.Fatal("an ERROR of unknown kind survived a run starting")
	}
}

// unsafeOwner starts the daemon with an OWNER.md that is a directory and a
// message waiting, and returns the ERROR path once the problem is reported.
func unsafeOwner(t *testing.T, h *harness) string {
	t.Helper()
	os.Mkdir(h.mainOwner(), 0o755)
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	errorFile := filepath.Join(h.mainCtrl, "ERROR")
	waitFor(t, "OWNER.md ERROR", func() bool { return fileContains(errorFile, MsgOwnerPrefix) })
	return errorFile
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
