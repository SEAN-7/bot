package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/SEAN-7/bot/internal/shell"
)

// hashLimit bounds how much of an inbox file is read for labelling.
const hashLimit = 16 << 20

type inboxRecord struct {
	File    string `json:"file"`
	From    string `json:"from"`
	Content string `json:"content"`
}

// encodeRecords renders one JSON object per line. Content is a JSON string,
// so nothing inside a file can create its own "from" field.
func encodeRecords(records []inboxRecord) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	for _, r := range records {
		enc.Encode(r)
	}
	return strings.TrimRight(b.String(), "\n")
}

func noteName(kind string, now time.Time) string {
	return now.UTC().Format("20060102T150405.000000000Z") + "-" + kind + ".md"
}

// writeNote drops a harness message into an agent's inbox.
func (d *daemon) writeNote(agentDir, kind, text string) error {
	return d.fs.Write(filepath.Join(agentDir, "inbox", noteName(kind, time.Now())), []byte(text+"\n"))
}

// pending lists deliverable inbox files: regular, not dot-prefixed, by name.
func (d *daemon) pending(agentDir string) ([]string, error) {
	entries, err := d.fs.List(filepath.Join(agentDir, "inbox"))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.Type == "file" && !strings.HasPrefix(e.Name, ".") {
			names = append(names, e.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// collectInbox reads, journals and moves every pending file into
// inbox/processed/, returning the records to show the model.
func (a *agent) collectInbox(j recorder) ([]inboxRecord, error) {
	d := a.d
	names, err := d.pending(a.dir)
	if err != nil || len(names) == 0 {
		return nil, err
	}
	processed := filepath.Join(a.dir, "inbox", "processed")
	if err := d.fs.Mkdir(processed); err != nil {
		return nil, err
	}
	var records []inboxRecord
	for _, name := range names {
		path := filepath.Join(a.dir, "inbox", name)
		data, size, err := d.fs.Read(path, hashLimit)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			data = []byte(fmt.Sprintf("(could not read %s: %v)", name, err))
		}
		sum := sha256.Sum256(data)
		target := name
		for i := 1; ; i++ {
			if _, err := d.fs.Stat(filepath.Join(processed, target)); errors.Is(err, fs.ErrNotExist) {
				break
			}
			target = fmt.Sprintf("%s-%d", name, i)
		}
		file := "inbox/processed/" + target
		content, clipped := shell.Clip(strings.ToValidUTF8(string(data), "�"), d.cfg.InboxClip)
		if clipped || size > int64(len(data)) {
			content += "\n" + fmt.Sprintf(MsgInboxClipFmt, filepath.Join(a.dir, file))
		}
		r := inboxRecord{File: file, From: a.label(name, hex.EncodeToString(sum[:])), Content: content}
		j.Record("inbox", map[string]any{"file": r.File, "from": r.From, "size": size, "content": r.Content})
		if err := d.fs.Rename(path, filepath.Join(processed, target)); err != nil {
			return records, err
		}
		records = append(records, r)
	}
	return records, nil
}

// label is "owner" only for messages the daemon delivered from the owner spool.
func (a *agent) label(name, sum string) string {
	if a.d.delivered.take(a.dir, name, sum) {
		return "owner"
	}
	return "data"
}
