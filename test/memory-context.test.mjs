import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { prepareSnapshot } from '../apps/context/request.mjs';
import { contribute } from '../apps/memory/behavior.mjs';
import { commands as memoryCommands } from '../apps/memory/commands.mjs';
import { createContext } from '../apps/context/behavior.mjs';
import { continuityAnchors, sampleForSummary } from '../apps/context/summary.mjs';
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

test('Hermes-sized memory rejects oversized new writes without trimming existing saved data', async () => {
  const api = stateFixture();
  const legacy = { revision: 7, USER: 'u'.repeat(5000), notes: 'n'.repeat(5000) };
  api.saved.set('memory', legacy);
  const commands = memoryCommands(api);
  assert.deepEqual(await commands.get.execute(), legacy);
  await assert.rejects(commands.update.execute({ section: 'USER', value: 'x'.repeat(1376) }), /Invalid memory/);
  await assert.rejects(commands.update.execute({ section: 'notes', value: 'x'.repeat(2201) }), /Invalid memory/);
  assert.deepEqual(await commands.get.execute(), legacy);
  await commands.update.execute({ section: 'USER', value: 'Consolidated preferences' });
  assert.equal(api.saved.get('memory').revision, 8);
  assert.equal(api.saved.get('memory').notes, legacy.notes);
});

test('prompt memory freezes between compactions, refreshes at a durable boundary, clears and survives restart', async () => {
  const state = stateFixture(), memory = stateFixture();
  memory.saved.set('memory', { revision: 1, USER: 'original preference', notes: '' });
  let boundary = null, fail = false;
  const journal = [{ id: 1, role: 'context', content: 'obsolete snapshot', metadata: { module: 'memory' } }, { id: 2, role: 'user', content: 'Human request' }];
  const input = { options: { contributors: ['memory'] }, tools: [], settings: {}, state: state.state,
    rpc: async method => { assert.equal(method, 'history'); return structuredClone(journal); },
    invoke: async (app, operation, request) => {
      if (app === 'memory') return contribute(memory);
    },
    prepare: async request => {
      if (fail) throw new Error('summary failed');
      assert.ok(!request.messages.some(message => message.content === 'obsolete snapshot'));
      return { messages: request.messages, boundary };
    } };
  const first = await prepareSnapshot(input);
  assert.match(first.systemContext, /original preference/);
  memory.saved.set('memory', { revision: 2, USER: 'new preference', notes: '' });
  assert.equal((await prepareSnapshot(input)).systemContext, first.systemContext);
  fail = true;
  await assert.rejects(prepareSnapshot(input), /summary failed/);
  assert.equal(state.saved.get('promptSnapshot').boundary, null);
  fail = false; boundary = 10;
  assert.match((await prepareSnapshot(input)).systemContext, /new preference/);
  // A fresh controller uses the same durable frozen snapshot, even if state changed.
  memory.saved.set('memory', { revision: 3, USER: '', notes: '' });
  assert.match((await prepareSnapshot({ ...input, state: { ...state.state } })).systemContext, /new preference/);
  boundary = 20;
  assert.equal((await prepareSnapshot(input)).systemContext, '');
  assert.equal((await prepareSnapshot({ ...input, options: { ...input.options, contributors: [] } })).systemContext, '');
  assert.deepEqual(journal[0].content, 'obsolete snapshot');
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

test('Hermes compaction preserves a bounded opening and active tools, prunes old output, and rolls the summary forward', async () => {
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
  assert.match(first.messages[2].content, /\/opt\/project\/report.go/);
  assert.match(first.messages[2].content, /PR #123/);
  assert.match(first.messages[2].content, /abc123def/);
  assert.match(first.messages[2].content, /Human Messages/);
  assert.match(first.messages[2].content, /bot-app memory search/);
  assert.match(requests[0].messages.find(message => message.id === 5).content, /Old shell output cleared/);
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

test('summary failures retain the prior summary and request; unknown outcomes survive pruning', async () => {
  const api = stateFixture({ maxInputChars: 1000, keepRecentGroups: 1 });
  const previous = { summary: 'Prior summary', throughId: 1 };
  api.saved.set('summary', previous);
  api.saved.set('compactionRequest', { keepRecentGroups: 1 });
  const messages = [{ id: 1, role: 'user', content: 'Original' }, { id: 2, role: 'user', content: 'Older work' },
    { id: 3, role: 'assistant', content: '', toolCalls: [{ id: 'x', name: 'shell', arguments: {} }] },
    { id: 4, role: 'tool', toolCallId: 'x', content: 'UNKNOWN interrupted external action '.repeat(100), isError: true },
    { id: 5, role: 'user', content: 'Current' }];
  await assert.rejects(createContext(api)({ messages, model: { complete: async request => {
    assert.equal(request.messages.find(message => message.id === 4).content, messages[3].content);
    return { text: '', toolCalls: [] };
  } } }), /usable text summary/);
  assert.deepEqual(api.saved.get('summary'), previous);
  assert.ok(api.saved.has('compactionRequest'));
});

test('large summary input is sampled with explicit elisions and anchors stay bounded', () => {
  const messages = Array.from({ length: 100 }, (_, i) => ({ id: i, role: 'user', content: 'x'.repeat(10000) + ` /path/to/file${i}` }));
  const sampled = sampleForSummary(messages, 50000);
  assert.ok(JSON.stringify(sampled).length < 50000);
  assert.match(JSON.stringify(sampled), /elided/);
  assert.ok(continuityAnchors(messages, 1000).length < 1200);
});

test('isolated bot refreshes system memory after compaction and preserves frozen memory across restart', { timeout: 45000 }, async t => {
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
      if (step < 2 && !systemContext.includes('original preference')) throw new Error('Memory was not frozen');
      if (step >= 2 && (!systemContext.includes('new preference') || systemContext.includes('unrefreshed'))) throw new Error('Boundary/restart snapshot failed');
      if (step >= 2) return { text: '', toolCalls: [] };
      return { text: '', toolCalls: [{ id: 'memory-step-' + step, name: 'shell', arguments: { yieldTimeMs:30000, command: step === 0
        ? ${JSON.stringify("bot-app memory update '{\"section\":\"USER\",\"value\":\"new preference\"}'")}
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
  saved.setState('app:memory', 'memory', { revision: 3, USER: 'unrefreshed preference', notes: '' }); saved.close();
  server = await startSupervisor({ home, port: 0, unsafe: true });
  await until(async () => (await api('/api/status')).state === 'idle');
  await api('/api/messages', { text: 'Isolated restart check' });
  await until(async () => (await api('/api/status')).pending === 0);
  assert.equal((await api('/api/status')).error, null);
});
