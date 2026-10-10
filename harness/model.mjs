import { builtinModels } from '@earendil-works/pi-ai/providers/all';

export function createModel(config, collection = builtinModels()) {
  const model = collection.getModel(config.provider, config.model);
  if (!model) throw new Error('Unknown host-configured provider/model');
  const maxTokens = model.maxTokens;
  const rates = [model.cost, ...(model.cost.tiers ?? [])];
  // Reserve at the most expensive input/cache and output tier for a full window.
  // No retries, agent-selected models, credentials or model options cross the boundary.
  const inputRate = Math.max(...rates.flatMap(rate => [rate.input * 2, rate.cacheRead, rate.cacheWrite]));
  const outputRate = Math.max(...rates.map(rate => rate.output));
  const reservation = (model.contextWindow * inputRate + maxTokens * outputRate) / 1e6;
  if (!Number.isFinite(reservation) || reservation <= 0) throw new Error('Missing model pricing; configure a priced model before enabling spending.');
  return {
    contextWindow: model.contextWindow, maxTokens, reservation,
    complete: (context, signal) => collection.completeSimple(model, context, { signal, maxTokens, maxRetries: 0, timeoutMs: 120000, cacheRetention: 'short' }),
  };
}
