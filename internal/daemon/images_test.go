package daemon

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// inboxRecordFor returns the run's inbox journal record for file.
func inboxRecordFor(t *testing.T, records []map[string]any, file string) map[string]any {
	t.Helper()
	for _, r := range records {
		if r["type"] == "inbox" && r["file"] == file {
			return r
		}
	}
	t.Fatalf("no inbox record for %s in %v", file, records)
	return nil
}

func TestInboxImageShown(t *testing.T) {
	h := newHarness(t, done())
	h.start()
	png := encodePNG(40, 30)
	h.drop(h.mainHome, "photo.png", string(png))
	records := h.waitEnds(h.mainID, 1)
	msg := h.requests()[0].Messages[0]
	if len(msg.Images) != 1 {
		t.Fatalf("%d images, want 1: %+v", len(msg.Images), msg)
	}
	img := msg.Images[0]
	if img.File != "inbox/processed/photo.png" || img.MediaType != "image/png" || !bytes.Equal(img.Data, png) {
		t.Fatalf("image %s %s, %d bytes", img.File, img.MediaType, len(img.Data))
	}
	if !strings.Contains(msg.Content, "[image/png 40x30,") {
		t.Fatalf("content %q", msg.Content)
	}
	shown, _ := inboxRecordFor(t, records, img.File)["image"].(map[string]any)
	copyPath, _ := shown["copy"].(string)
	if copyPath == "" {
		t.Fatalf("inbox record has no image copy: %v", shown)
	}
	data, err := os.ReadFile(copyPath)
	if err != nil || !bytes.Equal(data, png) {
		t.Fatalf("copy %s: %d bytes, %v", copyPath, len(data), err)
	}
	if info, _ := os.Stat(copyPath); info.Mode().Perm() != 0o640 {
		t.Fatalf("copy mode %v", info.Mode())
	}
}

func TestImageCopiedMidRun(t *testing.T) {
	h := newHarness(t, step("cp shot.png ~/inbox/", 100), done())
	h.start()
	os.WriteFile(filepath.Join(h.mainHome, "shot.png"), encodePNG(8, 8), 0o644)
	h.drop(h.mainHome, "go.md", "go")
	h.waitEnds(h.mainID, 1)
	msgs := h.requests()[1].Messages
	last, prev := msgs[len(msgs)-1], msgs[len(msgs)-2]
	if last.Role != "user" || len(last.Images) != 1 || last.Images[0].File != "inbox/processed/shot.png" {
		t.Fatalf("last message %+v", last)
	}
	if prev.Role != "tool" {
		t.Fatalf("message before the image is %q, want the tool result", prev.Role)
	}
}

func TestImagesKeepRecordOrder(t *testing.T) {
	h := newHarness(t, step("sleep 0.5", 100), done())
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	waitFor(t, "first request", func() bool { return len(h.requests()) >= 1 })
	h.drop(h.mainHome, "a.png", string(encodePNG(4, 4)))
	h.drop(h.mainHome, "b.md", "between")
	h.drop(h.mainHome, "c.jpg", string(encodeJPEG(4, 4)))
	h.waitEnds(h.mainID, 1)
	msgs := h.requests()[1].Messages
	last := msgs[len(msgs)-1]
	var files []string
	for _, img := range last.Images {
		files = append(files, img.File)
	}
	if strings.Join(files, " ") != "inbox/processed/a.png inbox/processed/c.jpg" {
		t.Fatalf("image files %v", files)
	}
	a, b, c := strings.Index(last.Content, "a.png"), strings.Index(last.Content, "b.md"), strings.Index(last.Content, "c.jpg")
	if a < 0 || b < a || c < b {
		t.Fatalf("records out of order: %q", last.Content)
	}
}

func TestImagesNeedCapableModel(t *testing.T) {
	h := newHarness(t, done())
	h.start()
	config := filepath.Join(h.mainHome, "config.json")
	os.WriteFile(config+".tmp", []byte(`{"provider":"replay","model":"replay-noimg"}`), 0o644)
	if err := os.Rename(config+".tmp", config); err != nil {
		t.Fatal(err)
	}
	h.drop(h.mainHome, "photo.png", string(encodePNG(4, 4)))
	h.waitEnds(h.mainID, 1)
	msg := h.requests()[0].Messages[0]
	if len(msg.Images) != 0 || !strings.Contains(msg.Content, "does not accept images") {
		t.Fatalf("%d images, content %q", len(msg.Images), msg.Content)
	}
}

func TestContinuationKeepsImages(t *testing.T) {
	h := newHarness(t, done(), done())
	h.start()
	h.drop(h.mainHome, "photo.png", string(encodePNG(4, 4)))
	h.waitEnds(h.mainID, 1)
	h.drop(h.mainHome, "more.md", "more")
	h.waitEnds(h.mainID, 2)
	msgs := h.requests()[1].Messages
	if len(msgs) != 3 || !strings.Contains(msgs[2].Content, "more") {
		t.Fatalf("second run did not continue the conversation: %+v", msgs)
	}
	if imgs := msgs[0].Images; len(imgs) != 1 || imgs[0].File != "inbox/processed/photo.png" {
		t.Fatalf("first message lost its image: %+v", msgs[0])
	}
}

func TestTextRunSendsNoImagesField(t *testing.T) {
	h := newHarness(t, step("true", 100), done())
	h.start()
	h.drop(h.mainHome, "a.md", "hello")
	h.waitEnds(h.mainID, 1)
	if n := len(h.requests()); n != 2 {
		t.Fatalf("%d requests, want 2", n)
	}
	if fileContains(h.reqLog, `"images"`) {
		t.Fatal(`request log holds an "images" field`)
	}
}

func TestImageBudgetWithinCollection(t *testing.T) {
	h := newHarness(t, step("sleep 0.5", 100), done())
	a, b, c := encodePNG(4, 4), encodeJPEG(64, 64), encodePNG(2, 2)
	if len(b) <= len(c) {
		t.Fatalf("b.jpg (%d bytes) must be larger than c.png (%d bytes)", len(b), len(c))
	}
	h.cfg.ImageBudget = len(a) + len(c)
	h.start()
	h.drop(h.mainHome, "go.md", "go")
	waitFor(t, "first request", func() bool { return len(h.requests()) >= 1 })
	h.drop(h.mainHome, "a.png", string(a))
	h.drop(h.mainHome, "b.jpg", string(b))
	h.drop(h.mainHome, "c.png", string(c))
	records := h.waitEnds(h.mainID, 1)
	msgs := h.requests()[1].Messages
	last := msgs[len(msgs)-1]
	var files []string
	for _, img := range last.Images {
		files = append(files, img.File)
	}
	if strings.Join(files, " ") != "inbox/processed/a.png inbox/processed/c.png" {
		t.Fatalf("image files %v", files)
	}
	note := fmt.Sprintf(MsgImageBudgetFmt, filepath.Join(h.mainHome, "inbox/processed/b.jpg"))
	if rec := inboxRecordFor(t, records, "inbox/processed/b.jpg"); rec["content"] != note || rec["image"] != nil {
		t.Fatalf("b.jpg record %v, want content %q and no image", rec, note)
	}
}

func TestImageBudgetCarriesOver(t *testing.T) {
	h := newHarness(t, done(), done())
	a, b := encodePNG(4, 4), encodePNG(5, 5)
	h.cfg.ImageBudget = len(a) + len(b) - 1 // b fits alone, not after a
	h.start()
	h.drop(h.mainHome, "a.png", string(a))
	h.waitEnds(h.mainID, 1)
	h.drop(h.mainHome, "b.png", string(b))
	h.waitEnds(h.mainID, 2)
	msgs := h.requests()[1].Messages
	if len(msgs) != 3 || len(msgs[0].Images) != 1 {
		t.Fatalf("second run did not continue the conversation: %+v", msgs)
	}
	if len(msgs[2].Images) != 0 || !strings.Contains(msgs[2].Content, "would exceed 20 MiB") {
		t.Fatalf("%d images, content %q", len(msgs[2].Images), msgs[2].Content)
	}
}

// failingAttach is a journal whose image copies cannot be saved.
type failingAttach struct{}

func (failingAttach) Record(string, map[string]any) error { return nil }
func (failingAttach) Attach(string, []byte) (string, error) {
	return "", errors.New("disk full")
}

func TestImageJournalCopyFails(t *testing.T) {
	h := newHarness(t)
	d := &daemon{cfg: h.cfg, delivered: &delivered{pending: map[string]string{}}}
	main := detachedMain(t, h, d)
	if err := os.MkdirAll(filepath.Join(h.mainHome, "inbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.drop(h.mainHome, "photo.png", string(encodePNG(4, 4)))
	records, shown, err := main.collectInbox(failingAttach{}, true, h.cfg.ImageBudget)
	if err != nil {
		t.Fatal(err)
	}
	note := fmt.Sprintf(MsgImageJournalFmt, "disk full", filepath.Join(h.mainHome, "inbox/processed/photo.png"))
	if len(records) != 1 || records[0].Content != note || len(shown) != 0 {
		t.Fatalf("records %+v, %d images; want content %q and none", records, len(shown), note)
	}
}
