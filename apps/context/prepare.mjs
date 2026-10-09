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
  const last = messages.findLast(message => message.usage);
  const usage = last?.usage;
  const input = usage && ['input', 'cacheRead', 'cacheWrite'].every(key => count(usage[key]))
    ? usage.input + usage.cacheRead + usage.cacheWrite : null;
  return {
    role: 'context',
    content: `History ~${estimate}/${count(window) && window > 0 ? format(window) : '?'} tokens; prior input ${input === null ? '?' : format(input)}.`,
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
  // The controller journals the observation before requesting a model response.
  // A provider retry with no new activity reuses the already recorded snapshot.
  return { ...result, append: input.messages.at(-1)?.metadata?.contextUsage
    ? [] : [usageNotice(input.messages, result.messages, description)] };
}
