// Package journal writes one JSONL file per run, ending with an end record.
package journal

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const stamp = "20060102T150405.000000000Z"

type Writer struct {
	f    *os.File
	path string
}

// Create starts a new run file named by its UTC start time. On a name clash
// the time is advanced a nanosecond, so names always sort in run order.
func Create(dir string, now time.Time) (*Writer, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	os.Chmod(dir, 0o755)
	for t := now.UTC(); ; t = t.Add(time.Nanosecond) {
		path := filepath.Join(dir, t.Format(stamp)+".jsonl")
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		f.Chmod(0o644)
		return &Writer{f: f, path: path}, nil
	}
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
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".jsonl") {
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

// MarkInterrupted appends an end record to a run that never finished, so
// recovery notifies the agent only once.
func MarkInterrupted(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	w := &Writer{f: f, path: path}
	return w.End("interrupted")
}
