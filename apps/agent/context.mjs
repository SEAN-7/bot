// Prompt policy belongs to the replaceable controller, not the durable core.
export async function prepareSnapshot({ options, tools, settings, rpc, invoke, state }) {
  // Admit pending notifications before invoking apps, then read again afterward.
  await rpc('history');
  let snapshot = await state.get('promptSnapshot', null);
  const collected = new Map();
  for (const app of options.contributors || []) {
    let items;
    try { items = await invoke(app, 'contribute', {}); }
    catch (error) {
      // A frozen prompt remains usable if its app is temporarily unavailable.
      if (!snapshot?.contributors[app]) console.error(`Contributor ${app} failed: ${error.message}`);
      continue;
    }
    if (!Array.isArray(items) || items.some(item => typeof item.key !== 'string' || typeof item.content !== 'string')) {
      throw new Error('Invalid context contributions from ' + app);
    }
    collected.set(app, items.filter(item => item.placement === 'system'));
    for (const item of items.filter(item => item.placement !== 'system')) await rpc('contribute', { module: app, ...item });
  }
  let messages = await rpc('history');
  const latest = messages.findLast(m => m.role === 'user' || m.metadata?.resumedInput);
  // Preserve legacy memory records on disk without replaying obsolete snapshots.
  const promptApps = new Set([...collected].filter(([, items]) => items.length).map(([app]) => app));
  for (const app of options.contributors || []) if (snapshot?.contributors[app]?.length) promptApps.add(app);
  const legacyPromptApps = new Set([...promptApps, ...Object.keys(snapshot?.contributors || {})]);
  messages = messages.filter(message => !legacyPromptApps.has(message.metadata?.module));
  for (const app of options.projections || []) messages = await invoke(app, 'project', { messages });
  let prepared = { messages, boundary: null };
  if (options.contextApp) prepared = await invoke(options.contextApp, 'prepare', { messages, tools, settings, modelApp: options.modelApp,
    // Account for prompt memory in the context budget without summarizing it.
    promptChars: Object.values(snapshot?.contributors || Object.fromEntries(collected)).flat().reduce((sum, item) => sum + item.content.length, 0) });
  const boundary = prepared.boundary ?? null;
  if (!snapshot || snapshot.boundary !== boundary || [...promptApps].some(app => !snapshot.contributors[app])) {
    const contributors = {};
    for (const app of promptApps) {
      // Reload after the summary call, so writes during compaction are visible.
      const items = snapshot || prepared.compacted ? await invoke(app, 'contribute', {}) : collected.get(app);
      if (!Array.isArray(items) || items.some(item => typeof item.content !== 'string')) throw new Error('Invalid prompt snapshot from ' + app);
      contributors[app] = items.filter(item => item.placement === 'system');
    }
    snapshot = { boundary, contributors };
    await state.set('promptSnapshot', snapshot);
  }
  messages = prepared.messages;
  for (const message of prepared.append || []) messages.push(await rpc('append', { message }));
  if (!Array.isArray(messages) || messages.some(m => typeof m.content !== 'string')) throw new Error('Invalid prepared context');
  if (latest && !messages.some(m => m.id === latest.id)) throw new Error('Context removed the current input');
  const systemContext = (options.contributors || []).flatMap(app => snapshot.contributors[app] || []).map(item => item.content).filter(Boolean).join('\n\n');
  if (prepared.inputBudgetChars && systemContext.length + messages.reduce((sum, message) => sum + JSON.stringify(message).length, 0) > prepared.inputBudgetChars) {
    throw Object.assign(new Error('Prepared history and refreshed saved memory exceed the model input budget; data was retained.'), { blocked: true });
  }
  return { messages, tools, settings, systemContext, latestInputId: latest?.id };
}
