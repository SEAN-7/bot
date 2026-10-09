# Applications

All replaceable behavior is an ordinary executable application paired with a skill. Model access, context assembly, memory, scheduling, skills and chat run in separate processes. The conversation controller is replaceable too. The core exposes one fixed shell tool and has no plugin loader or registration API.

Read [the process application contract](contract.md) for executable manifests, the core CLI, controller protocol and recovery. See [skills](skills.md) for discovery and the [local-time example](../examples/local-time) for a packaged app. Bundled JavaScript apps share optional helper code; new apps may use any CLI and language.

Ordinary app programs are editable under drafts/apps and take effect on their next invocation, like third-party CLI tools. They require no mandatory validation or activation. `bot-app updates apply '{}'` replaces only the running controller and its startup configuration, with recovery. Core changes remain operator-owned.
