---
name: context
description: Request conversation compaction while preserving full history and the current turn.
---

Run `bot-app context compact '{"keepRecentGroups":1}'` to request compaction before the next model response. It preserves the current input and complete tool call/results. The journal is never deleted. Compaction cannot shrink the active turn alone; reduce shell output or choose a larger model when that turn exceeds the input budget. A request survives provider failure and is retried on the next context preparation.
