---
name: model-provider
description: Model access for the conversation controller.
---

This app sends conversation context and available tools to the configured model
provider and returns its response to the controller. It is an internal model
adapter, not a coding agent or a task delegation capability.

Replies and context summaries use the provider's available output allowance.
The adapter sends no custom output-token cap; legacy maxTokens and summaryTokens
settings do not limit generation. Provider and context-window limits still apply.

Run `bot-app model-provider --help` to inspect its CLI. Provider credentials are
inherited from the process environment. Ordinary CLI programs can use inherited
provider credentials when authorized; do not print or copy their values.
