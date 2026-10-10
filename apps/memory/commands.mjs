import { readIndex, writeIndex, measure } from './index.mjs';

export function commands(api) {
  const empty = { type: 'object', properties: {}, additionalProperties: false };
  return {
    show: {
      description: 'Print the accepted Markdown index verbatim. Redirect to a working file, edit with the shell, then submit with write --stdin.',
      inputSchema: empty, outputFormat: 'text', execute: async () => (await readIndex(api)).markdown,
    },
    status: {
      description: 'Show index path, hash and estimated-token budget. maxIndexTokens defaults to 1000; estimates are ceil(UTF-8 bytes / 4), not a provider tokenizer.',
      inputSchema: empty, execute: async () => { const { markdown, ...status } = await readIndex(api); return status; },
    },
    check: {
      description: 'Measure proposed Markdown from --stdin without changing the accepted index. Returns fits and estimated tokens.',
      inputSchema: { type: 'string' }, inputFormat: 'text', execute: text => measure(api, text),
    },
    write: {
      description: 'Accept replacement Markdown from --stdin after checking its budget, saving atomically. Move details to linked notes when full. Rejected writes preserve the accepted index; accepted edits appear on the next model call.',
      inputSchema: { type: 'string' }, inputFormat: 'text', execute: text => writeIndex(api, text),
    },
    "search": {
    description: 'Search the full durable conversation, including messages omitted by context summaries. Searches message content and tool arguments. Matching is literal and case-insensitive; most recent matches come first.',
    inputSchema: {
      type: 'object', additionalProperties: false, required: ['query'],
      properties: { query: { type: 'string', minLength: 1, maxLength: 500 }, limit: { type: 'integer', minimum: 1, maximum: 50 } },
    },
    async execute({ query, limit = 10 }) {
      const needle = query.toLocaleLowerCase();
      const matches = [];
      for (const message of (await api.history()).reverse()) {
        const fields = { content: message.content ?? '', toolCalls: JSON.stringify(message.toolCalls ?? []) };
        const field = Object.keys(fields).find(key => fields[key].toLocaleLowerCase().includes(needle));
        if (!field) continue;
        const text = fields[field], at = text.toLocaleLowerCase().indexOf(needle);
        const offset = Math.max(0, at - 200), end = Math.min(text.length, offset + Math.min(2000, Math.floor(12000 / limit)));
        const { id, role, timestamp, toolCallId, toolName } = message;
        matches.push({ id, role, timestamp, toolCallId, toolName, field, offset, content: text.slice(offset, end),
          totalChars: text.length, truncated: offset > 0 || end < text.length });
        if (matches.length === limit) break;
      }
      return matches;
    },
  },
    "read": {
    description: 'Read an exact original journal record by ID, including tool arguments, results and metadata. Returns JSON text in bounded pages; follow nextOffset until null and concatenate data to reconstruct the record.',
    inputSchema: {
      type: 'object', additionalProperties: false, required: ['id'],
      properties: {
        id: { anyOf: [{ type: 'integer', minimum: 1 }, { type: 'string', minLength: 1 }] },
        offset: { type: 'integer', minimum: 0, description: 'UTF-16 character offset returned as nextOffset (default 0).' },
        limit: { type: 'integer', minimum: 1, maximum: 8000, description: 'Maximum characters per page (default 4000).' },
      },
    },
    async execute({ id, offset = 0, limit = 4000 }) {
      const message = (await api.history()).find(message => String(message.id) === String(id));
      if (!message) throw new Error(`Journal message ${id} not found`);
      const text = JSON.stringify(message);
      if (!Number.isInteger(offset) || offset < 0 || offset > text.length || !Number.isInteger(limit) || limit < 1 || limit > 8000) throw new Error('Invalid journal page bounds');
      const end = Math.min(text.length, offset + limit);
      return { id: message.id, format: 'json', offset, totalChars: text.length,
        nextOffset: end < text.length ? end : null, data: text.slice(offset, end) };
    },
  }
  };
}
