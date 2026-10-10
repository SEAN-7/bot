import { messageGroups } from './behavior.mjs';

// Persist the cut before clearing derived state. A restart between writes resumes
// the same reset rather than moving the boundary past newly arrived work.
export async function applyReset(state, messages) {
  const requested = await state.get('resetRequest', null);
  let boundary = await state.get('resetBoundary', null);
  if (requested) {
    if (boundary?.id !== requested.id) {
      const latest = messages.findLast(m => m.role === 'user' || m.metadata?.input || m.metadata?.resumedInput);
      const groups = messageGroups(messages);
      const current = latest ? groups.findIndex(group => group.some(m => m.id === latest.id)) : groups.length - 1;
      const older = groups.slice(0, Math.max(0, current)).flat();
      boundary = { id: requested.id, afterId: older.at(-1)?.id ?? null };
      await state.set('resetBoundary', boundary);
    }
    for (const key of ['summary', 'summaryProgress', 'compactionRequest', 'promptSnapshot']) await state.delete(key);
    await state.delete('resetRequest');
  }
  return boundary;
}

export function resetHistory(messages, boundary) {
  if (boundary?.afterId == null) return messages;
  const through = messages.findIndex(m => m.id === boundary.afterId);
  if (through < 0) throw Object.assign(new Error('Context reset boundary is missing from the journal; restore matching context state before continuing.'), { blocked: true });
  return messages.slice(through + 1);
}

export function resetNotice(boundary) {
  return { id: `reset:${boundary.id}`, role: 'context', timestamp: 0,
    content: `Working context was reset. Earlier conversation is omitted, not deleted or revoked. Recover original requests, permissions and outcomes with bot-app memory search and bot-app memory read, or the relevant communication app. Do not repeat external actions merely because their results are absent here.` };
}
