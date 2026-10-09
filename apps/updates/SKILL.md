---
name: updates
description: Replace the running conversation controller with recovery.
---

Ordinary apps are CLI programs. Edit, install or upgrade their working copies under
BOT_APPS (the historical drafts/apps directory), then run them. The next invocation
uses the current files. No mandatory validation, activation, probation or controller
restart is required. Choose tests appropriate to the change. Existing processes use
their loaded code until restarted. Preserve their data and credentials, and inspect
unknown external outcomes before retrying.

Use `bot-app updates apply '{}'` only to replace the running conversation controller
or its startup configuration. Inspect workspace/BOT-API.md first. This command
queues controller checks and replacement after the current turn; it does not
continue your work automatically. Arrange a genuine work continuation through the
scheduler when needed. The previous controller revision remains available for
recovery. Never send diagnostic prompts to the real conversation. The core has no
plugin loader or model-facing tool registration API. Core updates require the
operator installer.
