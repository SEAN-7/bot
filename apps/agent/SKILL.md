---
name: agent
description: The replaceable conversation controller, context assembly and agent loop.
---

This is an ordinary service process launched as `bot-app agent --serve` by the protected runtime. It exchanges bounded JSON lines over stdin/stdout using protocol 1. See BOT-API.md for core requests. It delegates model, context, projections, contributions and idle work to CLI apps configured in `config.apps.agent`. It can be replaced by an executable in any language. The core keeps one active event, owns the journal and executes only its fixed shell tool. Configure the controller ID with `config.controller`.

`services` lists background apps whose `watch` command runs while idle or working.
The controller supervises these ordinary CLI processes and restarts them after
exit. They enqueue durable reference notifications; communication apps own their
message and delivery state in workspace. Add an app to `projections` when its
notifications should be presented as context rather than human messages.

Controller `history` admits pending notifications during ongoing work. Prepare
context before each model call; recheck after slow projections or compaction.
Tool results are saved as soon as their supervised action returns and drive
the next model call. Plain assistant text is discarded. With no requested tools
and no new notifications, acknowledge delivery and stay idle; this does not
classify the objective as complete. There is no wait tool or tool-step limit.
Assistant protocol records retain tool calls and reasoning, not human replies.
