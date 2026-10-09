# Skills and apps

Skills are enabled by default. Every bundled app has SKILL.md with single-line `name` and `description` frontmatter. The skill index advertises names, descriptions and paths, and the bot reads the matching guide through shell before using a capability.

Use `bot-app --help`, `bot-app skills list '{}'`, `cat /path/to/SKILL.md`, or `bot-app skills read '{"name":"chat"}'`. `bot-app APP --help` lists CLI arguments. All capabilities execute through the single shell tool.

Workspace workflow guides live at workspace/skills/<name>/SKILL.md and refresh without restart. Ordinary app programs live in drafts/apps/<id> and include app.json with an executable command, SKILL.md and program files. Enable config.apps[id] for core-bridge access and run the app; edits take effect on the next invocation. Testing is optional engineering work. Controller replacement uses the updates app. See [the contract](contract.md). Programs may also be existing system commands or ordinary workspace scripts.

Use `bot-app skills usage '{}'` for recorded provider usage and estimated costs. Context-summary calls are excluded. Missing metrics mean unavailable, not zero.
