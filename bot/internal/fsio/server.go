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
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
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
	Op    string `json:"op"`
	Path  string `json:"path"`
	Path2 string `json:"path2,omitempty"`
	Data  []byte `json:"data,omitempty"`
	Limit int64  `json:"limit,omitempty"`
	Mode  uint32 `json:"mode,omitempty"`  // write's permission bits; mkdir's whole mode
	Stage string `json:"stage,omitempty"` // write's staging directory; "" is the target's own
}

type response struct {
	Err        string  `json:"err,omitempty"`
	NotExist   bool    `json:"notexist,omitempty"`
	Permission bool    `json:"permission,omitempty"`
	Exist      bool    `json:"exist,omitempty"`
	Data       []byte  `json:"data,omitempty"`
	Size       int64   `json:"size,omitempty"`
	Entries    []Entry `json:"entries,omitempty"`
	Entry      *Entry  `json:"entry,omitempty"`
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
			resp = response{Err: err.Error(), NotExist: notExist, Permission: errors.Is(err, fs.ErrPermission), Exist: errors.Is(err, fs.ErrExist)}
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
}

func handle(req request) (response, error) {
	switch req.Op {
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
		// Non-blocking open: a FIFO planted by the agent must not hang the
		// helper. Never through a link: another user may create files
		// where the helper reads (an inbox), and a link there must not
		// make the helper read what it points at.
		f, err := os.OpenFile(req.Path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
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
		return response{}, write(req.Path, req.Data, os.FileMode(req.Mode).Perm(), req.Stage)
	case "mkdir":
		return response{}, mkdir(req.Path, os.FileMode(req.Mode))
	case "rename":
		return response{}, os.Rename(req.Path, req.Path2)
	case "unlink":
		if err := syscall.Unlink(req.Path); err != nil {
			return response{}, &fs.PathError{Op: "unlink", Path: req.Path, Err: err}
		}
		return response{}, nil
	case "symlink":
		// Remove, then create: the only readers are the agent's own
		// commands, and a run starts with these links.
		if err := os.Remove(req.Path2); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return response{}, err
		}
		return response{}, os.Symlink(req.Path, req.Path2)
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

// write replaces path atomically, through a temporary file in stage (the
// target's own directory when empty), which must be on the same
// filesystem. Renaming over a symlink replaces the link itself, never its
// target.
func write(path string, data []byte, mode os.FileMode, stage string) error {
	if stage == "" {
		stage = filepath.Dir(path)
	}
	temp := filepath.Join(stage, "."+filepath.Base(path)+"."+random()+".tmp")
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

// mkdir creates path as a directory with exactly mode, sticky bit included,
// whatever the umask. An existing directory of this user's is left as it
// is; anything else at the path is an error that wraps fs.ErrExist.
func mkdir(path string, mode os.FileMode) error {
	err := os.Mkdir(path, mode)
	if err == nil {
		return os.Chmod(path, mode)
	}
	if !errors.Is(err, fs.ErrExist) {
		return err
	}
	info, lerr := os.Lstat(path)
	if lerr != nil {
		return lerr
	}
	if !info.IsDir() || owner(info) != os.Getuid() {
		return fmt.Errorf("%s is not a directory this user owns: %w", path, fs.ErrExist)
	}
	return nil
}

func owner(info fs.FileInfo) int {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid)
	}
	return -1
}
