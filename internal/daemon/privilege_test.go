package daemon

import (
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/SEAN-7/bot/internal/plugin"
)

// etcMode is the mode install.sh gives /etc/agent.
const etcMode = 0o711

// rootHarness runs the daemon as root with commands, file access and plugins
// as an unprivileged test user, so the kernel enforces the boundaries.
func rootHarness(t *testing.T, steps ...string) (*harness, string) {
	t.Helper()
	if os.Geteuid() != 0 || os.Getenv("AGENT_ROOT_TESTS") != "1" {
		t.Skip("needs root and AGENT_ROOT_TESTS=1")
	}
	name := os.Getenv("AGENT_TEST_USER")
	if name == "" {
		name = "agent-test"
	}
	u, err := user.Lookup(name)
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	h := newHarness(t, steps...)
	// t.TempDir creates 0700 directories; the test user must traverse them.
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
	pu, err := user.Lookup(provider)
	if err != nil {
		t.Fatal(err)
	}
	pgid, _ := strconv.Atoi(pu.Gid)
	os.Chmod(h.cfg.Etc, etcMode)
	os.Chmod(h.cfg.Credentials(), 0o700)
	os.Chown(h.cfg.Plugins(), 0, pgid)
	os.Chmod(h.cfg.Plugins(), 0o750)
	plugin := filepath.Join(h.cfg.Plugins(), "replay")
	os.Chown(plugin, 0, pgid)
	os.Chmod(plugin, 0o750)
	puid, _ := strconv.Atoi(pu.Uid)
	os.Chown(replayDir, puid, pgid)
	h.cfg.AgentUser, h.cfg.ProviderUser = name, provider
	return h, name
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
