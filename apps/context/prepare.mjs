import { createContext } from './behavior.mjs';

const count = value => Number.isFinite(value) && value >= 0;
const format = value => String(Math.round(value));

export function usageNotice(messages, prepared, description) {
  // Count native assistant content once, rather than also counting its journal copy.
  const contents = prepared.map(message => {
    const native = message.metadata?.nativeAssistant;
    return native ? { role: message.role, content: native.content }
      : { role: message.role, content: message.content, toolCalls: message.toolCalls };
  });
  const estimate = Math.ceil(JSON.stringify(contents).length / 4);
  const window = description?.contextWindow;
  const last = messages.findLast(message => message.role === 'assistant' && message.usage);
  const usage = last?.usage;
  const input = usage && ['input', 'cacheRead', 'cacheWrite'].every(key => count(usage[key]))
    ? usage.input + usage.cacheRead + usage.cacheWrite : null;
  const previous = input === null ? 'Previous provider input: unavailable.'
    : `Previous recorded provider input: ${format(input)} tokens (including cached input)`
      + (last.metadata?.nativeAssistant?.model ? `, model ${last.metadata.nativeAssistant.model}` : '') + '.';
  const capacity = count(window) && window > 0
    ? `Model context window: ${format(window)} tokens. Estimated history usage: ${(estimate / window * 100).toFixed(1)}%.`
    : 'Model context window: unavailable.';
  return {
    id: 'context-usage', role: 'system', timestamp: 0,
    content: `Context usage snapshot: prepared history ~${estimate} tokens (JSON characters / 4; approximate, excludes system prompt and tool schemas). ${capacity} ${previous}`,
    metadata: { contextUsage: true },
  };
}

export async function prepareContext(api, input) {
  let description;
  const model = {
    ...input.model,
    describe: input.model.describe ? async settings => {
      description = await input.model.describe(settings);
      return description;
    } : undefined,
  };
  const result = await createContext(api)({ ...input, model });
  // Transient tail data keeps the history/cache prefix stable and never enters the journal.
  return { ...result, messages: [...result.messages, usageNotice(input.messages, result.messages, description)] };
}
