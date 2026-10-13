// Package fsio performs all file access inside agent directories through a
// helper process running as the agent user, so that the kernel applies the
// agent's own permissions: a symlink the agent plants can reach only what the
// agent itself could reach.
package fsio

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

type Entry struct {
	Name string `json:"name"`
	Type string `json:"type"` // file, dir, symlink or other
	Size int64  `json:"size"`
	UID  int    `json:"uid"`
	Mode uint32 `json:"mode"` // permission bits only
	// Dev and Ino identify the file: a directory removed and created again
	// at the same path is a different one.
	Dev uint64 `json:"dev"`
	Ino uint64 `json:"ino"`
	// Target is a symlink's target, from stat only.
	Target string `json:"target,omitempty"`
}

type request struct {
	Op    string   `json:"op"`
	Path  string   `json:"path"`
	Path2 string   `json:"path2,omitempty"`
	Data  []byte   `json:"data,omitempty"`
	Limit int64    `json:"limit,omitempty"`
	Paths []string `json:"paths,omitempty"`
	Mode  uint32   `json:"mode,omitempty"` // mkdir's and write's permission bits
}

type response struct {
	Err        string   `json:"err,omitempty"`
	NotExist   bool     `json:"notexist,omitempty"`
	Permission bool     `json:"permission,omitempty"`
	Data       []byte   `json:"data,omitempty"`
	Size       int64    `json:"size,omitempty"`
	Entries    []Entry  `json:"entries,omitempty"`
	Entry      *Entry   `json:"entry,omitempty"`
	Paths      []string `json:"paths,omitempty"`
}

// Serve answers JSON-line requests until r is closed.
func Serve(r io.Reader, w io.Writer) error {
	if err := undumpable(); err != nil {
		return err
	}
	dec := json.NewDecoder(bufio.NewReader(r))
	enc := json.NewEncoder(w)
	for {
		var req request
		if err := dec.Decode(&req); err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}
		resp, err := handle(req)
		if err != nil {
			// A path below a non-directory (ENOTDIR) does not exist either.
			notExist := errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
			resp = response{Err: err.Error(), NotExist: notExist, Permission: errors.Is(err, fs.ErrPermission)}
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
}

func handle(req request) (response, error) {
	switch req.Op {
	case "scan":
		paths, err := scan(req.Path)
		return response{Paths: paths}, err
	case "clear":
		return response{}, clearDir(req.Path)
	case "purge_owned":
		return response{}, purgeOwned(req.Paths)
	case "list":
		items, err := os.ReadDir(req.Path)
		if err != nil {
			return response{}, err
		}
		var entries []Entry
		for _, item := range items {
			info, err := item.Info()
			if err != nil {
				continue // removed while listing
			}
			entries = append(entries, entry(info))
		}
		return response{Entries: entries}, nil
	case "read":
		// Non-blocking open: a FIFO planted by the agent must not hang the helper.
		f, err := os.OpenFile(req.Path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return response{}, err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return response{}, err
		}
		if !info.Mode().IsRegular() {
			return response{}, errors.New(req.Path + ": not a regular file")
		}
		data, err := io.ReadAll(io.LimitReader(f, req.Limit))
		return response{Data: data, Size: info.Size()}, err
	case "write":
		return response{}, write(req.Path, req.Data, os.FileMode(req.Mode).Perm())
	case "private":
		skipped, err := private(req.Paths)
		return response{Paths: skipped}, err
	case "rename":
		return response{}, os.Rename(req.Path, req.Path2)
	case "mkdir":
		return response{}, os.MkdirAll(req.Path, os.FileMode(req.Mode).Perm())
	case "remove":
		return response{}, os.RemoveAll(req.Path)
	case "unlink":
		if err := syscall.Unlink(req.Path); err != nil {
			return response{}, &fs.PathError{Op: "unlink", Path: req.Path, Err: err}
		}
		return response{}, nil
	case "symlink":
		temp := filepath.Join(filepath.Dir(req.Path2), "."+filepath.Base(req.Path2)+"."+random()+".tmp")
		if err := os.Symlink(req.Path, temp); err != nil {
			return response{}, err
		}
		if err := os.Rename(temp, req.Path2); err != nil {
			os.Remove(temp)
			return response{}, err
		}
		return response{}, nil
	case "stat":
		info, err := os.Lstat(req.Path)
		if err != nil {
			return response{}, err
		}
		e := entry(info)
		if e.Type == "symlink" {
			if e.Target, err = os.Readlink(req.Path); err != nil {
				return response{}, err
			}
		}
		return response{Entry: &e}, nil
	}
	return response{}, errors.New("unknown operation " + req.Op)
}

func entry(info fs.FileInfo) Entry {
	kind := "other"
	switch {
	case info.Mode().IsRegular():
		kind = "file"
	case info.IsDir():
		kind = "dir"
	case info.Mode()&fs.ModeSymlink != 0:
		kind = "symlink"
	}
	e := Entry{Name: info.Name(), Type: kind, Size: info.Size(), Mode: uint32(info.Mode().Perm())}
	e.UID = owner(info)
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		e.Dev, e.Ino = uint64(st.Dev), uint64(st.Ino)
	}
	return e
}

func random() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// write replaces path atomically. Renaming over a symlink replaces the link
// itself, never its target.
func write(path string, data []byte, mode os.FileMode) error {
	temp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+"."+random()+".tmp")
	f, err := os.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(temp, path)
	}
	if err != nil {
		os.Remove(temp)
	}
	return err
}

// private creates each of paths that is missing as a directory without
// permissions for others (see Client.Private), and returns those it left
// alone.
func private(paths []string) ([]string, error) {
	var skipped []string
	below := func(path string) bool {
		for _, s := range skipped {
			if strings.HasPrefix(path, s+"/") {
				return true
			}
		}
		return false
	}
	for _, path := range paths {
		if below(path) {
			continue
		}
		// mkdir never follows a link in the last component.
		if err := os.Mkdir(path, 0o750); err != nil && !errors.Is(err, fs.ErrExist) {
			return skipped, err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return skipped, err
		}
		if !info.IsDir() || owner(info) != os.Getuid() {
			skipped = append(skipped, path)
		}
	}
	return skipped, nil
}

// scan returns the control directories <home>/agents/<name>, sorted: every
// real directory there owned by this user. The daemon decides from what each
// holds whether it is an agent. It looks one level deep only; deeper agents
// belong to their own users.
func scan(home string) ([]string, error) {
	agents := filepath.Join(home, "agents")
	if !ownedDir(agents) {
		return nil, nil // missing, or a symlink
	}
	items, err := os.ReadDir(agents)
	if err != nil {
		return nil, err
	}
	var found []string
	for _, item := range items {
		dir := filepath.Join(agents, item.Name())
		if ownedDir(dir) {
			found = append(found, dir)
		}
	}
	sort.Strings(found)
	return found, nil
}

// ownedDir reports whether path is a real directory (not a symlink) owned by
// this user.
func ownedDir(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir() && owner(info) == os.Getuid()
}

func owner(info fs.FileInfo) int {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid)
	}
	return -1
}

// clearDir removes everything inside dir and keeps dir itself. It goes on
// past an entry it cannot remove and reports every failure.
func clearDir(dir string) error {
	items, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	var errs []error
	for _, item := range items {
		errs = append(errs, removeAll(filepath.Join(dir, item.Name())))
	}
	return errors.Join(errs...)
}

// purgeOwned removes each top-level entry of dirs that this user owns, leaving
// entries owned by others (the daemon's, or another agent's) in place. It goes
// on past a directory or entry it cannot remove and reports every failure.
func purgeOwned(dirs []string) error {
	var errs []error
	for _, dir := range dirs {
		items, err := os.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, item := range items {
			path := filepath.Join(dir, item.Name())
			info, err := os.Lstat(path)
			if err != nil || owner(info) != os.Getuid() {
				continue // vanished, or not ours
			}
			errs = append(errs, removeAll(path))
		}
	}
	return errors.Join(errs...)
}

// removeAll is os.RemoveAll for a tree that may hold directories without
// write or search permission for their owner (a module cache is read-only,
// for example): it first gives every directory this user owns u+rwx, never
// following symlinks, then removes the tree.
func removeAll(path string) error {
	writable(path)
	return os.RemoveAll(path)
}

func writable(path string) {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || owner(info) != os.Getuid() {
		return // not a directory (links included), or not ours to change
	}
	if info.Mode().Perm()&0o700 != 0o700 {
		os.Chmod(path, info.Mode().Perm()|0o700)
	}
	items, err := os.ReadDir(path)
	if err != nil {
		return // RemoveAll reports it
	}
	for _, item := range items {
		if item.IsDir() {
			writable(filepath.Join(path, item.Name()))
		}
	}
}
