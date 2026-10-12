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

	"github.com/SEAN-7/bot/internal/osuser"
	"github.com/SEAN-7/bot/internal/plugin"
)

// etcMode is the mode install.sh gives /etc/agent.
const etcMode = 0o711

// rootHarness runs the daemon as root with commands, file access and plugins
// as unprivileged test users, so the kernel enforces the boundaries: the
// main agent as AGENT_TEST_USER, subagents as the AGENT_TEST_POOL users, and
// the root-only operations through the real System.
func rootHarness(t *testing.T, steps ...string) (*harness, string) {
	t.Helper()
	if os.Geteuid() != 0 || os.Getenv("AGENT_ROOT_TESTS") != "1" {
		t.Skip("needs root and AGENT_ROOT_TESTS=1")
	}
	name := os.Getenv("AGENT_TEST_USER")
	if name == "" {
		name = "agent-test"
	}
	uid, gid := lookupIDs(t, name)
	h := newHarness(t, steps...)
	// t.TempDir creates 0700 directories; the test users must traverse them.
	for dir := h.root; dir != filepath.Dir(os.TempDir()) && dir != "/"; dir = filepath.Dir(dir) {
		if strings.HasPrefix(filepath.Base(dir), "Test") || dir == h.root || strings.HasPrefix(filepath.Base(dir), "00") {
			os.Chmod(dir, 0o755)
		}
	}
	replayDir := filepath.Join(h.root, "replay")
	os.MkdirAll(replayDir, 0o755)
	os.Chown(replayDir, uid, gid)
	h.reqLog = filepath.Join(replayDir, "requests.jsonl")
	os.WriteFile(filepath.Join(h.cfg.Credentials(), "replay.env"), plugin.FormatEnv(map[string]string{"REPLAY_SCRIPT": h.script, "REPLAY_LOG": h.reqLog}), 0o600)
	filepath.WalkDir(h.cfg.Home, func(path string, _ fs.DirEntry, err error) error {
		if err == nil {
			os.Lchown(path, uid, gid)
		}
		return err
	})
	binary, _ := os.ReadFile(h.cfg.Self)
	h.cfg.Self = filepath.Join(h.root, "daemon.test")
	os.WriteFile(filepath.Join(h.root, "daemon.test"), binary, 0o755)
	// Mirror install.sh: /etc/agent traversable only, plugins for the provider
	// group, credentials root-only; plugins run as a separate provider user.
	provider := os.Getenv("AGENT_TEST_PROVIDER_USER")
	if provider == "" {
		provider = "agent-test-provider"
	}
	puid, pgid := lookupIDs(t, provider)
	os.Chmod(h.cfg.Etc, etcMode)
	os.Chmod(h.cfg.Credentials(), 0o700)
	os.Chown(h.cfg.Plugins(), 0, pgid)
	os.Chmod(h.cfg.Plugins(), 0o750)
	plugin := filepath.Join(h.cfg.Plugins(), "replay")
	os.Chown(plugin, 0, pgid)
	os.Chmod(plugin, 0o750)
	os.Chown(replayDir, puid, pgid)
	h.cfg.AgentUser, h.cfg.ProviderUser = name, provider
	h.cfg.LookupUser = osuser.Lookup
	h.cfg.OwnerUID = 0
	// The pool: real users, the real System, homes in a root-owned Homes
	// and a sticky world-writable temporary directory, as on the VM.
	pool := os.Getenv("AGENT_TEST_POOL")
	if pool == "" {
		pool = "agent-test-1,agent-test-2,agent-test-3"
	}
	h.cfg.PoolUsers = strings.Split(pool, ",")
	sys := newExecSystem()
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
	return h, name
}

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

// rootChild creates a control directory under the main agent's agents/ as
// the main agent's user would: the directory and its config.json belong to
// that user.
func rootChild(t *testing.T, h *harness, name string) string {
	t.Helper()
	uid, gid := lookupIDs(t, h.cfg.AgentUser)
	agents := filepath.Join(h.cfg.Home, "agents")
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
	readme := filepath.Join(h.cfg.Home, "README.md")
	os.Remove(readme)
	os.Symlink(secret, readme)
	leak := filepath.Join(h.cfg.Home, "inbox", "leak.md")
	os.Symlink(secret, leak)
	u, _ := user.Lookup(name)
	uid, _ := strconv.Atoi(u.Uid)
	os.Lchown(readme, uid, -1)
	os.Lchown(leak, uid, -1)
	h.drop(h.cfg.Home, "go.md", "go")
	h.start()
	h.waitEnds(h.cfg.Home, 1)
	if sys := h.requests()[0].System[1].Text; !strings.Contains(sys, "could not be read") {
		t.Fatalf("README segment %q", sys)
	}
	if fileContains(h.reqLog, "TOPSECRET") {
		t.Fatal("privileged file reached the model")
	}
	for _, run := range h.runs(h.cfg.Home) {
		if fileContains(run, "TOPSECRET") {
			t.Fatal("privileged file reached the journal")
		}
	}
}

func TestPrivilegeErrorSymlink(t *testing.T) {
	h, name := rootHarness(t)
	original := secretFile(t, h, 0o644, "ORIGINAL")
	errorFile := filepath.Join(h.cfg.Home, "ERROR")
	os.Symlink(original, errorFile)
	u, _ := user.Lookup(name)
	uid, _ := strconv.Atoi(u.Uid)
	os.Lchown(errorFile, uid, -1)
	os.WriteFile(filepath.Join(h.cfg.Home, "config.json"), []byte(`{"provider":"replay","model":"nope"}`), 0o644)
	h.drop(h.cfg.Home, "go.md", "go")
	h.start()
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
	h.drop(h.cfg.Home, "go.md", "go")
	h.start()
	h.waitEnds(h.cfg.Home, 1)
	if tool := h.requests()[1].Messages[2].Content; strings.HasPrefix(tool, "exit status 0") {
		t.Fatalf("agent wrote to its journal: %q", tool)
	}
	for _, run := range h.runs(h.cfg.Home) {
		if fileContains(run, "tampered\n") {
			t.Fatal("journal modified")
		}
	}
}

func TestPrivilegeCommandsRunAsAgent(t *testing.T) {
	h, name := rootHarness(t, step("id -un; cat ../../etc/agent/credentials/replay.env; ../../etc/agent/plugins/replay </dev/null; echo plugin-rc=$?", 100), done())
	h.drop(h.cfg.Home, "go.md", "go")
	h.start()
	h.waitEnds(h.cfg.Home, 1)
	tool := h.requests()[1].Messages[2].Content
	if !strings.Contains(tool, name) || strings.Contains(tool, "REPLAY_SCRIPT=") || !strings.Contains(tool, "plugin-rc=126") {
		t.Fatalf("tool %q", tool)
	}
}

func TestPrivilegeHelperNotDumpable(t *testing.T) {
	h, _ := rootHarness(t, step("cat /proc/$(pgrep -f 'daemon.test _fsio' | head -n 1)/environ >/dev/null; echo environ-rc=$?", 100), done())
	h.drop(h.cfg.Home, "go.md", "go")
	h.start()
	h.waitEnds(h.cfg.Home, 1)
	if tool := h.requests()[1].Messages[2].Content; !strings.Contains(tool, "environ-rc=1") {
		t.Fatalf("agent can inspect the file helper: %q", tool)
	}
}

func TestPrivilegeMessagesLogNotWritable(t *testing.T) {
	h, _ := rootHarness(t, step(sendMessage, 100), step("echo forged >> "+filepath.Join("..", "..", "log", "messages.jsonl")+"; echo rc=$?", 100), done())
	h.drop(h.cfg.Home, "go.md", "go")
	h.start()
	h.waitEnds(h.cfg.Home, 1)
	if tool := h.requests()[2].Messages[4].Content; !strings.Contains(tool, "rc=1") {
		t.Fatalf("agent wrote to the messages log: %q", tool)
	}
	if fileContains(h.cfg.MessagesFile(), "forged") {
		t.Fatal("messages log modified by the agent")
	}
}

// TestPrivilegeOwnerFileOfOtherUser gives /etc/agent/OWNER.md to the agent's
// own user in the root-owned /etc/agent: only the file fails the check.
func TestPrivilegeOwnerFileOfOtherUser(t *testing.T) {
	h, name := rootHarness(t, done())
	owner := h.cfg.OwnerFile()
	os.WriteFile(owner, []byte("Be brief."), 0o644)
	u, _ := user.Lookup(name)
	uid, _ := strconv.Atoi(u.Uid)
	if err := os.Chown(owner, uid, -1); err != nil {
		t.Fatal(err)
	}
	h.drop(h.cfg.Home, "go.md", "go")
	h.start()
	errorFile := filepath.Join(h.cfg.Home, "ERROR")
	want := MsgOwnerPrefix + owner + " is not owned by its owner\n"
	waitFor(t, "OWNER.md ERROR", func() bool {
		data, _ := os.ReadFile(errorFile)
		return string(data) == want
	})
	if n := len(h.requests()); n != 0 {
		t.Fatalf("%d requests with an OWNER.md the agent owns", n)
	}
	os.Chown(owner, 0, -1)
	h.waitEnds(h.cfg.Home, 1)
	if got := h.requests()[0].System[0].Text; got != SystemPrompt+ownerSegment("Be brief.") {
		t.Fatalf("first segment %q", got)
	}
}

// TestPrivilegeSubagentIsolation: a subagent cannot write its parent's home,
// its control directory or its OWNER.md, nor read a sibling's home; its
// parent can read its home and journals; a sibling can read neither.
func TestPrivilegeSubagentIsolation(t *testing.T) {
	h, _ := rootHarness(t)
	ctrlA, ctrlB := rootChild(t, h, "a"), rootChild(t, h, "b")
	h.start()
	homeA, homeB := h.childHome(ctrlA), h.childHome(ctrlB)
	if ownerName(t, homeA) == ownerName(t, homeB) {
		t.Fatalf("siblings share user %s", ownerName(t, homeA))
	}
	journalA := h.cfg.JournalDir(ctrlA)
	// Each home is its user's, setgid to its group; each journal directory
	// is root's, in the subagent's group, traversable but not listable.
	for _, home := range []string{homeA, homeB} {
		uid, gid := lookupIDs(t, ownerName(t, home))
		statIs(t, home, uid, gid, fs.ModeDir|fs.ModeSetgid|0o750)
	}
	_, gidA := lookupIDs(t, ownerName(t, homeA))
	statIs(t, journalA, 0, gidA, fs.ModeDir|0o751)
	steps := []string{matchStep("task-for-a", fmt.Sprintf("touch %s/x; echo rc1=$?; echo hi > %s/OWNER.md; echo rc2=$?; cat %s/TASK.md; echo rc3=$?", h.cfg.Home, ctrlA, homeB), 100)}
	h.setScript(steps...)
	h.drop(homeA, "task.md", "task-for-a")
	h.waitEnds(ctrlA, 1)
	exitCodes(t, h.toolOutput("rc1="), false, "rc1", "rc2", "rc3")
	for _, path := range []string{filepath.Join(h.cfg.Home, "x"), filepath.Join(ctrlA, "OWNER.md")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("subagent created %s", path)
		}
	}
	runA := h.runs(ctrlA)[0]
	steps = append(steps,
		matchStep("task-for-main", fmt.Sprintf("cat agents/a/home/TASK.md; echo rc=$?; cat %s/*.jsonl >/dev/null; echo jrc=$?", journalA), 100),
		matchStep("task-for-b", fmt.Sprintf("cat %s/*.jsonl; echo jrc=$?; cat %s; echo frc=$?", journalA, runA), 100))
	h.setScript(steps...)
	h.drop(h.cfg.Home, "go.md", "task-for-main")
	h.waitEnds(h.cfg.Home, 1)
	exitCodes(t, h.toolOutput("jrc="), true, "rc", "jrc")
	h.drop(homeB, "task.md", "task-for-b")
	h.waitEnds(ctrlB, 1)
	exitCodes(t, h.toolOutput("frc="), false, "jrc", "frc")
}

// TestPrivilegeReclaimKillsEverything: removing a control directory stops
// what its user left running, removes its crontab, lingering, home and
// temporary files, and empties its group.
func TestPrivilegeReclaimKillsEverything(t *testing.T) {
	h, _ := rootHarness(t)
	mine := filepath.Join(h.cfg.TmpDirs[0], "mine")
	h.setScript(matchStep("task-for-c", "setsid sleep 300 </dev/null >/dev/null 2>&1 & echo '* * * * * true' | crontab -; echo cron=$?; touch "+mine+"; echo tmp=$?", 100))
	ctrl := rootChild(t, h, "c")
	h.start()
	home := h.childHome(ctrl)
	user := ownerName(t, home)
	if user != h.cfg.PoolUsers[0] {
		t.Fatalf("first subagent got %s", user)
	}
	if got := groupMembers(t, user); got != h.cfg.AgentUser {
		t.Fatalf("group %s members %q, want the parent %s", user, got, h.cfg.AgentUser)
	}
	if withSystemd() && !lingering(user) {
		t.Fatalf("%s not lingering after admission", user)
	}
	h.drop(home, "task.md", "task-for-c")
	h.waitEnds(ctrl, 1)
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
	if runs := h.runs(ctrl); len(runs) != 1 {
		t.Fatalf("journals not kept: %v", runs)
	}
}

// TestPrivilegeReusedUserCannotReadOldJournals: once a user is reclaimed, its
// next holder cannot read the journals of the subagent that held it before;
// they belong to the former parent's group.
func TestPrivilegeReusedUserCannotReadOldJournals(t *testing.T) {
	h, _ := rootHarness(t)
	ctrlA := rootChild(t, h, "a")
	h.start()
	homeA := h.childHome(ctrlA)
	user := ownerName(t, homeA)
	h.drop(homeA, "task.md", "task-for-a")
	h.waitEnds(ctrlA, 1)
	journalA, runA := h.cfg.JournalDir(ctrlA), h.runs(ctrlA)[0]
	os.RemoveAll(ctrlA)
	h.waitReclaimed(user)
	_, mainGid := lookupIDs(t, h.cfg.AgentUser)
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
	h.waitEnds(ctrlB, 1)
	exitCodes(t, h.toolOutput("frc="), false, "rc", "frc")
}

// TestPrivilegeSpoolFileNotOwnedIgnored: a file in a subagent's spool that
// its user does not own is never delivered as its message and stays put.
func TestPrivilegeSpoolFileNotOwnedIgnored(t *testing.T) {
	h, _ := rootHarness(t)
	ctrl := rootChild(t, h, "c")
	h.start()
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
	waitFor(t, "the child's own message", func() bool { return countNotes(h.cfg.Home, "-c.md") >= 1 })
	time.Sleep(300 * time.Millisecond) // several polls
	if _, err := os.Stat(planted); err != nil {
		t.Fatalf("foreign spool file not left in place: %v", err)
	}
	if n := countNotes(h.cfg.Home, "-c.md"); n != 1 {
		t.Fatalf("%d messages from the child", n)
	}
	for _, sub := range []string{"inbox", "inbox/processed"} {
		matches, _ := filepath.Glob(filepath.Join(h.cfg.Home, sub, "*.md"))
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
	ctrl := rootChild(t, h, "c")
	owner := filepath.Join(ctrl, "OWNER.md")
	os.WriteFile(owner, []byte("Child rules."), 0o644)
	mainUID, _ := lookupIDs(t, name)
	os.Lchown(owner, mainUID, -1)
	h.start()
	home := h.childHome(ctrl)
	h.drop(home, "task.md", "first")
	h.waitEnds(ctrl, 1)
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
	if n := len(h.runs(ctrl)); n != 1 {
		t.Fatalf("%d runs with an OWNER.md the child owns", n)
	}
	if names := inboxNames(home); len(names) != 1 || names[0] != "next.md" {
		t.Fatalf("inbox touched: %v", names)
	}
	os.Lchown(owner, mainUID, -1)
	h.waitEnds(ctrl, 2)
	if _, err := os.Stat(errorFile); !os.IsNotExist(err) {
		t.Fatal("OWNER.md ERROR not cleared")
	}
}

// TestPrivilegeMigrateOwnership: Migrate gives each pool user what the main
// agent's user owned of its subagent, links included but never their
// targets, leaves other owners' files alone, and gives a nested control
// directory to the parent's user; the daemon then admits both without
// reclaiming anything.
func TestPrivilegeMigrateOwnership(t *testing.T) {
	h, name := rootHarness(t)
	mainUID, mainGID := lookupIDs(t, name)
	secret := secretFile(t, h, 0o600, "root only")
	legacy := filepath.Join(h.cfg.Home, "agents", "recipes")
	nested := filepath.Join(legacy, "agents", "scraper")
	legacyAgent(t, legacy, map[string]string{"TASK.md": "recipes task", "notes/n.md": "note"})
	legacyAgent(t, nested, map[string]string{"TASK.md": "scraper task"})
	if err := os.Symlink(secret, filepath.Join(legacy, "link")); err != nil {
		t.Fatal(err)
	}
	if err := filepath.WalkDir(h.cfg.Home, func(path string, _ fs.DirEntry, err error) error {
		if err == nil {
			err = os.Lchown(path, mainUID, mainGID)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "rootfile"), []byte("root's"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(h.cfg); err != nil {
		t.Fatal(err)
	}
	user1, user2 := h.cfg.PoolUsers[0], h.cfg.PoolUsers[1]
	uid1, gid1 := lookupIDs(t, user1)
	uid2, gid2 := lookupIDs(t, user2)
	home1, home2 := h.cfg.PoolHome(user1), h.cfg.PoolHome(user2)
	statIs(t, filepath.Join(home1, "TASK.md"), uid1, gid1, 0o644)
	statIs(t, filepath.Join(home1, "notes"), uid1, gid1, fs.ModeDir|0o755)
	statIs(t, filepath.Join(home1, "notes", "n.md"), uid1, gid1, 0o644)
	statIs(t, filepath.Join(home1, "inbox", "processed"), uid1, gid1, fs.ModeDir|0o755)
	statIs(t, filepath.Join(home1, "agents"), uid1, gid1, fs.ModeDir|0o755)
	if info, err := os.Lstat(filepath.Join(home1, "link")); err != nil || int(info.Sys().(*syscall.Stat_t).Uid) != uid1 {
		t.Fatalf("link: %v %v", info, err)
	}
	statIs(t, secret, 0, 0, 0o600)
	statIs(t, filepath.Join(home1, "rootfile"), 0, 0, 0o644)
	// Control directories belong to the parent.
	statIs(t, legacy, mainUID, mainGID, fs.ModeDir|0o755)
	statIs(t, filepath.Join(legacy, "config.json"), mainUID, mainGID, 0o644)
	ctrl := filepath.Join(home1, "agents", "scraper")
	statIs(t, ctrl, uid1, gid1, fs.ModeDir|0o755)
	statIs(t, filepath.Join(ctrl, "config.json"), uid1, gid1, 0o644)
	statIs(t, filepath.Join(home2, "TASK.md"), uid2, gid2, 0o644)
	statIs(t, filepath.Join(home2, "inbox"), uid2, gid2, fs.ModeDir|0o755)
	h.start()
	if got := h.childHome(legacy); got != home1 {
		t.Fatalf("recipes home %q", got)
	}
	if got := h.childHome(ctrl); got != home2 {
		t.Fatalf("scraper home %q", got)
	}
	time.Sleep(300 * time.Millisecond) // several polls
	for path, want := range map[string]string{filepath.Join(home1, "TASK.md"): "recipes task", filepath.Join(home2, "TASK.md"): "scraper task"} {
		if got, err := os.ReadFile(path); err != nil || string(got) != want {
			t.Fatalf("%s: %q %v", path, got, err)
		}
	}
	if strings.Contains(h.logs.String(), "reclaimed") {
		t.Fatalf("reclaimed after migration:\n%s", h.logs)
	}
}
