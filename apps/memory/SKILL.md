---
name: memory
description: Maintain a small injected Markdown index; retrieve durable notes and original journal evidence.
---

The app injects its index and brief usage instructions before each model call. No separate memory workflow needs to be loaded.

- `bot-app memory show` prints the accepted Markdown verbatim. Edit a temporary copy with ordinary shell tools.
- `bot-app memory check --stdin < /tmp/memory-index.md` reports its budget without saving.
- `bot-app memory write --stdin < /tmp/memory-index.md` validates and atomically accepts it; oversized writes leave the previous index intact.
- `bot-app memory status` shows the path, hash, size and budget.
- `bot-app memory search '{"query":"exact words","limit":5}'` searches the full journal. Try different or fewer words if needed.
- `bot-app memory read '{"id":123}'` reads exact original JSON in pages. Follow `nextOffset` until null and concatenate `data`.

The accepted file is `workspace/memory/INDEX.md`. Keep descriptions and pointers to durable files, with a few frequently relevant facts. Paths are relative to the workspace. Store detail in ordinary files and maintain them during foreground work. There is no reviewer, automatic consolidation, or background model call. The app does not check whether linked content is true or up to date.

`maxIndexTokens` defaults to 1000 (128–10000). The deterministic estimate is `ceil(UTF-8 bytes / 4)`; actual provider token counts differ. The budget covers the index, with a small fixed usage header added separately. Update through `write` to preserve atomic validation; direct file edits bypass that protection. Invalid direct edits are rejected at read time, and context can temporarily retain its last valid snapshot.

First use preserves prior USER/notes verbatim in a linked legacy Markdown file and leaves their original state untouched for rollback. Small legacy facts also remain in the initial index; larger sections are linked without truncation. Accepted index changes, including clearing it, appear on the next model request and survive restarts independently of compaction.
