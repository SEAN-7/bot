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
	"syscall"
	"time"
)

type Entry struct {
	Name    string    `json:"name"`
	Type    string    `json:"type"` // file, dir, symlink or other
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

type request struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Path2 string `json:"path2,omitempty"`
	Data  []byte `json:"data,omitempty"`
	Limit int64  `json:"limit,omitempty"`
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
			resp = response{Err: err.Error(), NotExist: errors.Is(err, fs.ErrNotExist), Permission: errors.Is(err, fs.ErrPermission)}
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
		return response{}, write(req.Path, req.Data)
	case "rename":
		return response{}, os.Rename(req.Path, req.Path2)
	case "mkdir":
		return response{}, os.MkdirAll(req.Path, 0o755)
	case "remove":
		return response{}, os.RemoveAll(req.Path)
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
	return Entry{Name: info.Name(), Type: kind, Size: info.Size(), ModTime: info.ModTime()}
}

func random() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// write replaces path atomically. Renaming over a symlink replaces the link
// itself, never its target.
func write(path string, data []byte) error {
	temp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+"."+random()+".tmp")
	f, err := os.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
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

// scan returns home and, breadth first, every real directory at
// <agent>/agents/<name> that contains an inbox directory.
func scan(home string) ([]string, error) {
	if _, err := os.Lstat(home); err != nil {
		return nil, err
	}
	found := []string{home}
	for i := 0; i < len(found); i++ {
		agents := filepath.Join(found[i], "agents")
		if info, err := os.Lstat(agents); err != nil || !info.IsDir() {
			continue // missing, or a symlink, which could nest agents inside themselves
		}
		items, err := os.ReadDir(agents)
		if err != nil {
			continue
		}
		var children []string
		for _, item := range items {
			if !item.IsDir() { // DirEntry types come from lstat: symlinks are not dirs
				continue
			}
			child := filepath.Join(found[i], "agents", item.Name())
			if info, err := os.Lstat(filepath.Join(child, "inbox")); err == nil && info.IsDir() {
				children = append(children, child)
			}
		}
		sort.Strings(children)
		found = append(found, children...)
	}
	return found, nil
}
