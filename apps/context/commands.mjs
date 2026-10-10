export function commands(api) {
  return {
    "compact": {
    description: 'Request a summary of older conversation before the next model response, even below the automatic threshold. Preserves the current user request and its tool activity, plus recent groups. Full history remains on disk. Cannot shrink the current turn itself.',
    inputSchema: { type: 'object', properties: {
      keepRecentGroups: { type: 'integer', minimum: 1, maximum: 100, description: 'Recent complete message groups to retain (default 1). The entire current user turn is always retained.' },
    }, additionalProperties: false },
    execute: async ({ keepRecentGroups = 1 } = {}) => {
      await api.state.set('compactionRequest', { keepRecentGroups });
      return { status: 'requested', message: 'Compaction will be attempted before the next model response. If there are no eligible older messages, context stays unchanged.' };
    },
  }
  };
}
