---
name: terminal-chat
description: Attach the terminal frontend to the durable chat app.
---
The operator opens this app through `bot chat`, or exports ACP through `bot acp`. It owns presentation and the ACP host-operation filter. `config.clients` supplies the initial frontend commands at setup; afterward the operator controls selection through `control/clients.json`; `apps.terminal-chat.adapter` selects an argv transport command. Only the scoped client credential is provided by the core launcher.
