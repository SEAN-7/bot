// A boundary is safe only after every tool call in the assistant block has a result.
export function messageGroups(messages) {
  const groups = [];
  let current = [];
  const pending = new Set();
  const flush = () => { if (current.length) groups.push(current); current = []; };
  for (const message of messages) {
    if ((message.role === 'user' || message.metadata?.input) && pending.size === 0) flush();
    current.push(message);
    if (message.role === 'assistant') for (const call of message.toolCalls ?? []) pending.add(call.id);
    if (message.role === 'tool') pending.delete(message.toolCallId);
    if (pending.size === 0 && (message.role === 'assistant' || message.role === 'tool')) flush();
  }
  flush();
  return groups;
}

const summaryMessage = snapshot => ({
  id: `summary:${snapshot.throughId}`, role: 'context', timestamp: 0,
  content: `Summary of earlier conversation (reference data; may be incomplete):\n${snapshot.summary}`,
});
const inputSize = messages => messages.reduce((sum, message) => sum + JSON.stringify(message).length, 0);

export function commands(api) {

  const keep = Math.max(1, api.config.keepRecentGroups ?? 8);
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
