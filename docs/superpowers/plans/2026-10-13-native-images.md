# Native Images Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** JPEG, PNG and GIF files in an agent's inbox reach an image-capable model as real images, with journal copies, notes for everything that cannot be shown, and Anthropic plugin support.

**Architecture:** The daemon classifies each inbox file after reading it (`classify`), attaches shown images to the user message that carries their records (`proto.Message.Images`), and saves a copy of each beside the run journal (`journal.Writer.Attach`). Models opt in through `describe` (`proto.Model.Images`). The Anthropic plugin renders images as labelled base64 image blocks and reports HTTP 413 as a context overflow.

**Tech Stack:** Go standard library only (`net/http.DetectContentType`, `image`, `image/jpeg`, `image/png`, `image/gif`, `unicode/utf8`).

**Spec:** `docs/superpowers/specs/2026-10-13-native-images-design.md`

## Global Constraints

- Go standard library only. Run `go vet ./...` and `go test ./...` before every commit.
- Work on `main`; commit and push each task. Commit messages end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- A request without images must be byte-for-byte what it is today. Do not change `SystemPrompt`, `proto.ShTool()` or how existing blocks are built.
- Limits: at most 5 MiB (`5 << 20` bytes, by the file's full size) and at most 2000 px on each side. Shown types: `image/jpeg`, `image/png`, `image/gif`.
- Plugin protocol stays version 1; new fields are `omitempty`.
- `internal/notes/notes.md` holds facts, not instructions.

## Review Focus

- A JPEG saved under a `.png` name or no extension (the bridge's naming) is shown as `image/jpeg`: detection is by content. Test in Task 2.
- A photo written straight into the inbox without the dot-name rename can be read half-written: it must become a "could not decode" note giving the path, and the run continues. Test in Task 2.
- A follow-up within 4 minutes continues the conversation: the earlier image must still be in the request. Test in Task 4.
- Several images and text files collected together: images keep record order and each image's `File` matches its record. Tests in Tasks 4 and 5.
- A UTF-8 text file cut mid-rune by the 16 MiB read limit is still text, not a binary note. Test in Task 2.

---

### Task 1: Protocol fields

**Files:**
- Modify: `internal/proto/proto.go` (`Message`, `Model`, new `Image`)
- Modify: `internal/plugin/config.go` (next to `ContextTokens`)
- Test: `internal/proto/proto_test.go`, `internal/plugin/plugin_test.go`

**Interfaces:**
- Produces:
  - `type Image struct { File string \`json:"file"\`; MediaType string \`json:"media_type"\`; Data []byte \`json:"data"\` }`
  - `proto.Message.Images []Image \`json:"images,omitempty"\`` (user messages only; document it in the `Message` comment)
  - `proto.Model.Images bool \`json:"images,omitempty"\``
  - `func AcceptsImages(d proto.DescribeResponse, model string) bool` in `internal/plugin`

- [ ] **Step 1: Write the failing tests**

```go
func TestImagesOmittedWhenEmpty(t *testing.T) {
	m, _ := json.Marshal(Message{Role: "user", Content: "hi"})
	if string(m) != `{"role":"user","content":"hi"}` {
		t.Fatalf("message: %s", m)
	}
	d, _ := json.Marshal(Model{ID: "m", ContextTokens: 1})
	if strings.Contains(string(d), "images") {
		t.Fatalf("model: %s", d)
	}
}

func TestImageDataIsBase64(t *testing.T) {
	in := Image{File: "inbox/processed/a.png", MediaType: "image/png", Data: []byte{1, 2, 3}}
	data, _ := json.Marshal(in)
	if !strings.Contains(string(data), `"data":"AQID"`) { t.Fatalf("%s", data) }
	var out Image
	if json.Unmarshal(data, &out); !reflect.DeepEqual(in, out) { t.Fatalf("round trip %+v", out) }
}

// internal/plugin
func TestAcceptsImages(t *testing.T) {
	d := proto.DescribeResponse{Models: []proto.Model{{ID: "a", Images: true}, {ID: "b"}}}
	// a → true, b → false, "missing" → false
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/proto ./internal/plugin`
Expected: FAIL to compile (`Image`, `Images`, `AcceptsImages` undefined).

- [ ] **Step 3: Add the fields, `Image` and `AcceptsImages`**

- [ ] **Step 4: Run the tests**

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit and push** — "Add images to the plugin protocol".

---

### Task 2: Classifying inbox files

**Files:**
- Create: `internal/daemon/classify.go`
- Modify: `internal/daemon/messages.go` (new constants)
- Test: `internal/daemon/classify_test.go`

**Interfaces:**
- Produces:
  - `const imageMaxBytes = 5 << 20`, `const imageMaxEdge = 2000`
  - `type shownImage struct { MediaType string; Width, Height int; Data []byte }`
  - `func classify(data []byte, size int64, path string, images bool) (text bool, content string, img *shownImage)`: `data` is what was read, `size` the file's full size, `path` its full path (for notes), `images` whether the model accepts images. `text == true` means "handle as text, as today" (`content`, `img` empty). Otherwise `content` is the record's content and `img` is non-nil only when shown.

Order of checks (from the spec): `http.DetectContentType(data)` is `image/jpeg`, `image/png` or `image/gif` → size over `imageMaxBytes` → `image.Decode` error → either side over `imageMaxEdge` → `!images` → shown. Any other `image/*` → type note. Otherwise, not valid UTF-8 → binary note, but when `int64(len(data)) < size` first drop an incomplete rune at the end (at most 3 bytes, using `utf8.FullRune` on the tail). Otherwise text. Import the three decoders for their side effects.

Exact copy for `messages.go`:

```go
	MsgImageShownFmt       = "[%s %dx%d, %d bytes, shown below]"
	MsgImageTooLargeFmt    = "[image not shown: %d bytes is over the 5 MiB limit; resize it and copy the result into ~/inbox/. File: %s]"
	MsgImageTooWideFmt     = "[image not shown: %dx%d is over 2000 px a side; resize it and copy the result into ~/inbox/. File: %s]"
	MsgImageUndecodableFmt = "[image not shown: could not decode it: %v. File: %s]"
	MsgImageTypeFmt        = "[image not shown: %s is not supported, only JPEG, PNG and GIF; convert it and copy the result into ~/inbox/. File: %s]"
	MsgImageNoModelFmt     = "[image not shown: the model does not accept images. File: %s]"
	MsgImageJournalFmt     = "[image not shown: its journal copy could not be saved: %v. File: %s]"
	MsgBinaryFmt           = "[binary file not shown: %s, %d bytes. File: %s]"
```

(`MsgImageJournalFmt` is used in Task 4.)

- [ ] **Step 1: Write the failing table test `TestClassify`**

Helpers in the test file: `encodePNG(w, h int) []byte`, `encodeJPEG(w, h int) []byte`, `encodeGIF(w, h int) []byte` (an `image.NewRGBA`/`NewPaletted` of that size, standard encoders). Cases, with `path = "/home/x/inbox/processed/f"` unless noted:

| Case | data / size / images | Expect |
|---|---|---|
| PNG shown | `encodePNG(40,30)`, len, true | `img` = `image/png` 40×30, `Data` equal; content `[image/png 40x30, N bytes, shown below]` |
| JPEG shown | `encodeJPEG(20,10)` | `img.MediaType == "image/jpeg"` |
| GIF shown | `encodeGIF(5,5)` | `img.MediaType == "image/gif"` |
| JPEG named .png | `encodeJPEG(8,8)`, path `.../photo.png` | `img.MediaType == "image/jpeg"` |
| Too large | `encodePNG(4,4)`, size `5<<20 + 1` | no img; content contains `is over the 5 MiB limit` and the path |
| Too wide | `encodePNG(2001,10)` | no img; contains `2001x10 is over 2000 px` |
| Too tall | `encodePNG(10,2001)` | no img; contains `10x2001` |
| 2000 px exactly | `encodePNG(2000,2000)` | shown |
| Half-written JPEG | `encodeJPEG(64,64)[:40]` | no img; contains `could not decode it` and the path |
| WebP | `[]byte("RIFF\x24\x00\x00\x00WEBPVP8 ")` + padding | no img; contains `image/webp is not supported` |
| No-image model | `encodePNG(4,4)`, images false | no img; contains `does not accept images` |
| Binary | `[]byte{0xff, 0x00, 0x01, 0x02}` | text false; content starts `[binary file not shown:` and contains `4 bytes` |
| Latin-1 text | `[]byte("caf\xe9 au lait")` | binary note |
| UTF-8 text | `[]byte("héllo")` | text true |
| Rune cut by read limit | `[]byte("héllo")[:2]`, size 100 | text true |

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./internal/daemon -run TestClassify`
Expected: FAIL to compile (`classify` undefined).

- [ ] **Step 3: Implement `classify` and add the message constants**

- [ ] **Step 4: Run the tests**

Run: `go vet ./... && go test ./internal/daemon -run TestClassify`
Expected: PASS.

- [ ] **Step 5: Commit and push** — "Classify inbox files as text, images or binary".

---

### Task 3: Journal attachments

**Files:**
- Modify: `internal/journal/journal.go` (`Writer` keeps `gid` and a count; new method)
- Test: `internal/journal/journal_test.go`

**Interfaces:**
- Produces: `func (w *Writer) Attach(ext string, data []byte) (string, error)`: writes `<run stem>-<n>.<ext>` in the run file's directory, `n` counting this writer's attachments from 1, created `O_EXCL` with mode 0640 and the writer's group (when `gid >= 0`, as `Create` does). Returns the full path. On error it removes what it created.

- [ ] **Step 1: Write the failing test `TestAttach`**

`w, _ := Create(dir, now, -1)`; `p1, _ := w.Attach("jpg", []byte("a"))`; `p2, _ := w.Attach("png", []byte("b"))`. Assert:
- `filepath.Base(p1) == stem + "-1.jpg"` and `filepath.Base(p2) == stem + "-2.png"`, where `stem` is the run file's name without `.jsonl`;
- contents are `a` and `b`; both modes are `0o640`;
- `IsRun(filepath.Base(p1))` is false;
- after `w.End("done")`, `Latest(dir)` returns the run file.

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./internal/journal -run TestAttach`
Expected: FAIL to compile (`Attach` undefined).

- [ ] **Step 3: Implement `Attach`** (store `gid` in `Writer` in `Create`).

- [ ] **Step 4: Run the tests**

Run: `go vet ./... && go test ./internal/journal`
Expected: PASS.

- [ ] **Step 5: Commit and push** — "Save attachments beside a run journal".

---

### Task 4: Daemon wiring

**Files:**
- Modify: `internal/daemon/run.go` (`recorder`, `run`, `steps`)
- Modify: `internal/daemon/inbox.go` (`collectInbox`)
- Modify: `internal/daemon/harness_test.go` (`replayHeader`)
- Create: `internal/daemon/images_test.go`

**Interfaces:**
- Consumes: `classify`, `shownImage`, `MsgImageJournalFmt` (Task 2); `Writer.Attach` (Task 3); `proto.Image`, `Message.Images`, `plugin.AcceptsImages` (Task 1).
- Produces:
  - `recorder` gains `Attach(ext string, data []byte) (string, error)`.
  - `func (a *agent) collectInbox(j recorder, images bool) ([]inboxRecord, []proto.Image, error)`: images in record order, `File` equal to the record's `File`.
  - `steps(ctx, j, cfg, contextTokens int, images bool, owner string)`; `run` passes `plugin.AcceptsImages(describe, cfg.Model)`.

In `collectInbox`, call `classify(data, size, filepath.Join(a.home, file), images)` where today's `shell.Clip` line is. Text keeps today's code path. For a shown image: `j.Attach(ext, img.Data)` with `ext` from the media type (`jpg`, `png`, `gif`). On success, add `"image": {"media_type", "width", "height", "bytes", "sha256", "copy"}` to the existing `inbox` journal record (`sha256` is the hash already computed). On failure, log it and use `MsgImageJournalFmt` as the content, with no image. Both user messages in `steps` (the first one, and the one appended after a step) carry the images collected with their records.

`replayHeader` becomes:
`{"models":[{"id":"replay-1","context_tokens":1000,"images":true,"options":{"style":{"description":"s","values":["a","b"]}}},{"id":"replay-noimg","context_tokens":1000}]}`

- [ ] **Step 1: Write the failing tests in `images_test.go`**

Reuse the PNG/JPEG encoders from Task 2's test file.
- `TestInboxImageShown`: `newHarness(t, done())`, `h.start()`, `h.drop(h.mainHome, "photo.png", string(encodePNG(40, 30)))`, `h.waitEnds(h.mainID, 1)`. The first request's user message has one image with `File == "inbox/processed/photo.png"`, `MediaType == "image/png"` and identical `Data`, and `Content` contains `[image/png 40x30,`. The run's `inbox` journal record has `image.copy`; that file holds the same bytes with mode 0640.
- `TestImageCopiedMidRun`: script `step("cp shot.png ~/inbox/", 100), done()`. Write `shot.png` (`encodePNG(8, 8)`) into `h.mainHome` after `h.start()`, then drop `go.md`. In the second request, the last message is a user message whose `Images[0].File == "inbox/processed/shot.png"`, and the message before it is the tool result.
- `TestImagesKeepRecordOrder`: script `step("sleep 0.5", 100), done()`. Drop `go.md`; after the first request, drop `a.png`, `b.md` and `c.jpg`. The second request's last user message has `Images` files `[inbox/processed/a.png, inbox/processed/c.jpg]`, and its `Content` lists `a.png`, `b.md` and `c.jpg` in that order.
- `TestImagesNeedCapableModel`: after `h.start()`, write `{"provider":"replay","model":"replay-noimg"}` to `filepath.Join(h.mainHome, "config.json")`, then drop `photo.png`. The request has no images, and `Content` contains `does not accept images`.
- `TestContinuationKeepsImages`: script `done(), done()`. Drop `photo.png`, wait for one end, then drop `more.md` and wait for two ends. The second run's request still contains the first user message with its one image (continuation, as in `TestFollowUpContinuesConversation`).
- `TestTextRunSendsNoImagesField`: a text-only run (`step("true", 100), done()`, drop `a.md`). The raw request log (`h.reqLog`) contains no `"images"`.

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/daemon -run 'Image|Continuation|TextRun'`
Expected: the image tests FAIL on their assertions. `TestTextRunSendsNoImagesField` already passes; it guards the unchanged path.

- [ ] **Step 3: Implement the wiring**

- [ ] **Step 4: Run the whole suite**

Run: `go vet ./... && go test ./...`
Expected: PASS, including every existing daemon test after the header change.

- [ ] **Step 5: Commit and push** — "Show inbox images to models that accept them".

---

### Task 5: Anthropic plugin

**Files:**
- Modify: `plugins/anthropic/main.go` (`Describe`, block types, `buildMessages`, HTTP error branch)
- Test: `plugins/anthropic/main_test.go`

**Interfaces:**
- Consumes: `proto.Image`, `Message.Images`, `Model.Images` (Task 1).
- Produces: user messages rendered as `text(Content)`, then for each image `text("<File>:")` and `{"type":"image","source":{"type":"base64","media_type":…,"data":<std base64>}}`. HTTP 413 gives `Error.Kind == proto.ErrContextOverflow`, whatever the body.

- [ ] **Step 1: Write the failing tests**
- `TestDescribeDeclaresImages`: every model in `Describe().Models` has `Images == true`.
- `TestImageBlocks`: complete with `Messages: []proto.Message{{Role: "user", Content: "records", Images: []proto.Image{{File: "inbox/processed/a.png", MediaType: "image/png", Data: []byte{1, 2, 3}}, {File: "inbox/processed/b.jpg", MediaType: "image/jpeg", Data: []byte{4}}}}}`. The single API message's content is `[text "records", text "inbox/processed/a.png:", image(png, "AQID"), text "inbox/processed/b.jpg:", image(jpeg, "BA==")]`. The last block (the second image) carries `cache_control`.
- Extend `TestContextOverflowKind`: `fakeAPI{status: 413, body: `{"type":"error","error":{"type":"request_too_large","message":"Request exceeds the maximum allowed number of bytes."}}`}` is overflow, and so is status 413 with body `too large`.

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./plugins/anthropic`
Expected: FAIL (no images in describe, no image blocks, 413 not overflow).

- [ ] **Step 3: Implement**

- [ ] **Step 4: Run the tests**

Run: `go vet ./... && go test ./...`
Expected: PASS, with `TestMessageMapping` and `TestRequestShape` unchanged.

- [ ] **Step 5: Commit and push** — "Send inbox images to the Anthropic API".

---

### Task 6: Docs, release notes and privilege tests

**Files:**
- Modify: `docs/spec.md`:
  - The loop: step 5 and **Context**: classification, notes, images on the user message, the limits.
  - Plugin protocol: `images` on user messages and on models; "interprets nothing but `context_tokens`, `images` and `usage`"; HTTP 413 in the anthropic line.
  - Journal: the `image` field and the `<run>-<n>.<ext>` copies.
- Modify: `internal/seed/README.md`: next to the "Messages arrive as files in ~/inbox/" bullet, say that JPEG, PNG and GIF files up to 5 MiB and 2000 px a side are shown as images when the model accepts them, that copying an image into `~/inbox/` shows it after the current command, and that other binary files arrive as a note with their path.
- Modify: `internal/notes/notes.md`: a new `## 0.5.0` section above `## 0.4.1`, stating the same facts plus the journal copies.

- [ ] **Step 1: Make the edits**
- [ ] **Step 2: Run** `go vet ./... && go test ./...`. Expected: PASS (seed and notes tests included).
- [ ] **Step 3: Run the root privilege tests in Docker**, as in the `privilege-tests-in-docker` memory (`golang:1.27`, CI's users, `AGENT_ROOT_TESTS=1 go test ./internal/daemon -run Privilege`). Expected: PASS.
- [ ] **Step 4: Commit and push** — "Document native images".

---

### Task 7: Release 0.5.0 and deploy

- [ ] **Step 1:** `scripts/release.sh 0.5.0`, then commit the updated `plugins/index.json` and `install.sh` and push.
- [ ] **Step 2:** Tag `v0.5.0`, push the tag, and confirm the release workflow publishes without a checksum mismatch.
- [ ] **Step 3:** Deploy by running the release's `install.sh` on the VM, as in the `deploy-token-via-stdin` memory.
- [ ] **Step 4:** Verify with read-only checks: `systemctl is-active agent` prints `active`, and `journalctl -u agent` has no errors since the restart.
- [ ] **Step 5:** Check `git status` is clean and `main` matches `origin/main`. Report to Sean, including the Telegram message that asks the agent to save photos straight into `~/inbox/`.
