---
name: context
description: Request conversation compaction while preserving full history and the current turn.
---

Run `bot-app context compact '{"keepRecentGroups":1}'` to request compaction before the next model response. It preserves the current input and complete tool call/results. The journal is never deleted. Compaction cannot shrink the active turn alone; reduce shell output or choose a larger model when that turn exceeds the input budget. A request survives provider failure and is retried on the next context preparation.

Every model call receives a temporary system message containing context usage data: an approximate size of the prepared history, the selected model's context window, and the previous recorded provider input count including cached tokens. The estimate counts native assistant content once and excludes the system prompt and tool schemas; the provider count describes the previous call, not the current request. The snapshot is appended after the prepared history and is never saved to the journal. It contains usage data only, with no compaction reminder. The model library maps system messages to the provider's supported system or developer role.
