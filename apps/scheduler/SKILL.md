---
name: scheduler
description: Create, inspect or cancel durable future wake-ups in the same conversation.
---

Run `bot-app scheduler create '{"text":"requested work","at":"2030-01-01T09:00:00+10:00"}'`. The time needs an explicit timezone. An optional intervalMs repeats; missed intervals coalesce into one wake-up. Use `bot-app scheduler list '{}'` and `bot-app scheduler cancel '{"id":"..."}'`. The supervised `bot-app scheduler watch` service checks due work once a second, including while the controller is busy. Add `["bot-app","scheduler","watch"]` to the controller service commands. It retains schedules through restarts and enqueues stable event IDs into the durable queue. Cancellation cannot retract already queued work. Preserve the user's notification intent in the scheduled text.

Schedules live in `workspace/scheduler/scheduler.sqlite`. Each watch/create/cancel operation holds an app-owned transaction so concurrent commands cannot lose schedule changes. Existing core-backed schedules are imported once on first use. Their original state remains available for rollback; after downgrading, reconcile schedules from the newer database before starting the old scheduler to avoid replaying completed slots.
