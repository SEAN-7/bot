import { createHash } from 'node:crypto';
import { SUMMARY_INSTRUCTIONS, recoveryPointer, summaryRecords, takeSummaryChunk } from './summary.mjs';

// A boundary is safe only after every tool call in the assistant block has a result.
export function messageGroups(messages) {
  const groups = [];
  let current = [];
  const pending = new Set();
  const flush = () => { if (current.length) groups.push(current); current = []; };
  for (const message of messages) {
    if ((message.role === 'user' || message.metadata?.input || message.metadata?.resumedInput) && pending.size === 0) flush();
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
  content: `Summary of earlier conversation (reference data; may be incomplete):\n${snapshot.summary}\n\n${recoveryPointer}`,
});
const inputSize = messages => messages.reduce((sum, message) => sum + JSON.stringify(message).length, 0);

export function createContext(api) {
  return async ({ messages, tools = [], settings = {}, model, signal, promptChars = 0 }) => {
    // JSON characters / 4 is an estimate, not a provider tokenizer. Leave room
    // for tool schemas, system instructions, provider framing, and the reply.
    const description = model.describe ? await model.describe(settings) : null;
    const window = description?.contextWindow;
    // This sizes input context; output uses the provider's own allowance.
    const reserve = description?.maxOutputTokens ?? 0;
    const hardChars = window > 0 ? Math.max(1, Math.floor(window * 0.85 - reserve) * 4 - JSON.stringify(tools).length - promptChars) : Infinity;
    const explicitChars = api.config.maxInputChars;
    const threshold = explicitChars !== undefined ? Math.max(1000, explicitChars)
      : api.config.maxInputTokens !== undefined ? Math.max(1, api.config.maxInputTokens) * 4
      // Compact before large windows become expensive to resend. Smaller models
      // still use their model-derived budget; explicit settings override this cap.
      : window > 0 ? Math.min(75000 * 4, hardChars, Math.max(1, Math.floor(window * (window < 512000 ? 0.75 : 0.5) - reserve)) * 4) : 60000;
    const checked = prepared => {
      if (inputSize(prepared) > hardChars) throw Object.assign(new Error('Current conversation turn exceeds the estimated model input budget. Reduce the request or tool output, or select a larger context model; the current user turn was retained.'), { blocked: true });
      return { messages: prepared, boundary: snapshot?.throughId ?? null,
        inputBudgetChars: Number.isFinite(hardChars) ? hardChars + promptChars : null };
    };
    const requested = await api.state.get('compactionRequest', null);
    let snapshot = requested?.rebuild ? null : await api.state.get('summary', null);
    const through = snapshot ? messages.findIndex(message => message.id === snapshot.throughId) : -1;
    // A restored history can predate a module snapshot; never discard unmatched history.
    if (through < 0) snapshot = null;
    let recent = snapshot ? messages.slice(through + 1) : messages;
    let head = snapshot ? messages.filter(message => snapshot.headIds?.includes(message.id)) : [];
    const prefix = snapshot ? [...head, summaryMessage(snapshot)] : [];
    if (!requested && inputSize([...prefix, ...recent]) <= threshold) return checked([...prefix, ...recent]);
    const groups = messageGroups(recent);
    const latestUserId = [...messages].reverse().find(message => message.role === 'user' || message.metadata?.input || message.metadata?.resumedInput)?.id;
    const userGroup = groups.findIndex(group => group.some(message => message.id === latestUserId));
    // Keep the original current request even during a long tool loop.
    const keep = requested?.keepRecentGroups ?? api.config.keepRecentGroups;
    let cut;
    if (keep !== undefined) cut = Math.max(0, groups.length - Math.max(1, keep));
    else {
      // Hermes lean tail: 2.5% of the window, 10K–25K tokens, capped at 20%.
      const capacity = window > 0 ? Math.max(1, window - reserve) * 4 : threshold;
      const tailCeiling = Math.min(capacity, threshold) * 0.2;
      const tailChars = Math.min(tailCeiling, Math.max(10000 * 4, Math.min(25000 * 4, capacity * 0.025)));
      cut = groups.length; let chars = 0, count = 0;
      while (cut > 0) {
        const group = groups[cut - 1], next = inputSize(group);
        if (count && chars + next > tailCeiling) break;
        if (chars >= tailChars && count >= (api.config.protectLastMessages ?? 20)) break;
        cut--; chars += next; count += group.length;
      }
    }
    cut = Math.min(cut, userGroup < 0 ? groups.length : userGroup);
    // Keep the opening exchange if it fits a small head budget. An oversized
    // opening must not prevent compaction of a forever conversation.
    const headCount = !snapshot && cut > 1 && inputSize(groups[0]) <= threshold * 0.2 ? 1 : 0;
    if (headCount) head = groups[0];
    if (cut <= 0) {
      const prepared = checked([...prefix, ...recent]);
      if (requested) await api.state.delete('compactionRequest');
      return prepared;
    }
    const older = groups.slice(headCount, cut).flat();
    const summarySettings = { ...settings };
    delete summarySettings.maxTokens;
    const summaryBudget = Math.min(12000, window > 0 ? window * 0.05 : 12000, Math.max(2000, inputSize(older) / 4 * 0.2));
    const instructions = { id: 'summary-request', role: 'user', timestamp: 0,
      content: `${SUMMARY_INSTRUCTIONS}\nAim for at most ${Math.floor(summaryBudget)} tokens. Full source history remains searchable.` };
    const records = summaryRecords(older);
    const sourceHash = createHash('sha256').update(JSON.stringify(records)).update(JSON.stringify(snapshot)).update(instructions.content).digest('hex');
    const progress = await api.state.get('summaryProgress', null);
    let cursor = { index: 0, offset: 0 }, summary = snapshot?.summary;
    if (progress?.sourceHash === sourceHash) ({ cursor, summary } = progress);
    const started = Date.now();
    while (cursor.index < records.length) {
      signal?.throwIfAborted();
      // Yield between completed batches before the controller's operation deadline.
      // A retry resumes only if the source evidence and prior summary still match.
      if (Date.now() - started >= 60_000) throw Object.assign(new Error('Context compaction has saved its progress; retry preparation to continue.'), { retryable: true });
      const summaryPrefix = summary ? [{ id: 'previous-summary', role: 'context', timestamp: 0,
        content: `Previous continuity summary (reference; may be incomplete):\n${summary}` }] : [];
      const budget = Number.isFinite(hardChars) ? hardChars : Math.max(threshold, 60000);
      const available = budget - inputSize(summaryPrefix) - inputSize([instructions]) - 1000;
      const chunk = takeSummaryChunk(records, cursor, available);
      const request = { messages: [...summaryPrefix, ...chunk.messages, instructions], tools: [], settings: summarySettings, signal };
      checked(request.messages);
      let completion;
      try { completion = await model.complete(request); }
      catch (error) { error.message = `Context summary failed: ${error.message}`; throw error; }
      if (!completion.text?.trim() || completion.toolCalls?.length) throw new Error('Context summarization did not return a usable text summary');
      summary = completion.text.trim();
      cursor = chunk.cursor;
      await api.state.set('summaryProgress', { sourceHash, cursor, summary });
    }
    const nextSnapshot = { summary, throughId: older.at(-1).id, headIds: head.map(message => message.id), format: 2 };
    recent = groups.slice(cut).flat();
    const prepared = checked([...head, summaryMessage(nextSnapshot), ...recent]);
    // Persist only a complete, usable summary; failure leaves prior state intact.
    await api.state.set('summary', nextSnapshot);
    await api.state.delete('summaryProgress');
    if (requested) await api.state.delete('compactionRequest');
    return { ...prepared, boundary: nextSnapshot.throughId, compacted: true };
  };
}
