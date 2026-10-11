package daemon

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

// messageLimit matches the limit `agent message` enforces.
const messageLimit = 64 << 10

// appendMessage adds one record to the owner conversation. Only the daemon
// writes this root-owned file, so its labels can be trusted.
func (d *daemon) appendMessage(record map[string]any) error {
	record["t"] = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(d.cfg.MessagesFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(append(data, '\n'))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// collectMessages moves each agent's queued `agent message` files into the
// owner conversation. Appending before removing means a crash can only
// duplicate a message, never lose one.
func (d *daemon) collectMessages(dirs []string) {
	for _, dir := range dirs {
		spool := filepath.Join(dir, ".messages")
		entries, err := d.fs.List(spool)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			d.cfg.Logger.Printf("%s: messages: %v", d.rel(dir), err)
			continue
		}
		var names []string
		for _, e := range entries {
			if e.Type == "file" && !strings.HasPrefix(e.Name, ".") {
				names = append(names, e.Name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			path := filepath.Join(spool, name)
			data, size, err := d.fs.Read(path, messageLimit)
			if err != nil {
				d.cfg.Logger.Printf("%s: message %s: %v", d.rel(dir), name, err)
				continue
			}
			text := strings.ToValidUTF8(string(data), "�")
			if size > int64(len(data)) {
				text += "\n[message truncated]"
			}
			if err := d.appendMessage(map[string]any{"from": "agent", "agent": d.rel(dir), "text": text}); err != nil {
				d.cfg.Logger.Printf("messages log: %v", err)
				return
			}
			if err := d.fs.Remove(path); err != nil {
				d.cfg.Logger.Printf("%s: message %s: %v", d.rel(dir), name, err)
			}
		}
	}
}
