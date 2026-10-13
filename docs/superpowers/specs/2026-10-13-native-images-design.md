# Native images: design

Date: 2026-10-13. Target release: 0.5.0.

## Goal

When the owner sends the agent a photo, the agent's own model sees it, instead
of a second model's text description of it. The agent can also choose to look
at any image file it has, such as a screenshot it took.

Today the harness sends only text. The main agent works around this with its
own `~/tools/bin/vision` script, which asks another model to describe an image.
Each look costs about 6¢: an extra Opus turn that rereads the whole cached
conversation (~130k tokens) just to run the script, plus the script's call and
the description it returns. Native images cost about 1¢ a photo, need no extra
turn and lose no detail.

This belongs in the harness because the agent cannot build it: only the harness
builds the model request, so no script can put an image in front of the model.

## Decisions

| Question | Decision |
| --- | --- |
| How does an image reach the model? | It is a file in the inbox. No new command, directory or daemon interface. |
| How does the agent view an image mid-run? | `cp FILE ~/inbox/`. Inbox files collected after a step are already appended after the command result. |
| Which providers? | Anthropic only. The openai-compatible plugin does not declare image support, so its models get a note instead. |
| Formats | JPEG, PNG and GIF, which the standard library decodes. WebP and others get a note. |
| Limits | 5 MiB and 2000 px on each side. Nothing is resized by the harness. |
| Journal | A copy of every image shown is kept beside the run's journal, root-owned like it. |
| Protocol version | Stays 1. The new fields are optional and omitted when empty. |
| Other binary inbox files | Replaced by a one-line note instead of being sent as mangled text. |

## Inbox files

Collection is unchanged up to the read (at most 16 MiB through the agent's
helper, then journaled and moved to `inbox/processed/`). The daemon then
classifies the data:

1. `http.DetectContentType` says `image/jpeg`, `image/png` or `image/gif`:
   - over 5 MiB (by the file's full size): note, "over the 5 MiB limit";
   - `image.Decode` fails: note, "could not decode", with the error;
   - wider or taller than 2000 px: note, with its size, "over 2000 px a side";
   - the model does not accept images: note, "the model does not accept
     images";
   - otherwise the image is shown.
2. Any other `image/*` type: note, naming the type and the supported ones.
3. Data that is not valid UTF-8 (ignoring a rune cut off by the read limit):
   note, "binary file not shown", with the detected type and size.
4. Anything else is text and is handled exactly as today.

Every note gives the file's full path, so the agent can still work on the file
(resize it, convert it, copy the result into `~/inbox/`).

The record keeps its usual shape, `{"file", "from", "content"}`, and its label
rules are unchanged. For a shown image `content` describes it, for example
`[image/jpeg 1280x960, 169027 bytes, shown below]`; for a note, `content` is the
note. Exact wording lives in `internal/daemon/messages.go` with the other
harness messages.

Why these limits:
- 5 MiB keeps the base64 form under the API's 10 MB limit per image.
- In a request with more than 20 images the API rejects any image over 2000 px
  on a side instead of downscaling it. Such a request could never succeed, so
  the agent would be stuck retrying it. Capping every image at 2000 px avoids
  this. Opus sees at most 2576 px, so little is lost; Telegram photos are at
  most 1280 px.
- Decoding the whole image, not just its header, stops a damaged file from
  causing an API error that the daemon would retry forever.

A behaviour change: an inbox text file in a non-UTF-8 encoding is now a note
instead of text with replacement characters.

## Conversation

Shown images ride on the same user message as their records: the message built
from the inbox at the start of a run, and each message appended after a step.
They stay in the conversation for as long as it is continued, like any other
message. Every call resends them; the provider's prompt cache makes that cheap
(about $0.0004 an image per call on Opus 5.5).

The request limit is 32 MB. If accumulated images exceed it, the API answers
413, which the Anthropic plugin reports as `context_overflow`. The daemon's
existing handling applies: a reset note mid-run, or the first-call guard on a
fresh run's first call.

To keep a conversation well under that limit, the daemon also caps the images
it shows: at most 20 MiB of raw image data per conversation
(`Config.ImageBudget`), counted over every image in its messages, so a
continued conversation counts its earlier runs' images. While collecting, an
image that would push the total over the budget gets a note
(`MsgImageBudgetFmt`) instead, and is neither attached nor copied to the
journal; later images that still fit are shown. A new conversation, after its
end or a context reset, starts with the whole budget. (A ruling from the
final review; this design first relied on the 413 alone.)

## Plugin protocol

`proto.Message` gains `images`, used on user messages only:

```go
type Image struct {
	File      string `json:"file"`       // the record's file, e.g. inbox/processed/NAME
	MediaType string `json:"media_type"` // image/jpeg, image/png or image/gif
	Data      []byte `json:"data"`       // raw bytes; JSON carries them as base64
}

Images []Image `json:"images,omitempty"`
```

`proto.Model` in `describe` gains `images` (`bool`, omitted when false). The
daemon reads it with the context size at the start of each run
(`plugin.AcceptsImages(describe, model)`, next to `plugin.ContextTokens`) and
sends images only to models that set it. The spec's "the daemon interprets
nothing but `context_tokens` and `usage`" becomes "... `context_tokens`,
`images` and `usage`".

Both fields are omitted when empty, so every request without images is
byte-for-byte what it is today: existing conversations and their prompt caches
are untouched, and a plugin built before this change keeps working (its models
get notes).

## Plugins

**anthropic**
- Every model declares `images: true`.
- A user message becomes its records' text block, then for each image a text
  block naming its file (`inbox/processed/NAME:`) and an image block with a
  base64 source. The Vision docs recommend labelling each of several images.
- The existing marker on the last block also works when that block is an image.
- HTTP 413 is reported with kind `context_overflow`.

**openai-compatible**: unchanged.

**replay**: its header's models may set `images`; its request log records
images like any other field.

## Journal

The guarantee that every model message is journaled, and that agents cannot
change journals, extends to images:

- The daemon writes a copy of each shown image beside the run's journal as
  `<run>-<k>.<ext>` (`k` counts the run's images from 1; `ext` is `jpg`, `png`
  or `gif`), root-owned 0640 in the agent's group, like the journal itself.
- The image's `inbox` record gains
  `"image": {"media_type", "width", "height", "bytes", "sha256", "copy"}`, where
  `copy` is the copy's path. The record never holds the image data.

Run files are recognised by `journal.IsRun` (`<stamp>.jsonl`), which
`journal.Latest` and crash recovery use, and `agent log` globs `*.jsonl`. The
copies match neither, so they cannot be mistaken for runs.

## Seed README and release notes

The seed README states that JPEG, PNG and GIF files in the inbox are shown to
the model (with the limits), and that copying an image into `~/inbox/` shows
it after the current command. `internal/notes/notes.md` gets a 0.5.0 section
with the same facts, plus the binary-file note, so running agents learn them at
their next wake.

## Tests

- **Classification**, table-driven: JPEG, PNG and GIF shown; over 5 MiB; over
  2000 px wide and tall; WebP; damaged JPEG; binary; non-UTF-8 text; UTF-8 text
  unchanged; a model without images.
- **Daemon**, with the replay plugin:
  - an image in the inbox at wake is attached to the first user message;
  - an image copied in during a run is attached after that command's result;
  - a model without `images` gets the note and no image;
  - a run without images sends exactly the requests it sends today;
  - the journal record and the image copy, with their modes.
- **Anthropic plugin**: the label and image blocks; the cache marker on a final
  image block; 413 reported as `context_overflow`; describe declares `images`.
- `go vet ./...`, `go test ./...` and the root privilege tests in Docker.

## Out of scope

- Images for the openai-compatible plugin.
- WebP, and resizing or converting images in the harness.
- Images in owner or subagent messages (`agent send`, `agent message`), which
  stay text.
- Changing the Telegram bridge to save photos into `~/inbox/`. That is the
  agent's own code; the owner asks it to.

## Rollout

Release 0.5.0, deploy it to the VM with the release's `install.sh`, and check
the service with `systemctl is-active agent` and `journalctl -u agent`.
