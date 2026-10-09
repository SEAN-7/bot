---
name: skills
description: Discover apps and workflow skills, inspect capabilities and author new app packages.
---

Run `bot-app skills list '{}'` for skill names, descriptions and paths. Read a guide with `cat /absolute/path/SKILL.md` or `bot-app skills read '{"name":"chat"}'`. Run `bot-app APP --help` for arguments. Programs return JSON on stdout and failures on stderr with nonzero exit codes. Add ordinary programs in workspace, and pair them with workspace/skills/<name>/SKILL.md using single-line name and description frontmatter. For an ordinary app program, put app.json (id, version, command: an argv array, description), program files and SKILL.md under drafts/apps/<id>; enable config.apps[id] for core-bridge access and run it. Edits and upgrades take effect on the next invocation, with no mandatory validation, activation or probation. Test when useful. Only replacing the running controller or its startup configuration uses bot-app updates apply. See BOT-API.md for the app CLI contract. The core has no plugin loader or tool registration API. Shell uses command OR sessionId: poll, send chars, endInput, or terminate through the same shell tool. Commands are supervised and stop on cancellation/restart. Inspect uncertain side effects before replay. Do not detach background services.

Use `bot-app skills usage '{}'` to inspect recorded token usage and estimated costs without direct journal access. Context-summary model calls are not included. Missing metrics mean unavailable.
