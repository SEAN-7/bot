package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/SEAN-7/bot/internal/cli"
	"github.com/SEAN-7/bot/internal/osuser"
	"github.com/SEAN-7/bot/internal/plugin"
)

// etcMode is the mode install.sh gives /etc/agent.
const etcMode = 0o711

// rootHarness runs the daemon as root with commands, file access and plugins
// as unprivileged test users, so the kernel enforces the boundaries: every
// agent as one of the AGENT_TEST_POOL users, main as the first, admitted
// from root's control directory, and the root-only operations through the
// real System. It returns main's user.
func rootHarness(t *testing.T, steps ...string) (*harness, string) {
	t.Helper()
	if os.Geteuid() != 0 || os.Getenv("AGENT_ROOT_TESTS") != "1" {
		t.Skip("needs root and AGENT_ROOT_TESTS=1")
	}
	h := newHarness(t, steps...)
	// t.TempDir creates 0700 directories; the test users must traverse them.
	for dir := h.root; dir != filepath.Dir(os.TempDir()) && dir != "/"; dir = filepath.Dir(dir) {
		if strings.HasPrefix(filepath.Base(dir), "Test") || dir == h.root || strings.HasPrefix(filepath.Base(dir), "00") {
			os.Chmod(dir, 0o755)
		}
	}
	replayDir := filepath.Join(h.root, "replay")
	os.MkdirAll(replayDir, 0o755)
	h.reqLog = filepath.Join(replayDir, "requests.jsonl")
	os.WriteFile(filepath.Join(h.cfg.Credentials(), "replay.env"), plugin.FormatEnv(map[string]string{"REPLAY_SCRIPT": h.script, "REPLAY_LOG": h.reqLog}), 0o600)
	binary, _ := os.ReadFile(h.cfg.Self)
	h.cfg.Self = filepath.Join(h.root, "daemon.test")
	os.WriteFile(filepath.Join(h.root, "daemon.test"), binary, 0o755)
	// Mirror install.sh: /etc/agent traversable only, root's control
	// directories root-only, plugins for the provider group, credentials
	// root-only; plugins run as a separate provider user.
	provider := os.Getenv("AGENT_TEST_PROVIDER_USER")
	if provider == "" {
		provider = "agent-test-provider"
	}
	puid, pgid := lookupIDs(t, provider)
	os.Chmod(h.cfg.Etc, etcMode)
	os.Chmod(filepath.Dir(h.mainCtrl), 0o700)
	os.Chmod(h.mainCtrl, 0o700)
	os.Chmod(h.cfg.Credentials(), 0o700)
	os.Chown(h.cfg.Plugins(), 0, pgid)
	os.Chmod(h.cfg.Plugins(), 0o750)
	plugin := filepath.Join(h.cfg.Plugins(), "replay")
	os.Chown(plugin, 0, pgid)
	os.Chmod(plugin, 0o750)
	os.Chown(replayDir, puid, pgid)
	h.cfg.RootUser, h.cfg.ProviderUser = "root", provider
	h.cfg.LookupUser = osuser.Lookup
	// The pool: real users, the real System, homes in a root-owned Homes
	// and a sticky world-writable temporary directory, as on the VM.
	pool := os.Getenv("AGENT_TEST_POOL")
	if pool == "" {
		pool = "agent-test-1,agent-test-2,agent-test-3"
	}
	h.cfg.PoolUsers = strings.Split(pool, ",")
	h.mainHome = h.cfg.PoolHome(h.cfg.PoolUsers[0])
	sys := newExecSystem()
	if !withSystemd() {
		sys = noLinger{sys} // a container without systemd: everything else is real
	}
	h.cfg.System = sys
	if err := os.Chmod(h.cfg.Homes, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, u := range h.cfg.PoolUsers {
		lookupIDs(t, u)
		if err := sys.PrepareHome(u, h.cfg.PoolHome(u)); err != nil {
			t.Fatal(err)
		}
	}
	tmp := filepath.Join(h.root, "tmp")
	if err := os.Chmod(tmp, 0o777|fs.ModeSticky); err != nil {
		t.Fatal(err)
	}
	h.cfg.TmpDirs = []string{tmp}
	// Undo what admission did to the real users, whatever the test left:
	// after the daemon stops (newHarness's cleanup runs after this one).
	t.Cleanup(func() {
		h.stop()
		for _, u := range h.cfg.PoolUsers {
			for _, err := range []error{sys.DisableLinger(u), sys.RemoveCrontab(u), sys.StopProcesses(u), sys.SetMembers(u, nil)} {
				if err != nil {
					t.Logf("cleanup %s: %v", u, err)
				}
			}
		}
	})
	return h, h.cfg.PoolUsers[0]
}

// noLinger is the real System without lingering, which needs systemd.
type noLinger struct{ System }

func (noLinger) EnableLinger(string) error  { return nil }
func (noLinger) DisableLinger(string) error { return nil }

// lookupIDs returns the uid and primary gid of a test user.
func lookupIDs(t *testing.T, name string) (int, int) {
	t.Helper()
	u, err := user.Lookup(name)
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	return uid, gid
}

// rootChild creates a control directory under main's agents/ as main's user
// would, once main is admitted: the directory and its starting config.json
// belong to that user.
func rootChild(t *testing.T, h *harness, name string) string {
	t.Helper()
	uid, gid := lookupIDs(t, h.cfg.PoolUsers[0])
	agents := filepath.Join(h.mainHome, "agents")
	ctrl := filepath.Join(agents, name)
	if err := os.MkdirAll(ctrl, 0o755); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(ctrl, "config.json")
	if err := os.WriteFile(config, []byte(`{"provider":"replay","model":"replay-1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{agents, ctrl, config} {
		if err := os.Lchown(path, uid, gid); err != nil {
			t.Fatal(err)
		}
	}
	return ctrl
}

// ownerName is the user owning path.
func ownerName(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	u, err := user.LookupId(strconv.Itoa(int(info.Sys().(*syscall.Stat_t).Uid)))
	if err != nil {
		t.Fatal(err)
	}
	return u.Username
}

// toolOutput waits for a command result containing marker and returns it.
func (h *harness) toolOutput(marker string) string {
	h.t.Helper()
	var out string
	waitFor(h.t, "a command result with "+marker, func() bool {
		for _, req := range h.requests() {
			for _, m := range req.Messages {
				if strings.Contains(m.Content, marker) {
					out = m.Content
					return true
				}
			}
		}
		return false
	})
	return out
}

// exitCodes reads the "key=N" lines a command echoed and fails unless each
// key is present and its code is zero (want true) or non-zero (want false).
func exitCodes(t *testing.T, out string, want bool, keys ...string) {
	t.Helper()
	t.Logf("command result: %q", out)
	for _, key := range keys {
		m := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `=(\d+)$`).FindStringSubmatch(out)
		if m == nil {
			t.Fatalf("no %s in %q", key, out)
		}
		if (m[1] == "0") != want {
			t.Fatalf("%s=%s, want success %v, in %q", key, m[1], want, out)
		}
	}
}

// exitCode runs a command as root and returns its exit status.
func exitCode(t *testing.T, name string, args ...string) int {
	t.Helper()
	err := exec.Command(name, args...).Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	} else if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return 0
}

// groupMembers is the member list of group in the group database.
func groupMembers(t *testing.T, group string) string {
	t.Helper()
	out, err := exec.Command("getent", "group", group).Output()
	if err != nil {
		t.Fatalf("getent group %s: %v", group, err)
	}
	fields := strings.Split(strings.TrimSpace(string(out)), ":")
	if len(fields) != 4 {
		t.Fatalf("getent group %s: %q", group, out)
	}
	return fields[3]
}

// statIs fails unless path has this owner, group and mode.
func statIs(t *testing.T, path string, uid, gid int, mode fs.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	st := info.Sys().(*syscall.Stat_t)
	if int(st.Uid) != uid || int(st.Gid) != gid || info.Mode() != mode {
		t.Fatalf("%s is %d:%d %v, want %d:%d %v", path, st.Uid, st.Gid, info.Mode(), uid, gid, mode)
	}
}

// withSystemd reports whether systemd runs this machine, so lingering can be
// checked; containers without it still check everything else.
func withSystemd() bool {
	_, err := os.Stat("/run/systemd/system")
	return err == nil
}

func lingering(user string) bool {
	_, err := os.Stat(filepath.Join("/var/lib/systemd/linger", user))
	return err == nil
}

// waitReclaimed waits until the pool no longer holds user.
func (h *harness) waitReclaimed(user string) {
	h.t.Helper()
	waitFor(h.t, "reclaim of "+user, func() bool {
		return !strings.Contains(poolJSON(h), `"`+user+`"`) && strings.Contains(h.logs.String(), user+": reclaimed")
	})
}

func secretFile(t *testing.T, h *harness, mode os.FileMode, text string) string {
	path := filepath.Join(h.root, "secret")
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
	os.Chmod(path, mode)
	return path
}

func TestPrivilegeSymlinkedReadme(t *testing.T) {
	h, name := rootHarness(t, done())
	secret := secretFile(t, h, 0o600, "TOPSECRET")
	h.start()
	readme := filepath.Join(h.mainHome, "README.md")
	os.Remove(readme)
	os.Symlink(secret, readme)
	leak := filepath.Join(h.mainHome, "inbox", "leak.md")
	os.Symlink(secret, leak)
	u, _ := user.Lookup(name)
	uid, _ := strconv.Atoi(u.Uid)
	os.Lchown(readme, uid, -1)
	os.Lchown(leak, uid, -1)
	h.drop(h.mainHome, "go.md", "go")
	h.waitEnds(h.mainID, 1)
	if sys := h.requests()[0].System[1].Text; !strings.Contains(sys, "could not be read") {
		t.Fatalf("README segment %q", sys)
	}
	if fileContains(h.reqLog, "TOPSECRET") {
		t.Fatal("privileged file reached the model")
	}
	for _, run := range h.runs(h.mainID) {
		if fileContains(run, "TOPSECRET") {
			t.Fatal("privileged file reached the journal")
		}
	}
}

// TestPrivilegeErrorSymlink: a parent that plants a symlink as its child's
// ERROR cannot make the daemon write through it.
func TestPrivilegeErrorSymlink(t *testing.T) {
	h, name := rootHarness(t)
	original := secretFile(t, h, 0o644, "ORIGINAL")
	h.start()
	ctrl := rootChild(t, h, "c")
	home := h.childHome(ctrl)
	os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"provider":"replay","model":"nope"}`), 0o644)
	errorFile := filepath.Join(ctrl, "ERROR")
	os.Symlink(original, errorFile)
	u, _ := user.Lookup(name)
	uid, _ := strconv.Atoi(u.Uid)
	os.Lchown(errorFile, uid, -1)
	h.drop(home, "go.md", "go")
	waitFor(t, "ERROR written", func() bool { return fileContains(errorFile, "config.json") })
	if data, _ := os.ReadFile(original); string(data) != "ORIGINAL" {
		t.Fatalf("privileged file overwritten: %q", data)
	}
	if info, _ := os.Lstat(errorFile); info.Mode()&fs.ModeSymlink != 0 {
		t.Fatal("ERROR is still a symlink")
	}
}

func TestPrivilegeJournalNotWritable(t *testing.T) {
	h, _ := rootHarness(t, step("echo tampered >> journal/*.jsonl", 100), done())
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	h.waitEnds(h.mainID, 1)
	if tool := h.requests()[1].Messages[2].Content; strings.HasPrefix(tool, "exit status 0") {
		t.Fatalf("agent wrote to its journal: %q", tool)
	}
	for _, run := range h.runs(h.mainID) {
		if fileContains(run, "tampered\n") {
			t.Fatal("journal modified")
		}
	}
}

func TestPrivilegeCommandsRunAsAgent(t *testing.T) {
	h, name := rootHarness(t, step("id -un; cat ../../etc/agent/credentials/replay.env; ../../etc/agent/plugins/replay </dev/null; echo plugin-rc=$?", 100), done())
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	h.waitEnds(h.mainID, 1)
	tool := h.requests()[1].Messages[2].Content
	if !strings.Contains(tool, name) || strings.Contains(tool, "REPLAY_SCRIPT=") || !strings.Contains(tool, "plugin-rc=126") {
		t.Fatalf("tool %q", tool)
	}
}

func TestPrivilegeHelperNotDumpable(t *testing.T) {
	h, _ := rootHarness(t, step("cat /proc/$(pgrep -f 'daemon.test _fsio' | head -n 1)/environ >/dev/null; echo environ-rc=$?", 100), done())
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	h.waitEnds(h.mainID, 1)
	if tool := h.requests()[1].Messages[2].Content; !strings.Contains(tool, "environ-rc=1") {
		t.Fatalf("agent can inspect the file helper: %q", tool)
	}
}

func TestPrivilegeConversationLogNotWritable(t *testing.T) {
	h, _ := rootHarness(t)
	log := h.cfg.ConversationFile(h.mainID)
	h.setScript(step(sendMessage, 100), step("echo forged >> "+log+"; echo rc=$?", 100), done())
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	h.waitEnds(h.mainID, 1)
	if tool := h.requests()[2].Messages[4].Content; !strings.Contains(tool, "rc=1") {
		t.Fatalf("agent wrote to its conversation log: %q", tool)
	}
	if fileContains(log, "forged") {
		t.Fatal("conversation log modified by the agent")
	}
}

// TestPrivilegeOwnerFileOfOtherUser gives main's OWNER.md to main's own user
// in root's control directory: only the file fails the check.
func TestPrivilegeOwnerFileOfOtherUser(t *testing.T) {
	h, name := rootHarness(t, done())
	owner := filepath.Join(h.mainCtrl, "OWNER.md")
	os.WriteFile(owner, []byte("Be brief."), 0o644)
	u, _ := user.Lookup(name)
	uid, _ := strconv.Atoi(u.Uid)
	if err := os.Chown(owner, uid, -1); err != nil {
		t.Fatal(err)
	}
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	errorFile := filepath.Join(h.mainCtrl, "ERROR")
	want := MsgOwnerPrefix + owner + " is not owned by its owner\n"
	waitFor(t, "OWNER.md ERROR", func() bool {
		data, _ := os.ReadFile(errorFile)
		return string(data) == want
	})
	if n := len(h.requests()); n != 0 {
		t.Fatalf("%d requests with an OWNER.md the agent owns", n)
	}
	os.Chown(owner, 0, -1)
	h.waitEnds(h.mainID, 1)
	if got := h.requests()[0].System[0].Text; got != SystemPrompt+ownerSegment("Be brief.") {
		t.Fatalf("first segment %q", got)
	}
}

// TestPrivilegeSubagentIsolation: a subagent cannot write its parent's home,
// its control directory or its OWNER.md, nor read a sibling's home; its
// parent can read its home and journals; a sibling can read neither.
func TestPrivilegeSubagentIsolation(t *testing.T) {
	h, _ := rootHarness(t)
	h.start()
	ctrlA, ctrlB := rootChild(t, h, "a"), rootChild(t, h, "b")
	homeA, homeB := h.childHome(ctrlA), h.childHome(ctrlB)
	if ownerName(t, homeA) == ownerName(t, homeB) {
		t.Fatalf("siblings share user %s", ownerName(t, homeA))
	}
	journalA := h.cfg.JournalDir(childID("a"))
	// Each home is its user's, setgid to its group; each journal directory
	// is root's, in the agent's group, traversable but not listable.
	for _, home := range []string{h.mainHome, homeA, homeB} {
		uid, gid := lookupIDs(t, ownerName(t, home))
		statIs(t, home, uid, gid, fs.ModeDir|fs.ModeSetgid|0o750)
		// The starting config moved into the home, written as its agent.
		if got := ownerName(t, filepath.Join(home, "config.json")); got != ownerName(t, home) {
			t.Fatalf("%s/config.json belongs to %s", home, got)
		}
	}
	for _, ctrl := range []string{h.mainCtrl, ctrlA, ctrlB} {
		if _, err := os.Lstat(filepath.Join(ctrl, "config.json")); !os.IsNotExist(err) {
			t.Fatalf("config.json left in %s: %v", ctrl, err)
		}
	}
	_, gidA := lookupIDs(t, ownerName(t, homeA))
	statIs(t, journalA, 0, gidA, fs.ModeDir|0o751)
	_, mainGid := lookupIDs(t, ownerName(t, h.mainHome))
	statIs(t, h.cfg.JournalDir(h.mainID), 0, mainGid, fs.ModeDir|0o751)
	steps := []string{matchStep("task-for-a", fmt.Sprintf("touch %s/x; echo rc1=$?; echo hi > %s/OWNER.md; echo rc2=$?; cat %s/TASK.md; echo rc3=$?", h.mainHome, ctrlA, homeB), 100)}
	h.setScript(steps...)
	h.drop(homeA, "task.md", "task-for-a")
	h.waitEnds(childID("a"), 1)
	exitCodes(t, h.toolOutput("rc1="), false, "rc1", "rc2", "rc3")
	for _, path := range []string{filepath.Join(h.mainHome, "x"), filepath.Join(ctrlA, "OWNER.md")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("subagent created %s", path)
		}
	}
	runA := h.runs(childID("a"))[0]
	steps = append(steps,
		matchStep("task-for-main", fmt.Sprintf("cat agents/a/home/TASK.md; echo rc=$?; cat %s/*.jsonl >/dev/null; echo jrc=$?", journalA), 100),
		matchStep("task-for-b", fmt.Sprintf("cat %s/*.jsonl; echo jrc=$?; cat %s; echo frc=$?", journalA, runA), 100))
	h.setScript(steps...)
	h.drop(h.mainHome, "go.md", "task-for-main")
	h.waitEnds(h.mainID, 1)
	exitCodes(t, h.toolOutput("jrc="), true, "rc", "jrc")
	h.drop(homeB, "task.md", "task-for-b")
	h.waitEnds(childID("b"), 1)
	exitCodes(t, h.toolOutput("frc="), false, "jrc", "frc")
}

// TestPrivilegeReclaimKillsEverything: removing a control directory stops
// what its user left running, removes its crontab, lingering, home and
// temporary files, and empties its group.
func TestPrivilegeReclaimKillsEverything(t *testing.T) {
	h, _ := rootHarness(t)
	mine := filepath.Join(h.cfg.TmpDirs[0], "mine")
	h.setScript(matchStep("task-for-c", "setsid sleep 300 </dev/null >/dev/null 2>&1 & echo '* * * * * true' | crontab -; echo cron=$?; touch "+mine+"; echo tmp=$?", 100))
	h.start()
	if got := groupMembers(t, h.cfg.PoolUsers[0]); got != "" {
		t.Fatalf("main's group %s members %q, want none: root needs no membership", h.cfg.PoolUsers[0], got)
	}
	ctrl := rootChild(t, h, "c")
	home := h.childHome(ctrl)
	user := ownerName(t, home)
	if user != h.cfg.PoolUsers[1] {
		t.Fatalf("main's first subagent got %s", user)
	}
	if got := groupMembers(t, user); got != h.cfg.PoolUsers[0] {
		t.Fatalf("group %s members %q, want the parent %s", user, got, h.cfg.PoolUsers[0])
	}
	if withSystemd() && !lingering(user) {
		t.Fatalf("%s not lingering after admission", user)
	}
	h.drop(home, "task.md", "task-for-c")
	h.waitEnds(childID("c"), 1)
	exitCodes(t, h.toolOutput("cron="), true, "cron", "tmp")
	if code := exitCode(t, "pgrep", "-u", user, "sleep"); code != 0 {
		t.Fatalf("the detached sleep is not running: pgrep %d", code)
	}
	if code := exitCode(t, "crontab", "-l", "-u", user); code != 0 {
		t.Fatalf("no crontab installed: %d", code)
	}
	os.RemoveAll(ctrl)
	h.waitReclaimed(user)
	if code := exitCode(t, "pgrep", "-u", user); code != 1 {
		t.Fatalf("pgrep -u %s exited %d: processes survived", user, code)
	}
	if code := exitCode(t, "crontab", "-l", "-u", user); code == 0 {
		t.Fatal("crontab kept")
	}
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
		t.Fatalf("home not emptied: %v %v", entries, err)
	}
	if _, err := os.Lstat(mine); !os.IsNotExist(err) {
		t.Fatal("temporary file kept")
	}
	if got := groupMembers(t, user); got != "" {
		t.Fatalf("group %s keeps members %q", user, got)
	}
	if withSystemd() && lingering(user) {
		t.Fatalf("%s still lingering", user)
	} else if !withSystemd() {
		t.Log("no systemd: lingering not checked")
	}
	if runs := h.runs(childID("c")); len(runs) != 1 {
		t.Fatalf("journals not kept: %v", runs)
	}
}

// TestPrivilegeReusedUserCannotReadOldJournals: once a user is reclaimed, its
// next holder cannot read the journals of the subagent that held it before;
// they belong to the former parent's group.
func TestPrivilegeReusedUserCannotReadOldJournals(t *testing.T) {
	h, name := rootHarness(t)
	h.start()
	ctrlA := rootChild(t, h, "a")
	homeA := h.childHome(ctrlA)
	user := ownerName(t, homeA)
	h.drop(homeA, "task.md", "task-for-a")
	h.waitEnds(childID("a"), 1)
	journalA, runA := h.cfg.JournalDir(childID("a")), h.runs(childID("a"))[0]
	os.RemoveAll(ctrlA)
	h.waitReclaimed(user)
	_, mainGid := lookupIDs(t, name)
	for _, path := range []string{journalA, runA} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if gid := int(info.Sys().(*syscall.Stat_t).Gid); gid != mainGid {
			t.Fatalf("%s has group %d, want the parent's %d", path, gid, mainGid)
		}
	}
	h.setScript(matchStep("task-for-b", fmt.Sprintf("cat %s/*.jsonl; echo rc=$?; cat %s; echo frc=$?", journalA, runA), 100))
	ctrlB := rootChild(t, h, "b")
	homeB := h.childHome(ctrlB)
	if got := ownerName(t, homeB); got != user {
		t.Fatalf("b got %s, not the reclaimed %s", got, user)
	}
	h.drop(homeB, "task.md", "task-for-b")
	h.waitEnds(childID("b"), 1)
	exitCodes(t, h.toolOutput("frc="), false, "rc", "frc")
}

// TestPrivilegeSpoolFileNotOwnedIgnored: a file in a subagent's spool that
// its user does not own is never delivered as its message and stays put.
func TestPrivilegeSpoolFileNotOwnedIgnored(t *testing.T) {
	h, _ := rootHarness(t)
	h.start()
	ctrl := rootChild(t, h, "c")
	home := h.childHome(ctrl)
	uid, gid := lookupIDs(t, ownerName(t, home))
	spool := filepath.Join(home, ".messages")
	if err := os.Mkdir(spool, 0o755); err != nil {
		t.Fatal(err)
	}
	os.Lchown(spool, uid, gid)
	planted := h.queue(spool, "planted")
	// The child's own message in the same spool is delivered.
	temp := filepath.Join(spool, ".g")
	os.WriteFile(temp, []byte("genuine"), 0o644)
	os.Lchown(temp, uid, gid)
	if err := os.Rename(temp, filepath.Join(spool, "20261012T000000Z.md")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the child's own message", func() bool { return countNotes(h.mainHome, "-c.md") >= 1 })
	time.Sleep(300 * time.Millisecond) // several polls
	if _, err := os.Stat(planted); err != nil {
		t.Fatalf("foreign spool file not left in place: %v", err)
	}
	if n := countNotes(h.mainHome, "-c.md"); n != 1 {
		t.Fatalf("%d messages from the child", n)
	}
	for _, sub := range []string{"inbox", "inbox/processed"} {
		matches, _ := filepath.Glob(filepath.Join(h.mainHome, sub, "*.md"))
		for _, m := range matches {
			if fileContains(m, "planted") {
				t.Fatalf("a root-owned spool file was delivered as the child's: %s", m)
			}
		}
	}
	if !strings.Contains(h.logs.String(), "ignoring "+planted) {
		t.Fatalf("foreign file not logged:\n%s", h.logs)
	}
}

// TestPrivilegeChildOwnerMd: a subagent's OWNER.md owned by its parent's user
// leads its system prompt; once the subagent's own user owns it, the
// subagent stops with an OWNER.md ERROR until the file is fixed.
func TestPrivilegeChildOwnerMd(t *testing.T) {
	h, name := rootHarness(t)
	h.start()
	ctrl := rootChild(t, h, "c")
	owner := filepath.Join(ctrl, "OWNER.md")
	os.WriteFile(owner, []byte("Child rules."), 0o644)
	mainUID, _ := lookupIDs(t, name)
	os.Lchown(owner, mainUID, -1)
	home := h.childHome(ctrl)
	h.drop(home, "task.md", "first")
	h.waitEnds(childID("c"), 1)
	if got := h.requests()[0].System[0].Text; got != SystemPrompt+ownerSegment("Child rules.") {
		t.Fatalf("child's first segment %q", got)
	}
	childUID, _ := lookupIDs(t, ownerName(t, home))
	if err := os.Lchown(owner, childUID, -1); err != nil {
		t.Fatal(err)
	}
	h.drop(home, "next.md", "second")
	errorFile := filepath.Join(ctrl, "ERROR")
	want := MsgOwnerPrefix + owner + " is not owned by its owner\n"
	waitFor(t, "OWNER.md ERROR", func() bool {
		data, _ := os.ReadFile(errorFile)
		return string(data) == want
	})
	time.Sleep(300 * time.Millisecond) // several polls
	if n := len(h.runs(childID("c"))); n != 1 {
		t.Fatalf("%d runs with an OWNER.md the child owns", n)
	}
	if names := inboxNames(home); len(names) != 1 || names[0] != "next.md" {
		t.Fatalf("inbox touched: %v", names)
	}
	os.Lchown(owner, mainUID, -1)
	h.waitEnds(childID("c"), 2)
	if _, err := os.Stat(errorFile); !os.IsNotExist(err) {
		t.Fatal("OWNER.md ERROR not cleared")
	}
}

// TestPrivilegeOldLogsUnreadable: what an earlier release left readable by
// everyone (main's conversation, and the journals of agents removed before
// the upgrade) is tightened before any agent runs, so no subagent can read
// it: an old removed child of main's journals go to main's group, an old
// removed grandchild's to its parent's, and an old removed top-level
// agent's to root's.
func TestPrivilegeOldLogsUnreadable(t *testing.T) {
	h, name := rootHarness(t)
	_, mainGid := lookupIDs(t, name)
	const stamp = "20250101T000000.000000000Z.jsonl"
	oldFile := func(path, text string) string {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		os.Chmod(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		os.Chmod(path, 0o644)
		return path
	}
	oldRun := func(id string) string {
		return oldFile(filepath.Join(h.cfg.JournalDir(id), stamp), `{"type":"end"}`+"\n")
	}
	conversation := oldFile(h.cfg.ConversationFile(h.mainID), `{"from":"owner","text":"human secret"}`+"\n")
	// main and a hold their users already, so main's home is kept and a's
	// control directory can be made before the daemon starts.
	user := h.cfg.PoolUsers[1]
	writePool(t, h, `[{"user":"`+name+`","agent":"main","state":"active"},{"user":"`+user+`","agent":"`+childID("a")+`","state":"active"}]`)
	ctrlA := rootChild(t, h, "a")
	removed := oldRun(childID("old"))
	grandchild := oldRun(childID("a/agents/oldg"))
	top := oldRun("gone")
	h.start()
	homeA := h.childHome(ctrlA)
	if got := ownerName(t, homeA); got != user {
		t.Fatalf("a got %s, want its recorded %s", got, user)
	}
	_, gidA := lookupIDs(t, user)
	statIs(t, conversation, 0, mainGid, 0o640)
	statIs(t, removed, 0, mainGid, 0o640)
	statIs(t, filepath.Dir(removed), 0, mainGid, fs.ModeDir|0o751)
	statIs(t, grandchild, 0, gidA, 0o640)
	statIs(t, top, 0, 0, 0o640)
	statIs(t, filepath.Dir(top), 0, 0, fs.ModeDir|0o751)
	h.setScript(matchStep("task-for-a", fmt.Sprintf("cat %s; echo mrc=$?; cat %s; echo orc=$?; ls %s; echo lrc=$?; cat %s; echo trc=$?", conversation, removed, filepath.Dir(removed), top), 100))
	h.drop(homeA, "task.md", "task-for-a")
	h.waitEnds(childID("a"), 1)
	out := h.toolOutput("mrc=")
	exitCodes(t, out, false, "mrc", "orc", "lrc", "trc")
	if strings.Contains(out, "human secret") {
		t.Fatal("subagent read main's conversation")
	}
}

// TestPrivilegeLostPoolIgnoresPlantedJournalLink: after pool.json is lost, a
// home is recovered only if its journal link and the journal directory's
// group both say it is the agent's own. A previous holder that pointed its
// journal link at a sibling's journals cannot pass its files on to that
// sibling, even when the parent points the sibling's home link at them.
func TestPrivilegeLostPoolIgnoresPlantedJournalLink(t *testing.T) {
	h, _ := rootHarness(t)
	h.start()
	// main runs once, so its journal link keeps its home when the pool is lost.
	h.drop(h.mainHome, "go.md", "go")
	h.waitEnds(h.mainID, 1)
	ctrlC, ctrlD := rootChild(t, h, "c"), rootChild(t, h, "d")
	homeC := h.childHome(ctrlC)
	h.childHome(ctrlD)
	userC := ownerName(t, homeC)
	uid, gid := lookupIDs(t, userC)
	planted := filepath.Join(homeC, "planted.txt")
	if err := os.WriteFile(planted, []byte("for d"), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Lchown(planted, uid, gid)
	link := filepath.Join(homeC, "journal")
	os.Remove(link)
	if err := os.Symlink(h.cfg.JournalDir(childID("d")), link); err != nil {
		t.Fatal(err)
	}
	os.Lchown(link, uid, gid)
	h.stop()
	os.Remove(h.cfg.PoolFile())
	os.RemoveAll(ctrlC)
	linkD := filepath.Join(ctrlD, "home")
	os.Remove(linkD)
	if err := os.Symlink(homeC, linkD); err != nil {
		t.Fatal(err)
	}
	mainUID, mainGID := lookupIDs(t, h.cfg.PoolUsers[0])
	os.Lchown(linkD, mainUID, mainGID)
	h.start()
	// main recovers its home by its own evidence.
	waitFor(t, "main admitted again", func() bool {
		return strings.Count(h.logs.String(), "main: admitted as "+h.cfg.PoolUsers[0]) == 2
	})
	time.Sleep(20 * h.cfg.Poll)
	if strings.Count(h.logs.String(), "main/agents/d: admitted as") != 1 {
		t.Fatal("d admitted on a journal link planted by its home's previous holder")
	}
	if !fileContains(planted, "for d") {
		t.Fatal("c's former home changed")
	}
}

// TestPrivilegeLostPoolNewAgentSkipsLiveHome: after pool.json is lost, a
// new root child admitted before main is never given main's user, whose home
// holds main's evidence under the real groups; main then recovers it.
func TestPrivilegeLostPoolNewAgentSkipsLiveHome(t *testing.T) {
	h, name := rootHarness(t, done())
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	h.waitEnds(h.mainID, 1)
	marker := filepath.Join(h.mainHome, "marker.txt")
	os.WriteFile(marker, []byte("mine"), 0o644)
	h.stop()
	os.Remove(h.cfg.PoolFile())
	early := topLevel(t, h, "aaa") // sorts before main
	os.Chmod(early, 0o700)
	h.start()
	if got := ownerName(t, h.childHome(early)); got == name {
		t.Fatalf("new agent given main's user %s", got)
	}
	waitFor(t, "main admitted again", func() bool { return strings.Count(h.logs.String(), "main: admitted as "+name) == 2 })
	if data, err := os.ReadFile(marker); err != nil || string(data) != "mine" {
		t.Fatalf("main's home cleared: %q %v", data, err)
	}
}

// TestPrivilegeRootSpoolPrivate: an owner message root queued for main,
// waiting in /etc/agent (which every user may traverse), cannot be listed or
// read by another pool user.
func TestPrivilegeRootSpoolPrivate(t *testing.T) {
	h, _ := rootHarness(t)
	if err := cli.SendChild(h.cfg.RootHome(), "main", "owner secret", time.Now()); err != nil {
		t.Fatal(err)
	}
	spool := filepath.Join(h.cfg.RootHome(), ".messages", "agents", "main")
	entries, err := os.ReadDir(spool)
	if err != nil || len(entries) != 1 {
		t.Fatalf("spool %v %v", entries, err)
	}
	uid, gid := lookupIDs(t, h.cfg.PoolUsers[1])
	for _, args := range [][]string{{"ls", spool}, {"cat", filepath.Join(spool, entries[0].Name())}} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}}
		out, err := cmd.CombinedOutput()
		if err == nil || strings.Contains(string(out), "owner secret") {
			t.Fatalf("%s as %s: %v %q", strings.Join(args, " "), h.cfg.PoolUsers[1], err, out)
		}
	}
}
