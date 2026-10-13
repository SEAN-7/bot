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

	"github.com/SEAN-7/bot/internal/proto"
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
	return to.fs.Write(filepath.Join(to.home, "inbox", noteName(kind, time.Now())), []byte(text+"\n"), 0o640)
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

// imageExt names a shown image's journal copy. Its keys are the image types
// the daemon can check, and so the only ones it shows.
var imageExt = map[string]string{"image/jpeg": "jpg", "image/png": "png", "image/gif": "gif"}

// collectInbox reads, journals and moves every pending file into
// inbox/processed/, returning the records to show the model and, in record
// order, the images shown with them. imageTypes is the image media types the
// model accepts, budget how many more bytes of images the conversation may
// show.
func (a *agent) collectInbox(j recorder, imageTypes []string, budget int) ([]inboxRecord, []proto.Image, error) {
	d := a.d
	names, err := a.pending()
	if err != nil || len(names) == 0 {
		return nil, nil, err
	}
	processed := filepath.Join(a.home, "inbox", "processed")
	if err := a.fs.Mkdir(processed, 0o750); err != nil {
		return nil, nil, err
	}
	var records []inboxRecord
	var shown []proto.Image
	for _, name := range names {
		path := filepath.Join(a.home, "inbox", name)
		data, size, err := a.fs.Read(path, hashLimit)
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
		text, content, img := true, "", (*shownImage)(nil)
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
		if img != nil && len(img.Data) > budget {
			content, img = fmt.Sprintf(MsgImageBudgetFmt, filepath.Join(a.home, file)), nil
		}
		var image map[string]any
		if img != nil {
			copyPath, err := j.Attach(imageExt[img.MediaType], img.Data)
			if err != nil {
				a.logf("journal copy of %s: %v", file, err)
				content, img = fmt.Sprintf(MsgImageJournalFmt, err, filepath.Join(a.home, file)), nil
			} else {
				image = map[string]any{"media_type": img.MediaType, "width": img.Width, "height": img.Height, "bytes": len(img.Data), "sha256": hash, "copy": copyPath}
			}
		}
		r := inboxRecord{File: file, From: a.label(name, hash), Content: content}
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
			budget -= len(img.Data)
			shown = append(shown, proto.Image{File: file, MediaType: img.MediaType, Data: img.Data})
		}
	}
	return records, shown, nil
}

// imageBytes is how many bytes of images messages show.
func imageBytes(messages []proto.Message) int {
	n := 0
	for _, m := range messages {
		for _, img := range m.Images {
			n += len(img.Data)
		}
	}
	return n
}

// label is the sender the daemon recorded when it delivered this file
// ("owner" or "agents/NAME"), and "data" for anything else.
func (a *agent) label(name, sum string) string {
	if from := a.d.delivered.take(a.id, name, sum); from != "" {
		return from
	}
	return "data"
}
