---
name: agent
description: The replaceable conversation controller and tool-only agent loop.
---

The protected runtime launches this self-contained executable with `--serve`. It exchanges bounded JSON lines using controller protocol 1; see BOT-API.md. Its only behavior dependencies are command hooks in `config.apps.agent`:

- `hooks.prepare`: optional argv command. Receives `{messages,tools}` as JSON on stdin and returns a model request. It may include `append` records for the controller to journal.
- `hooks.complete`: required argv command. Receives that request and returns `{text,toolCalls,metadata?,usage?}` as JSON.
- `services`: argv commands supervised while idle or working, restarted after exit. For example `["bot-app","scheduler","watch"]` delivers scheduled notifications independently of model work.

Diagnostics go to stderr. Commands can use any language. The default preparation command is `bot-app context prepare --stdin`; model completion is `bot-app model-provider complete --stdin`. Context, memory, projections and provider settings belong to those apps.

Controller `history` admits pending notifications. The controller rechecks history after preparation before starting a model call. Tool outcomes are saved before the next request. Plain assistant text is discarded; text-only responses are rejected and retried. With no tools requested, acknowledge delivery and stay idle; this does not classify the objective as complete. Human replies use communication apps through shell.

Controller revisions contain only this app directory, so keep its program dependencies inside the package or installed runtime. Other apps stay independently editable. Apply controller code or startup changes with `bot-app updates apply '{}'`; ordinary app changes need no apply.
