---
name: memory
description: Read or update persistent preferences and notes; search complete conversation history.
---

Run `bot-app memory get '{}'` before updating. Replace one section with `bot-app memory update '{"section":"USER","value":"..."}'` or section notes; preserve useful existing facts. Empty values clear a section. Limits follow configured maxChars; validation rejects oversized input. Run `bot-app memory search '{"query":"literal text","limit":10}'` to search the full durable conversation including compacted history. Matches are recent first and excerpts are bounded. Remembered data can be stale; the human's current instructions take precedence.

Like [Hermes's built-in memory](https://hermes-agent.nousresearch.com/docs/user-guide/features/memory), USER preferences and working notes appear as a separate reference block in the system prompt. The controller captures a snapshot on first use and refreshes it after successful context compaction. It stays frozen between those boundaries, including service restarts, to preserve the prompt prefix. Writes persist immediately and `get` returns live state; a write does not immediately replace the frozen prompt block. Memory cannot be summarized away with conversation history. Clearing a section takes effect in the prompt at the next compaction boundary too.

Default limits are 1,375 characters for USER and 2,200 for notes. `userMaxChars` and `notesMaxChars` override individual limits; existing `maxChars` overrides both. Existing saved data is preserved, including entries above the new defaults. When a write exceeds the limit, consolidate or remove stale details yourself and retry; memory never silently truncates or auto-compacts entries. Save durable preferences, environment facts, corrections and useful lessons; keep transient tool output in the searchable journal.
