---
name: scheduler
description: Create, inspect or cancel durable future wake-ups in the same conversation.
---

Run `bot-app scheduler create '{"text":"requested work","at":"2030-01-01T09:00:00+10:00"}'`. The time needs an explicit timezone. An optional intervalMs repeats; missed intervals coalesce into one wake-up. Use `bot-app scheduler list '{}'` and `bot-app scheduler cancel '{"id":"..."}'`. The internal scheduler keeps delivering through the single durable queue across restarts; this app creates no detached timers. Cancellation cannot retract already queued work. Preserve the user's notification intent in the scheduled text.
