# Context plugin

This plugin automatically compacts older conversation near the model input budget.
It also provides `compact_conversation` so the agent can request compaction earlier.

The tool schedules compaction before the next model response. Its optional
`keepRecentGroups` argument defaults to 1; automatic compaction keeps 8 groups by
default. The current user turn and complete tool-call/result groups are always
retained. If nothing older can be summarized safely, the request is a no-op.

Compaction uses the configured model and can incur model charges. The full
conversation stays in the journal; only the model input is summarized. A failed
summary preserves both the existing summary and the compaction request for retry.
Disabling or replacing this plugin also removes or replaces the tool. No core API
or lifecycle changes are required.
