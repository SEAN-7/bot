package fsio

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "_fsio" {
		if err := Serve(os.Stdin, os.Stdout); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func start(t *testing.T) *Client {
	t.Helper()
	c, err := Start(os.Args[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestWriteIsAtomicAndReadBack(t *testing.T) {
	c, dir := start(t), t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := c.Write(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, size, err := c.Read(path, 100)
	if err != nil || string(data) != "hello" || size != 5 {
		t.Fatalf("got %q %d %v", data, size, err)
	}
	entries, _ := c.List(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name, ".") {
			t.Fatalf("leftover temp file %s", e.Name)
		}
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v", info.Mode())
	}
}

func TestWriteReplacesSymlinkNotTarget(t *testing.T) {
	c, dir := start(t), t.TempDir()
	target := filepath.Join(dir, "target")
	os.WriteFile(target, []byte("ORIGINAL"), 0o644)
	link := filepath.Join(dir, "ERROR")
	os.Symlink(target, link)
	if err := c.Write(link, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(target); string(data) != "ORIGINAL" {
		t.Fatalf("target changed: %q", data)
	}
	if info, _ := os.Lstat(link); info.Mode()&fs.ModeSymlink != 0 {
		t.Fatal("link still a symlink")
	}
}

// Write gives the file the mode asked for (less the umask), also when it
// replaces one with another mode.
func TestWriteMode(t *testing.T) {
	c, dir := start(t), t.TempDir()
	path := filepath.Join(dir, "note.md")
	os.WriteFile(path, []byte("old"), 0o644)
	os.Chmod(path, 0o644)
	if err := c.Write(path, []byte("new"), 0o640); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", info.Mode())
	}
}

// Private makes each path a real directory closed to others: a missing one
// is created 0750, an existing one loses only its permissions for others.
// It never follows a link, leaves anything that is not a directory alone,
// and skips what lies below one it left alone. It returns what it left alone.
func TestPrivate(t *testing.T) {
	c, dir := start(t), t.TempDir()
	path := func(p string) string { return filepath.Join(dir, p) }
	os.Mkdir(path("open"), 0o755)
	os.Mkdir(path("group"), 0o775)
	os.Mkdir(path("closed"), 0o700)
	for _, p := range []string{"open", "group", "closed"} {
		os.Chmod(path(p), map[string]os.FileMode{"open": 0o755, "group": 0o775, "closed": 0o700}[p])
	}
	outside := t.TempDir()
	os.Chmod(outside, 0o755)
	os.Symlink(outside, path("linked"))
	os.WriteFile(path("file"), []byte("x"), 0o644)
	skipped, err := c.Private(true, path("new"), path("new/sub"), path("open"), path("group"), path("closed"),
		path("linked"), path("linked/sub"), path("file"), path("file/sub"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{path("linked"), path("file")}; !reflect.DeepEqual(skipped, want) {
		t.Errorf("skipped %q, want %q", skipped, want)
	}
	for p, want := range map[string]os.FileMode{"new": 0o750, "new/sub": 0o750, "open": 0o750, "group": 0o770, "closed": 0o700} {
		if info, err := os.Lstat(path(p)); err != nil || !info.IsDir() || info.Mode().Perm() != want {
			t.Errorf("%s: %v %v, want %v", p, info.Mode(), err, want)
		}
	}
	if info, _ := os.Stat(outside); info.Mode().Perm() != 0o755 {
		t.Errorf("followed a link: %v", info.Mode())
	}
	if _, err := os.Lstat(filepath.Join(outside, "sub")); !os.IsNotExist(err) {
		t.Errorf("created below a link: %v", err)
	}
	if info, _ := os.Lstat(path("file")); !info.Mode().IsRegular() || info.Mode().Perm() != 0o644 {
		t.Errorf("file changed: %v", info.Mode())
	}
}

// Private without create closes an existing directory but never creates a
// missing one: that is an error, and it stops there.
func TestPrivateWithoutCreate(t *testing.T) {
	c, dir := start(t), t.TempDir()
	open, missing, after := filepath.Join(dir, "open"), filepath.Join(dir, "gone"), filepath.Join(dir, "after")
	os.Mkdir(open, 0o755)
	os.Chmod(open, 0o755)
	os.Mkdir(after, 0o755)
	os.Chmod(after, 0o755)
	_, err := c.Private(false, open, missing, after)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing path: %v, want not exist", err)
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Fatalf("created a missing directory: %v", err)
	}
	if info, _ := os.Stat(open); info.Mode().Perm() != 0o750 {
		t.Errorf("open: %v, want 0750", info.Mode())
	}
	if info, _ := os.Stat(after); info.Mode().Perm() != 0o755 {
		t.Errorf("went on past the error: %v", info.Mode())
	}
}

// Private keeps a setgid bit: a home's subdirectories stay in its group.
func TestPrivateKeepsSetgid(t *testing.T) {
	c, dir := start(t), t.TempDir()
	path := filepath.Join(dir, "inbox")
	os.Mkdir(path, 0o755)
	if err := os.Chmod(path, 0o755|fs.ModeSetgid); err != nil {
		t.Skip(err)
	}
	if info, _ := os.Stat(path); info.Mode()&fs.ModeSetgid == 0 {
		t.Skip("cannot set setgid here")
	}
	if _, err := c.Private(true, path); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Mode()&(fs.ModeSetgid|fs.ModePerm) != fs.ModeSetgid|0o750 {
		t.Fatalf("mode %v", info.Mode())
	}
}

func TestReadLimitReportsFullSize(t *testing.T) {
	c, dir := start(t), t.TempDir()
	path := filepath.Join(dir, "big")
	os.WriteFile(path, make([]byte, 100), 0o644)
	data, size, err := c.Read(path, 10)
	if err != nil || len(data) != 10 || size != 100 {
		t.Fatalf("got %d %d %v", len(data), size, err)
	}
}

func TestNotExist(t *testing.T) {
	c := start(t)
	_, _, err := c.Read(filepath.Join(t.TempDir(), "missing"), 10)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("got %v", err)
	}
	if _, err := c.Stat(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stat got %v", err)
	}
}

// A path below a file does not exist either (ENOTDIR).
func TestNotExistBelowFile(t *testing.T) {
	c, dir := start(t), t.TempDir()
	os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644)
	if _, err := c.Stat(filepath.Join(dir, "f", "x")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stat got %v", err)
	}
}

func TestListTypes(t *testing.T) {
	c, dir := start(t), t.TempDir()
	os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644)
	os.Mkdir(filepath.Join(dir, "d"), 0o755)
	os.Symlink("f", filepath.Join(dir, "l"))
	entries, err := c.List(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, e := range entries {
		got[e.Name] = e.Type
	}
	if got["f"] != "file" || got["d"] != "dir" || got["l"] != "symlink" || len(got) != 3 {
		t.Fatalf("got %v", got)
	}
}

func TestSymlinkReplaces(t *testing.T) {
	c, dir := start(t), t.TempDir()
	path := filepath.Join(dir, "journal")
	os.WriteFile(path, []byte("x"), 0o644)
	if err := c.Symlink("/var/log/agent", path); err != nil {
		t.Fatal(err)
	}
	e, err := c.Stat(path)
	if err != nil || e.Type != "symlink" {
		t.Fatalf("got %+v %v", e, err)
	}
	if target, _ := os.Readlink(path); target != "/var/log/agent" {
		t.Fatalf("target %s", target)
	}
}

func TestRenameMkdirRemove(t *testing.T) {
	c, dir := start(t), t.TempDir()
	if err := c.Mkdir(filepath.Join(dir, "a", "b"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{filepath.Join(dir, "a"), filepath.Join(dir, "a", "b")} {
		if info, err := os.Stat(d); err != nil || info.Mode().Perm() != 0o750 {
			t.Fatalf("%s: %v %v", d, info, err)
		}
	}
	os.WriteFile(filepath.Join(dir, "x"), []byte("1"), 0o644)
	if err := c.Rename(filepath.Join(dir, "x"), filepath.Join(dir, "a", "b", "x")); err != nil {
		t.Fatal(err)
	}
	if err := c.Remove(filepath.Join(dir, "a")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a")); !os.IsNotExist(err) {
		t.Fatalf("not removed: %v", err)
	}
}

func TestUnlinkRemovesOnlyNonDirectories(t *testing.T) {
	c, dir := start(t), t.TempDir()
	file, sub, link := filepath.Join(dir, "f"), filepath.Join(dir, "d"), filepath.Join(dir, "l")
	os.WriteFile(file, []byte("x"), 0o644)
	os.MkdirAll(sub, 0o755)
	os.WriteFile(filepath.Join(sub, "g"), []byte("x"), 0o644)
	os.Symlink(file, link)
	if err := c.Unlink(link); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("link followed: %v", err)
	}
	if err := c.Unlink(file); err != nil {
		t.Fatal(err)
	}
	if err := c.Unlink(sub); err == nil {
		t.Fatal("directory unlinked")
	}
	if _, err := os.Stat(filepath.Join(sub, "g")); err != nil {
		t.Fatalf("directory contents removed: %v", err)
	}
	if err := c.Unlink(file); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
}

func TestConcurrentRequests(t *testing.T) {
	c, dir := start(t), t.TempDir()
	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			path := filepath.Join(dir, fmt.Sprint(i))
			if err := c.Write(path, []byte(fmt.Sprint(i)), 0o644); err != nil {
				errs <- err
				return
			}
			data, _, err := c.Read(path, 100)
			if err != nil || string(data) != fmt.Sprint(i) {
				errs <- fmt.Errorf("read %d: %q %v", i, data, err)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestRestartsAfterHelperDies(t *testing.T) {
	c, dir := start(t), t.TempDir()
	c.cmd.Process.Kill()
	c.cmd.Wait()
	path := filepath.Join(dir, "x")
	var err error
	for i := 0; i < 2; i++ { // the first call may fail on the dead helper
		if err = c.Write(path, []byte("ok"), 0o644); err == nil {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestReadFIFODoesNotBlock(t *testing.T) {
	c, dir := start(t), t.TempDir()
	path := filepath.Join(dir, "README.md")
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, _, err := c.Read(path, 10); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("reading a FIFO succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reading a FIFO blocked the helper")
	}
}

func TestHelperCallTimeout(t *testing.T) {
	c, dir := start(t), t.TempDir()
	c.Timeout = 500 * time.Millisecond
	c.mu.Lock()
	pid := c.cmd.Process.Pid
	c.mu.Unlock()
	syscall.Kill(pid, syscall.SIGSTOP)
	begin := time.Now()
	if _, _, err := c.Read(filepath.Join(dir, "x"), 10); err == nil {
		t.Fatal("call to a stopped helper succeeded")
	}
	if time.Since(begin) > 3*time.Second {
		t.Fatalf("call blocked for %s", time.Since(begin))
	}
	if err := c.Write(filepath.Join(dir, "y"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("helper not restarted: %v", err)
	}
}

func controlDir(t *testing.T, home, name string) string {
	t.Helper()
	dir := filepath.Join(home, "agents", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestScanFindsControlDirs(t *testing.T) {
	// Every real directory the user owns is a control directory, whatever it
	// holds: the daemon decides what it is.
	c, home := start(t), t.TempDir()
	b, a := controlDir(t, home, "b"), controlDir(t, home, "a")
	empty := filepath.Join(home, "agents", "empty")
	os.MkdirAll(empty, 0o755)
	linked := filepath.Join(home, "agents", "linked")
	os.MkdirAll(linked, 0o755)
	os.Symlink(filepath.Join(t.TempDir(), "home"), filepath.Join(linked, "home"))
	// A home that is a real directory or a regular file is no link, but
	// the control directory is still found.
	homeDir := filepath.Join(home, "agents", "homedir")
	os.MkdirAll(filepath.Join(homeDir, "home"), 0o755)
	homeFile := filepath.Join(home, "agents", "homefile")
	os.MkdirAll(homeFile, 0o755)
	os.WriteFile(filepath.Join(homeFile, "home"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(home, "agents", "file"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(home, "agents", "a", "home", "agents", "deep"), 0o755) // not scanned: one level
	got, err := c.Scan(home)
	want := []string{a, b, empty, homeDir, homeFile, linked}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v %v", got, err)
	}
}

func TestScanSkipsSymlinks(t *testing.T) {
	c, home := start(t), t.TempDir()
	real := t.TempDir()
	controlDir(t, real, "x")
	// agents/ itself a symlink: nothing is found through it.
	os.Symlink(filepath.Join(real, "agents"), filepath.Join(home, "agents"))
	if got, err := c.Scan(home); err != nil || len(got) != 0 {
		t.Fatalf("symlinked agents: got %v %v", got, err)
	}
	os.Remove(filepath.Join(home, "agents"))
	// agents/x a symlink to a real control directory, agents/z a genuine
	// control directory.
	os.MkdirAll(filepath.Join(home, "agents"), 0o755)
	os.Symlink(filepath.Join(real, "agents", "x"), filepath.Join(home, "agents", "x"))
	z := controlDir(t, home, "z")
	got, err := c.Scan(home)
	if err != nil || !reflect.DeepEqual(got, []string{z}) {
		t.Fatalf("got %v %v", got, err)
	}
}

// A directory in agents/ that another user owns is not a control directory
// of this helper's user. Only root can make one, so the test needs root.
func TestScanSkipsOtherUsersDirs(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to give a directory to another user")
	}
	c, home := start(t), t.TempDir()
	mine, theirs := controlDir(t, home, "mine"), controlDir(t, home, "theirs")
	if err := os.Lchown(theirs, 1, 1); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Scan(home); err != nil || !reflect.DeepEqual(got, []string{mine}) {
		t.Fatalf("got %v %v", got, err)
	}
	// agents/ itself owned by another user: nothing is found.
	if err := os.Lchown(filepath.Join(home, "agents"), 1, 1); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Scan(home); err != nil || len(got) != 0 {
		t.Fatalf("got %v %v", got, err)
	}
}

// Private leaves a directory another user owns alone and returns it. Only
// root can make one, so the test needs root.
func TestPrivateSkipsOtherUsersDirs(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to give a directory to another user")
	}
	c, dir := start(t), t.TempDir()
	theirs := filepath.Join(dir, "theirs")
	os.Mkdir(theirs, 0o755)
	os.Chmod(theirs, 0o755)
	if err := os.Lchown(theirs, 1, 1); err != nil {
		t.Fatal(err)
	}
	skipped, err := c.Private(true, theirs, filepath.Join(theirs, "sub"))
	if err != nil || !reflect.DeepEqual(skipped, []string{theirs}) {
		t.Fatalf("skipped %q, %v", skipped, err)
	}
	if info, _ := os.Stat(theirs); info.Mode().Perm() != 0o755 {
		t.Fatalf("another user's directory changed: %v", info.Mode())
	}
}

func TestScanMissingHome(t *testing.T) {
	c := start(t)
	if got, err := c.Scan(filepath.Join(t.TempDir(), "missing")); err != nil || len(got) != 0 {
		t.Fatalf("got %v %v", got, err)
	}
}

func TestEntryHasOwnerAndMode(t *testing.T) {
	c, dir := start(t), t.TempDir()
	path := filepath.Join(dir, "f")
	os.WriteFile(path, []byte("x"), 0o640)
	os.Chmod(path, 0o640) // independent of umask
	e, err := c.Stat(path)
	if err != nil || e.UID != os.Getuid() || e.Mode != 0o640 {
		t.Fatalf("stat got %+v %v", e, err)
	}
	entries, err := c.List(dir)
	if err != nil || len(entries) != 1 || entries[0].UID != os.Getuid() || entries[0].Mode != 0o640 {
		t.Fatalf("list got %+v %v", entries, err)
	}
}

func TestClearKeepsDir(t *testing.T) {
	c, dir := start(t), t.TempDir()
	os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, ".hidden"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(dir, "sub", "deep"), 0o755)
	os.WriteFile(filepath.Join(dir, "sub", "deep", "g"), []byte("x"), 0o644)
	if err := c.Clear(dir); err != nil {
		t.Fatal(err)
	}
	items, err := os.ReadDir(dir)
	if err != nil || len(items) != 0 {
		t.Fatalf("left %v %v", items, err)
	}
	if err := c.Clear(filepath.Join(dir, "missing")); err != nil {
		t.Fatalf("missing dir: %v", err)
	}
}

func TestPurgeOwnedRemovesOwnEntries(t *testing.T) {
	c := start(t)
	one, two := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(one, "f"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(two, "sub", "deep"), 0o755)
	os.WriteFile(filepath.Join(two, "sub", "deep", "g"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(two, ".hidden"), []byte("x"), 0o644)
	if err := c.PurgeOwned([]string{one, filepath.Join(one, "missing"), two}); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{one, two} {
		if items, err := os.ReadDir(dir); err != nil || len(items) != 0 {
			t.Fatalf("%s left %v %v", dir, items, err)
		}
	}
}

// readOnlyTree builds dir/ro (0555) holding a file and a 0000 directory with
// a file inside, and a link to outside, a read-only directory that must keep
// its mode: clearing never follows links.
func readOnlyTree(t *testing.T, dir string) (outside string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	outside = t.TempDir()
	ro := filepath.Join(dir, "ro")
	os.MkdirAll(filepath.Join(ro, "locked"), 0o755)
	os.WriteFile(filepath.Join(ro, "f"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(ro, "locked", "g"), []byte("x"), 0o644)
	os.Symlink(outside, filepath.Join(ro, "out"))
	os.Chmod(filepath.Join(ro, "locked"), 0)
	os.Chmod(ro, 0o555)
	os.Chmod(outside, 0o555)
	t.Cleanup(func() { os.Chmod(outside, 0o755) })
	return outside
}

func TestClearReadOnlyDirs(t *testing.T) {
	c, dir := start(t), t.TempDir()
	outside := readOnlyTree(t, dir)
	if err := c.Clear(dir); err != nil {
		t.Fatal(err)
	}
	if items, err := os.ReadDir(dir); err != nil || len(items) != 0 {
		t.Fatalf("left %v %v", items, err)
	}
	if info, err := os.Stat(outside); err != nil || info.Mode().Perm() != 0o555 {
		t.Fatalf("followed a link: %v %v", info.Mode(), err)
	}
}

func TestPurgeOwnedReadOnlyDirsAndContinues(t *testing.T) {
	c := start(t)
	one, two := t.TempDir(), t.TempDir()
	outside := readOnlyTree(t, two)
	notDir := filepath.Join(one, "file")
	os.WriteFile(notDir, []byte("x"), 0o644)
	// The first "directory" cannot be listed; the second is still purged.
	if err := c.PurgeOwned([]string{notDir, two}); err == nil {
		t.Fatal("expected the unlistable directory's error")
	}
	if items, err := os.ReadDir(two); err != nil || len(items) != 0 {
		t.Fatalf("left %v %v", items, err)
	}
	if info, err := os.Stat(outside); err != nil || info.Mode().Perm() != 0o555 {
		t.Fatalf("followed a link: %v %v", info.Mode(), err)
	}
}

func TestEntryIdentity(t *testing.T) {
	c, dir := start(t), t.TempDir()
	path := filepath.Join(dir, "d")
	os.Mkdir(path, 0o755)
	keep := filepath.Join(dir, "keep") // holds the inode, so it cannot be reused
	os.Rename(path, keep)
	os.Mkdir(path, 0o755)
	a, err1 := c.Stat(keep)
	b, err2 := c.Stat(path)
	if err1 != nil || err2 != nil || a.Ino == 0 || a.Ino == b.Ino && a.Dev == b.Dev {
		t.Fatalf("identities %d/%d and %d/%d: %v %v", a.Dev, a.Ino, b.Dev, b.Ino, err1, err2)
	}
	if again, _ := c.Stat(path); again.Ino != b.Ino || again.Dev != b.Dev {
		t.Fatal("identity not stable")
	}
}

func TestStatSymlinkTarget(t *testing.T) {
	c, dir := start(t), t.TempDir()
	link := filepath.Join(dir, "l")
	os.Symlink("/some/where", link)
	if e, err := c.Stat(link); err != nil || e.Type != "symlink" || e.Target != "/some/where" {
		t.Fatalf("got %+v %v", e, err)
	}
	if e, err := c.Stat(dir); err != nil || e.Target != "" {
		t.Fatalf("got %+v %v", e, err)
	}
}
