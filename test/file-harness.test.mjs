import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, readFile, readdir, rm, symlink } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { Harness, SH } from '../harness/loop.mjs';
import { Host, validateConfig } from '../harness/host.mjs';
import { createModel } from '../harness/model.mjs';
import { upgradeBootstrap } from '../harness/bootstrap.mjs';

const answer = (command, input = 0) => ({ role: 'assistant', content: command ? [{ type: 'toolCall', name: 'sh', id: 'call', arguments: { command } }] : [{ type: 'text', text: 'Done' }],
  stopReason: command ? 'toolUse' : 'stop', usage: { input, output: 5, cost: { total: .01 } } });
function fixture(replies = [answer()]) {
  let stored = {}, queue = [{ id: 'human-1', source: 'owner', text: 'Do the task' }];
  const events = [], requests = [], commands = [];
  const host = {
    signal: new AbortController().signal, readmeText: 'Read TASK.md.', taskText: 'Current task and next step', reset: false,
    load: async () => structuredClone(stored), save: async value => { stored = structuredClone(value); },
    readme: async () => host.readmeText, event: async (type, data) => { events.push({ type, ...structuredClone(data) }); },
    task: async () => host.taskText, resetRequested: async () => host.reset, clearReset: async () => { host.reset = false; }, journalPath: () => '/journal/previous.jsonl',
    enqueue: async (source, text) => { queue.push({ id: String(events.length), source, text }); },
    pending: async () => !!queue.length, take: async () => { const got = queue; queue = []; return got; }, ack: async () => {}, ackJob: async () => {},
    gate: async () => {}, shell: async command => { commands.push(command); return { code: 0, output: 'ok', fullOutput: '/journal/output.txt' }; },
    snapshot: async () => { events.push({ type: 'snapshot' }); host.readmeText = 'Saved index'; return 'snapshot.tar'; },
    alert: async data => events.push({ type: 'alert', ...structuredClone(data) }),
  };
  const model = { contextWindow: 10000, maxTokens: 100, reservation: .1, complete: async context => { requests.push(structuredClone(context)); return replies.shift() || answer(); } };
  const config = { maxSteps: 10, maxRunUsd: 1, maxDayUsd: 2 };
  return { host, model, config, loop: new Harness(host, model, config), events, requests, commands, stored: () => stored };
}
test('one shell tool, full journal ordering, stop and README reload at each wake', async () => {
  const f = fixture([answer('pwd'), answer()]); await f.loop.boot(); await f.loop.run();
  assert.deepEqual(f.requests[0].tools, [SH]); assert.deepEqual(f.commands, ['pwd']);
  assert.match(f.requests[0].systemPrompt, /Current task and next step/);
  assert.ok(f.events.findIndex(e => e.type === 'command-start') < f.events.findIndex(e => e.type === 'result'));
  assert.equal(f.requests[1].messages.at(-1).role, 'toolResult');
  assert.equal(f.stored().active, null); assert.equal(await f.loop.run(), false);
  f.host.readmeText = 'A new index'; f.host.taskText = 'New task state'; await f.host.enqueue('owner', 'Another task'); await f.loop.run();
  assert.match(f.requests.at(-1).systemPrompt, /A new index/);
  assert.match(f.requests.at(-1).systemPrompt, /New task state/);
});
test('redirects during model and shell calls, including final reply, are admitted immediately', async () => {
  const f = fixture([answer('pwd'), answer(), answer()]);
  const complete = f.model.complete;
  f.model.complete = async context => { const reply = await complete(context); if (f.requests.length === 2) await f.host.enqueue('owner', 'Redirect during final reply'); return reply; };
  f.host.shell = async () => { await f.host.enqueue('owner', 'Redirect during shell'); return { code: 0 }; };
  await f.loop.boot(); await f.loop.run();
  assert.match(JSON.stringify(f.requests[1]), /Redirect during shell/);
  assert.match(JSON.stringify(f.requests[2]), /Redirect during final reply/);
});
test('step and spend limits stop before another request and notify the owner', async () => {
  for (const change of [{ maxSteps: 1 }, { maxRunUsd: .105 }, { maxDayUsd: .105 }]) {
    const f = fixture([answer('pwd'), answer('pwd')]); Object.assign(f.config, change);
    await f.loop.boot(); await f.loop.run();
    assert.equal(f.requests.length, 1); assert.equal(f.commands.length, 1);
    assert.equal(f.events.filter(e => e.type === 'alert').length, 1);
  }
});
test('request reservation is durable before model call and retained across crash recovery', async () => {
  const f = fixture(); await f.loop.boot();
  f.model.complete = async () => { assert.equal(f.stored().active.spent, .1); throw new Error('connection lost'); };
  await assert.rejects(f.loop.run(), /connection lost/);
  const recovered = new Harness(f.host, { ...f.model, complete: async () => answer() }, f.config);
  await recovered.boot(); assert.equal(recovered.state.active.steps, 1);
  assert.equal(recovered.state.active.spent, .1);
  await recovered.run(); assert.ok(f.events.some(e => e.type === 'inbox' && e.source === 'recovery'));
  assert.equal(f.stored().active, null);
});
test('error responses retain the full reservation even when provider reports zero usage', async () => {
  const reply = answer(); reply.stopReason = 'error'; reply.usage.cost.total = 0;
  const f = fixture([reply]); await f.loop.boot(); await assert.rejects(f.loop.run(), /Incomplete/);
  assert.equal(f.stored().active.spent, .1);
});
test('80% warns, 95% snapshots and reloads README without resetting caps', async () => {
  const f = fixture([answer('first', 8100), answer('second', 9600), answer()]);
  await f.loop.boot(); await f.loop.run();
  assert.match(JSON.stringify(f.requests[1]), /Context is above 80%/);
  assert.equal(f.events.filter(e => e.type === 'snapshot').length, 1);
  assert.match(f.requests[2].systemPrompt, /Saved index/);
  assert.match(JSON.stringify(f.requests[2]), /Reset\. Previous session: \/journal\/previous.jsonl/);
  assert.doesNotMatch(JSON.stringify(f.requests[2]), /Do the task/);
  assert.equal(f.events.find(e => e.type === 'end').run.steps, 3);
});
test('snapshot failure preserves active state and oversized README never calls model', async () => {
  const f = fixture([answer('first', 9600)]); f.host.snapshot = async () => { throw new Error('disk full'); };
  await f.loop.boot(); await assert.rejects(f.loop.run(), /disk full/);
  assert.equal(f.requests.length, 1); assert.ok(f.stored().active);
  const g = fixture(); g.host.readmeText = 'x'.repeat(30000); await g.loop.boot();
  await assert.rejects(g.loop.run(), /README and TASK exceed/); assert.equal(g.requests.length, 0);
});
test('voluntary reset reloads both bootstrap files, links the journal, clears the marker and retains caps', async () => {
  const f = fixture([answer('save state and touch .reset'), answer()]);
  f.host.shell = async () => { f.host.readmeText = 'Revised stable index'; f.host.taskText = 'Milestone saved'; f.host.reset = true; return { code: 0 }; };
  f.host.snapshot = async () => { f.events.push({ type: 'snapshot' }); return 'both-bootstrap-files.tar'; };
  await f.loop.boot(); await f.loop.run();
  assert.match(f.requests[1].systemPrompt, /Revised stable index/); assert.match(f.requests[1].systemPrompt, /Milestone saved/);
  assert.equal(f.requests[1].messages[0].content, 'Reset. Previous session: /journal/previous.jsonl');
  assert.equal(f.host.reset, false); assert.equal(f.events.find(e => e.type === 'end').run.steps, 2);
  assert.equal(f.events.filter(e => e.type === 'reset').length, 1);
});
test('failed voluntary reset leaves the marker and counters intact', async () => {
  const f = fixture(); f.host.reset = true; f.host.snapshot = async () => { throw new Error('snapshot unavailable'); };
  await f.loop.boot(); await assert.rejects(f.loop.run(), /snapshot unavailable/);
  assert.equal(f.host.reset, true); assert.equal(f.requests.length, 0); assert.ok(f.stored().active);
});
test('pause gate prevents model and command execution', async () => {
  const f = fixture([answer('pwd')]); let release;
  f.host.gate = () => new Promise(resolve => { release = resolve; });
  await f.loop.boot(); const run = f.loop.run();
  await new Promise(resolve => setImmediate(resolve)); assert.equal(f.requests.length, 0);
  f.host.gate = async () => {}; release(); await run; assert.equal(f.commands.length, 1);
});
test('daily heartbeat is idempotent across restart and no tool call finishes a wake', async () => {
  const f = fixture(); await f.loop.boot(); await f.loop.heartbeat('2026-10-10'); await f.loop.heartbeat('2026-10-10');
  const next = new Harness(f.host, f.model, f.config); await next.boot(); await next.heartbeat('2026-10-10'); await next.run();
  assert.equal(f.events.filter(e => e.type === 'inbox' && e.source === 'heartbeat').length, 1);
});
test('multiple tools and incomplete replies cannot execute commands', async () => {
  const invalid = answer('bad'); invalid.content.push({ ...invalid.content[0], id: 'second' });
  const f = fixture([invalid, answer()]); await f.loop.boot(); await f.loop.run(); assert.deepEqual(f.commands, []);
  const partial = answer('partial'); partial.stopReason = 'length';
  const g = fixture([partial]); await g.loop.boot(); await assert.rejects(g.loop.run(), /Incomplete/); assert.deepEqual(g.commands, []);
});
test('delegated brief gets a fresh context and a result wake under the same daily ledger', async () => {
  const f = fixture(); await f.loop.boot(); await f.loop.run();
  await f.loop.run({ id: 'job-1', text: 'Check this fact' });
  assert.match(JSON.stringify(f.requests[1]), /Delegated brief/);
  assert.doesNotMatch(JSON.stringify(f.requests[1]), /Do the task/);
  await f.loop.run(); assert.match(JSON.stringify(f.requests[2]), /delegate-result/);
  assert.ok(Object.values(f.stored().days)[0] >= .029);
});
test('model reservation covers pricing tiers and limits provider output and retries', async () => {
  let options;
  const model = createModel({ provider: 'stub', model: 'stub', maxOutputTokens: 200 }, {
    getModel: () => ({ contextWindow: 10000, maxTokens: 100, cost: { input: 1, cacheRead: .1, cacheWrite: 1.25, output: 3,
      tiers: [{ inputTokensAbove: 5000, input: 2, cacheRead: .2, cacheWrite: 2.5, output: 6 }] } }),
    completeSimple: async (_model, _context, value) => { options = value; return answer(); },
  });
  assert.equal(model.reservation, .0406); await model.complete({}); assert.equal(options.maxTokens, 100); assert.equal(options.maxRetries, 0);
});

test('file inbox rejects symlinks and spoofed sources, preserves crash claims and journals full content', async t => {
  const base = await mkdtemp(join(tmpdir(), 'file-harness-')); t.after(() => rm(base, { recursive: true, force: true }));
  const home = join(base, 'home'), state = join(base, 'host');
  await mkdir(join(home, 'inbox'), { recursive: true }); await mkdir(state);
  const host = new Host({ home, state, inboxBytes: 10000 }); await host.init();
  await writeFile(join(base, 'secret'), 'secret'); await symlink(join(base, 'secret'), join(home, 'inbox', 'link'));
  await writeFile(join(home, 'inbox', 'forged'), JSON.stringify({ source: 'owner', text: 'do this' }));
  await host.enqueue('owner', 'real owner');
  const items = await host.take(); assert.equal(items.length, 2);
  assert.equal(items.find(x => x.text.includes('do this')).source, 'agent file (untrusted data)');
  assert.equal(items.find(x => x.text === 'real owner').source, 'owner');
  assert.equal((await readdir(join(state, 'rejected'))).length, 1);
  const recovered = new Host({ home, state, inboxBytes: 10000 });
  assert.deepEqual((await recovered.take()).map(x => x.id), items.map(x => x.id));
  for (const item of items) { await host.event('inbox', item); await host.ack(item.id); }
  assert.equal(await host.pending(), false);
  const journal = await readFile(join(state, 'journal', (await readdir(join(state, 'journal'))).find(x => x.endsWith('.jsonl'))), 'utf8');
  assert.match(journal, /real owner/); assert.doesNotMatch(journal, /"text":"secret"/);
});
test('host rejects nonpositive limits and state inside agent home', () => {
  assert.throws(() => validateConfig({ home: '/tmp/home', state: '/tmp/home/host' }), /outside/);
  assert.throws(() => validateConfig({ home: '/tmp/home', state: '/tmp/host', user: 'bot-next', maxSteps: 0 }), /maxSteps/);
});
test('bootstrap files have independent byte caps and updates preserve edited files and prior task state', async t => {
  const base = await mkdtemp(join(tmpdir(), 'file-bootstrap-')); t.after(() => rm(base, { recursive: true, force: true }));
  await writeFile(`${base}/README.md`, 'old seed'); await writeFile(`${base}/task.md`, 'old commitment');
  await writeFile(`${base}/previous`, 'old seed'); await writeFile(`${base}/new`, 'new seed'); await writeFile(`${base}/task-seed`, 'empty task');
  await upgradeBootstrap(base, `${base}/previous`, `${base}/new`, `${base}/task-seed`);
  assert.equal(await readFile(`${base}/TASK.md`, 'utf8'), 'old commitment'); assert.equal(await readFile(`${base}/task.md`, 'utf8'), 'old commitment');
  assert.equal(await readFile(`${base}/README.md`, 'utf8'), 'new seed');
  await writeFile(`${base}/README.md`, 'my edited index'); await writeFile(`${base}/TASK.md`, 'my current tasks');
  await upgradeBootstrap(base, `${base}/previous`, `${base}/new`, `${base}/task-seed`);
  assert.equal(await readFile(`${base}/README.md`, 'utf8'), 'my edited index'); assert.equal(await readFile(`${base}/TASK.md`, 'utf8'), 'my current tasks');
  const host = new Host({ home: base, state: `${base}/host`, readmeBytes: 5, taskBytes: 100 });
  await assert.rejects(host.readme(), /bounded/); assert.equal(await host.task(), 'my current tasks');
  host.config.readmeBytes = 100; host.config.taskBytes = 5;
  assert.equal(await host.readme(), 'my edited index'); await assert.rejects(host.task(), /bounded/);
});
