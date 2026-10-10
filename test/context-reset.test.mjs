import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { commands } from '../apps/context/commands.mjs';
import { createContext } from '../apps/context/behavior.mjs';
import { prepareSnapshot } from '../apps/context/request.mjs';
import { Store } from '../src/store.mjs';
import { initialize } from '../src/releases.mjs';
import { createConfig } from '../apps/setup/config.mjs';
import { startSupervisor } from '../src/supervisor.mjs';

function fixture() {
  const saved = new Map(), journal = [
    { id: 1, role: 'user', content: 'Earlier approved work' },
    { id: 2, role: 'assistant', content: 'Obsolete answer' },
    { id: 3, role: 'context', content: 'Incoming Telegram message 87', metadata: { input: true } },
    { id: 4, role: 'assistant', content: '', toolCalls: [{ id: 'reset', name: 'shell', arguments: { command: "bot-app context reset '{}'" } }] },
    { id: 5, role: 'tool', toolCallId: 'reset', content: '{"status":"requested"}' },
  ];
  const state = { get: async (key, fallback) => structuredClone(saved.has(key) ? saved.get(key) : fallback),
    set: async (key, value) => saved.set(key, structuredClone(value)), delete: async key => saved.delete(key) };
  const api = { state, config: { maxInputChars: 10000 } };
  const model = { complete: async () => { throw new Error('Reset must not call a summarizer'); } };
  const input = { options: { contributors: ['memory'] }, tools: [], settings: {}, state,
    rpc: async () => structuredClone(journal),
    invoke: async () => [{ key: 'memory:2', placement: 'system', content: 'Live saved permission' }],
    prepare: request => createContext(api)({ ...request, model }) };
  saved.set('summary', { throughId: 2, summary: 'Incorrect permission claim' });
  saved.set('summaryProgress', { summary: 'Contaminated partial summary' });
  saved.set('compactionRequest', { rebuild: true });
  saved.set('promptSnapshot', { boundary: 2, contributors: { memory: [{ key: 'memory:1', placement: 'system', content: 'Stale saved memory' }] } });
  return { api, saved, journal, input, model };
}

test('reset drops derived context without summarizing, refreshes memory and preserves the current turn and journal', async () => {
  const { api, saved, journal, input } = fixture(), original = structuredClone(journal);
  assert.equal((await commands(api).reset.execute()).status, 'requested');
  const result = await prepareSnapshot(input);
  assert.match(result.messages[0].content, /omitted, not deleted or revoked/);
  assert.deepEqual(result.messages.slice(1), journal.slice(2));
  assert.equal(result.systemContext, 'Live saved permission');
  for (const key of ['summary', 'summaryProgress', 'compactionRequest', 'resetRequest']) assert.equal(saved.has(key), false);
  assert.deepEqual(journal, original);
  // Fresh app invocations and later rebuilds must not resurrect discarded history.
  assert.deepEqual(await prepareSnapshot({ ...input, state: { ...api.state } }), result);
  journal.push({ id: 6, role: 'user', content: 'Next request' });
  await commands(api).compact.execute({ rebuild: true });
  const rebuilt = await prepareSnapshot({ ...input, prepare: request => createContext(api)({ ...request,
    model: { complete: async request => {
      assert.doesNotMatch(JSON.stringify(request), /Earlier approved work|Obsolete answer|Incorrect permission/);
      return { text: 'Current work retained' };
    } } }) });
  assert.doesNotMatch(JSON.stringify(rebuilt), /Earlier approved work|Obsolete answer|Incorrect permission/);
  assert.equal(rebuilt.messages.at(-1).id, 6);
});

test('reset retains calls and results when a new input arrives inside a tool group, even without compaction', async () => {
  const { api, journal, input } = fixture();
  journal.splice(4, 0, { id: 45, role: 'user', content: 'Arrived while reset was running' });
  await commands(api).reset.execute();
  const result = await prepareSnapshot({ ...input, prepare: null });
  assert.deepEqual(result.messages.slice(1), journal.slice(2));
  assert.equal(result.latestInputId, 45);
  assert.equal(result.systemContext, 'Live saved permission');
});

test('interrupted reset resumes its persisted boundary without dropping newer work', async () => {
  const { api, saved, journal, input } = fixture();
  await commands(api).reset.execute();
  const originalDelete = api.state.delete;
  api.state.delete = async key => { if (key === 'summaryProgress') throw new Error('Interrupted reset'); return originalDelete(key); };
  await assert.rejects(prepareSnapshot(input), /Interrupted reset/);
  assert.ok(saved.has('resetRequest'));
  assert.equal(saved.get('resetBoundary').afterId, 2);
  journal.push({ id: 6, role: 'user', content: 'Arrived after interruption' });
  api.state.delete = originalDelete;
  const result = await prepareSnapshot(input);
  assert.deepEqual(result.messages.slice(1), journal.slice(2));
  assert.equal(saved.has('resetRequest'), false);
  assert.equal(result.systemContext, 'Live saved permission');
});

test('reset handles empty history and retains a first active turn', async () => {
  for (const keepCurrent of [false, true]) {
    const { api, journal, input } = fixture();
    journal.splice(0, keepCurrent ? 2 : journal.length);
    await commands(api).reset.execute();
    const result = await prepareSnapshot(input);
    assert.deepEqual(result.messages.slice(1), journal);
  }
});

test('isolated bot can reset through its shell and keep the boundary across restart', { timeout: 45000 }, async t => {
  const home = await mkdtemp(join(tmpdir(), 'bot-context-reset-'));
  let server;
  t.after(async () => { await server?.stop(); await rm(home, { recursive: true, force: true }); });
  const config = createConfig({ disable: 'chat,skills,scheduler,web-search' });
  await initialize(home, config);
  const seed = new Store(home);
  seed.append({ role: 'user', content: 'OLD_JOURNAL_EVIDENCE' });
  const old = seed.append({ role: 'assistant', content: 'Old completed work' });
  seed.setState('app:context', 'summary', { throughId: old.id, summary: 'CONTAMINATED_SUMMARY' });
  seed.setState('app:memory', 'memory', { revision: 2, USER: 'CURRENT_PERMISSION', notes: '' });
  seed.setState('app:context', 'promptSnapshot', { boundary: old.id, contributors: { memory: [{ key: 'memory:1', placement: 'system', content: 'STALE_MEMORY' }] } });
  seed.close();
  const fakeModel = api => ({
    describe: () => ({ contextWindow: 100000 }),
    async complete({ messages, tools, systemContext }) {
      if (!tools.length) throw new Error('Unexpected summarization');
      const step = await api.state.get('step', 0); await api.state.set('step', step + 1);
      if (step === 0) return { text: '', toolCalls: [{ id: 'reset-context', name: 'shell', arguments: {
        command: "bot-app context reset '{}'", yieldTimeMs: 30000 } }] };
      const text = JSON.stringify(messages);
      if (/CONTAMINATED_SUMMARY|OLD_JOURNAL_EVIDENCE/.test(text)) throw new Error('Old context returned');
      if (!systemContext.includes('CURRENT_PERMISSION') || systemContext.includes('STALE_MEMORY')) throw new Error('Memory did not refresh');
      if (!text.includes('Isolated reset request')) throw new Error('Current request lost');
      if (!messages.some(m => m.role === 'tool' && m.toolCallId === 'reset-context')) throw new Error('Reset result lost');
      return { text: '', toolCalls: [] };
    }
  });
  await writeFile(join(home, 'drafts/apps/demo-model/behavior.mjs'), 'export const createModel = ' + fakeModel.toString());
  const until = async check => { for (let i = 0; i < 1000; i++) { if (await check()) return; await new Promise(resolve => setTimeout(resolve, 20)); } assert.fail('Isolated bot timed out'); };
  const api = (path, body) => fetch(server.url + path, { method: body ? 'POST' : 'GET', headers: { authorization: 'Bearer ' + server.token, 'content-type': 'application/json' }, body: body ? JSON.stringify(body) : undefined }).then(r => r.json());
  const start = async () => { server = await startSupervisor({ home, port: 0, unsafe: true }); await until(async () => (await api('/api/status')).state === 'idle'); };
  await start();
  await api('/api/messages', { text: 'Isolated reset request' });
  await until(async () => (await api('/api/status')).pending === 0);
  assert.equal((await api('/api/status')).error, null);
  assert.ok((await api('/api/messages')).messages.some(m => m.content === 'OLD_JOURNAL_EVIDENCE'));
  await server.stop(); server = null;
  const saved = new Store(home);
  assert.equal(saved.getState('app:context', 'resetBoundary', null).afterId, old.id);
  assert.equal(saved.getState('app:context', 'summary', null), null);
  saved.close();
  await start();
  await api('/api/messages', { text: 'Isolated restart check' });
  await until(async () => (await api('/api/status')).pending === 0);
  assert.equal((await api('/api/status')).error, null);
});
