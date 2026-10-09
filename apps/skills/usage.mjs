import { DatabaseSync } from 'node:sqlite';
import { resolve } from 'node:path';

export function summarizeUsage(messages) {
  const fields = ['input', 'output', 'cacheRead', 'cacheWrite', 'totalTokens', 'reasoning', 'cacheWrite1h'];
  const totals = {}, reported = {}, cost = {}, costReported = {};
  let responses = 0, withUsage = 0, latest = null;
  for (const message of messages) {
    if (message.role !== 'assistant' && !message.metadata?.modelObservation) continue;
    responses++;
    const usage = message.usage;
    if (!usage || typeof usage !== 'object') continue;
    withUsage++;
    const selected = {};
    for (const field of fields) if (Number.isFinite(usage[field]) && usage[field] >= 0) {
      totals[field] = (totals[field] ?? 0) + usage[field];
      reported[field] = (reported[field] ?? 0) + 1;
      selected[field] = usage[field];
    }
    const latestCost = {};
    for (const field of ['input', 'output', 'cacheRead', 'cacheWrite', 'total']) if (Number.isFinite(usage.cost?.[field]) && usage.cost[field] >= 0) {
      cost[field] = (cost[field] ?? 0) + usage.cost[field];
      costReported[field] = (costReported[field] ?? 0) + 1;
      latestCost[field] = usage.cost[field];
    }
    latest = { id: message.id, timestamp: message.timestamp, usage: { ...selected, cost: latestCost } };
  }
  return { scope: 'saved model responses only; excludes unsaved requests and context-summary calls', responses, withUsage,
    withoutUsage: responses - withUsage, totals, reported, estimatedCost: cost, costReported, latest };
}

export function readUsage(home) {
  const db = new DatabaseSync(resolve(home, 'control/journal.sqlite'), { readOnly: true });
  try {
    const messages = db.prepare('SELECT id, data FROM messages ORDER BY id').iterate();
    return summarizeUsage((function* () { for (const row of messages) yield { ...JSON.parse(row.data), id: row.id }; })());
  } finally { db.close(); }
}

import { fileURLToPath } from 'node:url';
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { console.log(JSON.stringify(readUsage(resolve(process.argv[2] ?? '..')), null, 2)); }
  catch (error) { console.error(`Cannot read saved usage: ${error.message}. See bot-docs for sandbox limitations.`); process.exitCode = 1; }
}
