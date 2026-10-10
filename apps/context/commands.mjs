import { randomUUID } from 'node:crypto';

export function commands(api) {
  return {
    "reset": {
      description: 'Start fresh before the next model response: discard the summary and older working history, refresh saved memory, and retain the current request with its complete tool activity. Does not summarize or delete the journal, saved memory, or channel messages. Older evidence remains searchable.',
      inputSchema: { type: 'object', properties: {}, additionalProperties: false },
      execute: async () => {
        await api.state.set('resetRequest', { id: randomUUID() });
        return { status: 'requested', message: 'Working context will reset before the next model response. The current request and its tool activity stay intact; older records remain searchable.' };
      },
    },
    "compact": {
    description: 'Request a summary of older conversation before the next model response, even below the automatic threshold. Preserves the current user request and its tool activity, plus recent groups. Full history remains on disk. Cannot shrink the current turn itself.',
    inputSchema: { type: 'object', properties: {
      rebuild: { type: 'boolean', description: 'Rebuild from original journal evidence instead of merging the saved summary. Use to repair an inaccurate summary.' },
      keepRecentGroups: { type: 'integer', minimum: 1, maximum: 100, description: 'Recent complete message groups to retain (default 1). The entire current user turn is always retained.' },
    }, additionalProperties: false },
    execute: async ({ keepRecentGroups = 1, rebuild = false } = {}) => {
      await api.state.delete('summaryProgress');
      await api.state.set('compactionRequest', { keepRecentGroups, ...(rebuild ? { rebuild: true } : {}) });
      return { status: 'requested', message: 'Compaction will be attempted before the next model response. If there are no eligible older messages, context stays unchanged.' };
    },
  }
  };
}
