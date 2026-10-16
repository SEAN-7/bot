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

	"github.com/SEAN-7/bot/internal/fsio"
	"github.com/SEAN-7/bot/internal/proto"
	"github.com/SEAN-7/bot/internal/shell"
)

// inboxReadLimit bounds how much of an inbox file is read: a text file is
// clipped far below it, and an image longer than it is not shown.
const inboxReadLimit = 16 << 20

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
	return to.writeInbox(noteName(kind, time.Now()), []byte(text+"\n"))
}

// writeInbox writes name into the agent's inbox, through its helper, with
// the temporary file in the home directory: a run never collects a file
// half written, and the inbox needs no staging directory.
func (a *agent) writeInbox(name string, data []byte) error {
	return a.fs.WriteStaged(filepath.Join(a.home, "inbox", name), a.home, data, 0o640)
}

// pending lists the inbox files that wake the agent: regular, not
// dot-prefixed, by name.
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

// deliverable lists the inbox files a run collects: every regular file, a
// dot-file too, by name, with the uid that wrote each. Directories
// (processed/) are skipped.
func (a *agent) deliverable() ([]fsio.Entry, error) {
	entries, err := a.fs.List(filepath.Join(a.home, "inbox"))
	if err != nil {
		return nil, err
	}
	var files []fsio.Entry
	for _, e := range entries {
		if e.Type == "file" {
			files = append(files, e)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}

// collectInbox reads, journals and moves every pending file into
// inbox/processed/, returning the records to show the model and, in record
// order, the images shown with them. imageTypes is the image media types the
// model accepts.
func (a *agent) collectInbox(j recorder, imageTypes []string) ([]inboxRecord, []proto.Image, error) {
	d := a.d
	files, err := a.deliverable()
	if err != nil || len(files) == 0 {
		return nil, nil, err
	}
	processed := filepath.Join(a.home, "inbox", "processed")
	// Created again if the agent removed it. One the agent replaced with a
	// link is left alone and followed, but it must lead to a directory.
	if skipped, err := a.fs.Private(processed); err != nil {
		return nil, nil, err
	} else if len(skipped) > 0 {
		if _, err := a.fs.Stat(processed + "/."); err != nil {
			return nil, nil, err
		}
	}
	var records []inboxRecord
	var shown []proto.Image
	for _, e := range files {
		name := e.Name
		path := filepath.Join(a.home, "inbox", name)
		data, size, err := a.fs.Read(path, inboxReadLimit)
		readable := err == nil
		if errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			data = []byte(fmt.Sprintf("(could not read %s: %v)", name, err))
		}
		sum := sha256.Sum256(data)
		hash := hex.EncodeToString(sum[:])
		target := name
		for i := 1; ; i++ {
			if _, err := a.fs.Stat(filepath.Join(processed, target)); errors.Is(err, fs.ErrNotExist) {
				break
			}
			target = fmt.Sprintf("%s-%d", name, i)
		}
		file := "inbox/processed/" + target
		text, content, img := true, "", (*proto.Image)(nil)
		if readable {
			text, content, img = classify(data, size, filepath.Join(a.home, file), imageTypes)
		}
		if text {
			var clipped bool
			content, clipped = shell.Clip(strings.ToValidUTF8(string(data), "�"), d.cfg.InboxClip)
			if clipped || size > int64(len(data)) {
				content += "\n" + fmt.Sprintf(MsgInboxClipFmt, filepath.Join(a.home, file))
			}
		}
		var image map[string]any
		if img != nil {
			copyPath, err := j.Attach(strings.TrimPrefix(img.MediaType, "image/"), img.Data)
			if err != nil {
				a.logf("journal copy of %s: %v", file, err)
				content, img = fmt.Sprintf(MsgImageJournalFmt, err, filepath.Join(a.home, file)), nil
			} else {
				image = map[string]any{"media_type": img.MediaType, "bytes": len(img.Data), "sha256": hash, "copy": copyPath}
			}
		}
		r := inboxRecord{File: file, From: a.label(e.UID), Content: content}
		fields := map[string]any{"file": r.File, "from": r.From, "size": size, "content": r.Content}
		if image != nil {
			fields["image"] = image
		}
		j.Record("inbox", fields)
		if err := a.fs.Rename(path, filepath.Join(processed, target)); err != nil {
			return records, shown, err
		}
		records = append(records, r)
		if img != nil {
			img.File = file
			shown = append(shown, *img)
		}
	}
	return records, shown, nil
}

// label is who wrote an inbox file, by its owner uid, which the kernel set:
// "owner" for the agent's parent (root for a top-level agent), "agents/NAME"
// for its running subagent NAME, and "data" for anyone else, the agent and
// the harness's own notes included.
func (a *agent) label(uid int) string {
	if uid == a.parent.uid() {
		return "owner"
	}
	d := a.d
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.agents {
		if c.parent == a && c.uid() == uid {
			return "agents/" + filepath.Base(c.ctrl)
		}
	}
	return "data"
}
