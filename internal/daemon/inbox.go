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

// writeNote drops a harness message into an agent's inbox, through its helper.
func (d *daemon) writeNote(to *agent, kind, text string) error {
	return to.fs.Write(filepath.Join(to.home, "inbox", noteName(kind, time.Now())), []byte(text+"\n"))
}

// pending lists deliverable inbox files: regular, not dot-prefixed, by name.
func (a *agent) pending() ([]string, error) {
	entries, err := a.fs.List(filepath.Join(a.home, "inbox"))
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
	names, err := a.pending()
	if err != nil || len(names) == 0 {
		return nil, err
	}
	processed := filepath.Join(a.home, "inbox", "processed")
	if err := a.fs.Mkdir(processed); err != nil {
		return nil, err
	}
	var records []inboxRecord
	for _, name := range names {
		path := filepath.Join(a.home, "inbox", name)
		data, size, err := a.fs.Read(path, hashLimit)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			data = []byte(fmt.Sprintf("(could not read %s: %v)", name, err))
		}
		sum := sha256.Sum256(data)
		target := name
		for i := 1; ; i++ {
			if _, err := a.fs.Stat(filepath.Join(processed, target)); errors.Is(err, fs.ErrNotExist) {
				break
			}
			target = fmt.Sprintf("%s-%d", name, i)
		}
		file := "inbox/processed/" + target
		content, clipped := shell.Clip(strings.ToValidUTF8(string(data), "�"), d.cfg.InboxClip)
		if clipped || size > int64(len(data)) {
			content += "\n" + fmt.Sprintf(MsgInboxClipFmt, filepath.Join(a.home, file))
		}
		r := inboxRecord{File: file, From: a.label(name, hex.EncodeToString(sum[:])), Content: content}
		j.Record("inbox", map[string]any{"file": r.File, "from": r.From, "size": size, "content": r.Content})
		if err := a.fs.Rename(path, filepath.Join(processed, target)); err != nil {
			return records, err
		}
		records = append(records, r)
	}
	return records, nil
}

// label is the sender the daemon recorded when it delivered this file
// ("owner" or "agents/NAME"), and "data" for anything else.
func (a *agent) label(name, sum string) string {
	if from := a.d.delivered.take(a.id, name, sum); from != "" {
		return from
	}
	return "data"
}
