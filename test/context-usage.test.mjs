import test from 'node:test';
import assert from 'node:assert/strict';
import { prepareContext, usageNotice } from '../apps/context/prepare.mjs';
import { toPiMessages } from '../apps/model-provider/behavior.mjs';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { initialize, stageDrafts } from '../src/releases.mjs';
import { createConfig } from '../src/cli.mjs';
import { startSupervisor } from '../src/supervisor.mjs';

const fixture = (config = {}) => {
  const state = new Map();
  return { config, state: {
    get: async (key, fallback) => state.get(key) ?? fallback,
    set: async (key, value) => state.set(key, value),
    delete: async key => state.delete(key),
  }, saved: state };
};

test('usage snapshot includes cached provider input and is appended without modifying history or state', async () => {
  const api = fixture();
  const messages = [
    { id: 1, role: 'assistant', content: 'Earlier answer', usage: { input: 2, cacheRead: 180000, cacheWrite: 1000 }, metadata: { nativeAssistant: { model: 'example-model', content: [{ type: 'text', text: 'Earlier answer' }] } } },
    { id: 2, role: 'user', content: 'Continue' },
  ];
  const original = structuredClone(messages);
  let descriptions = 0;
  const model = { describe: async () => { descriptions++; return { contextWindow: 1000000 }; }, complete: () => assert.fail('No model call is needed for visibility') };
  const first = await prepareContext(api, { messages, model });
  assert.deepEqual(first.messages.slice(0, -1), original);
  const note = first.messages.at(-1);
  assert.equal(note.metadata.contextUsage, true);
  assert.match(note.content, /181002 tokens \(including cached input\), model example-model/);
  assert.match(note.content, /window: 1000000 tokens/);
  assert.match(note.content, /approximate, excludes system prompt and tool schemas/);
  assert.equal(note.role, 'system');
  assert.doesNotMatch(note.content, /compact|bot-app/);
  assert.deepEqual(messages, original);
  assert.equal(api.saved.size, 0);
  const next = await prepareContext(api, { messages: [...messages, { id: 3, role: 'tool', content: 'Result' }], model });
  assert.deepEqual(next.messages.slice(0, messages.length), first.messages.slice(0, -1));
  assert.equal(next.messages.filter(m => m.metadata?.contextUsage).length, 1);
  assert.equal(descriptions, 2);
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
  assert.match(note.content, /Previous provider input: unavailable/);
  assert.match(note.content, /context window: unavailable/);
  assert.equal(note.content, usageNotice([message], [{ ...message, content: 'short' }], null).content);
  assert.match(usageNotice([], [], null).content, /Previous provider input: unavailable/);
});

test('usage snapshot describes compacted history without entering the summary or changing safe boundaries', async () => {
  const api = fixture({ maxInputChars: 1000, keepRecentGroups: 1 });
  const messages = [
    { id: 1, role: 'user', content: 'x'.repeat(1500) },
    { id: 2, role: 'assistant', content: 'Finished earlier task', usage: { input: 20, cacheRead: 100, cacheWrite: 5 } },
    { id: 3, role: 'context', content: 'Incoming message', metadata: { input: true } },
    { id: 4, role: 'assistant', content: '', toolCalls: [{ id: 'a', name: 'shell', arguments: {} }] },
    { id: 5, role: 'tool', content: 'Completed', toolCallId: 'a' },
  ];
  let summaryRequest;
  const result = await prepareContext(api, { messages, model: {
    describe: () => ({ contextWindow: 10000 }),
    complete: async request => { summaryRequest = request; return { text: 'Earlier task complete.' }; },
  } });
  assert.deepEqual(summaryRequest.messages.slice(0, -1), messages.slice(0, 2));
  assert.deepEqual(result.messages.slice(1, -1), messages.slice(2));
  assert.equal(api.saved.get('summary').throughId, 2);
  assert.match(result.messages.at(-1).content, /Previous recorded provider input: 125 tokens/);
  assert.equal(summaryRequest.messages.some(m => m.metadata?.contextUsage), false);
});

test('isolated controller receives fresh usage on every model call without journaling the snapshots', { timeout: 20000 }, async t => {
  const home = await mkdtemp(join(tmpdir(), 'bot-context-usage-'));
  let server;
  t.after(async () => { await server?.stop(); await rm(home, { recursive: true, force: true }); });
  await initialize(home, createConfig({ disable: 'chat,memory,skills,scheduler,web-search' }));
  await writeFile(join(home, 'drafts/apps/demo-model/behavior.mjs'), `export const createModel = () => ({
    describe: () => ({ contextWindow: 10000 }),
    async complete({ messages }) {
      const notice = messages.at(-1);
      if (notice.role !== 'system' || !notice.metadata?.contextUsage || /compact|bot-app/.test(notice.content)) throw new Error('Missing or advisory usage notice');
      if (messages.filter(m => m.metadata?.contextUsage).length !== 1) throw new Error('Usage notices accumulated');
      const previous = messages.some(m => m.role === 'tool');
      if (previous && !notice.content.includes('35 tokens (including cached input)')) throw new Error('Previous usage did not refresh');
      return { text: previous ? 'Usage visibility verified' : '',
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
  const history = await until(async () => { const messages = (await api('/api/messages')).messages; return messages.some(m => m.content === 'Usage visibility verified') && messages; });
  assert.equal(history.filter(m => m.role === 'assistant').length, 2);
  assert.equal(history.some(m => m.metadata?.contextUsage || m.role === 'system'), false);
});
