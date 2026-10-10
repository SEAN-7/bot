import { summaryEvidence } from './helpers/summary.mjs';
import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { prepareSnapshot } from '../apps/context/request.mjs';
import { contribute } from '../apps/memory/behavior.mjs';
import { commands as memoryCommands } from '../apps/memory/commands.mjs';
import { writeIndex } from '../apps/memory/index.mjs';
import { createContext } from '../apps/context/behavior.mjs';
import { summaryRecords, takeSummaryChunk } from '../apps/context/summary.mjs';
import { createModel } from '../apps/model-provider/behavior.mjs';
import { Store } from '../src/store.mjs';
import { initialize } from '../src/releases.mjs';
import { createConfig } from '../apps/setup/config.mjs';
import { startSupervisor } from '../src/supervisor.mjs';

function stateFixture(config = {}) {
  const saved = new Map();
  return { config, saved, state: {
    get: async (key, fallback) => structuredClone(saved.has(key) ? saved.get(key) : fallback),
    set: async (key, value) => saved.set(key, structuredClone(value)),
    delete: async key => saved.delete(key),
  } };
}

test('live index refreshes without compaction, survives restart and preserves unrelated frozen contributors', async t => {
  const workspace = await mkdtemp(join(tmpdir(), 'bot-index-context-'));
  t.after(() => rm(workspace, { recursive: true, force: true }));
  const state = stateFixture(), memory = { ...stateFixture(), paths: { workspace } };
  await writeIndex(memory, 'original preference');
  let boundary = null, fail = false, unavailable = false, other = 'frozen contribution';
  const journal = [{ id: 1, role: 'context', content: 'obsolete snapshot', metadata: { module: 'memory' } }, { id: 2, role: 'user', content: 'Human request' }];
  // Upgrade from an existing frozen memory snapshot without losing history.
  state.saved.set('promptSnapshot', { boundary: null, contributors: { memory: [{ key: 'old', placement: 'system', content: 'stale legacy snapshot' }] } });
  const input = { options: { contributors: ['memory', 'other'] }, tools: [], settings: {}, state: state.state,
    rpc: async method => { assert.equal(method, 'history'); return structuredClone(journal); },
    invoke: async app => {
      if (app === 'other') return [{ key: other, placement: 'system', content: other }];
      if (unavailable) throw new Error('memory unavailable');
      return contribute(memory);
    },
    prepare: async request => {
      if (fail) throw new Error('summary failed');
      assert.ok(!request.messages.some(message => message.content === 'obsolete snapshot'));
      return { messages: request.messages, boundary };
    } };
  const first = await prepareSnapshot(input);
  assert.match(first.systemContext, /original preference/);
  assert.doesNotMatch(first.systemContext, /stale legacy snapshot/);
  await writeIndex(memory, 'new preference'); other = 'later contribution';
  const updated = await prepareSnapshot(input);
  assert.match(updated.systemContext, /new preference/);
  assert.match(updated.systemContext, /frozen contribution/);
  assert.doesNotMatch(updated.systemContext, /original preference|later contribution/);
  assert.match((await prepareSnapshot({ ...input, state: { ...state.state } })).systemContext, /new preference/);
  unavailable = true;
  assert.match((await prepareSnapshot(input)).systemContext, /new preference/);
  unavailable = false; fail = true;
  await assert.rejects(prepareSnapshot(input), /summary failed/);
  assert.equal(state.saved.get('promptSnapshot').boundary, null);
  fail = false; boundary = 10;
  assert.match((await prepareSnapshot(input)).systemContext, /later contribution/);
  await writeIndex(memory, '');
  assert.doesNotMatch((await prepareSnapshot(input)).systemContext, /new preference/);
  assert.equal((await prepareSnapshot({ ...input, options: { ...input.options, contributors: [] } })).systemContext, '');
  assert.equal(journal[0].content, 'obsolete snapshot');
});

test('model provider sends saved memory in the actual system prompt, separately from messages', async () => {
  let captured;
  const model = createModel({ provider: 'fixture', model: 'fixture', systemPrompt: 'Base policy' }, {
    getModel: () => ({ id: 'fixture', provider: 'fixture' }),
    completeSimple: async (_model, request) => { captured = request; return { stopReason: 'stop', content: [] }; },
  });
  await model.complete({ messages: [{ role: 'user', content: 'Human request' }], tools: [], systemContext: 'Saved reference memory' });
  assert.equal(captured.systemPrompt, 'Base policy\n\nSaved reference memory');
  assert.equal(captured.messages.length, 1);
  assert.equal(captured.messages[0].content, 'Human request');
});

test('compaction preserves a bounded opening, all older tool evidence and exact active tools', async () => {
  const api = stateFixture({ maxInputChars: 10000, keepRecentGroups: 1 });
  api.saved.set('compactionRequest', { keepRecentGroups: 1 });
  const messages = [
    { id: 1, role: 'user', content: 'Opening request' }, { id: 2, role: 'assistant', content: 'Opening answer' },
    { id: 3, role: 'user', content: 'Inspect /opt/project/report.go for PR #123 at abc123def' },
    { id: 4, role: 'assistant', content: '', toolCalls: [{ id: 'old-tool', name: 'shell', arguments: { command: 'cat /opt/project/report.go' } }] },
    { id: 5, role: 'tool', toolCallId: 'old-tool', toolName: 'shell', content: 'Verbose old output '.repeat(100) },
    { id: 6, role: 'user', content: 'Current request' },
    { id: 7, role: 'assistant', content: '', toolCalls: [{ id: 'active-tool', name: 'shell', arguments: {} }] },
    { id: 8, role: 'tool', toolCallId: 'active-tool', content: 'Active output '.repeat(100) },
  ];
  const original = structuredClone(messages), requests = [];
  const model = { complete: async request => { requests.push(request); return { text: '## Goal\nReview report\n## Progress\nDone inspecting.', toolCalls: [] }; } };
  const prepare = createContext(api);
  const first = await prepare({ messages, model });
  assert.deepEqual(first.messages.slice(0, 2), messages.slice(0, 2));
  assert.deepEqual(first.messages.slice(3), messages.slice(5));
  assert.match(first.messages[2].content, /bot-app memory search/);
  assert.deepEqual(summaryEvidence(requests[0]), messages.slice(2, 5));
  assert.match(requests[0].messages.at(-1).content, /Latest Unresolved Request/);
  assert.match(requests[0].messages.at(-1).content, /Session Log/);
  assert.deepEqual(requests[0].tools, []);
  assert.equal(first.compacted, true);
  api.saved.set('compactionRequest', { keepRecentGroups: 1 });
  const next = await prepare({ messages: [...messages, { id: 9, role: 'user', content: 'Next task' }], model });
  assert.match(requests[1].messages[0].content, /Done inspecting/);
  assert.equal(next.boundary, 8);
  assert.deepEqual(next.messages.slice(0, 2), messages.slice(0, 2));
  assert.deepEqual(messages, original);
});

test('summary failures retain the prior summary and request, including unknown tool outcomes', async () => {
  const api = stateFixture({ maxInputChars: 1000, keepRecentGroups: 1 });
  const previous = { summary: 'Prior summary', throughId: 1 };
  api.saved.set('summary', previous);
  api.saved.set('compactionRequest', { keepRecentGroups: 1 });
  const messages = [{ id: 1, role: 'user', content: 'Original' }, { id: 2, role: 'user', content: 'Older work' },
    { id: 3, role: 'assistant', content: '', toolCalls: [{ id: 'x', name: 'shell', arguments: {} }] },
    { id: 4, role: 'tool', toolCallId: 'x', content: 'UNKNOWN interrupted external action '.repeat(100), isError: true },
    { id: 5, role: 'user', content: 'Current' }];
  await assert.rejects(createContext(api)({ messages, model: { complete: async request => {
    assert.equal(summaryEvidence(request).find(message => message.id === 4).content, messages[3].content);
    return { text: '', toolCalls: [] };
  } } }), /usable text summary/);
  assert.deepEqual(api.saved.get('summary'), previous);
  assert.ok(api.saved.has('compactionRequest'));
});

test('oversized tool results and arguments are fully covered across bounded summary batches', async () => {
  const api = stateFixture({ maxInputChars: 1000, keepRecentGroups: 1 });
  const incoming = JSON.stringify({ messages: [{ id: 243, sender: 'human', text: 'Build a database of Australian radiology companies, using LSPNs as the inbox.' },
    { id: 245, sender: 'human', text: 'Yes, create the company and site tables; commit and push.' }] });
  const messages = [
    { id: 1, role: 'context', content: 'Incoming communication message', metadata: { input: true } },
    { id: 2, role: 'assistant', content: '', toolCalls: [{ id: 'read', name: 'shell', arguments: { command: 'bot-app example read ' + 'a'.repeat(12000) + 'END_ARGUMENT' } }],
      metadata: { nativeAssistant: { content: [{ type: 'thinking', thinking: 'PRIVATE_REASONING' }] } } },
    { id: 3, role: 'tool', toolCallId: 'read', content: JSON.stringify({ stdout: 'x'.repeat(18000) + incoming + 'y'.repeat(18000), exitCode: 0 }) },
    { id: 4, role: 'context', content: 'Command completed', metadata: { input: true } },
  ];
  const original = structuredClone(messages), requests = [];
  const model = { describe: () => ({ contextWindow: 4000, maxOutputTokens: 500 }), complete: async request => {
    assert.ok(request.messages.reduce((n, m) => n + JSON.stringify(m).length, 0) < (4000 * 0.85 - 500) * 4);
    assert.ok(!JSON.stringify(request).includes('PRIVATE_REASONING'));
    if (requests.length) assert.match(request.messages[0].content, /retained summary/);
    requests.push(request); return { text: 'retained summary', toolCalls: [] };
  } };
  const result = await createContext(api)({ messages, model });
  assert.ok(requests.length > 2);
  const evidence = summaryEvidence(requests);
  assert.deepEqual(evidence, messages.slice(0, 3).map(m => m.id === 2 ? { ...m, metadata: {} } : m));
  assert.match(evidence[2].content, /Australian radiology/);
  assert.deepEqual(result.messages.slice(1), messages.slice(3));
  assert.equal(api.saved.get('summary').throughId, 3);
  assert.deepEqual(messages, original);
  // A failure after an earlier successful batch must not advance the boundary.
  api.saved.set('compactionRequest', { rebuild: true, keepRecentGroups: 1 });
  const saved = structuredClone(api.saved.get('summary')); let calls = 0;
  await assert.rejects(createContext(api)({ messages, model: { ...model, complete: async () => {
    if (++calls === 2) throw new Error('later batch failed');
    return { text: 'partial summary' };
  } } }), /later batch failed/);
  assert.deepEqual(api.saved.get('summary'), saved);
  assert.equal(api.saved.get('compactionRequest').rebuild, true);
});

test('long compaction yields with a checkpoint and resumes after restart without skipping evidence', async t => {
  const api = stateFixture({ maxInputChars: 1000, keepRecentGroups: 1 });
  const messages = [{ id: 1, role: 'user', content: 'a'.repeat(30000) },
    { id: 2, role: 'assistant', content: 'done' }, { id: 3, role: 'user', content: 'Current request' }];
  let now = 0, pause = true;
  t.mock.method(Date, 'now', () => now);
  const requests = [];
  const model = { describe: () => ({ contextWindow: 4000, maxOutputTokens: 500 }), complete: async request => {
    requests.push(request); if (pause) now += 61000;
    return { text: 'Checkpoint summary' };
  } };
  await assert.rejects(createContext(api)({ messages, model }), error => error.retryable && /saved its progress/.test(error.message));
  assert.equal(api.saved.has('summary'), false);
  assert.ok(api.saved.get('summaryProgress').cursor.offset > 0);
  pause = false;
  const prepared = await createContext({ ...api })({ messages, model });
  assert.deepEqual(summaryEvidence(requests), messages.slice(0, 2));
  assert.equal(prepared.boundary, 2);
  assert.equal(api.saved.has('summaryProgress'), false);

  // A different history must never reuse a checkpoint for the old evidence.
  api.saved.delete('summary'); requests.length = 0; pause = true;
  await assert.rejects(createContext(api)({ messages, model }), /saved its progress/);
  pause = false; requests.length = 0;
  const changed = structuredClone(messages); changed[0].content = 'b'.repeat(30000);
  await createContext(api)({ messages: changed, model });
  assert.deepEqual(summaryEvidence(requests), changed.slice(0, 2));
});

test('summary chunks preserve escape-heavy and non-BMP text without skipping or splitting characters', () => {
  const messages = [{ id: 8, role: 'tool', content: '\\"\n😀'.repeat(2000) }];
  const records = summaryRecords(messages), requests = [];
  let cursor = { index: 0, offset: 0 };
  while (cursor.index < records.length) {
    const chunk = takeSummaryChunk(records, cursor, 500);
    assert.ok(chunk.messages.reduce((n, m) => n + JSON.stringify(m).length, 0) <= 500);
    requests.push(chunk); cursor = chunk.cursor;
  }
  assert.deepEqual(summaryEvidence(requests), messages);
  assert.throws(() => takeSummaryChunk(records, { index: 0, offset: 0 }, 10), /budget cannot fit/);
});

test('rebuild ignores a contaminated summary and recovers tool-only human messages from the journal', async () => {
  const api = stateFixture({ maxInputChars: 10000, keepRecentGroups: 1 });
  const messages = [{ id: 1, role: 'context', content: 'Incoming notification', metadata: { input: true } },
    { id: 2, role: 'assistant', content: '', toolCalls: [{ id: 'read', name: 'shell', arguments: { command: 'bot-app telegram read' } }] },
    { id: 3, role: 'tool', toolCallId: 'read', content: JSON.stringify({ stdout: JSON.stringify({ sender: 'human', text: 'Yes, build the database. Commit and push.' }) }) },
    { id: 4, role: 'context', content: 'Current notification', metadata: { input: true } }];
  api.saved.set('summary', { throughId: 3, summary: 'No database was requested and no permission was given.' });
  api.saved.set('compactionRequest', { rebuild: true, keepRecentGroups: 1 });
  const result = await createContext(api)({ messages, model: { complete: async request => {
    assert.doesNotMatch(JSON.stringify(request), /No database was requested/);
    assert.deepEqual(summaryEvidence(request), messages.slice(0, 3));
    return { text: 'Database requested; commit and push authorized (journal 3).' };
  } } });
  assert.match(result.messages[0].content, /Database requested/);
  assert.equal(result.boundary, 3);
  assert.equal(api.saved.has('compactionRequest'), false);
});

test('journal search finds tool arguments and exact paginated reads recover the entire original record', async () => {
  const messages = [{ id: 11, role: 'assistant', content: '', toolCalls: [{ id: 'x', name: 'shell', arguments: { command: 'sqlite3 radiology.sqlite ' + '😀\n'.repeat(9000) } }] },
    { id: 12, role: 'tool', toolCallId: 'x', content: 'prefix '.repeat(1000) + 'approved company schema' + ' suffix'.repeat(1000) }];
  const api = { ...stateFixture(), history: async () => structuredClone(messages) }, commands = memoryCommands(api);
  const [match] = await commands.search.execute({ query: 'RADIOLOGY.SQLITE' });
  assert.equal(match.id, 11); assert.equal(match.field, 'toolCalls'); assert.equal(match.truncated, true);
  const [result] = await commands.search.execute({ query: 'approved' });
  assert.equal(result.id, 12); assert.ok(result.offset > 0); assert.equal(result.truncated, true);
  let offset = 0, text = '';
  do {
    const page = await commands.read.execute({ id: String(match.id), offset, limit: 1000 });
    assert.equal(page.offset, offset); assert.ok(page.data.length <= 1000);
    text += page.data; offset = page.nextOffset;
  } while (offset !== null);
  assert.deepEqual(JSON.parse(text), messages[0]);
  await assert.rejects(commands.read.execute({ id: 999 }), /not found/);
  await assert.rejects(commands.read.execute({ id: 11, offset: 1000000 }), /bounds/);
});

test('isolated bot accepts raw Markdown, refreshes on the next model call, compacts and reloads after restart', { timeout: 45000 }, async t => {
  const home = await mkdtemp(join(tmpdir(), 'bot-memory-context-'));
  let server;
  t.after(async () => { await server?.stop(); await rm(home, { recursive: true, force: true }); });
  const config = createConfig({ disable: 'chat,skills,scheduler,web-search' });
  config.apps.context.keepRecentGroups = 1;
  await initialize(home, config);
  const seed = new Store(home);
  seed.setState('app:memory', 'memory', { revision: 1, USER: 'original preference', notes: '' });
  seed.append({ role: 'user', content: 'Opening isolated task' }); seed.append({ role: 'assistant', content: 'Opening completed' });
  seed.append({ role: 'user', content: 'Earlier isolated task /tmp/retained-source.txt' }); seed.append({ role: 'assistant', content: 'Earlier completed' });
  seed.close();
  await writeFile(join(home, 'drafts/apps/demo-model/behavior.mjs'), `export const createModel = api => ({
    describe: () => ({ contextWindow: 100000 }),
    async complete({ messages, tools, systemContext }) {
      if (!tools.length) return { text: '## Goal\\nIsolated work\\n## Progress\\nEarlier tasks completed.', toolCalls: [] };
      const step = await api.state.get('step', 0); await api.state.set('step', step + 1);
      if (messages.some(m => m.metadata?.module === 'memory')) throw new Error('Memory leaked into compactable journal');
      if (step === 0 && !systemContext.includes('original preference')) throw new Error('Legacy memory missing');
      if (step > 0 && step < 3 && (!systemContext.includes('new preference') || systemContext.includes('original preference'))) throw new Error('Index did not refresh immediately');
      if (step >= 3 && !systemContext.includes('restarted preference')) throw new Error('Index did not reload after restart');
      if (step >= 2) return { text: '', toolCalls: [] };
      return { text: '', toolCalls: [{ id: 'memory-step-' + step, name: 'shell', arguments: { yieldTimeMs:30000, command: step === 0
        ? ${JSON.stringify("printf '%s' 'new preference' | bot-app memory write --stdin")}
        : ${JSON.stringify("bot-app context compact '{\"keepRecentGroups\":1}'")} } }] };
    }
  });`);
  const until = async check => { for (let i = 0; i < 1000; i++) { if (await check()) return; await new Promise(resolve => setTimeout(resolve, 20)); } assert.fail('Isolated bot timed out'); };
  const api = (path, body) => fetch(server.url + path, { method: body ? 'POST' : 'GET', headers: { authorization: 'Bearer ' + server.token, 'content-type': 'application/json' }, body: body ? JSON.stringify(body) : undefined }).then(r => r.json());
  server = await startSupervisor({ home, port: 0, unsafe: true });
  await until(async () => (await api('/api/status')).state === 'idle');
  await api('/api/messages', { text: 'Isolated memory test' });
  await until(async () => (await api('/api/status')).pending === 0);
  assert.equal((await api('/api/status')).error, null);
  const history = (await api('/api/messages')).messages;
  assert.equal(history.filter(message => message.role === 'tool').length, 2);
  assert.ok(!history.some(message => message.metadata?.module === 'memory'));
  assert.ok(history.some(message => message.content.includes('/tmp/retained-source.txt')));
  await server.stop();
  const saved = new Store(home);
  assert.match(JSON.stringify(saved.getState('app:context', 'promptSnapshot', null)), /new preference/);
  saved.close();
  await writeFile(join(home, 'workspace/memory/INDEX.md'), 'restarted preference');
  server = await startSupervisor({ home, port: 0, unsafe: true });
  await until(async () => (await api('/api/status')).state === 'idle');
  await api('/api/messages', { text: 'Isolated restart check' });
  await until(async () => (await api('/api/status')).pending === 0);
  assert.equal((await api('/api/status')).error, null);
});
