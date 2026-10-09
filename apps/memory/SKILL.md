---
name: memory
description: Read or update persistent preferences and notes; search complete conversation history.
---

Run `bot-app memory get '{}'` before updating. Replace one section with `bot-app memory update '{"section":"USER","value":"..."}'` or section notes; preserve useful existing facts. Empty values clear a section. Limits follow configured maxChars; validation rejects oversized input. Run `bot-app memory search '{"query":"literal text","limit":10}'` to search the full durable conversation including compacted history. Matches are recent first and excerpts are bounded. Remembered data can be stale; the human's current instructions take precedence.
