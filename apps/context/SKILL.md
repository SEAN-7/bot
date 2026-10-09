---
name: context
description: Request conversation compaction while preserving full history and the current turn.
---

Run `bot-app context compact '{"keepRecentGroups":1}'` to request compaction before the next model response. It preserves the current input and complete tool call/results. The journal is never deleted. Compaction cannot shrink the active turn alone; reduce shell output or choose a larger model when that turn exceeds the input budget. A request survives provider failure and is retried on the next context preparation.

Before each model response, the controller records a short usage snapshot in the journal, e.g. `History ~57323/1000000 tokens; prior input 37549.` Earlier snapshots remain at their original positions until normal compaction. History is estimated from JSON characters / 4, counting native assistant content once and excluding the system prompt and tool schemas; prior input includes cached tokens from the preceding model call. `?` means unavailable. Growth between snapshots is normal. Retries without new activity reuse the last snapshot. The model adapter presents these records as system messages.
