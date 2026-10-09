import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm, readFile, access } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import Ajv from 'ajv';
import { contribute } from '../apps/memory/behavior.mjs';
const memoryModule=api=>api.on('beforeModel',()=>contribute(api));
import { commands as memoryCommands } from '../apps/memory/commands.mjs';
import { tick } from '../apps/scheduler/behavior.mjs';
const schedulerModule=api=>api.on('tick',input=>tick(api,input));
import { commands as schedulerCommands } from '../apps/scheduler/commands.mjs';
import shellExecution from '../src/execution.mjs';
import { commands as moduleCommands } from '../apps/updates/commands.mjs';
import { createContext, messageGroups } from '../apps/context/behavior.mjs';
const contextModule=api=>api.registerContext(createContext(api));
import { commands as contextCommands } from '../apps/context/commands.mjs';
import { createModel, classifyModelError } from '../apps/model-provider/behavior.mjs';
const piModel=(api,collection)=>api.registerModel(createModel(api.config,collection));

// Exercise the migrated command bodies against the same durable-state fixtures.
function installCommands(api, entries, names) {
  for(const [name, entry] of Object.entries(entries)) api.registerCommand({ ...entry, name: names[name] });
}
function memory(api) { memoryModule(api); installCommands(api,memoryCommands(api),{get:'memory_get',update:'memory_update',search:'history_search'}); }
function scheduler(api) { schedulerModule(api); installCommands(api,schedulerCommands(api),{create:'schedule_create',list:'schedule_list',cancel:'schedule_cancel'}); }
function context(api) { contextModule(api); installCommands(api,contextCommands(api),{compact:'compact_conversation'}); }
function execution(api) { api.registerCommand(shellExecution(api)); installCommands(api,moduleCommands(api),{apply:'module_apply'}); }

function harness(config = {}, state = new Map()) {
  const tools = new Map();
  const hooks = new Map();
  const events = new Map();
  const enqueueCalls = [];
  const processes = new Set();
  const ajv = new Ajv({strict:false});
  const api = {
    config, paths: { workspace: tmpdir(), drafts: join(tmpdir(), 'drafts') },
    state: { get: async (key, fallback) => structuredClone(state.has(key) ? state.get(key) : fallback),
      set: async (key, value) => { state.set(key, structuredClone(value)); }, delete: async key => state.delete(key) },
    history: async () => [],
    enqueue: async event => { enqueueCalls.push(event); events.set(event.id, event); },
    registerCommand: tool => { tools.set(tool.name, { ...tool, validate: ajv.compile(tool.inputSchema) }); },
    registerContext: manager => { api.context = manager; },
    registerModel: model => { api.model = model; },
    on: (name, fn) => hooks.set(name, fn), log: () => {}, proposeUpdate: async () => ({ staged: true }),
    trackProcess: async pid => processes.add(pid), releaseProcess: async pid => processes.delete(pid),
  };
  return { api, state, tools, hooks, events, enqueueCalls, processes,
    async call(name, args = {}, signal = new AbortController().signal) {
      const tool = tools.get(name);
      assert.ok(tool.validate(args), JSON.stringify(tool.validate.errors));
      return tool.execute(args, { signal, callId: 'test-call' });
    },
  };
}

test('memory is bounded, persistent, revisioned, and emits a clearing revision', async () => {
  const h = harness({ maxChars: 256 });
  memory(h.api);
  assert.deepEqual(await h.hooks.get('beforeModel')({}), []);
  const update = { section: 'USER', value: 'Prefers plain language.' };
  await h.call('memory_update', update);
  const first = await h.hooks.get('beforeModel')({});
  await h.call('memory_update', update);
  assert.deepEqual(await h.hooks.get('beforeModel')({}), first);
  assert.equal(h.tools.get('memory_update').validate({ section: 'USER', value: 'a'.repeat(257) }), false);
  await h.call('memory_update', { section: 'USER', value: '' });
  const cleared = await h.hooks.get('beforeModel')({});
  assert.notEqual(cleared[0].key, first[0].key);
  assert.match(cleared[0].content, /"USER":""/);
  const restarted = harness({}, h.state);
  memory(restarted.api);
  assert.equal((await restarted.call('memory_get')).revision, 2);
});

test('history search finds archived conversation text with bounded recent matches', async () => {
  const h = harness();
  memory(h.api);
  h.api.history = async () => [
    { id: 'a', role: 'user', content: 'Need a durable JOURNAL' },
    { id: 'b', role: 'assistant', content: 'Created the journal' },
    { id: 'c', role: 'tool', content: 'unrelated' },
  ];
  assert.deepEqual((await h.call('history_search', { query: 'journal', limit: 1 })).map(m => m.id), ['b']);
});

test('scheduler retries the same durable slot ID after enqueue-before-state failure', async () => {
  const h = harness();
  scheduler(h.api);
  const entry = await h.call('schedule_create', { text: 'Check completion', at: '2020-01-01T00:00:00Z' });
  const save = h.api.state.set;
  h.api.state.set = async () => { throw new Error('simulated crash'); };
  await assert.rejects(h.hooks.get('tick')({}), /simulated crash/);
  h.api.state.set = save;
  // Simulate a fresh module instance restoring the old schedule state.
  scheduler(h.api);
  await h.hooks.get('tick')({});
  await h.hooks.get('tick')({});
  assert.equal(h.enqueueCalls.length, 2);
  assert.equal(h.enqueueCalls[0].id, h.enqueueCalls[1].id);
  assert.equal(h.events.size, 1);
  assert.equal((await h.call('schedule_list'))[0].status, 'completed');
  assert.equal(h.enqueueCalls[0].metadata.scheduleId, entry.id);
});

test('recurring schedule coalesces downtime, cancellation persists, schemas reject extra input', async () => {
  const h = harness();
  scheduler(h.api);
  const entry = await h.call('schedule_create', { text: 'Periodic check', at: '2020-01-01T00:00:00Z', intervalMs: 60_000 });
  await h.hooks.get('tick')({});
  assert.equal(h.events.size, 1);
  assert.ok(Date.parse((await h.call('schedule_list'))[0].nextAt) > Date.now());
  await h.call('schedule_cancel', { id: entry.id });
  await h.hooks.get('tick')({});
  assert.equal(h.events.size, 1);
  assert.equal((await h.call('schedule_list'))[0].status, 'cancelled');
  assert.equal(h.tools.get('schedule_create').validate({ text: 'x', at: '2020-01-01T00:00:00Z', cron: '*' }), false);
  await assert.rejects(h.call('schedule_create', { text: 'x', at: '2020-01-01T00:00:00.000' }), /timezone/);
});

function conversation() {
  return [
    { id: 1, role: 'user', content: 'Earlier work ' + 'x'.repeat(1500) },
    { id: 2, role: 'assistant', content: '', toolCalls: [{ id: 'a', name: 'one', arguments: {} }, { id: 'b', name: 'two', arguments: {} }] },
    { id: 3, role: 'tool', content: 'one result', toolCallId: 'a', toolName: 'one' },
    { id: 4, role: 'context', content: 'remembered data' },
    { id: 5, role: 'tool', content: 'two result', toolCallId: 'b', toolName: 'two' },
    { id: 6, role: 'user', content: 'Continue' },
    { id: 7, role: 'assistant', content: 'Working' },
  ];
}

test('context compacts whole tool groups and reuses an identical summary prefix', async () => {
  const h = harness({ maxInputChars: 1000, keepRecentGroups: 1 });
  context(h.api);
  const messages = conversation();
  const original = structuredClone(messages);
  const requests = [];
  const model = { complete: async request => { requests.push(request); return { text: 'Earlier work used two tools.', toolCalls: [] }; } };
  const first = await h.api.context({ messages, model, settings: {} });
  assert.deepEqual(first.messages.slice(1).map(m => m.id), [6, 7]);
  assert.equal(h.state.get('summary').throughId, 5);
  assert.deepEqual(requests[0].messages.slice(0, -1), messages.slice(0, 5));
  assert.deepEqual(requests[0].tools, []);
  const later = await h.api.context({ messages: [...messages, { id: 8, role: 'user', content: 'Another task' }], model, settings: {} });
  assert.deepEqual(later.messages[0], first.messages[0]);
  assert.equal(requests.length, 1);
  assert.deepEqual(messages, original);
  assert.deepEqual(messageGroups(messages).map(group => group.map(m => m.id)), [[1, 2, 3, 4, 5], [6, 7]]);
});

test('summary failure preserves state and open tool blocks remain together', async () => {
  const h = harness({ maxInputChars: 1000, keepRecentGroups: 1 });
  context(h.api);
  await assert.rejects(h.api.context({ messages: conversation(), model: { complete: async () => { throw new Error('provider unavailable'); } } }), /provider unavailable/);
  assert.equal(h.state.has('summary'), false);
  const open = conversation().slice(0, 4);
  assert.equal(messageGroups(open).length, 1);
  const result = await h.api.context({ messages: open, model: { complete: async () => { assert.fail('Cannot summarize an open tool block'); } } });
  assert.deepEqual(result.messages, open);
});

test('context retains the current user request across a long tool loop', async () => {
  const h = harness({ maxInputChars: 1000, keepRecentGroups: 1 });
  context(h.api);
  const messages = conversation().slice(0, 5);
  messages.push({ id: 6, role: 'assistant', content: '', toolCalls: [{ id: 'c', name: 'three', arguments: {} }] },
    { id: 7, role: 'tool', toolCallId: 'c', toolName: 'three', content: 'third result' });
  const result = await h.api.context({ messages, model: { complete: async () => assert.fail('Current user request must remain') } });
  assert.deepEqual(result.messages, messages);
});

test('agent requests compaction below the automatic threshold while retaining current tool groups', async () => {
  const h = harness({ maxInputChars: 100_000, keepRecentGroups: 8 });
  context(h.api);
  const messages = conversation();
  messages.push(
    { id: 8, role: 'assistant', content: '', toolCalls: [{ id: 'compact', name: 'compact_conversation', arguments: {} }] },
    { id: 9, role: 'tool', content: '{"status":"requested"}', toolCallId: 'compact', toolName: 'compact_conversation' },
    { id: 10, role: 'assistant', content: '', toolCalls: [{ id: 'next', name: 'one', arguments: {} }] },
  );
  const original = structuredClone(messages);
  const requests = [];
  const model = { complete: async request => { requests.push(request); return { text: 'Earlier tools completed.', toolCalls: [] }; } };
  assert.deepEqual((await h.api.context({ messages, model })).messages, messages);
  assert.equal(requests.length, 0);
  assert.equal((await h.call('compact_conversation')).status, 'requested');
  assert.deepEqual(h.state.get('compactionRequest'), { keepRecentGroups: 1 });
  const result = await h.api.context({ messages, model });
  assert.deepEqual(requests[0].messages.slice(0, -1), messages.slice(0, 5));
  assert.deepEqual(requests[0].tools, []);
  assert.deepEqual(result.messages.slice(1), messages.slice(5));
  assert.equal(h.state.get('summary').throughId, 5);
  assert.equal(h.state.has('compactionRequest'), false);
  assert.deepEqual(messages, original);
  await h.api.context({ messages, model });
  assert.equal(requests.length, 1);
});

test('compaction tool validates retention and honors the requested recent groups', async () => {
  const h = harness({ maxInputChars: 100_000 });
  context(h.api);
  const validate = h.tools.get('compact_conversation').validate;
  for (const args of [{ keepRecentGroups: 0 }, { keepRecentGroups: 101 }, { keepRecentGroups: 1.5 }, { unexpected: true }]) {
    assert.equal(validate(args), false);
  }
  assert.equal(validate({ keepRecentGroups: 100 }), true);
  const messages = [
    { id: 1, role: 'user', content: 'First task' },
    { id: 2, role: 'assistant', content: 'First answer' },
    { id: 3, role: 'user', content: 'Second task' },
    { id: 4, role: 'assistant', content: 'Second answer' },
    { id: 5, role: 'user', content: 'Current task' },
  ];
  await h.call('compact_conversation', { keepRecentGroups: 2 });
  assert.deepEqual(h.state.get('compactionRequest'), { keepRecentGroups: 2 });
  const result = await h.api.context({ messages, model: { complete: async () => ({ text: 'First task completed.' }) } });
  assert.deepEqual(result.messages.slice(1), messages.slice(2));
  assert.equal(h.state.get('summary').throughId, 2);
});

test('requested compaction clears a no-op without summarizing the current user turn', async () => {
  const h = harness({ maxInputChars: 100_000 });
  context(h.api);
  const messages = conversation().slice(0, 4);
  const original = structuredClone(messages);
  await h.call('compact_conversation');
  const result = await h.api.context({ messages, model: { complete: async () => assert.fail('No safe older user turn exists') } });
  assert.deepEqual(result.messages, original);
  assert.deepEqual(messages, original);
  assert.equal(h.state.has('summary'), false);
  assert.equal(h.state.has('compactionRequest'), false);
});

test('requested compaction survives provider failure and retries on the next preparation', async () => {
  const h = harness({ maxInputChars: 100_000 });
  context(h.api);
  const messages = conversation();
  const original = structuredClone(messages);
  await h.call('compact_conversation');
  await assert.rejects(h.api.context({ messages, model: { complete: async () => { throw new Error('provider unavailable'); } } }), /provider unavailable/);
  assert.deepEqual(h.state.get('compactionRequest'), { keepRecentGroups: 1 });
  assert.equal(h.state.has('summary'), false);
  assert.deepEqual(messages, original);
  const result = await h.api.context({ messages, model: { complete: async () => ({ text: 'Earlier work completed.', toolCalls: [] }) } });
  assert.deepEqual(result.messages.slice(1), messages.slice(5));
  assert.equal(h.state.has('compactionRequest'), false);
  assert.equal(h.state.get('summary').throughId, 5);
});

test('persisted compaction request is consumed by a fresh context plugin registration', async () => {
  const h = harness({ maxInputChars: 100_000 });
  context(h.api);
  await h.call('compact_conversation');
  const restarted = harness({ maxInputChars: 100_000 }, h.state);
  context(restarted.api);
  let calls = 0;
  const result = await restarted.api.context({ messages: conversation(), model: { complete: async () => { calls++; return { text: 'Restored earlier work.' }; } } });
  assert.equal(calls, 1);
  assert.deepEqual(result.messages.slice(1).map(message => message.id), [6, 7]);
  assert.equal(restarted.state.has('compactionRequest'), false);
  assert.equal(restarted.state.get('summary').throughId, 5);
});

test('Pi adapter preserves native thinking and tool calls and replays actual tool results', async () => {
  const h = harness({ provider: 'provider', model: 'model' });
  const selected = { id: 'model', provider: 'provider', api: 'anthropic-messages' };
  const native = { role: 'assistant', api: selected.api, provider: selected.provider, model: selected.id, timestamp: 123, stopReason: 'toolUse',
    usage: { input: 1, output: 2 }, responseId: 'native-response', content: [
      { type: 'thinking', thinking: 'reasoning', thinkingSignature: 'signed-opaque' },
      { type: 'toolCall', id: 'native-call', name: 'shell', arguments: { command: 'pwd' }, thoughtSignature: 'tool-signature' },
    ] };
  let captured;
  piModel(h.api, { getModel: () => selected, getModels: () => [selected], completeSimple: async (_model, request, options) => {
    captured = { request, options };
    return { ...native, content: [{ type: 'text', text: 'Finished.' }], stopReason: 'stop' };
  } });
  const schema = { type: 'object', properties: { command: { type: 'string' } }, required: ['command'], additionalProperties: false };
  const result = await h.api.model.complete({ messages: [
    { role: 'context', content: 'Memory data', timestamp: 1 },
    { role: 'assistant', content: '', toolCalls: [{ id: 'native-call', name: 'shell', arguments: { command: 'pwd' } }], metadata: { nativeAssistant: native } },
    { role: 'tool', content: '/workspace', toolCallId: 'native-call', toolName: 'shell', isError: false, timestamp: 124 },
  ], tools: [{ name: 'shell', description: 'Run shell', inputSchema: schema }] });
  assert.deepEqual(captured.request.messages[1], native);
  assert.notEqual(captured.request.messages[1], native);
  assert.equal(captured.request.messages[0].role, 'user');
  assert.equal(captured.request.messages[2].role, 'toolResult');
  assert.equal(captured.request.messages[2].toolCallId, 'native-call');
  assert.deepEqual(captured.request.tools[0].parameters, schema);
  assert.equal(captured.options.maxRetries, 0);
  assert.equal(result.text, 'Finished.');
  assert.equal(result.metadata.nativeAssistant.responseId, 'native-response');
});

test('Pi registry loads without calling providers, and provider failures are classified', () => {
  const h = harness();
  piModel(h.api);
  assert.ok(h.api.model.listModels().length > 0);
  assert.equal(classifyModelError(new Error('rate limit'), 429).retryable, true);
  assert.equal(classifyModelError(new Error('API key missing')).blocked, true);
  assert.equal(classifyModelError(new Error('Invalid request'), 400).retryable, undefined);
});

test('Pi refuses incomplete tool calls after output truncation', async () => {
  const h = harness({ provider: 'provider', model: 'model' });
  piModel(h.api, {
    getModel: () => ({ provider: 'provider', id: 'model', api: 'openai-responses' }),
    completeSimple: async () => ({ stopReason: 'length', content: [{ type: 'toolCall', id: 'partial', name: 'shell', arguments: { command: 'touch' } }] }),
  });
  await assert.rejects(h.api.model.complete({ messages: [], tools: [] }), error => error.blocked && /output limit/.test(error.message));
});

test('shell runs from workspace, bounds bytes, reports failure, and releases tracked groups', async t => {
  const directory = await mkdtemp(join(tmpdir(), 'bot-module-test-'));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const h = harness({ outputBytes: 1024 });
  h.api.paths.workspace = directory;
  execution(h.api);
  const result = await h.call('shell', { command: 'pwd; yes x | head -c 3000; printf error >&2; exit 7' });
  assert.equal(result.exitCode, 7);
  assert.equal(result.truncated, true);
  assert.ok(Buffer.byteLength(result.stdout + result.stderr) <= 1024);
  assert.match(result.stdout, /bot-module-test-/);
  assert.equal(h.processes.size, 0);
  assert.deepEqual(await h.call('module_apply'), { staged: true });
});

test('shell timeout and abort kill descendants and report uncertain effects', async t => {
  const directory = await mkdtemp(join(tmpdir(), 'bot-module-kill-'));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const h = harness();
  h.api.paths.workspace = directory;
  execution(h.api);
  const timed = await h.call('shell', { command: '(sleep 0.4; touch late-timeout) & wait', timeoutMs: 40 });
  assert.equal(timed.timedOut, true);
  assert.equal(timed.outcome, 'unknown');
  const controller = new AbortController();
  const pending = h.call('shell', { command: '(sleep 0.4; touch late-abort) & wait' }, controller.signal);
  setTimeout(() => controller.abort(), 40);
  const aborted = await pending;
  assert.equal(aborted.cancelled, true);
  assert.equal(aborted.outcome, 'unknown');
  await new Promise(resolve => setTimeout(resolve, 500));
  await assert.rejects(access(join(directory, 'late-timeout')));
  await assert.rejects(access(join(directory, 'late-abort')));
  assert.equal(h.processes.size, 0);
});

test('shell startup gate prevents command execution when supervisor tracking fails', async t => {
  const directory = await mkdtemp(join(tmpdir(), 'bot-module-gate-'));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const h = harness();
  h.api.paths.workspace = directory;
  h.api.trackProcess = async () => { throw new Error('tracking rejected'); };
  execution(h.api);
  await assert.rejects(h.call('shell', { command: 'touch must-not-run' }), /tracking rejected/);
  await assert.rejects(readFile(join(directory, 'must-not-run')));
});

test('context derives compaction budget from a small selected model', async () => {
  const h = harness();
  context(h.api);
  const messages = [
    { id: 1, role: 'user', content: 'x'.repeat(4500) },
    { id: 2, role: 'assistant', content: 'earlier answer' },
    { id: 3, role: 'user', content: 'y'.repeat(3000) },
  ];
  let calls = 0;
  const model = {
    describe: () => ({ contextWindow: 4000, maxOutputTokens: 1000 }),
    complete: async () => { calls++; return { text: 'Earlier request completed.', toolCalls: [] }; }
  };
  const result = await h.api.context({ messages, model });
  assert.equal(calls, 1);
  assert.deepEqual(result.messages.slice(1), [messages[2]]);
  assert.equal(h.state.get('summary').throughId, 2);
  await assert.rejects(h.api.context({ messages: [{ id: 4, role: 'user', content: 'z'.repeat(12000) }], model }), error => error.blocked && /current user turn was retained/.test(error.message));
});

test('Pi describes the selected model without making a provider call', () => {
  const h = harness({ provider: 'provider', model: 'small' });
  piModel(h.api, {
    getModel: (provider, id) => ({ provider, id, contextWindow: id === 'small' ? 4000 : 100000, maxTokens: 1000 }),
    completeSimple: () => assert.fail('Description must not call the provider')
  });
  assert.deepEqual(h.api.model.describe({}), { contextWindow: 4000, maxOutputTokens: 1000 });
  assert.equal(h.api.model.describe({ model: 'large' }).contextWindow, 100000);
});

async function shellOutput(h, result) {
  const deadline = Date.now() + 5000;
  while (result.outcome === 'running' && !result.stdout && Date.now() < deadline) {
    result = await h.call('shell', { sessionId: result.sessionId, yieldTimeMs: 100 });
  }
  return result;
}

test('shell yields a supervised session, polls incremental output, and accepts stdin and EOF', async t => {
  const h = harness();
  execution(h.api);
  t.after(() => h.hooks.get('stop')());
  const first = await shellOutput(h, await h.call('shell', { command: 'printf first; read line; printf "second:%s" "$line"; cat; printf done', yieldTimeMs: 30 }));
  assert.equal(first.outcome, 'running');
  assert.equal(first.stdout, 'first');
  assert.equal(h.processes.size, 1);
  const idle = await h.call('shell', { sessionId: first.sessionId, yieldTimeMs: 0 });
  assert.equal(idle.stdout, '');
  assert.equal(idle.sessionId, first.sessionId);
  const second = await shellOutput(h, await h.call('shell', { sessionId: first.sessionId, chars: 'hello\n', yieldTimeMs: 30 }));
  assert.equal(second.outcome, 'running');
  assert.equal(second.stdout, 'second:hello');
  const final = await h.call('shell', { sessionId: first.sessionId, chars: 'tail', endInput: true });
  assert.equal(final.outcome, 'completed');
  assert.equal(final.stdout, 'taildone');
  assert.equal(final.exitCode, 0);
  assert.equal(h.processes.size, 0);
  await assert.rejects(h.call('shell', { sessionId: first.sessionId }), /not found/);
});

test('yielding never kills commands and completion remains available until polled', async t => {
  const h = harness();
  execution(h.api);
  t.after(() => h.hooks.get('stop')());
  const first = await h.call('shell', { command: 'sleep 0.15; printf finished; exit 9', yieldTimeMs: 10 });
  assert.equal(first.outcome, 'running');
  assert.equal(first.timedOut, false);
  await new Promise(resolve => setTimeout(resolve, 250));
  const final = await h.call('shell', { sessionId: first.sessionId, yieldTimeMs: 0 });
  assert.equal(final.exitCode, 9);
  assert.equal(final.stdout, 'finished');
  assert.equal(final.timedOut, false);
  assert.equal(final.outcome, 'completed');
  assert.equal(h.tools.get('shell').validate({ command: 'true', timeoutMs: 180_000 }), true);
});

test('yielded sessions bound unread output, reset the output budget on polling, and stop descendants', async t => {
  const directory = await mkdtemp(join(tmpdir(), 'bot-session-kill-'));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const h = harness({ outputBytes: 1024 });
  h.api.paths.workspace = directory;
  execution(h.api);
  t.after(() => h.hooks.get('stop')());
  const first = await shellOutput(h, await h.call('shell', { command: 'yes x | head -c 4000; read line; printf fresh; (sleep 0.4; touch late) & wait', yieldTimeMs: 100 }));
  assert.equal(first.truncated, true);
  assert.equal(Buffer.byteLength(first.stdout), 1024);
  const next = await shellOutput(h, await h.call('shell', { sessionId: first.sessionId, chars: '\n', yieldTimeMs: 30 }));
  assert.equal(next.stdout, 'fresh');
  assert.equal(next.truncated, false);
  const stopped = await h.call('shell', { sessionId: first.sessionId, terminate: true });
  assert.equal(stopped.cancelled, true);
  assert.equal(stopped.outcome, 'unknown');
  await new Promise(resolve => setTimeout(resolve, 500));
  await assert.rejects(access(join(directory, 'late')));
  assert.equal(h.processes.size, 0);
});

test('explicit timeout, polling cancellation, and module stop clean up yielded sessions', async () => {
  const h = harness({ maxSessions: 1 });
  execution(h.api);
  const first = await h.call('shell', { command: 'sleep 10', timeoutMs: 40, yieldTimeMs: 0 });
  const timed = await h.call('shell', { sessionId: first.sessionId });
  assert.equal(timed.timedOut, true);
  assert.equal(timed.outcome, 'unknown');
  const second = await h.call('shell', { command: 'sleep 10', yieldTimeMs: 30 });
  await assert.rejects(h.call('shell', { command: 'true' }), /session limit/);
  const controller = new AbortController();
  const pending = h.call('shell', { sessionId: second.sessionId }, controller.signal);
  controller.abort();
  assert.equal((await pending).cancelled, true);
  const third = await h.call('shell', { command: 'sleep 10', yieldTimeMs: 30 });
  await h.hooks.get('stop')();
  assert.equal(h.processes.size, 0);
  await assert.rejects(h.call('shell', { sessionId: third.sessionId }), /not found/);
});

test('input sent immediately after yield cannot bypass supervisor registration', async t => {
  const directory = await mkdtemp(join(tmpdir(), 'bot-session-gate-'));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const h = harness();
  h.api.paths.workspace = directory;
  let approve;
  h.api.trackProcess = async pid => { await new Promise(resolve => { approve = resolve; }); h.processes.add(pid); };
  execution(h.api);
  t.after(() => h.hooks.get('stop')());
  const first = await h.call('shell', { command: 'touch started; read line; printf "%s" "$line"', yieldTimeMs: 0 });
  const input = h.call('shell', { sessionId: first.sessionId, chars: 'hello\n' });
  while (!approve) await new Promise(resolve => setImmediate(resolve));
  await assert.rejects(access(join(directory, 'started')));
  approve();
  const final = await input;
  assert.equal(final.outcome, 'completed');
  assert.equal(final.stdout, 'hello');
  assert.equal(h.processes.size, 0);
});

test('incremental shell output preserves UTF-8 characters split between polls', async t => {
  const h = harness();
  execution(h.api);
  t.after(() => h.hooks.get('stop')());
  const command = `${JSON.stringify(process.execPath)} -e 'process.stdout.write(Buffer.from([0xf0,0x9f])); process.stdin.once("data",()=>{process.stdout.write(Buffer.from([0x9a,0x80]));process.exit(0)});'`;
  const first = await h.call('shell', { command, yieldTimeMs: 500 });
  assert.equal(first.outcome, 'running');
  assert.equal(first.stdout, '');
  const final = await h.call('shell', { sessionId: first.sessionId, chars: '\n' });
  assert.equal(final.outcome, 'completed');
  assert.equal(final.stdout, '🚀');
});
