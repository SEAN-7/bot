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
	if err := c.Write(path, []byte("hello")); err != nil {
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
	if err := c.Write(link, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(target); string(data) != "ORIGINAL" {
		t.Fatalf("target changed: %q", data)
	}
	if info, _ := os.Lstat(link); info.Mode()&fs.ModeSymlink != 0 {
		t.Fatal("link still a symlink")
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
	if err := c.Mkdir(filepath.Join(dir, "a", "b")); err != nil {
		t.Fatal(err)
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

func TestConcurrentRequests(t *testing.T) {
	c, dir := start(t), t.TempDir()
	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			path := filepath.Join(dir, fmt.Sprint(i))
			if err := c.Write(path, []byte(fmt.Sprint(i))); err != nil {
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
		if err = c.Write(path, []byte("ok")); err == nil {
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
	if err := c.Write(filepath.Join(dir, "y"), []byte("ok")); err != nil {
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
	c, home := start(t), t.TempDir()
	controlDir(t, home, "b")
	controlDir(t, home, "a")
	os.MkdirAll(filepath.Join(home, "agents", "noconfig"), 0o755)
	os.MkdirAll(filepath.Join(home, "agents", "a", "home", "agents", "deep"), 0o755) // not scanned: one level
	got, err := c.Scan(home)
	want := []string{filepath.Join(home, "agents", "a"), filepath.Join(home, "agents", "b")}
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
	// agents/x a symlink to a real control directory, agents/y with a
	// config.json symlink, agents/z a genuine control directory.
	os.MkdirAll(filepath.Join(home, "agents"), 0o755)
	os.Symlink(filepath.Join(real, "agents", "x"), filepath.Join(home, "agents", "x"))
	y := filepath.Join(home, "agents", "y")
	os.MkdirAll(y, 0o755)
	os.Symlink(filepath.Join(real, "agents", "x", "config.json"), filepath.Join(y, "config.json"))
	z := controlDir(t, home, "z")
	got, err := c.Scan(home)
	if err != nil || !reflect.DeepEqual(got, []string{z}) {
		t.Fatalf("got %v %v", got, err)
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
