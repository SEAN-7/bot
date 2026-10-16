// Package journal writes one JSONL file per run, ending with an end record.
package journal

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const stamp = "20060102T150405.000000000Z"

type Writer struct {
	f           *os.File
	path        string
	gid         int
	attachments int
}

// Directories are 0751: a child's journal directory is nested inside its
// parent's, so they must be traversable by others. Files are 0640.
const (
	dirMode  = 0o751
	fileMode = 0o640
)

// Create starts a new run file named by its UTC start time. On a name clash
// the time is advanced a nanosecond, so names always sort in run order. A
// gid of zero or more becomes the file's group; a negative gid leaves it.
func Create(dir string, now time.Time, gid int) (*Writer, error) {
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return nil, err
	}
	for t := now.UTC(); ; t = t.Add(time.Nanosecond) {
		path := filepath.Join(dir, t.Format(stamp)+".jsonl")
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, fileMode)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		err = f.Chmod(fileMode)
		if err == nil && gid >= 0 {
			err = f.Chown(-1, gid)
		}
		if err != nil {
			f.Close()
			os.Remove(path)
			return nil, err
		}
		return &Writer{f: f, path: path, gid: gid}, nil
	}
}

// IsRun reports whether name is a run file, <stamp>.jsonl, as opposed to
// other files in the directory such as the conversation log.
func IsRun(name string) bool {
	stem, ok := strings.CutSuffix(name, ".jsonl")
	if !ok {
		return false
	}
	_, err := time.Parse(stamp, stem)
	return err == nil
}

// Secure creates an agent's journal directory dir and sets the journal modes
// on everything under it: directories 0751 and regular files 0640, with group
// gid when it is zero or more. It does not follow symlinks and leaves them and
// other types alone. It fixes dir/agents itself but not what is inside: those
// are the subagents' journal directories, with their own groups.
func Secure(dir string, gid int) error { return secure(dir, gid, false) }

// SecureTree is Secure including the subagents' journal directories, for
// handing a whole subtree to one group.
func SecureTree(dir string, gid int) error { return secure(dir, gid, true) }

func secure(dir string, gid int, children bool) error {
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return err
	}
	agents := filepath.Join(dir, "agents")
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		var mode os.FileMode
		switch {
		case d.IsDir():
			mode = dirMode
		case d.Type().IsRegular():
			mode = fileMode
		default:
			return nil
		}
		if err := os.Chmod(path, mode); err != nil {
			return err
		}
		if gid >= 0 {
			if err := os.Lchown(path, -1, gid); err != nil {
				return err
			}
		}
		if path == agents && d.IsDir() && !children {
			return filepath.SkipDir
		}
		return nil
	})
}

func (w *Writer) Path() string { return w.path }

// Record appends one durable line: {"t", "type", ...fields}.
func (w *Writer) Record(typ string, fields map[string]any) error {
	line := map[string]any{}
	for k, v := range fields {
		line[k] = v
	}
	line["t"] = time.Now().UTC().Format(time.RFC3339Nano)
	line["type"] = typ
	data, err := json.Marshal(line)
	if err != nil {
		return err
	}
	if _, err := w.f.Write(append(data, '\n')); err != nil {
		return err
	}
	return w.f.Sync()
}

// Attach saves data beside the run file as <run>-<n>.<ext>, n counting this
// writer's attachments from 1, with the run file's mode and group. It returns
// the path and leaves nothing behind on error.
func (w *Writer) Attach(ext string, data []byte) (string, error) {
	w.attachments++
	stem := strings.TrimSuffix(w.path, ".jsonl")
	path := stem + "-" + strconv.Itoa(w.attachments) + "." + ext
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fileMode)
	if err != nil {
		return "", err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(fileMode)
	}
	if err == nil && w.gid >= 0 {
		err = f.Chown(-1, w.gid)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

// End writes the clean-end marker and closes the file.
func (w *Writer) End(reason string) error {
	err := w.Record("end", map[string]any{"reason": reason})
	if cerr := w.f.Close(); err == nil {
		err = cerr
	}
	return err
}

// Close closes the file without an end marker.
func (w *Writer) Close() error { return w.f.Close() }

// Latest returns the newest run file in dir and whether it ended cleanly.
func Latest(dir string) (string, bool, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	} else if err != nil {
		return "", false, err
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && IsRun(e.Name()) {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", false, nil
	}
	sort.Strings(names)
	path := filepath.Join(dir, names[len(names)-1])
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var last struct {
		Type string `json:"type"`
	}
	json.Unmarshal([]byte(lines[len(lines)-1]), &last)
	return path, last.Type == "end", nil
}

// LastVersion returns the harness version in the start record of the newest
// run file in dir, and whether there is a run file at all. A start record
// without a version (a run before 0.6.0) gives "".
func LastVersion(dir string) (string, bool, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	} else if err != nil {
		return "", false, err
	}
	newest := ""
	for _, e := range entries {
		if e.Type().IsRegular() && IsRun(e.Name()) && e.Name() > newest {
			newest = e.Name()
		}
	}
	if newest == "" {
		return "", false, nil
	}
	f, err := os.Open(filepath.Join(dir, newest))
	if err != nil {
		return "", true, err
	}
	defer f.Close()
	line, err := bufio.NewReaderSize(f, 64<<10).ReadSlice('\n')
	if err != nil && len(line) == 0 {
		return "", true, nil
	}
	var start struct {
		Type    string `json:"type"`
		Version string `json:"version"`
	}
	if json.Unmarshal(line, &start) != nil || start.Type != "start" {
		return "", true, nil
	}
	return start.Version, true, nil
}

// MarkInterrupted appends an end record to a run that never finished, so
// recovery notifies the agent only once.
func MarkInterrupted(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	w := &Writer{f: f, path: path, gid: -1}
	return w.End("interrupted")
}
