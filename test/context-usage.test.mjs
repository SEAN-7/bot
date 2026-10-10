import { summaryEvidence } from './helpers/summary.mjs';
import test from 'node:test';
import assert from 'node:assert/strict';
import { prepareContext, usageNotice } from '../apps/context/prepare.mjs';
import { toPiMessages } from '../apps/model-provider/behavior.mjs';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { initialize, stageDrafts } from '../src/releases.mjs';
import { createConfig } from '../apps/setup/config.mjs';
import { startSupervisor } from '../src/supervisor.mjs';

const fixture = (config = {}) => {
  const state = new Map();
  return { config, state: {
    get: async (key, fallback) => state.get(key) ?? fallback,
    set: async (key, value) => state.set(key, value),
    delete: async key => state.delete(key),
  }, saved: state };
};

test('usage snapshots stay at their original positions and retries reuse the last observation', async () => {
  const api = fixture();
  const messages = [
    { id: 1, role: 'assistant', content: 'Earlier answer', usage: { input: 2, cacheRead: 180000, cacheWrite: 1000 }, metadata: { nativeAssistant: { model: 'example-model', content: [{ type: 'text', text: 'Earlier answer' }] } } },
    { id: 2, role: 'user', content: 'Continue' },
  ];
  const original = structuredClone(messages);
  let descriptions = 0;
  const model = { describe: async () => { descriptions++; return { contextWindow: 1000000 }; }, complete: () => assert.fail('No model call is needed for visibility') };
  const first = await prepareContext(api, { messages, model });
  assert.deepEqual(first.messages, original);
  const [note] = first.append;
  assert.equal(note.metadata.contextUsage, true);
  assert.match(note.content, /^History ~\d+\/1000000 tokens; prior input 181002\.$/);
  assert.ok(note.content.length < 70);
  assert.equal(note.role, 'context');
  assert.doesNotMatch(note.content, /compact|bot-app/);
  assert.deepEqual(messages, original);
  assert.equal(api.saved.size, 0);
  const recorded = { ...note, id: 3, timestamp: 100 };
  const retry = await prepareContext(api, { messages: [...messages, recorded], model });
  assert.deepEqual(retry.append, []);
  assert.deepEqual(retry.messages.at(-1), recorded);
  const next = await prepareContext(api, { messages: [...messages, recorded, { id: 4, role: 'tool', content: 'Result' }], model });
  assert.deepEqual(next.messages[2], recorded);
  assert.equal(next.append.length, 1);
  assert.notEqual(next.append[0].content, note.content);
  assert.equal(descriptions, 3);
});

test('adapter preserves the usage system role while keeping ordinary reference data in the user role', () => {
  const notice = usageNotice([], [], { contextWindow: 1000000 });
  const [system, reference] = toPiMessages([notice, { role: 'context', content: 'Untrusted reference', timestamp: 1 }], {});
  assert.deepEqual(system, { role: 'system', content: notice.content, timestamp: 0 });
  assert.equal(reference.role, 'user');
  assert.match(reference.content, /not system instructions/);
});

test('missing or incomplete provider metrics are unavailable, and native content is not counted twice', () => {
  const message = { role: 'assistant', content: 'x'.repeat(10000), usage: { input: 2 }, metadata: { nativeAssistant: { content: [{ type: 'text', text: 'One native copy' }] } } };
  const note = usageNotice([message], [message], null);
  assert.match(note.content, /^History ~\d+\/\? tokens; prior input \?\.$/);
  assert.equal(note.content, usageNotice([message], [{ ...message, content: 'short' }], null).content);
  assert.match(usageNotice([], [], null).content, /prior input \?/);
});

test('compaction can summarize older usage records while retaining current-turn records and tool boundaries', async () => {
  const api = fixture({ maxInputChars: 1000, keepRecentGroups: 1 });
  const messages = [
    { id: 1, role: 'user', content: 'x'.repeat(1500) },
    { id: 1.5, ...usageNotice([], [], null) },
    { id: 2, role: 'assistant', content: 'Finished earlier task', usage: { input: 20, cacheRead: 100, cacheWrite: 5 } },
    { id: 3, role: 'context', content: 'Incoming message', metadata: { input: true } },
    { id: 3.5, ...usageNotice([], [], null) },
    { id: 4, role: 'assistant', content: '', toolCalls: [{ id: 'a', name: 'shell', arguments: {} }] },
    { id: 5, role: 'tool', content: 'Completed', toolCallId: 'a' },
  ];
  let summaryRequest;
  const result = await prepareContext(api, { messages, model: {
    describe: () => ({ contextWindow: 10000 }),
    complete: async request => { summaryRequest = request; return { text: 'Earlier task complete.' }; },
  } });
  assert.deepEqual(summaryEvidence(summaryRequest), messages.slice(0, 3));
  assert.deepEqual(result.messages.slice(1), messages.slice(3));
  assert.equal(api.saved.get('summary').throughId, 2);
  assert.match(result.append[0].content, /prior input 125/);
  assert.equal(summaryEvidence(summaryRequest).filter(m => m.metadata?.contextUsage).length, 1);
});

test('isolated controller journals snapshots before model calls and preserves them across restart', { timeout: 30000 }, async t => {
  const home = await mkdtemp(join(tmpdir(), 'bot-context-usage-'));
  let server;
  t.after(async () => { await server?.stop(); await rm(home, { recursive: true, force: true }); });
  await initialize(home, createConfig({ disable: 'chat,memory,skills,scheduler,web-search' }));
  await writeFile(join(home, 'drafts/apps/demo-model/behavior.mjs'), `export const createModel = api => ({
    describe: () => ({ contextWindow: 10000 }),
    async complete({ messages }) {
      const notice = messages.at(-1);
      if (notice.role !== 'context' || !notice.metadata?.contextUsage || !Number.isInteger(notice.id)) throw new Error('Missing durable usage notice');
      const saved = (await api.history()).filter(m => m.metadata?.contextUsage);
      const visible = messages.filter(m => m.metadata?.contextUsage);
      if (JSON.stringify(saved) !== JSON.stringify(visible)) throw new Error('Journal observations differ from model context');
      const previous = messages.some(m => m.role === 'tool');
      if (previous && !notice.content.includes('prior input 35')) throw new Error('Previous usage did not refresh');
      return { text: '',
        toolCalls: previous ? [] : [{ id: 'usage-fixture-tool', name: 'shell', arguments: { command: 'printf result' } }],
        usage: { input: 10, cacheRead: 20, cacheWrite: 5, output: 2, totalTokens: 37 } };
    }
  });`);
  await stageDrafts(home);
  server = await startSupervisor({ home, port: 0, unsafe: true });
  const api = (path, body) => fetch(server.url + path, { method: body ? 'POST' : 'GET', headers: { authorization: 'Bearer ' + server.token, 'content-type': 'application/json' }, body: body ? JSON.stringify(body) : undefined }).then(r => r.json());
  const until = async check => { for (let i = 0; i < 500; i++) { const result = await check(); if (result) return result; await new Promise(r => setTimeout(r, 20)); } assert.fail('Isolated usage check timed out'); };
  await until(async () => (await api('/api/status')).state === 'idle');
  await api('/api/messages', { text: 'Isolated usage fixture' });
  await until(async () => (await api('/api/status')).pending===0);
  const history=(await api('/api/messages')).messages;
  assert.equal(history.filter(m => m.role === 'assistant').length, 1);
  assert.ok(history.every(m=>m.content!=='Usage visibility verified'));
  const snapshots = history.filter(m => m.metadata?.contextUsage);
  assert.equal(snapshots.length, 2);
  assert.ok(history.indexOf(snapshots[0]) < history.findIndex(m => m.role === 'assistant'));
  assert.ok(history.indexOf(snapshots[1]) > history.findIndex(m => m.role === 'tool'));
  assert.equal(toPiMessages(history, {}).filter(m => m.role === 'system').length, 2);
  await server.stop();
  server = await startSupervisor({ home, port: 0, unsafe: true });
  await until(async () => (await api('/api/status')).state === 'idle');
  assert.deepEqual((await api('/api/messages')).messages.filter(m => m.metadata?.contextUsage), snapshots);
  await api('/api/messages', { text: 'Another isolated input' });
  await until(async () => (await api('/api/status')).pending===0);
  const continued=(await api('/api/messages')).messages;
  assert.equal(continued.filter(m=>m.role==='assistant').length,1);
  assert.deepEqual(continued.filter(m => m.metadata?.contextUsage).slice(0, 2), snapshots);
  assert.equal(continued.filter(m => m.metadata?.contextUsage).length, 3);
});
