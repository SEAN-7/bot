# Adopting the 0.6 app layout

The 0.6 defaults use controller protocol 2 and executable command hooks and self-contained controller
revisions. Ordinary apps keep their own code and state through controller rollback.
Core updates still preserve editable app copies. Existing installations need an
explicit adoption of the related default apps before staging this release.

Stop at an idle boundary and retain the previous installed release, controller
revision, editable drafts, client command configuration and a consistent database
backup. Compare editable files with the previously installed upstream sources.
Merge local changes; never replace customized apps wholesale.

Adopt the new `agent`, `context`, `scheduler`, `chat` and `_lib` implementations,
plus the new `setup` and `terminal-chat` packages. Preserve all unrelated apps,
channel databases, credentials and workspace files. The controller package must
be self-contained: its revision no longer includes sibling apps or `_lib`.

Run the new setup app's explicit conversion while the service is stopped:

```sh
node /path/to/new-release/app/apps/setup/main.mjs migrate '{}' /path/to/bot-home
```

This command changes configuration only. It translates the default controller's
model/context selection into `hooks.prepare` and `hooks.complete` argv commands,
moves contributors and projections to the context app, and turns service/tick
app IDs into supervised watch commands. It preserves custom provider and context
settings and can be repeated safely. Custom controllers must adopt protocol 2 and declare `controllerProtocol:2` in startup configuration; protocol 2 removes core-driven ticks. Their other command conventions remain their own.

The operator must review and install the resulting `clients` mapping as
`control/clients.json`. Client selection is protected operator configuration;
editing draft configuration or applying a controller cannot change it. Commands
are relative to the installed app root. The default `terminal-chat` command owns
both the frontend and its ACP host-operation filter.

If the old controller has an `app:agent` / `promptSnapshot` state entry, copy it
to the selected context app's state namespace before startup, preserving the old
entry for rollback. Use the configured `stateNamespace` where present. This
retains the frozen memory boundary until the next successful compaction; all
other ordinary app state stays in place. The scheduler imports its old core-backed
schedule list once into `workspace/scheduler/scheduler.sqlite`; concurrent watch
and CLI commands then use app-owned transactions. When downgrading, reconcile
that list before starting the old scheduler so delivered slots are not replayed.

Build and validate a Linux bundle, then use its managed `update --yes` command.
Validate conversation behavior only in a temporary isolated bot home. Production
verification checks service health, pending/error/trial state, retained history,
credentials and the previous release without injecting conversation messages.
An older protocol-1 controller requires its retained old executable; the new core rejects a controller-only rollback across that boundary. If adoption or startup fails, restore the previous release and the backed-up
editable configuration/programs; retain all durable conversation and app data.
